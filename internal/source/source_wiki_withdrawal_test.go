package source

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWithdrawSourceWikiCurrentReprojectsOnlyIndependentSurvivors(t *testing.T) {
	input := mixedWikiWithdrawalInput(types.SourceWikiProjectionCurrent)
	input.Page.Content = "Source A body.\nSource B body."
	before := cloneWikiWithdrawalInputForTest(input)

	got, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalReproject, got.Disposition)
	require.Len(t, got.Entries, 1)
	require.Equal(t, "source-b", got.Entries[0].Contribution.SourceID)
	require.NotNil(t, got.Page)
	require.Equal(t, "Source B title", got.Page.Title)
	require.Equal(t, "Source B summary", got.Page.Summary)
	require.Equal(t, "Source B body.", got.Page.Content)
	require.Equal(t, []string{"file-b|pkg/B.go"}, got.Page.SourceRefs)
	require.Equal(t, "source-b", got.Page.Provenance.SourceID)
	require.Equal(t, "pkg/B", got.Page.Provenance.ModulePath)
	require.NotContains(t, got.Page.Title, "A")
	require.NotContains(t, got.Page.Summary, "A")
	require.NotContains(t, got.Page.Content, "Source A")
	require.NotContains(t, got.Page.Provenance.ModulePath, "A")
	require.Equal(t, before, input, "withdrawal must not mutate the input")
}

func TestWithdrawSourceWikiRevisionWithTargetWithdrawsWholeRevision(t *testing.T) {
	input := mixedWikiWithdrawalInput(types.SourceWikiProjectionRevision)
	input.Page.Content = "Source B body.\nSource A body."

	got, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalWithdrawPage, got.Disposition)
	require.Equal(t, types.SourceWikiWithdrawalReasonHistoricalOwner, got.Reason)
	require.Nil(t, got.Page)
	require.Empty(t, got.Entries, "a historical A+B revision cannot be rewritten into a B-only revision")
}

func TestWithdrawSourceWikiLeavesVerifiedBOnlyHistoryUnchanged(t *testing.T) {
	input := mixedWikiWithdrawalInput(types.SourceWikiProjectionRevision)
	input.SourceID = "source-a"
	input.Entries = input.Entries[:1]
	input.Page = withdrawalPageProjection(input.Entries[0])

	got, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalUnchanged, got.Disposition)
	require.Empty(t, got.Reason)
	require.Equal(t, input.Page, *got.Page)
	require.Len(t, got.Entries, 1)
	require.Equal(t, "source-b", got.Entries[0].Contribution.SourceID)
}

func TestWithdrawSourceWikiRemovesAllTopicsAndWithdrawsWhenNoCurrentSurvivor(t *testing.T) {
	input := mixedWikiWithdrawalInput(types.SourceWikiProjectionCurrent)
	firstA := input.Entries[1]
	secondA := wikiWithdrawalEntry(
		wikiContribution("source-a", "system", "overview", "snapshot-a", "snapshot-a", "A overview.", types.SourceWikiContributionCurrent,
			contributionEvidence("e001", "source-a", "snapshot-a", "file-a2", "version-a2", "pkg/A2.go")),
		"A overview", "A overview summary", "pkg/A2",
	)
	input.Entries = append(input.Entries, secondA)
	input.Page.Content = strings.Join([]string{firstA.Contribution.Body, secondA.Contribution.Body, input.Entries[0].Contribution.Body}, "\n")
	input.Page.SourceRefs = []string{"file-a|pkg/A.go", "file-a2|pkg/A2.go", "file-b|pkg/B.go"}
	input.Page.Provenance = &firstA.Provenance

	got, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalReproject, got.Disposition)
	require.Len(t, got.Entries, 1, "withdrawal removes every topic owned by source A")
	require.Equal(t, "source-b", got.Entries[0].Contribution.SourceID)
	require.Equal(t, "Source B body.", got.Page.Content)

	onlyA := input
	onlyA.Entries = []types.SourceWikiWithdrawalEntry{firstA}
	onlyA.Page = withdrawalPageProjection(firstA)
	withdrawn, err := WithdrawSourceWikiContributions(onlyA)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalWithdrawPage, withdrawn.Disposition)
	require.Equal(t, types.SourceWikiWithdrawalReasonNoSurvivingContribution, withdrawn.Reason)
	require.Nil(t, withdrawn.Page)
	require.Empty(t, withdrawn.Entries)
}

