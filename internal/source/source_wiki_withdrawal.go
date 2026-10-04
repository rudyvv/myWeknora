package source

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// ErrSourceWikiWithdrawalScope identifies an invalid operation scope. Content
// or attribution uncertainty is not an operational error; it returns a safe
// whole-page withdrawal decision instead.
var ErrSourceWikiWithdrawalScope = errors.New("source Wiki withdrawal scope is invalid")

const (
	sourceWikiWithdrawalMaxCompositionStates       = 8_192
	sourceWikiWithdrawalMaxCompositionCompareBytes = 8 << 20
	sourceWikiWithdrawalMaxMetadataBytes           = 16 << 20
	sourceWikiWithdrawalMaxMetadataItems           = 262_144
)

// WithdrawSourceWikiContributions removes every contribution owned by one
// source from a complete current-page or retained-revision projection. The
// caller supplies an already owner-scoped, complete entry set; this pure
// module does not discover owners, grant permissions, read persistence, or pin
// raw evidence. An absent target is unchanged only when the caller has already
// proved owner completeness and marked unknown affected body/owners as
// unattributed.
//
// Invalid scope returns ErrSourceWikiWithdrawalScope. Invalid or ambiguous
// content returns WithdrawPage with no partial entries. A revision containing
// the target source is always withdrawn in full; only a current page may be
// reprojected from surviving independent contributions.
func WithdrawSourceWikiContributions(input types.SourceWikiWithdrawalInput) (types.SourceWikiWithdrawalResult, error) {
	if input.TenantID == 0 || strings.TrimSpace(input.KnowledgeBaseID) == "" ||
		strings.TrimSpace(input.PageID) == "" || strings.TrimSpace(input.SourceID) == "" {
		return types.SourceWikiWithdrawalResult{}, fmt.Errorf("%w: tenant, knowledge base, page, and source are required", ErrSourceWikiWithdrawalScope)
	}
	if input.ProjectionKind != types.SourceWikiProjectionCurrent && input.ProjectionKind != types.SourceWikiProjectionRevision {
		return types.SourceWikiWithdrawalResult{}, fmt.Errorf("%w: projection kind must be current or revision", ErrSourceWikiWithdrawalScope)
	}
	if input.HasUnattributedBody {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonUnattributed), nil
	}
	if len(input.Entries) > types.SourceWikiContributionMaxCount {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonInvalidEntries), nil
	}
	if len(input.Page.Content) > types.SourceWikiContributionMaxTotalBodyBytes ||
		len(input.Page.SourceRefs) > types.SourceWikiContributionMaxCount*types.SourceWikiContributionMaxEvidencePerItem {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonInvalidEntries), nil
	}

	contributions := make([]types.SourceWikiContribution, len(input.Entries))
	for i, entry := range input.Entries {
		contributions[i] = entry.Contribution
	}
	set := types.SourceWikiContributionSet{
		TenantID: input.TenantID, KnowledgeBaseID: input.KnowledgeBaseID,
		Contributions: contributions,
	}
	if err := validateSourceWikiContributionSet(set); err != nil {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonInvalidEntries), nil
	}
	if !sourceWikiWithdrawalMetadataWithinBounds(input) {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonInvalidEntries), nil
	}
	for _, entry := range input.Entries {
		if !sourceWikiWithdrawalEntryValid(entry) {
			return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonInvalidEntries), nil
		}
	}
	if !sourceWikiWithdrawalProjectionValid(input.Page, input.Entries) {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonProjectionMismatch), nil
	}

	clonedSet := cloneSourceWikiContributionSet(set)
	entries := cloneSourceWikiWithdrawalEntries(input.Entries, clonedSet.Contributions)
	targetFound := false
	for _, entry := range entries {
		if entry.Contribution.SourceID == input.SourceID {
			targetFound = true
			break
		}
	}
	if !targetFound {
		page := cloneSourceWikiWithdrawalProjection(input.Page)
		sortSourceWikiWithdrawalEntries(entries)
		return types.SourceWikiWithdrawalResult{
			Disposition: types.SourceWikiWithdrawalUnchanged,
			Page:        &page,
			Entries:     entries,
		}, nil
	}
	if input.ProjectionKind == types.SourceWikiProjectionRevision {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonHistoricalOwner), nil
	}

	remaining := make([]types.SourceWikiWithdrawalEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Contribution.SourceID != input.SourceID {
			remaining = append(remaining, entry)
		}
	}
	if len(remaining) == 0 {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonNoSurvivingContribution), nil
	}
	sortSourceWikiWithdrawalEntries(remaining)

	// The incoming primary is validated against its exact entry above, but the
	// surviving projection follows the established stable first-identity rule.
	page := sourceWikiWithdrawalProject(remaining, 0)
	if len(page.Content) > types.SourceWikiContributionMaxTotalBodyBytes {
		return sourceWikiWithdrawalDecision(types.SourceWikiWithdrawalReasonInvalidEntries), nil
	}
	return types.SourceWikiWithdrawalResult{
		Disposition: types.SourceWikiWithdrawalReproject,
		Page:        &page,
		Entries:     remaining,
	}, nil
}

