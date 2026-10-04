package source

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	sourceWikiImpactMaxFacts        = 2 * sourceRelationReplayMaxFacts
	sourceWikiImpactMaxFactBytes    = 64 << 20
	sourceWikiImpactMaxContextBytes = 32 << 20
	sourceWikiImpactMaxRefs         = types.SourceWikiSkeletonMaxRelations
	sourceWikiImpactMaxTopics       = types.SourceWikiBatchMaxCandidates
	sourceWikiImpactMaxGraphWork    = 2_000_000
)

var (
	errSourceWikiImpactGraphWork = errors.New("source Wiki impact graph work limit exceeded")
	errSourceWikiImpactRefBound  = errors.New("source Wiki impact reference bound exceeded")
)

func sourceWikiImpactFallbackReason(err error) string {
	switch {
	case errors.Is(err, errSourceWikiImpactGraphWork):
		return "graph_work_limit_exceeded"
	case errors.Is(err, errSourceWikiImpactRefBound):
		return "topic_dependency_reference_bound_exceeded"
	default:
		return err.Error()
	}
}

type sourceWikiImpactWork struct{ used int }

func (work *sourceWikiImpactWork) add(count int) bool {
	if count < 0 || count > sourceWikiImpactMaxGraphWork-work.used {
		return false
	}
	work.used += count
	return true
}

type sourceWikiImpactFactRef struct {
	FileID  string
	Path    string
	Kind    string
	Role    string
	Quality string
	Range   types.SourceRange
}

type sourceWikiImpactFactIdentity struct {
	Kind    string
	Quality string
	Range   types.SourceRange
}

type sourceWikiImpactRelation struct {
	FromPath    string
	ToPath      string
	Kind        string
	Determinacy string
	Fingerprint string
	CausalPaths []string
	CausalRefs  []sourceWikiImpactFactRef
}

type sourceWikiImpactFlowEntry struct {
	FileID string
	Path   string
}

type sourceWikiImpactRelationDelta struct {
	Reason      string
	CausalPaths []string
	Relation    sourceWikiImpactRelation
}

type sourceWikiImpactTopicScope struct {
	Paths     map[string]bool
	Relations map[string]bool
	Modules   map[string]bool
}

type sourceWikiImpactSnapshotIndex struct {
	input                  types.SourceWikiImpactSnapshot
	membersByPath          map[string]types.SourceWikiImpactMember
	membersByID            map[string]types.SourceWikiImpactMember
	membersByDirectory     map[string]int
	factsByPath            map[string][]string
	factEvidenceByFileID   map[string]map[sourceWikiImpactFactIdentity]bool
	memberFingerprints     map[string]string
	relations              []sourceWikiImpactRelation
	outgoing               map[string][]int
	touching               map[string][]int
	moduleFacts            map[string]bool
	moduleEvidenceComplete map[string]bool
	flowFacts              map[string]bool
	flowEntryMembers       map[string][]sourceWikiImpactFlowEntry
	flowEvidenceComplete   bool
	interfaceConfig        map[string]bool
}

type sourceWikiImpactInventoryIndex struct {
	topics            map[string]types.SourceWikiImpactTopicDependencies
	modules           map[string]types.SourceWikiImpactModule
	modulePathsByPath map[string][]string
	moduleTopics      map[string]bool
	hasOverview       bool
}

type sourceWikiImpactMemberProjection struct {
	Path         string
	SourceFileID string
	Fingerprint  string
	ModulePaths  []string
}

type sourceWikiImpactRelationProjection struct {
	Kind             string
	FromFileID       string
	FromPath         string
	FromKey          string
	FromRange        types.SourceRange
	ToFileID         string
	ToPath           string
	ToKey            string
	ToRange          *types.SourceRange
	Determinacy      string
	Quality          string
	ResolutionReason string
	Causal           []sourceWikiImpactFactRef
}

type sourceWikiImpactTopicProjection struct {
	TenantID        uint64
	KnowledgeBaseID string
	SourceID        string
	TopicKey        string
	Kind            string
	ModulePath      string
	Members         []sourceWikiImpactMemberProjection
	Relations       []string
	ModuleInventory []string
}