func TestWithdrawSourceWikiSafelyWithdrawsUnattributedOrUnverifiableProjection(t *testing.T) {
	base := mixedWikiWithdrawalInput(types.SourceWikiProjectionCurrent)
	tests := []struct {
		name   string
		mutate func(*types.SourceWikiWithdrawalInput)
	}{
		{name: "unattributed body or owner", mutate: func(input *types.SourceWikiWithdrawalInput) { input.HasUnattributedBody = true }},
		{name: "body is not exact contribution composition", mutate: func(input *types.SourceWikiWithdrawalInput) { input.Page.Content += " manual text" }},
		{name: "source refs do not match entries", mutate: func(input *types.SourceWikiWithdrawalInput) { input.Page.SourceRefs = []string{"file-b|pkg/B.go"} }},
		{name: "primary provenance has no unique entry", mutate: func(input *types.SourceWikiWithdrawalInput) {
			input.Page.Provenance.SourceID = "missing-source"
		}},
		{name: "entry provenance disagrees with evidence", mutate: func(input *types.SourceWikiWithdrawalInput) {
			input.Entries[0].Provenance.Evidence[0].ID = "different-local-id"
		}},
		{name: "duplicate source topic identity", mutate: func(input *types.SourceWikiWithdrawalInput) {
			input.Entries = append(input.Entries, input.Entries[1])
		}},
		{name: "page body exceeds established bound", mutate: func(input *types.SourceWikiWithdrawalInput) {
			input.Page.Content = strings.Repeat("x", types.SourceWikiContributionMaxTotalBodyBytes+1)
		}},
		{name: "entry refs exceed deterministic metadata bound", mutate: func(input *types.SourceWikiWithdrawalInput) {
			input.Entries[0].SourceRefs = make([]string, sourceWikiWithdrawalMaxMetadataItems+1)
		}},
		{name: "over contribution bound", mutate: func(input *types.SourceWikiWithdrawalInput) {
			entry := input.Entries[0]
			input.Entries = make([]types.SourceWikiWithdrawalEntry, types.SourceWikiContributionMaxCount+1)
			for i := range input.Entries {
				input.Entries[i] = entry
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneWikiWithdrawalInputForTest(base)
			test.mutate(&input)
			got, err := WithdrawSourceWikiContributions(input)
			require.NoError(t, err, "content uncertainty is a safe withdrawal decision, not a retryable scope error")
			require.Equal(t, types.SourceWikiWithdrawalWithdrawPage, got.Disposition)
			require.NotEmpty(t, got.Reason)
			require.Nil(t, got.Page)
			require.Empty(t, got.Entries, "unsafe results expose no partial survivor set")
		})
	}
}

func TestWithdrawSourceWikiScopeErrorsAreTypedAndProjectionKindIsRequired(t *testing.T) {
	base := mixedWikiWithdrawalInput(types.SourceWikiProjectionCurrent)
	tests := []struct {
		name   string
		mutate func(*types.SourceWikiWithdrawalInput)
	}{
		{name: "missing tenant", mutate: func(input *types.SourceWikiWithdrawalInput) { input.TenantID = 0 }},
		{name: "missing knowledge base", mutate: func(input *types.SourceWikiWithdrawalInput) { input.KnowledgeBaseID = " " }},
		{name: "missing page", mutate: func(input *types.SourceWikiWithdrawalInput) { input.PageID = "" }},
		{name: "missing source", mutate: func(input *types.SourceWikiWithdrawalInput) { input.SourceID = "" }},
		{name: "projection kind must be explicit", mutate: func(input *types.SourceWikiWithdrawalInput) { input.ProjectionKind = "" }},
		{name: "unknown projection kind", mutate: func(input *types.SourceWikiWithdrawalInput) { input.ProjectionKind = "latest-ish" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := cloneWikiWithdrawalInputForTest(base)
			test.mutate(&input)
			got, err := WithdrawSourceWikiContributions(input)
			require.Error(t, err)
			require.True(t, errors.Is(err, ErrSourceWikiWithdrawalScope))
			require.Equal(t, types.SourceWikiWithdrawalResult{}, got)
		})
	}
}

func TestWithdrawSourceWikiOutputIsStableAndAcceptsEqualLocalEvidenceIDsAcrossSources(t *testing.T) {
	input := mixedWikiWithdrawalInput(types.SourceWikiProjectionCurrent)
	input.SourceID = "not-present"
	input.Page.Content = "Source A body.\nSource B body."
	input.Entries[0], input.Entries[1] = input.Entries[1], input.Entries[0]
	before := cloneWikiWithdrawalInputForTest(input)

	first, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	second, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, before, input)
	require.Equal(t, "e001", first.Entries[0].Contribution.Evidence[0].ID)
	require.Equal(t, "e001", first.Entries[1].Contribution.Evidence[0].ID)
}