func sourceWikiWithdrawalDecision(reason types.SourceWikiWithdrawalReason) types.SourceWikiWithdrawalResult {
	return types.SourceWikiWithdrawalResult{
		Disposition: types.SourceWikiWithdrawalWithdrawPage,
		Reason:      reason,
	}
}

func sourceWikiWithdrawalEntryValid(entry types.SourceWikiWithdrawalEntry) bool {
	contribution := entry.Contribution
	provenance := entry.Provenance
	wantState := "ready"
	if contribution.State == types.SourceWikiContributionStale {
		wantState = "stale"
	}
	if entry.Title == "" || entry.SourceRefs == nil || len(entry.SourceRefs) > types.SourceWikiContributionMaxEvidencePerItem ||
		provenance.SourceID != contribution.SourceID ||
		provenance.TopicKind != contribution.TopicKind || provenance.TopicKey != contribution.TopicKey ||
		provenance.ApplicableSnapshotID != contribution.ApplicableSnapshotID || provenance.State != wantState ||
		!reflect.DeepEqual(provenance.Evidence, contribution.Evidence) {
		return false
	}
	refs := make([]string, 0, len(contribution.Evidence))
	for _, evidence := range contribution.Evidence {
		refs = append(refs, evidence.KnowledgeID+"|"+evidence.Path)
	}
	return sourceWikiWithdrawalIDsEqual(entry.SourceRefs, refs)
}