// PlanSourceWikiImpact compares two complete fixed-snapshot inputs. The two
// inventories must be ordered previous then next and contain every retained,
// manual, expansion, and newly skeleton-planned topic. It is pure: it never
// reads source state, creates pins, or grants evidence ownership.
func PlanSourceWikiImpact(previous, next types.SourceWikiImpactSnapshot,
	inventories []types.SourceWikiImpactTopicInventory) (types.SourceWikiImpactPlan, error) {
	plan := types.SourceWikiImpactPlan{
		TenantID: previous.TenantID, KnowledgeBaseID: previous.KnowledgeBaseID,
		SourceID: previous.SourceID, PreviousSnapshotID: previous.SnapshotID,
		NextSnapshotID: next.SnapshotID, Disposition: types.SourceWikiImpactSourceWideStale,
	}
	invalid := func(err error) (types.SourceWikiImpactPlan, error) {
		plan.FallbackReason = "invalid_impact_input"
		plan.RescanSkeleton = true
		plan.RescanReasons = []string{"source_wide_stale_fallback"}
		return plan, err
	}
	fallback := func(reason string) (types.SourceWikiImpactPlan, error) {
		plan.Disposition = types.SourceWikiImpactSourceWideStale
		plan.Affected = nil
		plan.Unaffected = nil
		plan.Removed = nil
		plan.RescanSkeleton = true
		plan.FallbackReason = reason
		plan.RescanReasons = []string{"source_wide_stale_fallback"}
		return plan, nil
	}

	if previous.TenantID == 0 || previous.KnowledgeBaseID == "" || previous.SourceID == "" || previous.SnapshotID == "" ||
		next.TenantID == 0 || next.KnowledgeBaseID == "" || next.SourceID == "" || next.SnapshotID == "" {
		return invalid(errors.New("source Wiki impact snapshot identity is incomplete"))
	}
	if previous.TenantID != next.TenantID || previous.KnowledgeBaseID != next.KnowledgeBaseID || previous.SourceID != next.SourceID {
		return invalid(errors.New("source Wiki impact snapshots do not belong to the same tenant, knowledge base, and source"))
	}
	if previous.Stage != types.SourceWikiImpactPublishedComplete ||
		(next.Stage != types.SourceWikiImpactPreparingComplete && next.Stage != types.SourceWikiImpactPublishedComplete) {
		return invalid(errors.New("source Wiki impact requires a published-complete previous snapshot and a complete preparing or published next snapshot"))
	}
	if previous.SnapshotID == next.SnapshotID {
		return invalid(errors.New("source Wiki impact previous and next snapshot IDs must differ"))
	}
	if len(inventories) != 2 {
		return invalid(errors.New("source Wiki impact requires exactly previous and next topic inventories"))
	}
	if !inventoryIdentityMatches(inventories[0], previous) || !inventoryIdentityMatches(inventories[1], next) {
		return invalid(errors.New("source Wiki impact topic inventory identity does not match its snapshot"))
	}

	work := &sourceWikiImpactWork{}
	factsRemaining := sourceWikiImpactMaxFacts
	factBytesRemaining := sourceWikiImpactMaxFactBytes
	contextBytesRemaining := sourceWikiImpactMaxContextBytes
	refsRemaining := sourceWikiImpactMaxRefs
	previousIndex, reason, err := indexSourceWikiImpactSnapshot(previous, &factsRemaining, &factBytesRemaining, &contextBytesRemaining, &refsRemaining, work)
	if err != nil {
		if errors.Is(err, errSourceWikiImpactGraphWork) {
			return fallback("graph_work_limit_exceeded")
		}
		if errors.Is(err, errSourceWikiImpactRefBound) {
			return fallback("topic_dependency_reference_bound_exceeded")
		}
		return invalid(err)
	}
	if reason != "" {
		return fallback(reason)
	}
	nextIndex, reason, err := indexSourceWikiImpactSnapshot(next, &factsRemaining, &factBytesRemaining, &contextBytesRemaining, &refsRemaining, work)
	if err != nil {
		if errors.Is(err, errSourceWikiImpactGraphWork) {
			return fallback("graph_work_limit_exceeded")
		}
		if errors.Is(err, errSourceWikiImpactRefBound) {
			return fallback("topic_dependency_reference_bound_exceeded")
		}
		return invalid(err)
	}
	if reason != "" {
		return fallback(reason)
	}
	previousInventory, reason, err := indexSourceWikiImpactInventory(inventories[0], previousIndex, &refsRemaining, work)
	if err != nil {
		return invalid(err)
	}
	if reason != "" {
		return fallback(reason)
	}
	nextInventory, reason, err := indexSourceWikiImpactInventory(inventories[1], nextIndex, &refsRemaining, work)
	if err != nil {
		return invalid(err)
	}
	if reason != "" {
		return fallback(reason)
	}

	changedPaths, structureChanged, configChanged, err := sourceWikiImpactMemberChanges(previousIndex, nextIndex, work)
	if err != nil {
		return fallback(sourceWikiImpactFallbackReason(err))
	}
	if !work.add(2 * (len(previousIndex.relations) + len(nextIndex.relations))) {
		return fallback("graph_work_limit_exceeded")
	}
	relationDeltas := sourceWikiImpactRelationChanges(previousIndex, nextIndex)
	relationChangeReasons := make(map[string][]string)
	for _, delta := range relationDeltas {
		if !work.add(1 + len(delta.CausalPaths)) {
			return fallback("graph_work_limit_exceeded")
		}
		for _, filePath := range delta.CausalPaths {
			addSourceWikiImpactReason(changedPaths, filePath, delta.Reason)
			relationChangeReasons[filePath] = append(relationChangeReasons[filePath], delta.Reason)
		}
	}
	for filePath := range relationChangeReasons {
		relationChangeReasons[filePath] = compactSortedStrings(relationChangeReasons[filePath])
	}
	if len(relationDeltas) > 0 {
		structureChanged = true
		for _, delta := range relationDeltas {
			if sourceWikiImpactRelationIsInterfaceConfig(delta) {
				configChanged = true
			}
		}
	}

	if !work.add(len(previousInventory.modules) + len(nextInventory.modules)) {
		return fallback("graph_work_limit_exceeded")
	}
	sameModuleInventory, err := sameSourceWikiImpactModuleInventory(previousInventory, nextInventory, work)
	if err != nil {
		return fallback(sourceWikiImpactFallbackReason(err))
	}
	moduleChanged := !sameModuleInventory
	if moduleChanged {
		structureChanged = true
	}
	if structureChanged {
		plan.RescanSkeleton = true
		plan.RescanReasons = append(plan.RescanReasons, sourceWikiImpactRescanReasons(changedPaths, moduleChanged, len(relationDeltas))...)
		plan.RescanReasons = compactSortedStrings(plan.RescanReasons)
	}

	impactPaths, err := sourceWikiImpactReverseClosure(previousIndex, nextIndex, changedPaths, work)
	if err != nil {
		return fallback(sourceWikiImpactFallbackReason(err))
	}
	uncertainModules, err := sourceWikiImpactUncertainModules(impactPaths, previousIndex, nextIndex, previousInventory, nextInventory, work)
	if err != nil {
		return fallback(sourceWikiImpactFallbackReason(err))
	}
	configModules := map[string]bool{}
	if configChanged {
		configModules, err = sourceWikiImpactModulesForPaths(impactPaths, previousIndex, previousInventory, nextIndex, nextInventory, work)
		if err != nil {
			return fallback(sourceWikiImpactFallbackReason(err))
		}
		if len(configModules) == 0 {
			return fallback("interface_or_config_change_has_no_bounded_module")
		}
	}
	if configChanged {
		if !work.add(len(configModules)) {
			return fallback("graph_work_limit_exceeded")
		}
		for modulePath := range configModules {
			if !sourceWikiImpactHasModuleTopic(modulePath, previousInventory, nextInventory) {
				return fallback("interface_or_config_module_topic_missing")
			}
		}
	}
	if configChanged {
		if !sourceWikiImpactHasOverview(previousInventory, nextInventory) {
			return fallback("overview_topic_missing_for_source_wide_change")
		}
	}

	previousTopicKeys := sortedTopicKeys(previousInventory.topics)
	nextTopicKeys := sortedTopicKeys(nextInventory.topics)
	allTopicKeys := mergeSortedStrings(previousTopicKeys, nextTopicKeys)
	if !work.add(len(allTopicKeys)) {
		return fallback("graph_work_limit_exceeded")
	}
	for _, topicKey := range allTopicKeys {
		oldTopic, hadOld := previousInventory.topics[topicKey]
		newTopic, hasNew := nextInventory.topics[topicKey]
		if !hadOld {
			appendAffectedTopic(&plan, topicKey, "topic_added_to_complete_inventory")
			plan.RescanSkeleton = true
			plan.RescanReasons = append(plan.RescanReasons, "topic_inventory_changed")
			continue
		}
		if !hasNew {
			removalProven, err := sourceWikiImpactRemovalProven(oldTopic, previousIndex, nextIndex,
				previousInventory, nextInventory, work)
			if err != nil {
				return fallback(sourceWikiImpactFallbackReason(err))
			}
			if removalProven {
				plan.Removed = append(plan.Removed, types.SourceWikiRemovedTopic{TopicKey: topicKey, Reason: "source_contribution_absent_from_complete_next_snapshot"})
				plan.RescanSkeleton = true
				plan.RescanReasons = append(plan.RescanReasons, "source_topic_contribution_removed")
			} else {
				appendAffectedTopic(&plan, topicKey, "topic_missing_next_inventory_removal_unproven")
				plan.RescanSkeleton = true
				plan.RescanReasons = append(plan.RescanReasons, "topic_inventory_incomplete_or_downgraded")
			}
			continue
		}

		oldFingerprint, oldScope, err := sourceWikiImpactTopicFingerprint(previousIndex, previousInventory, oldTopic, work)
		if err != nil {
			return fallback(sourceWikiImpactFallbackReason(err))
		}
		newFingerprint, newScope, err := sourceWikiImpactTopicFingerprint(nextIndex, nextInventory, newTopic, work)
		if err != nil {
			return fallback(sourceWikiImpactFallbackReason(err))
		}
		forcedReasons := []string{}
		if oldTopic.Kind == "module" && uncertainModules[oldTopic.ModulePath] {
			forcedReasons = append(forcedReasons, "uncertain_relation_expanded_module")
		}
		if newTopic.Kind == "module" && uncertainModules[newTopic.ModulePath] {
			forcedReasons = append(forcedReasons, "uncertain_relation_expanded_module")
		}
		if configChanged && (oldTopic.Kind == "system" || newTopic.Kind == "system") {
			forcedReasons = append(forcedReasons, "public_interface_or_config_changed")
		}
		if oldTopic.Kind == "module" && configModules[oldTopic.ModulePath] || newTopic.Kind == "module" && configModules[newTopic.ModulePath] {
			forcedReasons = append(forcedReasons, "public_interface_or_config_changed")
		}
		if !work.add(len(oldScope.Paths) + len(newScope.Paths)) {
			return fallback("graph_work_limit_exceeded")
		}
		for filePath := range oldScope.Paths {
			if impactPaths[filePath] {
				forcedReasons = append(forcedReasons, "transitive_dependency_changed")
				break
			}
		}
		if len(forcedReasons) == 0 {
			for filePath := range newScope.Paths {
				if impactPaths[filePath] {
					forcedReasons = append(forcedReasons, "transitive_dependency_changed")
					break
				}
			}
		}
		if len(forcedReasons) > 0 {
			detailedReasons, err := sourceWikiImpactReasonsForTopic(topicKey, oldTopic, newTopic, oldScope, newScope,
				changedPaths, relationChangeReasons, moduleChanged, work)
			if err != nil {
				return fallback(sourceWikiImpactFallbackReason(err))
			}
			appendAffectedTopic(&plan, topicKey, append(detailedReasons, forcedReasons...)...)
			continue
		}
		if oldFingerprint == newFingerprint && oldTopic.EvidenceSHA != "" {
			plan.Unaffected = append(plan.Unaffected, types.SourceWikiUnaffectedTopic{
				TopicKey: topicKey, ApplicabilityFingerprint: newFingerprint, EvidenceSHA: oldTopic.EvidenceSHA,
			})
			continue
		}

		reasons, err := sourceWikiImpactReasonsForTopic(topicKey, oldTopic, newTopic, oldScope, newScope, changedPaths, relationChangeReasons, moduleChanged, work)
		if err != nil {
			return fallback(sourceWikiImpactFallbackReason(err))
		}
		if oldFingerprint == newFingerprint && oldTopic.EvidenceSHA == "" {
			reasons = append(reasons, "previous_evidence_fingerprint_missing")
		}
		reasons = append(reasons, "applicability_fingerprint_changed")
		appendAffectedTopic(&plan, topicKey, reasons...)
	}

	if !work.add(len(plan.Affected) + len(plan.Unaffected) + len(plan.Removed)) {
		return fallback("graph_work_limit_exceeded")
	}
	if configChanged || len(uncertainModules) > 0 {
		plan.RescanSkeleton = true
		if configChanged {
			plan.RescanReasons = append(plan.RescanReasons, "public_interface_or_config_changed")
		}
		if len(uncertainModules) > 0 {
			plan.RescanReasons = append(plan.RescanReasons, "uncertain_relation_module_expansion")
		}
	}
	plan.RescanReasons = compactSortedStrings(plan.RescanReasons)
	sort.Slice(plan.Affected, func(i, j int) bool { return plan.Affected[i].TopicKey < plan.Affected[j].TopicKey })
	sort.Slice(plan.Unaffected, func(i, j int) bool { return plan.Unaffected[i].TopicKey < plan.Unaffected[j].TopicKey })
	sort.Slice(plan.Removed, func(i, j int) bool { return plan.Removed[i].TopicKey < plan.Removed[j].TopicKey })
	plan.Disposition = types.SourceWikiImpactPlanned
	return plan, nil
}