func mixedWikiWithdrawalInput(kind types.SourceWikiProjectionKind) types.SourceWikiWithdrawalInput {
	a := wikiWithdrawalEntry(
		wikiContribution("source-a", "flow", "checkout", "snapshot-a", "snapshot-a", "Source A body.", types.SourceWikiContributionCurrent,
			contributionEvidence("e001", "source-a", "snapshot-a", "file-a", "version-a", "pkg/A.go")),
		"Source A title", "Source A summary", "pkg/A",
	)
	b := wikiWithdrawalEntry(
		wikiContribution("source-b", "flow", "checkout", "snapshot-b", "snapshot-b", "Source B body.", types.SourceWikiContributionCurrent,
			contributionEvidence("e001", "source-b", "snapshot-b", "file-b", "version-b", "pkg/B.go")),
		"Source B title", "Source B summary", "pkg/B",
	)
	return types.SourceWikiWithdrawalInput{
		TenantID: 7, KnowledgeBaseID: "kb-1", PageID: "page-1", SourceID: "source-a",
		ProjectionKind: kind, Entries: []types.SourceWikiWithdrawalEntry{b, a},
		Page: types.SourceWikiWithdrawalProjection{
			Title: a.Title, Summary: a.Summary, Content: "Source B body.\nSource A body.",
			SourceRefs: []string{"file-a|pkg/A.go", "file-b|pkg/B.go"}, Provenance: &a.Provenance,
		},
	}
}

func wikiWithdrawalEntry(contribution types.SourceWikiContribution, title, summary, modulePath string) types.SourceWikiWithdrawalEntry {
	state := "ready"
	if contribution.State == types.SourceWikiContributionStale {
		state = "stale"
	}
	refs := make([]string, 0, len(contribution.Evidence))
	for _, evidence := range contribution.Evidence {
		refs = append(refs, evidence.KnowledgeID+"|"+evidence.Path)
	}
	return types.SourceWikiWithdrawalEntry{
		Contribution: contribution, Title: title, Summary: summary,
		SourceRefs: refs,
		Provenance: types.SourceWikiProvenance{
			SourceID: contribution.SourceID, TopicKind: contribution.TopicKind, TopicKey: contribution.TopicKey,
			ModulePath: modulePath, State: state, ApplicableSnapshotID: contribution.ApplicableSnapshotID,
			Evidence: append([]types.SourceWikiEvidence(nil), contribution.Evidence...),
		},
	}
}

func withdrawalPageProjection(entry types.SourceWikiWithdrawalEntry) types.SourceWikiWithdrawalProjection {
	return types.SourceWikiWithdrawalProjection{
		Title: entry.Title, Summary: entry.Summary, Content: entry.Contribution.Body,
		SourceRefs: append([]string(nil), entry.SourceRefs...), Provenance: &entry.Provenance,
	}
}

func cloneWikiWithdrawalInputForTest(input types.SourceWikiWithdrawalInput) types.SourceWikiWithdrawalInput {
	input.Entries = cloneSourceWikiWithdrawalEntriesForTest(input.Entries)
	input.Page.SourceRefs = append([]string(nil), input.Page.SourceRefs...)
	if input.Page.Provenance != nil {
		copy := *input.Page.Provenance
		copy.Evidence = append([]types.SourceWikiEvidence(nil), input.Page.Provenance.Evidence...)
		input.Page.Provenance = &copy
	}
	return input
}