func sourceWikiWithdrawalMetadataWithinBounds(input types.SourceWikiWithdrawalInput) bool {
	bytesUsed, itemsUsed := 0, 0
	addString := func(value string) bool {
		bytesUsed += len(value)
		return bytesUsed <= sourceWikiWithdrawalMaxMetadataBytes
	}
	addItems := func(count int) bool {
		itemsUsed += count
		return itemsUsed <= sourceWikiWithdrawalMaxMetadataItems
	}
	addEvidence := func(evidence types.SourceWikiEvidence) bool {
		for _, value := range []string{
			evidence.ID, evidence.KnowledgeID, evidence.DataSourceID, evidence.SnapshotID,
			evidence.FileVersionID, evidence.ProjectID, evidence.CommitSHA, evidence.Path,
			evidence.Quality, evidence.GitLabURL, evidence.SHA256, evidence.TextSHA256,
		} {
			if !addString(value) {
				return false
			}
		}
		if !addItems(len(evidence.Symbols) + len(evidence.Context) + len(evidence.Diagnostics)) {
			return false
		}
		for _, value := range evidence.Symbols {
			if !addString(value) {
				return false
			}
		}
		for _, context := range evidence.Context {
			if !addString(context.Text) {
				return false
			}
		}
		for _, diagnostic := range evidence.Diagnostics {
			if !addString(diagnostic.Code) {
				return false
			}
		}
		if evidence.Region != nil {
			for _, value := range []string{evidence.Region.Kind, evidence.Region.Language, evidence.Region.Quality,
				evidence.Region.ExternalSource, evidence.Region.ExternalStatus, evidence.Region.ResolvedPath} {
				if !addString(value) {
					return false
				}
			}
		}
		return true
	}
	addProvenance := func(provenance types.SourceWikiProvenance) bool {
		for _, value := range []string{provenance.SourceID, provenance.TopicKind, provenance.TopicKey,
			provenance.ModulePath, provenance.State, provenance.ApplicableSnapshotID} {
			if !addString(value) {
				return false
			}
		}
		if !addItems(len(provenance.Evidence)) {
			return false
		}
		for _, evidence := range provenance.Evidence {
			if !addEvidence(evidence) {
				return false
			}
		}
		return true
	}
	if !addString(input.Page.Title) || !addString(input.Page.Summary) || !addItems(len(input.Page.SourceRefs)) {
		return false
	}
	for _, ref := range input.Page.SourceRefs {
		if !addString(ref) {
			return false
		}
	}
	if input.Page.Provenance != nil && !addProvenance(*input.Page.Provenance) {
		return false
	}
	for _, entry := range input.Entries {
		for _, value := range []string{entry.Title, entry.Summary, entry.Contribution.SourceID,
			entry.Contribution.TopicKind, entry.Contribution.TopicKey, entry.Contribution.OriginSnapshotID,
			entry.Contribution.ApplicableSnapshotID, entry.Contribution.FailureReason} {
			if !addString(value) {
				return false
			}
		}
		if !addItems(len(entry.SourceRefs)) {
			return false
		}
		for _, ref := range entry.SourceRefs {
			if !addString(ref) {
				return false
			}
		}
		if !addItems(len(entry.Contribution.Evidence)) {
			return false
		}
		for _, evidence := range entry.Contribution.Evidence {
			if !addEvidence(evidence) {
				return false
			}
		}
		if !addProvenance(entry.Provenance) {
			return false
		}
	}
	return true
}

func sourceWikiWithdrawalProjectionValid(page types.SourceWikiWithdrawalProjection, entries []types.SourceWikiWithdrawalEntry) bool {
	if page.Provenance == nil || page.SourceRefs == nil || len(entries) == 0 {
		return false
	}
	primaryIndex := -1
	bodyParts := make([]string, 0, len(entries))
	allRefs := make([]string, 0)
	for i, entry := range entries {
		bodyParts = append(bodyParts, entry.Contribution.Body)
		allRefs = append(allRefs, entry.SourceRefs...)
		if sameSourceWikiWithdrawalIdentity(entry.Contribution, *page.Provenance) {
			if primaryIndex >= 0 {
				return false
			}
			primaryIndex = i
		}
	}
	if primaryIndex < 0 {
		return false
	}
	primary := entries[primaryIndex]
	if page.Title != primary.Title || page.Summary != primary.Summary ||
		!reflect.DeepEqual(page.Provenance, &primary.Provenance) ||
		!sourceWikiWithdrawalIDsEqual(page.SourceRefs, allRefs) {
		return false
	}
	return sourceWikiWithdrawalBodyCompositionMatches(page.Content, bodyParts)
}

func sourceWikiWithdrawalProject(entries []types.SourceWikiWithdrawalEntry, primary int) types.SourceWikiWithdrawalProjection {
	bodies := make([]string, 0, len(entries))
	refs := make([]string, 0)
	for _, entry := range entries {
		bodies = append(bodies, entry.Contribution.Body)
		refs = append(refs, entry.SourceRefs...)
	}
	if len(bodies) > 16 {
		sort.Strings(bodies)
	}
	chosen := entries[primary]
	provenance := cloneSourceWikiWithdrawalProvenance(chosen.Provenance)
	return types.SourceWikiWithdrawalProjection{
		Title: chosen.Title, Summary: chosen.Summary,
		Content: strings.Join(bodies, "\n"), SourceRefs: sortedSourceWikiWithdrawalIDs(refs),
		Provenance: &provenance,
	}
}

