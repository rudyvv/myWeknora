package source

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	// SourceRelationFactRefsUnavailable means no ref set was safely established.
	SourceRelationFactRefsUnavailable = "unavailable"
	// SourceRelationFactRefsVerified means persisted refs match parsed facts in the snapshot.
	SourceRelationFactRefsVerified = "verified"
	// SourceRelationFactRefsReplayed means an exact legacy relation was reconstructed.
	SourceRelationFactRefsReplayed = "replayed"

	sourceRelationFactRefMaxCount = 256
	sourceRelationFactContextMax  = 1 << 20
	// Large snapshots fail closed instead of triggering unbounded legacy replay.
	sourceRelationReplayMaxFacts = 100_000

	sourceRelationFactReplayMaxRefs         int64 = 100_000
	sourceRelationFactReplayMaxContextBytes int64 = 32 << 20
)

// SourceRelationFactCapacityError identifies bounded replay or resolution
// work that cannot safely produce a complete route-fact reference set.
type SourceRelationFactCapacityError struct {
	Budget    string
	Used      int64
	Requested int64
	Limit     int64
}

func (err *SourceRelationFactCapacityError) Error() string {
	if err == nil {
		return "source relation fact capacity exceeded"
	}
	return fmt.Sprintf("source relation fact %s capacity exceeded: used %d, requested %d, limit %d",
		err.Budget, err.Used, err.Requested, err.Limit)
}

type sourceRelationFactCapacityBudget struct {
	refs         int64
	contextBytes int64
	maxRefs      int64
	maxContext   int64
}

func newSourceRelationFactCapacityBudget(maxRefs, maxContextBytes int64) *sourceRelationFactCapacityBudget {
	return &sourceRelationFactCapacityBudget{maxRefs: maxRefs, maxContext: maxContextBytes}
}

func (budget *sourceRelationFactCapacityBudget) addRefs(requested int64) error {
	if budget == nil || requested == 0 {
		return nil
	}
	if requested < 0 || requested > budget.maxRefs-budget.refs {
		return &SourceRelationFactCapacityError{
			Budget: "cumulative refs", Used: budget.refs, Requested: requested, Limit: budget.maxRefs,
		}
	}
	budget.refs += requested
	return nil
}

func (budget *sourceRelationFactCapacityBudget) checkContextBytes(requested int64) error {
	if budget == nil {
		return nil
	}
	if requested < 0 || requested > budget.maxContext-budget.contextBytes {
		return &SourceRelationFactCapacityError{
			Budget: "serialized context bytes", Used: budget.contextBytes, Requested: requested, Limit: budget.maxContext,
		}
	}
	return nil
}

func (budget *sourceRelationFactCapacityBudget) addContextBytes(requested int64) error {
	if err := budget.checkContextBytes(requested); err != nil {
		return err
	}
	if budget != nil {
		budget.contextBytes += requested
	}
	return nil
}

// SourceRelationFactRefsEncodedUpperBound returns a conservative upper bound
// for JSON encoding refs, allowing callers to reject oversized contexts before
// allocating the encoded buffer.
func sourceRelationFactRefsEncodedUpperBound(refs []types.SourceRelationFactRef) int64 {
	const encodedRefFixedOverhead int64 = 256

	size := int64(2) // surrounding JSON array brackets
	for _, ref := range refs {
		size += encodedRefFixedOverhead + 6*int64(len(ref.DataSourceID)+len(ref.SnapshotID)+len(ref.FileID)+
			len(ref.FileVersionID)+len(ref.Path)+len(ref.Kind)+len(ref.Role)+len(ref.Quality))
	}
	if len(refs) > 1 {
		size += int64(len(refs) - 1) // commas between refs
	}
	return size
}

// MarshalSourceRelationFactRefsBounded applies the same per-route serialized
// context cap used by verification before allocating JSON output.
func marshalSourceRelationFactRefsBounded(refs []types.SourceRelationFactRef) ([]byte, error) {
	upperBound := sourceRelationFactRefsEncodedUpperBound(refs)
	if upperBound > sourceRelationFactContextMax {
		return nil, &SourceRelationFactCapacityError{
			Budget: "serialized context bytes", Requested: upperBound, Limit: sourceRelationFactContextMax,
		}
	}
	encoded, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	if int64(len(encoded)) > sourceRelationFactContextMax {
		return nil, &SourceRelationFactCapacityError{
			Budget: "serialized context bytes", Requested: int64(len(encoded)), Limit: sourceRelationFactContextMax,
		}
	}
	return encoded, nil
}

// SourceRelationFactSnapshot must contain the complete parsed membership for
// one immutable snapshot, not a topic- or relation-filtered subset. Members
// are assumed not to be mutated after the resolver is constructed.
type SourceRelationFactSnapshot struct {
	TenantID     uint64
	DataSourceID string
	SnapshotID   string
	Complete     bool
	Members      []SourceRelationMember
}