func inventoryIdentityMatches(inventory types.SourceWikiImpactTopicInventory, snapshot types.SourceWikiImpactSnapshot) bool {
	return inventory.TenantID == snapshot.TenantID && inventory.KnowledgeBaseID == snapshot.KnowledgeBaseID &&
		inventory.SourceID == snapshot.SourceID && inventory.SnapshotID == snapshot.SnapshotID
}

func indexSourceWikiImpactSnapshot(snapshot types.SourceWikiImpactSnapshot, factsRemaining, factBytesRemaining, contextBytesRemaining, refsRemaining *int,
	work *sourceWikiImpactWork) (*sourceWikiImpactSnapshotIndex, string, error) {
	if !snapshot.ManifestComplete {
		return nil, "snapshot_manifest_incomplete", nil
	}
	if snapshot.ExpectedMemberCount < 0 {
		return nil, "", errors.New("negative source Wiki impact member count")
	}
	if snapshot.ExpectedRelationCount < 0 {
		return nil, "", errors.New("negative source Wiki impact relation count")
	}
	if snapshot.ExpectedMemberCount > types.SourceWikiSkeletonMaxFiles || len(snapshot.Members) > types.SourceWikiSkeletonMaxFiles {
		return nil, "snapshot_member_bound_exceeded", nil
	}
	if snapshot.ExpectedMemberCount != len(snapshot.Members) {
		return nil, "snapshot_member_count_mismatch", nil
	}
	if !snapshot.RelationsComplete {
		return nil, "snapshot_relations_incomplete", nil
	}
	if snapshot.ExpectedRelationCount > types.SourceWikiSkeletonMaxRelations || len(snapshot.Relations) > types.SourceWikiSkeletonMaxRelations {
		return nil, "snapshot_relation_bound_exceeded", nil
	}
	if snapshot.ExpectedRelationCount != len(snapshot.Relations) {
		return nil, "snapshot_relation_count_mismatch", nil
	}
	if !work.add(len(snapshot.Members) + len(snapshot.Relations)) {
		return nil, "graph_work_limit_exceeded", nil
	}
	index := &sourceWikiImpactSnapshotIndex{
		input: snapshot, membersByPath: make(map[string]types.SourceWikiImpactMember, len(snapshot.Members)),
		membersByID:          make(map[string]types.SourceWikiImpactMember, len(snapshot.Members)),
		membersByDirectory:   make(map[string]int),
		factsByPath:          make(map[string][]string, len(snapshot.Members)),
		factEvidenceByFileID: make(map[string]map[sourceWikiImpactFactIdentity]bool, len(snapshot.Members)),
		memberFingerprints:   make(map[string]string, len(snapshot.Members)),
		outgoing:             make(map[string][]int), touching: make(map[string][]int),
		moduleFacts: make(map[string]bool), moduleEvidenceComplete: make(map[string]bool),
		flowFacts: make(map[string]bool), flowEntryMembers: make(map[string][]sourceWikiImpactFlowEntry),
		flowEvidenceComplete: true, interfaceConfig: make(map[string]bool),
	}
	seenVersions := make(map[string]bool, len(snapshot.Members))
	for _, member := range snapshot.Members {
		if !validSourceWikiImpactPath(member.Path) {
			return nil, "", fmt.Errorf("invalid source Wiki impact path %q", member.Path)
		}
		if _, exists := index.membersByPath[member.Path]; exists {
			return nil, "", fmt.Errorf("duplicate source Wiki impact path %q", member.Path)
		}
		if !validSourceWikiImpactDigest(member.ContentSHA, member.Status == "parsed") {
			return nil, "incomplete_member_content_sha", nil
		}
		member.ContentSHA = strings.ToLower(member.ContentSHA)
		if member.ExpectedFactCount < 0 {
			return nil, "", errors.New("negative source Wiki impact fact count")
		}
		if !member.FactsComplete || member.ExpectedFactCount != len(member.Facts) {
			return nil, "member_facts_incomplete", nil
		}
		modulePath := path.Dir(member.Path)
		index.membersByDirectory[modulePath]++
		if _, initialized := index.moduleEvidenceComplete[modulePath]; !initialized {
			index.moduleEvidenceComplete[modulePath] = true
		}
		if member.Status != "parsed" || member.Quality != "structural" {
			index.moduleEvidenceComplete[modulePath] = false
			index.flowEvidenceComplete = false
		}
		switch member.Status {
		case "parsed":
			if member.SourceFileID == "" || member.FileVersionID == "" {
				return nil, "", fmt.Errorf("parsed source Wiki impact member %q lacks file identity", member.Path)
			}
			if member.ParserVersion == "" || member.Quality == "" {
				return nil, "incomplete_member_parser_metadata", nil
			}
			if _, exists := index.membersByID[member.SourceFileID]; exists {
				return nil, "", fmt.Errorf("duplicate source Wiki impact file identity %q", member.SourceFileID)
			}
			if seenVersions[member.FileVersionID] {
				return nil, "", fmt.Errorf("duplicate source Wiki impact file version identity %q", member.FileVersionID)
			}
			seenVersions[member.FileVersionID] = true
		case "excluded":
			if member.FileVersionID != "" {
				return nil, "", fmt.Errorf("excluded source Wiki impact member %q has a parsed file version", member.Path)
			}
			if member.SourceFileID != "" {
				if _, exists := index.membersByID[member.SourceFileID]; exists {
					return nil, "", fmt.Errorf("duplicate source Wiki impact file identity %q", member.SourceFileID)
				}
			}
			if len(member.Facts) != 0 {
				return nil, "incomplete_excluded_member_facts", nil
			}
		case "included":
			return nil, "preparing_snapshot_contains_unparsed_member", nil
		default:
			return nil, "", fmt.Errorf("unsupported source Wiki impact member status %q", member.Status)
		}
		if !work.add(len(member.Facts)) {
			return nil, "graph_work_limit_exceeded", nil
		}
		*factsRemaining -= len(member.Facts)
		if *factsRemaining < 0 {
			return nil, "snapshot_fact_bound_exceeded", nil
		}
		factStrings := make([]string, 0, len(member.Facts))
		factEvidence := make(map[sourceWikiImpactFactIdentity]bool, len(member.Facts))
		for _, fact := range member.Facts {
			if !work.add(1) {
				return nil, "graph_work_limit_exceeded", nil
			}
			if sourceWikiImpactFactSizeBound(fact) > *factBytesRemaining {
				return nil, "snapshot_fact_byte_bound_exceeded", nil
			}
			encoded, err := json.Marshal(fact)
			if err != nil {
				return nil, "", fmt.Errorf("encode source Wiki impact fact: %w", err)
			}
			*factBytesRemaining -= len(encoded)
			if *factBytesRemaining < 0 {
				return nil, "snapshot_fact_byte_bound_exceeded", nil
			}
			factStrings = append(factStrings, string(encoded))
			factEvidence[sourceWikiImpactFactIdentity{Kind: fact.Kind, Quality: fact.Quality, Range: fact.Range}] = true
			if !member.Generated && impactModuleFactCandidate(fact) {
				index.moduleFacts[modulePath] = true
			}
			if !member.Generated && impactModuleFactSignal(fact) && fact.Quality != "" && fact.Quality != "structural" {
				index.moduleEvidenceComplete[modulePath] = false
			}
			if impactFlowEvidenceFact(fact) && ((fact.Quality != "" && fact.Quality != "structural") || fact.Dynamic || fact.Certainty == "uncertain") {
				index.flowEvidenceComplete = false
			}
			if !member.Generated && fact.Kind == "api_request" {
				if route := canonicalImpactRoute(fact.HTTPMethod + " " + fact.RoutePath); route != "" {
					index.flowFacts[route] = true
					index.flowEntryMembers[route] = append(index.flowEntryMembers[route], sourceWikiImpactFlowEntry{
						FileID: member.SourceFileID, Path: member.Path,
					})
				}
			}
			if impactFactIsInterfaceConfig(fact) {
				index.interfaceConfig[member.Path] = true
			}
		}
		sort.Strings(factStrings)
		index.membersByPath[member.Path] = member
		if member.SourceFileID != "" {
			index.membersByID[member.SourceFileID] = member
			index.factEvidenceByFileID[member.SourceFileID] = factEvidence
		}
		index.factsByPath[member.Path] = factStrings
		memberProjection, err := json.Marshal(struct {
			Path          string
			SourceFileID  string
			ContentSHA    string
			Status        string
			Generated     bool
			ParserVersion string
			Quality       string
			Facts         []string
		}{member.Path, member.SourceFileID, strings.ToLower(member.ContentSHA), member.Status, member.Generated,
			member.ParserVersion, member.Quality, factStrings})
		if err != nil {
			return nil, "", fmt.Errorf("encode source Wiki impact member: %w", err)
		}
		index.memberFingerprints[member.Path] = sha256Hex(memberProjection)
	}

	seenRelationIDs := make(map[string]bool, len(snapshot.Relations))
	seenRelationFingerprints := make(map[string]bool, len(snapshot.Relations))
	if !work.add(len(snapshot.Relations)) {
		return nil, "graph_work_limit_exceeded", nil
	}
	for _, relation := range snapshot.Relations {
		if relation.ID == "" || seenRelationIDs[relation.ID] {
			return nil, "", errors.New("source Wiki impact relation identity is missing or duplicated")
		}
		seenRelationIDs[relation.ID] = true
		if relation.TenantID != snapshot.TenantID || relation.DataSourceID != snapshot.SourceID || relation.SnapshotID != snapshot.SnapshotID {
			return nil, "", errors.New("source Wiki impact relation is not bound to its exact snapshot")
		}
		from, exists := index.membersByID[relation.FromFileID]
		if !exists || from.Status != "parsed" || from.FileVersionID != relation.FromVersionID || from.Path != relation.FromPath {
			return nil, "", errors.New("source Wiki impact relation has an invalid from endpoint")
		}
		if relation.Kind == "" {
			return nil, "", errors.New("source Wiki impact relation identity is incomplete")
		}
		if relation.Determinacy != "certain" && relation.Determinacy != "uncertain" {
			return nil, "", errors.New("source Wiki impact relation determinacy is unsupported")
		}
		if relation.Quality == "" {
			return nil, "", errors.New("source Wiki impact relation quality is incomplete")
		}
		var to types.SourceWikiImpactMember
		if relation.ToFileID != "" {
			var ok bool
			to, ok = index.membersByID[relation.ToFileID]
			if !ok || to.Status != "parsed" || to.FileVersionID != relation.ToVersionID || to.Path != relation.ToPath {
				return nil, "", errors.New("source Wiki impact relation has an invalid to endpoint")
			}
		} else if relation.ToVersionID != "" || relation.Determinacy == "certain" {
			return nil, "", errors.New("source Wiki impact relation has an incomplete certain endpoint")
		}
		var fromRange types.SourceRange
		if err := json.Unmarshal(relation.FromRange, &fromRange); err != nil {
			return nil, "", errors.New("source Wiki impact relation has an invalid from range")
		}
		var toRange *types.SourceRange
		if relation.ToFileID != "" {
			var decoded types.SourceRange
			if err := json.Unmarshal(relation.ToRange, &decoded); err != nil {
				return nil, "", errors.New("source Wiki impact relation has an invalid to range")
			}
			toRange = &decoded
		}
		*contextBytesRemaining -= len(relation.Context)
		if *contextBytesRemaining < 0 {
			return nil, "snapshot_relation_context_bound_exceeded", nil
		}
		causal, err := sourceWikiImpactCausalRefs(relation.Context, snapshot, index, refsRemaining, work)
		if err != nil {
			return nil, "", err
		}
		projection := sourceWikiImpactRelationProjection{
			Kind: relation.Kind, FromFileID: relation.FromFileID, FromPath: relation.FromPath, FromKey: relation.FromKey,
			FromRange: fromRange, ToFileID: relation.ToFileID, ToPath: relation.ToPath, ToKey: relation.ToKey,
			ToRange: toRange, Determinacy: relation.Determinacy, Quality: relation.Quality,
			ResolutionReason: relation.ResolutionReason, Causal: causal,
		}
		encoded, err := json.Marshal(projection)
		if err != nil {
			return nil, "", fmt.Errorf("encode source Wiki impact relation: %w", err)
		}
		fingerprint := sha256Hex(encoded)
		if seenRelationFingerprints[fingerprint] {
			return nil, "", errors.New("duplicate source Wiki impact relation semantics")
		}
		seenRelationFingerprints[fingerprint] = true
		causalPaths := make([]string, 0, len(causal)+2)
		causalPaths = append(causalPaths, relation.FromPath)
		if relation.ToPath != "" {
			causalPaths = append(causalPaths, relation.ToPath)
		}
		for _, ref := range causal {
			causalPaths = append(causalPaths, ref.Path)
		}
		causalPaths = compactSortedStrings(causalPaths)
		index.relations = append(index.relations, sourceWikiImpactRelation{
			FromPath: relation.FromPath, ToPath: relation.ToPath, Kind: relation.Kind,
			Determinacy: relation.Determinacy, Fingerprint: fingerprint,
			CausalPaths: causalPaths, CausalRefs: causal,
		})
		if relation.Kind == "http_route" {
			if route := canonicalImpactRoute(relation.FromKey); route != "" {
				index.flowFacts[route] = true
				index.flowEntryMembers[route] = append(index.flowEntryMembers[route], sourceWikiImpactFlowEntry{
					FileID: relation.FromFileID, Path: relation.FromPath,
				})
			}
		}
	}
	sort.Slice(index.relations, func(i, j int) bool { return index.relations[i].Fingerprint < index.relations[j].Fingerprint })
	if !work.add(len(index.relations)) {
		return nil, "graph_work_limit_exceeded", nil
	}
	for indexInSlice := range index.relations {
		relation := &index.relations[indexInSlice]
		if !work.add(len(relation.CausalPaths)) {
			return nil, "graph_work_limit_exceeded", nil
		}
		index.touching[relation.FromPath] = append(index.touching[relation.FromPath], indexInSlice)
		if relation.ToPath != "" {
			index.touching[relation.ToPath] = append(index.touching[relation.ToPath], indexInSlice)
		}
		for _, causalPath := range relation.CausalPaths {
			if causalPath != relation.FromPath && causalPath != relation.ToPath {
				index.touching[causalPath] = append(index.touching[causalPath], indexInSlice)
			}
		}
		if relation.Determinacy == "certain" && relation.ToPath != "" {
			index.outgoing[relation.FromPath] = append(index.outgoing[relation.FromPath], indexInSlice)
		}
	}
	return index, "", nil
}