func cloneSourceWikiWithdrawalEntriesForTest(entries []types.SourceWikiWithdrawalEntry) []types.SourceWikiWithdrawalEntry {
	result := make([]types.SourceWikiWithdrawalEntry, len(entries))
	for i, entry := range entries {
		result[i] = entry
		result[i].Contribution = cloneSourceWikiContribution(entry.Contribution)
		result[i].SourceRefs = append([]string(nil), entry.SourceRefs...)
		result[i].Provenance.Evidence = append([]types.SourceWikiEvidence(nil), entry.Provenance.Evidence...)
	}
	return result
}

func TestWithdrawSourceWikiSurvivorProjectionUsesStableIdentityOrder(t *testing.T) {
	input := mixedWikiWithdrawalInput(types.SourceWikiProjectionCurrent)
	c := wikiWithdrawalEntry(
		wikiContribution("source-c", "system", "overview", "snapshot-c", "snapshot-c", "Source C body.", types.SourceWikiContributionCurrent,
			contributionEvidence("e-c", "source-c", "snapshot-c", "file-c", "version-c", "README.md")),
		"Source C title", "Source C summary", "README",
	)
	input.Entries = append(input.Entries, c)
	input.Page.Content = "Source A body.\nSource B body.\nSource C body."
	input.Page.SourceRefs = []string{"file-a|pkg/A.go", "file-b|pkg/B.go", "file-c|README.md"}

	got, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalReproject, got.Disposition)
	require.Equal(t, []string{"source-b", "source-c"}, []string{
		got.Entries[0].Contribution.SourceID, got.Entries[1].Contribution.SourceID,
	})
	require.Equal(t, "Source B title", got.Page.Title)
	require.Equal(t, "Source B body.\nSource C body.", got.Page.Content)
	require.True(t, reflect.DeepEqual([]string{"file-b|pkg/B.go", "file-c|README.md"}, got.Page.SourceRefs))
	require.NotContains(t, strings.Join(got.Page.SourceRefs, "\n"), "file-a")
}

func TestWithdrawSourceWikiBoundsAmbiguousPrefixCompositionWork(t *testing.T) {
	input := types.SourceWikiWithdrawalInput{
		TenantID: 7, KnowledgeBaseID: "kb-1", PageID: "page-prefix",
		SourceID: "source-absent", ProjectionKind: types.SourceWikiProjectionCurrent,
	}
	sharedPrefix := strings.Repeat("a", 200_000)
	var reverseBodies []string
	for i := 0; i < 16; i++ {
		sourceID := fmt.Sprintf("source-%02d", i)
		snapshotID := "snapshot-" + sourceID
		body := sharedPrefix + fmt.Sprintf("%02d", i)
		entry := wikiWithdrawalEntry(
			wikiContribution(sourceID, "module", "topic", snapshotID, snapshotID, body, types.SourceWikiContributionCurrent,
				contributionEvidence("e001", sourceID, snapshotID, "file-"+sourceID, "version-"+sourceID, "src/"+sourceID+".go")),
			"Title "+sourceID, "Summary "+sourceID, "src/"+sourceID,
		)
		input.Entries = append(input.Entries, entry)
		input.Page.SourceRefs = append(input.Page.SourceRefs, entry.SourceRefs...)
		reverseBodies = append([]string{body}, reverseBodies...)
	}
	input.Page.Title = input.Entries[0].Title
	input.Page.Summary = input.Entries[0].Summary
	input.Page.Provenance = &input.Entries[0].Provenance
	input.Page.Content = strings.Join(reverseBodies, "\n")

	got, err := WithdrawSourceWikiContributions(input)
	require.NoError(t, err)
	require.Equal(t, types.SourceWikiWithdrawalWithdrawPage, got.Disposition)
	require.Equal(t, types.SourceWikiWithdrawalReasonProjectionMismatch, got.Reason)
	require.Nil(t, got.Page)
	require.Empty(t, got.Entries)
}