type SourceRelationFactRefResolution struct {
	Status  string
	Refs    []types.SourceRelationFactRef
	Context types.JSON
	Err     error
}

// SourceRelationFactRefResolver verifies persisted refs and can replay legacy
// relations once against the exact complete snapshot supplied at construction.
// It is a provenance check, not an authorization grant; callers must still
// enforce tenant access when reading source content.
type SourceRelationFactRefResolver struct {
	snapshot      SourceRelationFactSnapshot
	usable        bool
	membersByFile map[string]SourceRelationMember

	replayOnce      sync.Once
	replayByID      map[sourceRelationIdentity][]types.SourceCodeRelation
	replayErr       error
	maxRefs         int64
	maxContextBytes int64
}

type sourceRelationIdentity struct {
	tenantID         uint64
	dataSourceID     string
	snapshotID       string
	kind             string
	fromFileID       string
	fromVersionID    string
	fromPath         string
	fromKey          string
	fromRange        types.SourceRange
	toFileID         string
	toVersionID      string
	toPath           string
	toKey            string
	toRange          types.SourceRange
	determinacy      string
	quality          string
	resolutionReason string
}

func NewSourceRelationFactRefResolver(snapshot SourceRelationFactSnapshot) *SourceRelationFactRefResolver {
	return newSourceRelationFactRefResolverWithLimits(snapshot,
		sourceRelationFactReplayMaxRefs, sourceRelationFactReplayMaxContextBytes)
}

func newSourceRelationFactRefResolverWithLimits(
	snapshot SourceRelationFactSnapshot,
	maxRefs, maxContextBytes int64,
) *SourceRelationFactRefResolver {
	resolver := &SourceRelationFactRefResolver{
		snapshot: snapshot, maxRefs: maxRefs, maxContextBytes: maxContextBytes,
	}
	if snapshot.TenantID == 0 || snapshot.DataSourceID == "" || snapshot.SnapshotID == "" ||
		!snapshot.Complete || len(snapshot.Members) > types.SourceWikiSkeletonMaxFiles {
		return resolver
	}

	resolver.membersByFile = make(map[string]SourceRelationMember, len(snapshot.Members))
	seenPaths := make(map[string]struct{}, len(snapshot.Members))
	seenVersions := make(map[string]struct{}, len(snapshot.Members))
	factCount := 0
	for _, member := range snapshot.Members {
		if member.FileID == "" || member.VersionID == "" || member.Path == "" {
			return resolver
		}
		if _, exists := resolver.membersByFile[member.FileID]; exists {
			return resolver
		}
		if _, exists := seenPaths[member.Path]; exists {
			return resolver
		}
		if _, exists := seenVersions[member.VersionID]; exists {
			return resolver
		}
		factCount += len(member.Facts)
		if factCount > sourceRelationReplayMaxFacts {
			return resolver
		}
		resolver.membersByFile[member.FileID] = member
		seenPaths[member.Path] = struct{}{}
		seenVersions[member.VersionID] = struct{}{}
	}
	resolver.usable = true
	return resolver
}

func (resolver *SourceRelationFactRefResolver) Resolve(relation types.SourceCodeRelation) SourceRelationFactRefResolution {
	if resolver == nil || !resolver.usable || relation.TenantID != resolver.snapshot.TenantID ||
		relation.DataSourceID != resolver.snapshot.DataSourceID || relation.SnapshotID != resolver.snapshot.SnapshotID ||
		relation.Kind != "http_route" {
		return unavailableSourceRelationFactRefs()
	}
	if int64(len(relation.Context)) > sourceRelationFactContextMax {
		return SourceRelationFactRefResolution{Status: SourceRelationFactRefsUnavailable, Err: &SourceRelationFactCapacityError{
			Budget: "per-route serialized context bytes", Requested: int64(len(relation.Context)), Limit: sourceRelationFactContextMax,
		}}
	}

	refs, hasRefs, valid := decodeSourceRelationFactRefs(relation.Context)
	if !valid {
		return unavailableSourceRelationFactRefs()
	}
	identity, ok := sourceRelationIdentityOf(relation)
	if !ok {
		return unavailableSourceRelationFactRefs()
	}
	resolver.replayOnce.Do(resolver.replaySnapshot)
	if resolver.replayErr != nil {
		return SourceRelationFactRefResolution{Status: SourceRelationFactRefsUnavailable, Err: resolver.replayErr}
	}
	matches := resolver.replayByID[identity]
	if len(matches) != 1 {
		return unavailableSourceRelationFactRefs()
	}
	causalRefs, hasCausalRefs, valid := decodeSourceRelationFactRefs(matches[0].Context)
	if !valid || (hasCausalRefs && !resolver.verifyRefs(causalRefs)) {
		return unavailableSourceRelationFactRefs()
	}
	if hasRefs {
		if !resolver.verifyRefs(refs) || !hasCausalRefs || !sameSourceRelationFactRefSet(refs, causalRefs) {
			return unavailableSourceRelationFactRefs()
		}
		return SourceRelationFactRefResolution{Status: SourceRelationFactRefsVerified, Refs: causalRefs, Context: matches[0].Context}
	}
	return SourceRelationFactRefResolution{Status: SourceRelationFactRefsReplayed, Refs: causalRefs, Context: matches[0].Context}
}