func sourceWikiImpactCausalRefs(raw types.JSON, snapshot types.SourceWikiImpactSnapshot,
	index *sourceWikiImpactSnapshotIndex, refsRemaining *int, work *sourceWikiImpactWork) ([]sourceWikiImpactFactRef, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if len(trimmed) > sourceRelationFactContextMax {
		return nil, errors.New("source Wiki impact relation causal context exceeds its bound")
	}
	var refs []types.SourceRelationFactRef
	if trimmed[0] != '[' || json.Unmarshal(trimmed, &refs) != nil {
		return nil, errors.New("source Wiki impact relation causal context is invalid")
	}
	if len(refs) > sourceRelationFactRefMaxCount {
		return nil, errSourceWikiImpactRefBound
	}
	if len(refs) > *refsRemaining {
		return nil, errSourceWikiImpactRefBound
	}
	if !work.add(len(refs)) {
		return nil, errSourceWikiImpactGraphWork
	}
	*refsRemaining -= len(refs)
	if *refsRemaining < 0 {
		return nil, errSourceWikiImpactRefBound
	}
	type encodedRef struct {
		ref     sourceWikiImpactFactRef
		encoded string
	}
	encodedRefs := make([]encodedRef, 0, len(refs))
	seen := make(map[sourceWikiImpactFactRef]bool, len(refs))
	for _, ref := range refs {
		if ref.DataSourceID != snapshot.SourceID || ref.SnapshotID != snapshot.SnapshotID {
			return nil, errors.New("source Wiki impact causal fact is bound to another snapshot")
		}
		member, exists := index.membersByID[ref.FileID]
		if !exists || member.Status != "parsed" || member.FileVersionID != ref.FileVersionID || member.Path != ref.Path {
			return nil, errors.New("source Wiki impact causal fact has an invalid member identity")
		}
		wantKind, validRole := sourceRelationFactRoleKinds[ref.Role]
		if !validRole || wantKind != ref.Kind || ref.Quality == "" || !sourceWikiImpactMemberHasFact(index, ref) {
			return nil, errors.New("source Wiki impact causal fact does not match parser evidence")
		}
		canonical := sourceWikiImpactFactRef{
			FileID: ref.FileID, Path: ref.Path, Kind: ref.Kind, Role: ref.Role,
			Quality: ref.Quality, Range: ref.Range,
		}
		if seen[canonical] {
			return nil, errors.New("source Wiki impact relation repeats a causal fact")
		}
		seen[canonical] = true
		encoded, _ := json.Marshal(canonical)
		encodedRefs = append(encodedRefs, encodedRef{ref: canonical, encoded: string(encoded)})
	}
	sort.Slice(encodedRefs, func(i, j int) bool {
		return encodedRefs[i].encoded < encodedRefs[j].encoded
	})
	result := make([]sourceWikiImpactFactRef, 0, len(encodedRefs))
	for _, item := range encodedRefs {
		result = append(result, item.ref)
	}
	return result, nil
}