func cloneSourceWikiWithdrawalEntries(entries []types.SourceWikiWithdrawalEntry, clonedContributions []types.SourceWikiContribution) []types.SourceWikiWithdrawalEntry {
	result := make([]types.SourceWikiWithdrawalEntry, len(entries))
	for i, entry := range entries {
		result[i] = entry
		result[i].Contribution = clonedContributions[i]
		result[i].SourceRefs = append([]string(nil), entry.SourceRefs...)
		result[i].Provenance = cloneSourceWikiWithdrawalProvenance(entry.Provenance)
	}
	return result
}

func cloneSourceWikiWithdrawalProjection(page types.SourceWikiWithdrawalProjection) types.SourceWikiWithdrawalProjection {
	page.SourceRefs = append([]string(nil), page.SourceRefs...)
	if page.Provenance != nil {
		provenance := cloneSourceWikiWithdrawalProvenance(*page.Provenance)
		page.Provenance = &provenance
	}
	return page
}

func cloneSourceWikiWithdrawalProvenance(provenance types.SourceWikiProvenance) types.SourceWikiProvenance {
	cloned := cloneSourceWikiContribution(types.SourceWikiContribution{Evidence: provenance.Evidence})
	provenance.Evidence = cloned.Evidence
	return provenance
}

func sameSourceWikiWithdrawalIdentity(contribution types.SourceWikiContribution, provenance types.SourceWikiProvenance) bool {
	return contribution.SourceID == provenance.SourceID && contribution.TopicKind == provenance.TopicKind && contribution.TopicKey == provenance.TopicKey
}

func sortSourceWikiWithdrawalEntries(entries []types.SourceWikiWithdrawalEntry) {
	sort.Slice(entries, func(i, j int) bool {
		a, b := entries[i].Contribution, entries[j].Contribution
		if a.SourceID != b.SourceID {
			return a.SourceID < b.SourceID
		}
		if a.TopicKind != b.TopicKind {
			return a.TopicKind < b.TopicKind
		}
		return a.TopicKey < b.TopicKey
	})
}

func sourceWikiWithdrawalIDsEqual(a, b []string) bool {
	return equalSourceWikiWithdrawalIDs(sortedSourceWikiWithdrawalIDs(a), sortedSourceWikiWithdrawalIDs(b))
}

func sortedSourceWikiWithdrawalIDs(ids []string) []string {
	result := append([]string(nil), ids...)
	sort.Strings(result)
	write := 0
	for _, id := range result {
		if id == "" || write > 0 && result[write-1] == id {
			continue
		}
		result[write] = id
		write++
	}
	return result[:write]
}

func equalSourceWikiWithdrawalIDs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// This matches the existing source Wiki page projection contract: at most 16
// contribution bodies may appear in any order, while larger projections use
// canonical lexical body order.
func sourceWikiWithdrawalBodyCompositionMatches(body string, parts []string) bool {
	if len(parts) == 0 || len(body) > types.SourceWikiContributionMaxTotalBodyBytes {
		return false
	}
	if len(parts) > 16 {
		canonical := append([]string(nil), parts...)
		sort.Strings(canonical)
		return body == strings.Join(canonical, "\n")
	}
	seen := make(map[string]bool)
	states, comparedBytes := 0, 0
	var match func(int, uint32, int) bool
	match = func(offset int, used uint32, count int) bool {
		if count == len(parts) {
			return offset == len(body)
		}
		state := fmt.Sprintf("%d/%d", offset, used)
		if seen[state] {
			return false
		}
		states++
		if states > sourceWikiWithdrawalMaxCompositionStates {
			return false
		}
		seen[state] = true
		for i, part := range parts {
			bit := uint32(1) << i
			if used&bit != 0 {
				continue
			}
			if len(part) > sourceWikiWithdrawalMaxCompositionCompareBytes-comparedBytes {
				return false
			}
			comparedBytes += len(part)
			if !strings.HasPrefix(body[offset:], part) {
				continue
			}
			next := offset + len(part)
			if count+1 == len(parts) {
				if match(next, used|bit, count+1) {
					return true
				}
			} else if next < len(body) && body[next] == '\n' && match(next+1, used|bit, count+1) {
				return true
			}
		}
		return false
	}
	return match(0, 0, 0)
}