func (resolver *SourceRelationFactRefResolver) replaySnapshot() {
	resolver.replayByID = make(map[sourceRelationIdentity][]types.SourceCodeRelation)
	relations, err := CorrelateSourceFactsBounded(resolver.snapshot.TenantID, resolver.snapshot.DataSourceID,
		resolver.snapshot.SnapshotID, resolver.snapshot.Members, resolver.maxRefs, resolver.maxContextBytes)
	if err != nil {
		resolver.replayByID = nil
		resolver.replayErr = err
		return
	}
	if len(relations) > types.SourceWikiSkeletonMaxRelations {
		resolver.replayByID = nil
		return
	}
	for _, relation := range relations {
		identity, ok := sourceRelationIdentityOf(relation)
		if ok {
			resolver.replayByID[identity] = append(resolver.replayByID[identity], relation)
		}
	}
}

func (resolver *SourceRelationFactRefResolver) verifyRefs(refs []types.SourceRelationFactRef) bool {
	if len(refs) == 0 || len(refs) > sourceRelationFactRefMaxCount {
		return false
	}
	seen := make(map[types.SourceRelationFactRef]struct{}, len(refs))
	for _, ref := range refs {
		if ref.DataSourceID != resolver.snapshot.DataSourceID || ref.SnapshotID != resolver.snapshot.SnapshotID ||
			ref.FileID == "" || ref.FileVersionID == "" || ref.Path == "" || ref.Quality == "" {
			return false
		}
		expectedKind, validRole := sourceRelationFactRoleKinds[ref.Role]
		if !validRole || ref.Kind != expectedKind {
			return false
		}
		if _, exists := seen[ref]; exists {
			return false
		}
		seen[ref] = struct{}{}
		member, exists := resolver.membersByFile[ref.FileID]
		if !exists || member.VersionID != ref.FileVersionID || member.Path != ref.Path {
			return false
		}
		factExists := false
		for _, fact := range member.Facts {
			if fact.Kind == ref.Kind && fact.Quality == ref.Quality && fact.Range == ref.Range {
				factExists = true
				break
			}
		}
		if !factExists {
			return false
		}
	}
	return true
}

func sameSourceRelationFactRefSet(first, second []types.SourceRelationFactRef) bool {
	if len(first) != len(second) {
		return false
	}
	refs := make(map[types.SourceRelationFactRef]struct{}, len(first))
	for _, ref := range first {
		refs[ref] = struct{}{}
	}
	for _, ref := range second {
		if _, exists := refs[ref]; !exists {
			return false
		}
	}
	return true
}

var sourceRelationFactRoleKinds = map[string]string{
	"api_prefix":           "api_prefix",
	"api_proxy":            "api_proxy",
	"spring_class_mapping": "spring_mapping",
}

func decodeSourceRelationFactRefs(context types.JSON) ([]types.SourceRelationFactRef, bool, bool) {
	raw := bytes.TrimSpace(context)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, false, true
	}
	if len(raw) > sourceRelationFactContextMax || raw[0] != '[' {
		return nil, false, false
	}
	var refs []types.SourceRelationFactRef
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, false, false
	}
	if len(refs) == 0 {
		return nil, false, true
	}
	return refs, true, true
}

func sourceRelationIdentityOf(relation types.SourceCodeRelation) (sourceRelationIdentity, bool) {
	var fromRange, toRange types.SourceRange
	if len(relation.FromRange) == 0 || len(relation.ToRange) == 0 ||
		json.Unmarshal(relation.FromRange, &fromRange) != nil || json.Unmarshal(relation.ToRange, &toRange) != nil {
		return sourceRelationIdentity{}, false
	}
	return sourceRelationIdentity{
		tenantID: relation.TenantID, dataSourceID: relation.DataSourceID, snapshotID: relation.SnapshotID,
		kind: relation.Kind, fromFileID: relation.FromFileID, fromVersionID: relation.FromVersionID,
		fromPath: relation.FromPath, fromKey: relation.FromKey, fromRange: fromRange,
		toFileID: relation.ToFileID, toVersionID: relation.ToVersionID, toPath: relation.ToPath,
		toKey: relation.ToKey, toRange: toRange, determinacy: relation.Determinacy,
		quality: relation.Quality, resolutionReason: relation.ResolutionReason,
	}, true
}

func unavailableSourceRelationFactRefs() SourceRelationFactRefResolution {
	return SourceRelationFactRefResolution{Status: SourceRelationFactRefsUnavailable}
}