func indexSourceWikiImpactInventory(inventory types.SourceWikiImpactTopicInventory, snapshot *sourceWikiImpactSnapshotIndex,
	refsRemaining *int, work *sourceWikiImpactWork) (*sourceWikiImpactInventoryIndex, string, error) {
	if !inventory.Complete {
		return nil, "topic_inventory_incomplete", nil
	}
	if inventory.ExpectedTopicCount < 0 || inventory.ExpectedModuleCount < 0 {
		return nil, "", errors.New("negative source Wiki impact inventory count")
	}
	if inventory.ExpectedTopicCount > sourceWikiImpactMaxTopics || len(inventory.Topics) > sourceWikiImpactMaxTopics {
		return nil, "topic_inventory_bound_exceeded", nil
	}
	if inventory.ExpectedModuleCount > sourceWikiImpactMaxTopics || len(inventory.ModuleInventory) > sourceWikiImpactMaxTopics {
		return nil, "module_inventory_bound_exceeded", nil
	}
	if inventory.ExpectedTopicCount != len(inventory.Topics) || inventory.ExpectedModuleCount != len(inventory.ModuleInventory) {
		return nil, "topic_or_module_inventory_count_mismatch", nil
	}
	if !work.add(len(inventory.Topics) + len(inventory.ModuleInventory)) {
		return nil, "graph_work_limit_exceeded", nil
	}
	result := &sourceWikiImpactInventoryIndex{
		topics:            make(map[string]types.SourceWikiImpactTopicDependencies, len(inventory.Topics)),
		modules:           make(map[string]types.SourceWikiImpactModule, len(inventory.ModuleInventory)),
		modulePathsByPath: make(map[string][]string),
		moduleTopics:      make(map[string]bool),
	}
	for _, module := range inventory.ModuleInventory {
		if !validSourceWikiImpactModulePath(module.Path) {
			return nil, "", fmt.Errorf("invalid source Wiki impact module path %q", module.Path)
		}
		if _, exists := result.modules[module.Path]; exists {
			return nil, "", fmt.Errorf("duplicate source Wiki impact module path %q", module.Path)
		}
		if module.ExpectedFileCount < 0 || module.ExpectedFileCount != len(module.FileIDs) {
			return nil, "topic_or_module_inventory_count_mismatch", nil
		}
		if len(module.FileIDs) > *refsRemaining {
			return nil, "topic_dependency_reference_bound_exceeded", nil
		}
		moduleFiles := append([]string(nil), module.FileIDs...)
		sort.Strings(moduleFiles)
		if !work.add(len(moduleFiles)) {
			return nil, "graph_work_limit_exceeded", nil
		}
		for i, fileID := range moduleFiles {
			if fileID == "" || (i > 0 && moduleFiles[i-1] == fileID) {
				return nil, "", fmt.Errorf("duplicate or empty source Wiki impact module member in %q", module.Path)
			}
			member, exists := snapshot.membersByID[fileID]
			if !exists || member.Status != "parsed" {
				return nil, "module_inventory_references_incomplete_snapshot", nil
			}
			if path.Dir(member.Path) != module.Path {
				return nil, "", fmt.Errorf("source Wiki impact module member path does not match module %q", module.Path)
			}
			result.modulePathsByPath[member.Path] = append(result.modulePathsByPath[member.Path], module.Path)
			*refsRemaining = *refsRemaining - 1
			if *refsRemaining < 0 {
				return nil, "topic_dependency_reference_bound_exceeded", nil
			}
		}
		result.modules[module.Path] = module
	}
	for _, topic := range inventory.Topics {
		if topic.TopicKey == "" || topic.Kind == "" {
			return nil, "", errors.New("source Wiki impact topic identity is incomplete")
		}
		if _, exists := result.topics[topic.TopicKey]; exists {
			return nil, "", fmt.Errorf("duplicate source Wiki impact topic key %q", topic.TopicKey)
		}
		if topic.ExpectedDependencyFileCount < 0 || topic.ExpectedDependencyFileCount != len(topic.DependencyFileIDs) {
			return nil, "topic_dependency_inventory_count_mismatch", nil
		}
		if len(topic.DependencyFileIDs) > *refsRemaining {
			return nil, "topic_dependency_reference_bound_exceeded", nil
		}
		if topic.Kind == "module" && !validSourceWikiImpactModulePath(topic.ModulePath) {
			return nil, "", fmt.Errorf("invalid source Wiki impact topic module path %q", topic.ModulePath)
		}
		dependencyIDs := append([]string(nil), topic.DependencyFileIDs...)
		sort.Strings(dependencyIDs)
		if !work.add(len(dependencyIDs)) {
			return nil, "graph_work_limit_exceeded", nil
		}
		for i, fileID := range dependencyIDs {
			if fileID == "" || (i > 0 && dependencyIDs[i-1] == fileID) {
				return nil, "", fmt.Errorf("duplicate or empty dependency identity for source Wiki topic %q", topic.TopicKey)
			}
			member, exists := snapshot.membersByID[fileID]
			if !exists || member.Status != "parsed" {
				return nil, "topic_dependency_references_incomplete_snapshot", nil
			}
			*refsRemaining = *refsRemaining - 1
			if *refsRemaining < 0 {
				return nil, "topic_dependency_reference_bound_exceeded", nil
			}
		}
		result.topics[topic.TopicKey] = topic
		if topic.Kind == "module" {
			result.moduleTopics[topic.ModulePath] = true
		}
		if topic.Kind == "system" {
			result.hasOverview = true
		}
	}
	for filePath := range result.modulePathsByPath {
		sort.Strings(result.modulePathsByPath[filePath])
	}
	return result, "", nil
}

func sourceWikiImpactMemberChanges(previous, next *sourceWikiImpactSnapshotIndex, work *sourceWikiImpactWork) (map[string][]string, bool, bool, error) {
	paths := make(map[string]bool, len(previous.membersByPath)+len(next.membersByPath))
	for filePath := range previous.membersByPath {
		paths[filePath] = true
	}
	for filePath := range next.membersByPath {
		paths[filePath] = true
	}
	if !work.add(len(paths)) {
		return nil, false, false, errSourceWikiImpactGraphWork
	}
	ordered := sortedBoolKeys(paths)
	changes := make(map[string][]string)
	structureChanged, configChanged := false, false
	if !work.add(len(previous.membersByID) + len(next.membersByID)) {
		return nil, false, false, errSourceWikiImpactGraphWork
	}
	previousPathByID := make(map[string]string, len(previous.membersByID))
	nextPathByID := make(map[string]string, len(next.membersByID))
	for fileID, member := range previous.membersByID {
		previousPathByID[fileID] = member.Path
	}
	for fileID, member := range next.membersByID {
		nextPathByID[fileID] = member.Path
	}
	for _, filePath := range ordered {
		oldMember, hadOld := previous.membersByPath[filePath]
		newMember, hasNew := next.membersByPath[filePath]
		if !hadOld {
			if oldPath, sameIdentity := previousPathByID[newMember.SourceFileID]; sameIdentity && oldPath != filePath {
				addSourceWikiImpactReason(changes, oldPath, "file_renamed")
				addSourceWikiImpactReason(changes, filePath, "file_renamed")
			} else {
				addSourceWikiImpactReason(changes, filePath, "file_added")
			}
			structureChanged = true
			if next.interfaceConfig[filePath] {
				addSourceWikiImpactReason(changes, filePath, "public_interface_or_config_changed")
				configChanged = true
			}
			continue
		}
		if !hasNew {
			if newPath, sameIdentity := nextPathByID[oldMember.SourceFileID]; sameIdentity && newPath != filePath {
				addSourceWikiImpactReason(changes, filePath, "file_renamed")
				addSourceWikiImpactReason(changes, newPath, "file_renamed")
			} else {
				addSourceWikiImpactReason(changes, filePath, "file_deleted")
			}
			structureChanged = true
			if previous.interfaceConfig[filePath] {
				addSourceWikiImpactReason(changes, filePath, "public_interface_or_config_changed")
				configChanged = true
			}
			continue
		}

		if oldMember.SourceFileID != newMember.SourceFileID {
			addSourceWikiImpactReason(changes, filePath, "source_file_identity_replaced")
			structureChanged = true
		}
		if oldMember.ContentSHA != newMember.ContentSHA {
			addSourceWikiImpactReason(changes, filePath, "content_changed")
		}
		if oldMember.Status != newMember.Status {
			addSourceWikiImpactReason(changes, filePath, "member_status_changed")
			structureChanged = true
		}
		if oldMember.Generated != newMember.Generated {
			addSourceWikiImpactReason(changes, filePath, "generated_status_changed")
			structureChanged = true
		}
		if oldMember.ParserVersion != newMember.ParserVersion || oldMember.Quality != newMember.Quality {
			addSourceWikiImpactReason(changes, filePath, "parser_or_quality_changed")
			structureChanged = true
		}
		if !work.add(len(previous.factsByPath[filePath]) + len(next.factsByPath[filePath])) {
			return nil, false, false, errSourceWikiImpactGraphWork
		}
		factsChanged := !sameStrings(previous.factsByPath[filePath], next.factsByPath[filePath])
		if factsChanged {
			addSourceWikiImpactReason(changes, filePath, "semantic_or_structure_facts_changed")
			structureChanged = true
			if previous.interfaceConfig[filePath] || next.interfaceConfig[filePath] {
				addSourceWikiImpactReason(changes, filePath, "public_interface_or_config_changed")
				configChanged = true
			}
		}
	}
	return changes, structureChanged, configChanged, nil
}

func sourceWikiImpactRelationChanges(previous, next *sourceWikiImpactSnapshotIndex) []sourceWikiImpactRelationDelta {
	oldByFingerprint := make(map[string]sourceWikiImpactRelation, len(previous.relations))
	newByFingerprint := make(map[string]sourceWikiImpactRelation, len(next.relations))
	for _, relation := range previous.relations {
		oldByFingerprint[relation.Fingerprint] = relation
	}
	for _, relation := range next.relations {
		newByFingerprint[relation.Fingerprint] = relation
	}
	var result []sourceWikiImpactRelationDelta
	for fingerprint, relation := range oldByFingerprint {
		if _, exists := newByFingerprint[fingerprint]; !exists {
			result = append(result, sourceWikiImpactRelationDelta{Reason: "static_relation_removed_or_changed", CausalPaths: relation.CausalPaths, Relation: relation})
		}
	}
	for fingerprint, relation := range newByFingerprint {
		if _, exists := oldByFingerprint[fingerprint]; !exists {
			result = append(result, sourceWikiImpactRelationDelta{Reason: "static_relation_added_or_changed", CausalPaths: relation.CausalPaths, Relation: relation})
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Reason != result[j].Reason {
			return result[i].Reason < result[j].Reason
		}
		return result[i].Relation.Fingerprint < result[j].Relation.Fingerprint
	})
	return result
}

func sourceWikiImpactRelationIsInterfaceConfig(delta sourceWikiImpactRelationDelta) bool {
	if delta.Relation.Kind == "http_route" {
		return true
	}
	for _, ref := range delta.Relation.CausalRefs {
		switch ref.Role {
		case "api_prefix", "api_proxy", "spring_class_mapping":
			return true
		}
	}
	return false
}

func sourceWikiImpactReverseClosure(previous, next *sourceWikiImpactSnapshotIndex, changes map[string][]string,
	work *sourceWikiImpactWork) (map[string]bool, error) {
	reverse := make(map[string][]string)
	for _, snapshot := range []*sourceWikiImpactSnapshotIndex{previous, next} {
		for _, relation := range snapshot.relations {
			if !work.add(1 + len(relation.CausalPaths)) {
				return nil, errSourceWikiImpactGraphWork
			}
			if relation.Determinacy == "certain" && relation.ToPath != "" {
				reverse[relation.ToPath] = append(reverse[relation.ToPath], relation.FromPath)
			}
			for _, causalPath := range relation.CausalPaths {
				reverse[causalPath] = append(reverse[causalPath], relation.FromPath)
			}
		}
	}
	for target := range reverse {
		reverse[target] = compactSortedStrings(reverse[target])
	}
	seeds := sortedMapKeys(changes)
	closure := make(map[string]bool, len(seeds))
	queue := append([]string(nil), seeds...)
	for _, seed := range seeds {
		closure[seed] = true
	}
	for cursor := 0; cursor < len(queue); cursor++ {
		current := queue[cursor]
		if !work.add(1 + len(reverse[current])) {
			return nil, errSourceWikiImpactGraphWork
		}
		for _, dependent := range reverse[current] {
			if closure[dependent] {
				continue
			}
			closure[dependent] = true
			queue = append(queue, dependent)
		}
	}
	return closure, nil
}

func sourceWikiImpactUncertainModules(impactPaths map[string]bool, previous, next *sourceWikiImpactSnapshotIndex,
	previousInventory, nextInventory *sourceWikiImpactInventoryIndex, work *sourceWikiImpactWork) (map[string]bool, error) {
	modules := make(map[string]bool)
	for _, item := range []struct {
		snapshot  *sourceWikiImpactSnapshotIndex
		inventory *sourceWikiImpactInventoryIndex
	}{{previous, previousInventory}, {next, nextInventory}} {
		if !work.add(len(item.snapshot.relations)) {
			return nil, errSourceWikiImpactGraphWork
		}
		for _, relation := range item.snapshot.relations {
			if relation.Determinacy == "certain" {
				continue
			}
			relevant := impactPaths[relation.FromPath] || impactPaths[relation.ToPath]
			for _, causalPath := range relation.CausalPaths {
				relevant = relevant || impactPaths[causalPath]
			}
			if !relevant {
				continue
			}
			if !work.add(len(relation.CausalRefs)) {
				return nil, errSourceWikiImpactGraphWork
			}
			if relation.ToPath == "" {
				candidateTargetBounded := false
				for _, ref := range relation.CausalRefs {
					if ref.Role == "spring_class_mapping" {
						candidateTargetBounded = true
						break
					}
				}
				if !candidateTargetBounded {
					return nil, errors.New("uncertain_relation_target_module_unbounded")
				}
			}
			if !work.add(len(relation.CausalPaths)) {
				return nil, errSourceWikiImpactGraphWork
			}
			found := false
			for _, endpoint := range []string{relation.FromPath, relation.ToPath} {
				if !work.add(len(item.inventory.modulePathsByPath[endpoint])) {
					return nil, errSourceWikiImpactGraphWork
				}
				for _, modulePath := range item.inventory.modulePathsByPath[endpoint] {
					modules[modulePath] = true
					found = true
				}
			}
			for _, causalPath := range relation.CausalPaths {
				if !work.add(len(item.inventory.modulePathsByPath[causalPath])) {
					return nil, errSourceWikiImpactGraphWork
				}
				for _, modulePath := range item.inventory.modulePathsByPath[causalPath] {
					modules[modulePath] = true
					found = true
				}
			}
			if !found {
				return nil, errors.New("uncertain_relation_has_no_bounded_module")
			}
		}
	}
	for modulePath := range modules {
		if !work.add(1) {
			return nil, errSourceWikiImpactGraphWork
		}
		if !sourceWikiImpactHasModuleTopic(modulePath, previousInventory, nextInventory) {
			return nil, errors.New("uncertain_relation_module_topic_missing")
		}
	}
	return modules, nil
}

func sourceWikiImpactModulesForPaths(paths map[string]bool, previous *sourceWikiImpactSnapshotIndex, previousInventory *sourceWikiImpactInventoryIndex,
	next *sourceWikiImpactSnapshotIndex, nextInventory *sourceWikiImpactInventoryIndex, work *sourceWikiImpactWork) (map[string]bool, error) {
	modules := make(map[string]bool)
	orderedPaths := sortedBoolKeys(paths)
	for _, inventory := range []*sourceWikiImpactInventoryIndex{previousInventory, nextInventory} {
		if !work.add(len(orderedPaths)) {
			return nil, errSourceWikiImpactGraphWork
		}
		for _, filePath := range orderedPaths {
			if !work.add(len(inventory.modulePathsByPath[filePath])) {
				return nil, errSourceWikiImpactGraphWork
			}
			for _, modulePath := range inventory.modulePathsByPath[filePath] {
				modules[modulePath] = true
			}
		}
	}
	for _, item := range []struct {
		snapshot  *sourceWikiImpactSnapshotIndex
		inventory *sourceWikiImpactInventoryIndex
	}{{previous, previousInventory}, {next, nextInventory}} {
		if !work.add(len(item.snapshot.relations)) {
			return nil, errSourceWikiImpactGraphWork
		}
		for _, relation := range item.snapshot.relations {
			if !work.add(1 + len(relation.CausalPaths)) {
				return nil, errSourceWikiImpactGraphWork
			}
			related := false
			for _, causalPath := range relation.CausalPaths {
				if paths[causalPath] {
					related = true
					break
				}
			}
			if !related {
				continue
			}
			for _, endpoint := range []string{relation.FromPath, relation.ToPath} {
				if !work.add(len(item.inventory.modulePathsByPath[endpoint])) {
					return nil, errSourceWikiImpactGraphWork
				}
				for _, modulePath := range item.inventory.modulePathsByPath[endpoint] {
					modules[modulePath] = true
				}
			}
			for _, causalPath := range relation.CausalPaths {
				if !work.add(len(item.inventory.modulePathsByPath[causalPath])) {
					return nil, errSourceWikiImpactGraphWork
				}
				for _, modulePath := range item.inventory.modulePathsByPath[causalPath] {
					modules[modulePath] = true
				}
			}
		}
	}
	return modules, nil
}

func sourceWikiImpactHasModuleTopic(modulePath string, inventories ...*sourceWikiImpactInventoryIndex) bool {
	for _, inventory := range inventories {
		if inventory.moduleTopics[modulePath] {
			return true
		}
	}
	return false
}

func sourceWikiImpactHasOverview(inventories ...*sourceWikiImpactInventoryIndex) bool {
	for _, inventory := range inventories {
		if inventory.hasOverview {
			return true
		}
	}
	return false
}

func sourceWikiImpactTopicFingerprint(snapshot *sourceWikiImpactSnapshotIndex, inventory *sourceWikiImpactInventoryIndex,
	topic types.SourceWikiImpactTopicDependencies, work *sourceWikiImpactWork) (string, sourceWikiImpactTopicScope, error) {
	scope := sourceWikiImpactTopicScope{Paths: make(map[string]bool), Relations: make(map[string]bool), Modules: make(map[string]bool)}
	if topic.Kind == "system" {
		for filePath := range snapshot.membersByPath {
			scope.Paths[filePath] = true
		}
	} else {
		for _, fileID := range topic.DependencyFileIDs {
			member := snapshot.membersByID[fileID]
			scope.Paths[member.Path] = true
		}
	}
	if topic.Kind == "module" {
		if module, exists := inventory.modules[topic.ModulePath]; exists {
			for _, fileID := range module.FileIDs {
				member := snapshot.membersByID[fileID]
				scope.Paths[member.Path] = true
			}
		}
	}

	queue := sortedBoolKeys(scope.Paths)
	for cursor := 0; cursor < len(queue); cursor++ {
		filePath := queue[cursor]
		if !work.add(1 + len(snapshot.outgoing[filePath]) + len(snapshot.touching[filePath])) {
			return "", scope, errSourceWikiImpactGraphWork
		}
		for _, relationIndex := range snapshot.touching[filePath] {
			relation := snapshot.relations[relationIndex]
			scope.Relations[relation.Fingerprint] = true
		}
		for _, relationIndex := range snapshot.outgoing[filePath] {
			relation := snapshot.relations[relationIndex]
			if relation.ToPath != "" && !scope.Paths[relation.ToPath] {
				scope.Paths[relation.ToPath] = true
				queue = append(queue, relation.ToPath)
			}
		}
		for _, relationIndex := range snapshot.touching[filePath] {
			relation := snapshot.relations[relationIndex]
			if !work.add(len(relation.CausalPaths)) {
				return "", scope, errSourceWikiImpactGraphWork
			}
			for _, causalPath := range relation.CausalPaths {
				if !scope.Paths[causalPath] {
					scope.Paths[causalPath] = true
					queue = append(queue, causalPath)
				}
			}
		}
	}
	if !work.add(len(scope.Paths)) {
		return "", scope, errSourceWikiImpactGraphWork
	}
	for filePath := range scope.Paths {
		for _, modulePath := range inventory.modulePathsByPath[filePath] {
			scope.Modules[modulePath] = true
		}
	}

	projection := sourceWikiImpactTopicProjection{
		TenantID: snapshot.input.TenantID, KnowledgeBaseID: snapshot.input.KnowledgeBaseID,
		SourceID: snapshot.input.SourceID, TopicKey: topic.TopicKey, Kind: topic.Kind, ModulePath: topic.ModulePath,
	}
	for _, filePath := range sortedBoolKeys(scope.Paths) {
		member, exists := snapshot.membersByPath[filePath]
		if !exists {
			return "", scope, errors.New("source Wiki impact closure references a missing path")
		}
		if member.Status != "parsed" && topic.Kind != "system" {
			return "", scope, errors.New("topic dependency closure includes a non-parsed member")
		}
		projection.Members = append(projection.Members, sourceWikiImpactMemberProjection{
			Path: member.Path, SourceFileID: member.SourceFileID,
			Fingerprint: snapshot.memberFingerprints[filePath], ModulePaths: inventory.modulePathsByPath[filePath],
		})
	}
	for fingerprint := range scope.Relations {
		projection.Relations = append(projection.Relations, fingerprint)
	}
	sort.Strings(projection.Relations)
	for modulePath := range scope.Modules {
		module, exists := inventory.modules[modulePath]
		if !exists {
			continue
		}
		fileIDs := append([]string(nil), module.FileIDs...)
		sort.Strings(fileIDs)
		if !work.add(len(fileIDs)) {
			return "", scope, errSourceWikiImpactGraphWork
		}
		projection.ModuleInventory = append(projection.ModuleInventory, modulePath+"\x00"+strings.Join(fileIDs, "\x00"))
	}
	if topic.Kind == "system" {
		for modulePath, module := range inventory.modules {
			fileIDs := append([]string(nil), module.FileIDs...)
			sort.Strings(fileIDs)
			if !work.add(len(fileIDs)) {
				return "", scope, errSourceWikiImpactGraphWork
			}
			projection.ModuleInventory = append(projection.ModuleInventory, modulePath+"\x00"+strings.Join(fileIDs, "\x00"))
		}
	}
	sort.Strings(projection.ModuleInventory)
	encoded, err := json.Marshal(projection)
	if err != nil {
		return "", scope, fmt.Errorf("encode source Wiki impact fingerprint: %w", err)
	}
	return sha256Hex(encoded), scope, nil
}

func sourceWikiImpactRemovalProven(topic types.SourceWikiImpactTopicDependencies,
	previous, next *sourceWikiImpactSnapshotIndex, previousInventory, nextInventory *sourceWikiImpactInventoryIndex,
	work *sourceWikiImpactWork) (bool, error) {
	if !topic.SourceOwned {
		return false, nil
	}
	switch topic.Kind {
	case "module":
		oldModule, hadOldModule := previousInventory.modules[topic.ModulePath]
		if !hadOldModule || len(oldModule.FileIDs) == 0 || previous.membersByDirectory[topic.ModulePath] == 0 {
			return false, nil
		}
		return !next.moduleFacts[topic.ModulePath] && nextInventory.modules[topic.ModulePath].Path == "" &&
			next.membersByDirectory[topic.ModulePath] == 0, nil
	case "flow":
		route := canonicalImpactRoute(strings.TrimPrefix(topic.TopicKey, "flow/"))
		if route == "" || len(topic.DependencyFileIDs) == 0 || !next.flowEvidenceComplete || next.flowFacts[route] {
			return false, nil
		}
		previousEntries := previous.flowEntryMembers[route]
		if len(previousEntries) == 0 {
			return false, nil
		}
		for _, entry := range previousEntries {
			if !work.add(1) {
				return false, errSourceWikiImpactGraphWork
			}
			if entry.Path != "" {
				if _, exists := next.membersByPath[entry.Path]; exists {
					return false, nil
				}
			}
			if entry.FileID != "" {
				if _, exists := next.membersByID[entry.FileID]; exists {
					return false, nil
				}
			}
		}
		return true, nil
	default:
		return false, nil
	}
}

func sourceWikiImpactReasonsForTopic(topicKey string, oldTopic, newTopic types.SourceWikiImpactTopicDependencies,
	oldScope, newScope sourceWikiImpactTopicScope, changes, relationChanges map[string][]string,
	moduleChanged bool, work *sourceWikiImpactWork) ([]string, error) {
	reasons := make([]string, 0)
	paths := make(map[string]bool, len(oldScope.Paths)+len(newScope.Paths))
	for filePath := range oldScope.Paths {
		paths[filePath] = true
	}
	for filePath := range newScope.Paths {
		paths[filePath] = true
	}
	if !work.add(len(paths)) {
		return nil, errSourceWikiImpactGraphWork
	}
	for filePath := range paths {
		if fileReasons, exists := changes[filePath]; exists {
			reasons = append(reasons, fileReasons...)
		}
		if relationReasons, exists := relationChanges[filePath]; exists {
			reasons = append(reasons, relationReasons...)
		}
	}
	if oldTopic.Kind == "module" && newTopic.Kind == "module" && oldTopic.ModulePath == newTopic.ModulePath && moduleChanged {
		for modulePath := range oldScope.Modules {
			if modulePath == oldTopic.ModulePath {
				reasons = append(reasons, "module_membership_changed")
			}
		}
		for modulePath := range newScope.Modules {
			if modulePath == newTopic.ModulePath {
				reasons = append(reasons, "module_membership_changed")
			}
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "source_dependency_closure_changed")
	}
	return compactSortedStrings(reasons), nil
}

func sameSourceWikiImpactModuleInventory(previous, next *sourceWikiImpactInventoryIndex, work *sourceWikiImpactWork) (bool, error) {
	if !work.add(len(previous.modules) + len(next.modules)) {
		return false, errSourceWikiImpactGraphWork
	}
	if len(previous.modules) != len(next.modules) {
		return false, nil
	}
	for _, modulePath := range sortedMapKeys(previous.modules) {
		oldModule := previous.modules[modulePath]
		newModule, exists := next.modules[modulePath]
		if !exists {
			return false, nil
		}
		if !work.add(len(oldModule.FileIDs) + len(newModule.FileIDs)) {
			return false, errSourceWikiImpactGraphWork
		}
		if !sameStringSet(oldModule.FileIDs, newModule.FileIDs) {
			return false, nil
		}
	}
	return true, nil
}

func sourceWikiImpactRescanReasons(changes map[string][]string, moduleChanged bool, relationDeltaCount int) []string {
	var reasons []string
	for _, fileReasons := range changes {
		for _, reason := range fileReasons {
			switch reason {
			case "file_added", "file_deleted", "file_renamed":
				reasons = append(reasons, reason)
			case "parser_or_quality_changed":
				reasons = append(reasons, "parser_or_quality_changed")
			case "semantic_or_structure_facts_changed", "generated_status_changed", "member_status_changed", "source_file_identity_replaced":
				reasons = append(reasons, "source_structure_changed")
			}
		}
	}
	if moduleChanged {
		reasons = append(reasons, "module_inventory_changed")
	}
	if relationDeltaCount > 0 {
		reasons = append(reasons, "static_relations_changed")
	}
	return compactSortedStrings(reasons)
}

func impactFactIsInterfaceConfig(fact types.ParsedSourceFact) bool {
	switch fact.Kind {
	case "api_prefix", "api_proxy", "spring_mapping", "java_type", "java_method", "java_mapper_method",
		"mybatis_mapper", "mybatis_statement", "mybatis_result_map", "mybatis_sql_fragment", "sql_table":
		return true
	default:
		return false
	}
}

func impactModuleFactSignal(fact types.ParsedSourceFact) bool {
	switch fact.Kind {
	case "spring_mapping", "mybatis_mapper", "mybatis_statement", "mybatis_result_map":
		return true
	case "java_type":
		name := strings.ToLower(fact.Name)
		for _, suffix := range []string{"controller", "service", "serviceimpl", "mapper", "repository", "configuration", "config", "application"} {
			if strings.HasSuffix(name, suffix) {
				return true
			}
		}
	}
	return false
}

func impactModuleFactCandidate(fact types.ParsedSourceFact) bool {
	if fact.Quality != "" && fact.Quality != "structural" {
		return false
	}
	return impactModuleFactSignal(fact)
}

func impactFlowEvidenceFact(fact types.ParsedSourceFact) bool {
	switch fact.Kind {
	case "api_request", "api_prefix", "api_proxy", "spring_mapping":
		return true
	default:
		return false
	}
}

func sourceWikiImpactMemberHasFact(index *sourceWikiImpactSnapshotIndex, ref types.SourceRelationFactRef) bool {
	return index.factEvidenceByFileID[ref.FileID][sourceWikiImpactFactIdentity{Kind: ref.Kind, Quality: ref.Quality, Range: ref.Range}]
}

func canonicalImpactRoute(value string) string {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) != 2 || !strings.HasPrefix(fields[1], "/") {
		return ""
	}
	return strings.ToUpper(fields[0]) + " " + fields[1]
}

func validSourceWikiImpactPath(value string) bool {
	if value == "" || strings.Contains(value, "\\") || strings.HasPrefix(value, "/") {
		return false
	}
	clean := path.Clean(value)
	return clean == value && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func validSourceWikiImpactModulePath(value string) bool {
	if value == "." {
		return true
	}
	return validSourceWikiImpactPath(value)
}

func validSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sourceWikiImpactFactSizeBound(fact types.ParsedSourceFact) int {
	const capAt = int64(sourceWikiImpactMaxFactBytes + 1)
	bound := int64(512)
	add := func(value string) {
		length := int64(len(value))
		if length > (capAt-bound)/6 {
			bound = capAt
			return
		}
		bound += length * 6
	}
	for _, value := range []string{
		fact.Kind, fact.Name, fact.Namespace, fact.RoutePath, fact.HTTPMethod, fact.StatementType,
		fact.StatementID, fact.MethodName, fact.Receiver, fact.TypeName, fact.SQL, fact.TargetNamespace,
		fact.TargetName, fact.OwnerKind, fact.OwnerName, fact.ReferenceKind, fact.Certainty, fact.Reason,
		fact.Quality, fact.Text,
	} {
		add(value)
	}
	for _, values := range [][]string{fact.HTTPMethods, fact.ParameterTypes, fact.ResultMapRefs, fact.IncludeRefs, fact.SuperTypes} {
		count := int64(len(values))
		if count > (capAt-bound)/32 {
			bound = capAt
			return int(capAt)
		}
		bound += count * 32
		for _, value := range values {
			add(value)
		}
	}
	if bound >= capAt {
		return int(capAt)
	}
	return int(bound)
}

func validSourceWikiImpactDigest(value string, requireSHA256 bool) bool {
	if requireSHA256 {
		return validSHA256(value)
	}
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sha256Hex(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func addSourceWikiImpactReason(changes map[string][]string, filePath, reason string) {
	if filePath == "" || reason == "" {
		return
	}
	changes[filePath] = append(changes[filePath], reason)
	changes[filePath] = compactSortedStrings(changes[filePath])
}

func appendAffectedTopic(plan *types.SourceWikiImpactPlan, topicKey string, reasons ...string) {
	plan.Affected = append(plan.Affected, types.SourceWikiAffectedTopic{TopicKey: topicKey, Reasons: compactSortedStrings(reasons)})
}

func sortedBoolKeys(values map[string]bool) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedTopicKeys(values map[string]types.SourceWikiImpactTopicDependencies) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func mergeSortedStrings(first, second []string) []string {
	merged := append(append([]string(nil), first...), second...)
	return compactSortedStrings(merged)
}

func compactSortedStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

func sameStrings(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for i := range first {
		if first[i] != second[i] {
			return false
		}
	}
	return true
}

func sameStringSet(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	a := append([]string(nil), first...)
	b := append([]string(nil), second...)
	sort.Strings(a)
	sort.Strings(b)
	return sameStrings(a, b)
}
