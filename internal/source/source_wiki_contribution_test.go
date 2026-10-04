package source

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func contributionEvidence(id, sourceID, snapshotID, fileID, versionID, path string) types.SourceWikiEvidence {
	return types.SourceWikiEvidence{
		ID: id, KnowledgeID: fileID, SHA256: strings.Repeat("a", 64), TextSHA256: strings.Repeat("b", 64),
		SourceEvidence: types.SourceEvidence{
			DataSourceID: sourceID, SnapshotID: snapshotID, FileVersionID: versionID, Path: path,
			CommitSHA: strings.Repeat("c", 40),
			Range:     types.SourceRange{StartByte: 3, EndByte: 18, StartLine: 2, EndLine: 3},
		},
	}
}

func wikiContribution(sourceID, kind, key, origin, applicable, body string, state types.SourceWikiContributionState, evidence ...types.SourceWikiEvidence) types.SourceWikiContribution {
	return types.SourceWikiContribution{
		SourceID: sourceID, TopicKind: kind, TopicKey: key,
		OriginSnapshotID: origin, ApplicableSnapshotID: applicable,
		Body: body, Evidence: evidence, State: state,
	}
}

func wikiContributionSet(contributions ...types.SourceWikiContribution) types.SourceWikiContributionSet {
	return types.SourceWikiContributionSet{TenantID: 7, KnowledgeBaseID: "kb-1", Contributions: contributions}
}

func wikiContributionTarget(sourceID, kind, key, snapshot string) types.SourceWikiContributionTarget {
	return types.SourceWikiContributionTarget{TenantID: 7, KnowledgeBaseID: "kb-1", SourceID: sourceID, TopicKind: kind, TopicKey: key, SnapshotID: snapshot}
}

func TestReplaceSourceWikiContributionReplacesOnlyMatchingSourceAndTopic(t *testing.T) {
	otherSource := wikiContribution("source-b", "flow", "checkout", "b-old", "b-old", "Independent source B body.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-b", "source-b", "b-old", "file-b", "b-version", "svc/B.java"))
	otherTopic := wikiContribution("source-a", "system", "overview", "a-old", "a-old", "Independent overview body.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-overview", "source-a", "a-old", "file-a", "a-version", "README.md"))
	oldTarget := wikiContribution("source-a", "flow", "checkout", "a-old", "a-old", "Old independent checkout body.", types.SourceWikiContributionStale,
		contributionEvidence("e-old", "source-a", "a-old", "file-a-old", "a-old-version", "svc/Old.java"))
	input := wikiContributionSet(otherSource, oldTarget, otherTopic)
	before := cloneWikiContributionSet(input)
	target := wikiContributionTarget("source-a", "flow", "checkout", "a-new")
	replacement := wikiContribution("source-a", "flow", "checkout", "a-new", "a-new", "Replacement checkout body.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-new", "source-a", "a-new", "file-a-new", "a-new-version", "svc/New.java"))

	got, err := ReplaceSourceWikiContribution(input, target, types.SourceWikiContributionOutcome{
		Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &replacement,
	})
	require.NoError(t, err)
	require.Len(t, got.Contributions, 3)
	require.Equal(t, replacement, got.Contributions[0])
	require.Equal(t, otherTopic, got.Contributions[1])
	require.Equal(t, otherSource, got.Contributions[2])
	require.Equal(t, []string{"e-new"}, evidenceIDsForContribution(t, got.Contributions[0]), "old evidence must not accumulate across replacement")
	require.Equal(t, before, input, "replacement must not mutate its input")
}

func TestReplaceSourceWikiContributionUnchangedAdvancesApplicabilityWithoutReplacingEvidence(t *testing.T) {
	oldEvidence := contributionEvidence("e-old", "source-a", "snapshot-old", "file-a", "version-old", "pkg/A.java")
	old := wikiContribution("source-a", "module", "pkg/A", "snapshot-old", "snapshot-old", "Verified old body.", types.SourceWikiContributionStale, oldEvidence)
	old.FailureReason = "last generation attempt failed"
	input := wikiContributionSet(old)
	target := wikiContributionTarget("source-a", "module", "pkg/A", "snapshot-new")

	got, err := ReplaceSourceWikiContribution(input, target, types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged})
	require.NoError(t, err)
	want := old
	want.ApplicableSnapshotID = "snapshot-new"
	want.State = types.SourceWikiContributionCurrent
	want.FailureReason = ""
	require.Equal(t, want, got.Contributions[0], "impact-verified unchanged content keeps exact origin evidence")
	require.Equal(t, "snapshot-old", got.Contributions[0].Evidence[0].SnapshotID)
	require.Equal(t, old, input.Contributions[0], "unchanged must not mutate its input")
}

func TestReplaceSourceWikiContributionFailurePreservesBodyButConfirmedDeletionRemovesExactTopic(t *testing.T) {
	first := wikiContribution("source-a", "flow", "checkout", "snapshot-old", "snapshot-old", "Last verified body.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-a", "source-a", "snapshot-old", "file-a", "version-a", "flows/Checkout.java"))
	second := wikiContribution("source-b", "flow", "checkout", "snapshot-b", "snapshot-b", "Other source body.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-b", "source-b", "snapshot-b", "file-b", "version-b", "flows/Checkout.java"))
	otherTopic := wikiContribution("source-a", "system", "overview", "snapshot-old", "snapshot-old", "Overview body.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-c", "source-a", "snapshot-old", "file-c", "version-c", "README.md"))
	input := wikiContributionSet(first, second, otherTopic)
	target := wikiContributionTarget("source-a", "flow", "checkout", "snapshot-new")

	failed, err := ReplaceSourceWikiContribution(input, target, types.SourceWikiContributionOutcome{
		Kind: types.SourceWikiContributionOutcomeGenerationFailed, FailureReason: "model generation failed",
	})
	require.NoError(t, err)
	wantFailed := first
	wantFailed.ApplicableSnapshotID = "snapshot-new"
	wantFailed.State = types.SourceWikiContributionStale
	wantFailed.FailureReason = "model generation failed"
	require.Equal(t, wantFailed, failed.Contributions[0])
	require.Equal(t, otherTopic, failed.Contributions[1])
	require.Equal(t, second, failed.Contributions[2])

	deleted, err := ReplaceSourceWikiContribution(input, target, types.SourceWikiContributionOutcome{
		Kind: types.SourceWikiContributionOutcomeConfirmedDeleted, CompleteSnapshotID: "snapshot-new",
	})
	require.NoError(t, err)
	require.Len(t, deleted.Contributions, 2)
	require.Equal(t, []types.SourceWikiContribution{otherTopic, second}, deleted.Contributions,
		"complete-snapshot deletion removes only the exact source/topic contribution")
	require.Equal(t, 3, len(input.Contributions), "neither outcome mutates the input")
}

func TestReplaceSourceWikiContributionRejectsUnattributedMixedBody(t *testing.T) {
	input := wikiContributionSet(wikiContribution("source-a", "module", "pkg/A", "snapshot-a", "snapshot-a", "Attributed body.",
		types.SourceWikiContributionCurrent, contributionEvidence("e-a", "source-a", "snapshot-a", "file-a", "v-a", "pkg/A.java")))
	input.HasUnattributedBody = true
	replacement := wikiContribution("source-a", "module", "pkg/A", "snapshot-b", "snapshot-b", "Replacement.",
		types.SourceWikiContributionCurrent, contributionEvidence("e-b", "source-a", "snapshot-b", "file-b", "v-b", "pkg/B.java"))

	got, err := ReplaceSourceWikiContribution(input, wikiContributionTarget("source-a", "module", "pkg/A", "snapshot-b"), types.SourceWikiContributionOutcome{
		Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &replacement,
	})
	require.ErrorContains(t, err, "unattributed")
	require.Equal(t, input, got, "unsafe mixed body must be returned unchanged for caller stale handling")
}

func TestReplaceSourceWikiContributionRejectsInvalidIdentityEvidenceAndOutcomes(t *testing.T) {
	validEvidence := contributionEvidence("e-1", "source-a", "snapshot-1", "file-a", "version-a", "pkg/A.java")
	valid := wikiContribution("source-a", "module", "pkg/A", "snapshot-1", "snapshot-1", "A body.", types.SourceWikiContributionCurrent, validEvidence)
	validSet := wikiContributionSet(valid)
	validTarget := wikiContributionTarget("source-a", "module", "pkg/A", "snapshot-2")

	wrongKB := validTarget
	wrongKB.KnowledgeBaseID = "kb-other"
	wrongTenant := validTarget
	wrongTenant.TenantID++
	wrongSourceReplacement := valid
	wrongSourceReplacement.SourceID = "source-b"
	wrongSnapshotReplacement := valid
	wrongSnapshotReplacement.OriginSnapshotID = "snapshot-1"
	wrongSnapshotReplacement.ApplicableSnapshotID = "snapshot-2"
	wrongEvidenceSnapshot := valid
	wrongEvidenceSnapshot.Evidence = []types.SourceWikiEvidence{contributionEvidence("e-wrong-snapshot", "source-a", "snapshot-other", "file-a", "version-a", "pkg/A.java")}
	duplicateEvidence := valid
	duplicateEvidence.Evidence = []types.SourceWikiEvidence{validEvidence, validEvidence}
	missingHash := valid
	missingHash.Evidence = []types.SourceWikiEvidence{validEvidence}
	missingHash.Evidence[0].TextSHA256 = ""
	wrongEvidenceSource := valid
	wrongEvidenceSource.Evidence = []types.SourceWikiEvidence{validEvidence}
	wrongEvidenceSource.Evidence[0].DataSourceID = "source-b"
	wrongEvidenceFile := valid
	wrongEvidenceFile.Evidence = []types.SourceWikiEvidence{validEvidence}
	wrongEvidenceFile.Evidence[0].KnowledgeID = ""
	wrongEvidenceVersion := valid
	wrongEvidenceVersion.Evidence = []types.SourceWikiEvidence{validEvidence}
	wrongEvidenceVersion.Evidence[0].FileVersionID = ""
	wrongEvidencePath := valid
	wrongEvidencePath.Evidence = []types.SourceWikiEvidence{validEvidence}
	wrongEvidencePath.Evidence[0].Path = ""
	wrongEvidenceRange := valid
	wrongEvidenceRange.Evidence = []types.SourceWikiEvidence{validEvidence}
	wrongEvidenceRange.Evidence[0].Range.EndByte = wrongEvidenceRange.Evidence[0].Range.StartByte
	wrongEvidenceCommit := valid
	wrongEvidenceCommit.Evidence = []types.SourceWikiEvidence{validEvidence}
	wrongEvidenceCommit.Evidence[0].CommitSHA = "not-a-commit"

	tests := []struct {
		name    string
		set     types.SourceWikiContributionSet
		target  types.SourceWikiContributionTarget
		outcome types.SourceWikiContributionOutcome
	}{
		{name: "tenant mismatch", set: validSet, target: wrongTenant, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "knowledge base mismatch", set: validSet, target: wrongKB, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "wrong replacement source", set: validSet, target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &wrongSourceReplacement}},
		{name: "replacement origin snapshot mismatch", set: validSet, target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &wrongSnapshotReplacement}},
		{name: "evidence origin snapshot mismatch", set: wikiContributionSet(wrongEvidenceSnapshot), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "evidence source mismatch", set: wikiContributionSet(wrongEvidenceSource), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "missing evidence file identity", set: wikiContributionSet(wrongEvidenceFile), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "missing evidence version identity", set: wikiContributionSet(wrongEvidenceVersion), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "missing evidence path identity", set: wikiContributionSet(wrongEvidencePath), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "evidence range invalid", set: wikiContributionSet(wrongEvidenceRange), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "evidence commit identity invalid", set: wikiContributionSet(wrongEvidenceCommit), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "duplicate evidence", set: wikiContributionSet(duplicateEvidence), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "missing evidence digest", set: wikiContributionSet(missingHash), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "duplicate source topic", set: wikiContributionSet(valid, valid), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "missing existing contribution", set: wikiContributionSet(), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged}},
		{name: "missing contribution cannot become stale on failure", set: wikiContributionSet(), target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeGenerationFailed, FailureReason: "model unavailable"}},
		{name: "invalid deletion proof", set: validSet, target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeConfirmedDeleted, CompleteSnapshotID: "snapshot-old"}},
		{name: "unknown outcome", set: validSet, target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: "guess"}},
		{name: "conflicting outcome payload", set: validSet, target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged, FailureReason: "unexpected"}},
		{name: "failed outcome without reason", set: validSet, target: validTarget, outcome: types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeGenerationFailed}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ReplaceSourceWikiContribution(test.set, test.target, test.outcome)
			require.Error(t, err)
			require.Equal(t, test.set, got, "invalid inputs do not produce a partially updated set")
		})
	}
}

func TestReplaceSourceWikiContributionSortsOutputAndLeavesAllInputsUntouched(t *testing.T) {
	first := wikiContribution("source-z", "flow", "z", "snap-z", "snap-z", "Z.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-z", "source-z", "snap-z", "file-z", "v-z", "z.go"))
	first.Evidence[0].Symbols = []string{"Controller.handle"}
	first.Evidence[0].Context = []types.SourceContext{{Text: "original context", Range: types.SourceRange{StartByte: 1, EndByte: 4, StartLine: 1, EndLine: 1}}}
	first.Evidence[0].Region = &types.SourceRegion{Kind: "script", Quality: "verified"}
	first.Evidence[0].Diagnostics = []types.SourceDiagnostic{{Code: "parser-note", Range: types.SourceRange{StartByte: 2, EndByte: 3, StartLine: 1, EndLine: 1}}}
	second := wikiContribution("source-a", "system", "overview", "snap-a", "snap-a", "A.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-a", "source-a", "snap-a", "file-a", "v-a", "a.go"))
	input := wikiContributionSet(first, second)
	before := cloneWikiContributionSet(input)
	replacement := wikiContribution("source-m", "module", "m", "snap-m", "snap-m", "M.", types.SourceWikiContributionCurrent,
		contributionEvidence("e-m", "source-m", "snap-m", "file-m", "v-m", "m.go"))
	target := wikiContributionTarget("source-m", "module", "m", "snap-m")

	got, err := ReplaceSourceWikiContribution(input, target, types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &replacement})
	require.NoError(t, err)
	require.Equal(t, []string{"source-a", "source-m", "source-z"}, []string{
		got.Contributions[0].SourceID, got.Contributions[1].SourceID, got.Contributions[2].SourceID,
	})
	require.Equal(t, before, input)
	require.Equal(t, second, got.Contributions[0])
	require.Equal(t, first, got.Contributions[2])
	got.Contributions[2].Evidence[0].Symbols[0] = "caller mutation"
	got.Contributions[2].Evidence[0].Context[0].Text = "caller mutation"
	got.Contributions[2].Evidence[0].Region.Kind = "caller mutation"
	got.Contributions[2].Evidence[0].Diagnostics[0].Code = "caller mutation"
	require.Equal(t, before.Contributions[0], input.Contributions[0],
		"returned nested evidence cannot alias and mutate the caller's input")
}

func TestReplaceSourceWikiContributionBoundsResultingBodySet(t *testing.T) {
	var contributions []types.SourceWikiContribution
	for _, sourceID := range []string{"source-a", "source-b", "source-c", "source-d"} {
		snapshot := "snapshot-" + sourceID
		contributions = append(contributions, wikiContribution(sourceID, "module", "topic", snapshot, snapshot,
			strings.Repeat("x", types.SourceWikiContributionMaxBodyBytes), types.SourceWikiContributionCurrent,
			contributionEvidence("e-"+sourceID, sourceID, snapshot, "file-"+sourceID, "version-"+sourceID, "pkg/File.java")))
	}
	input := wikiContributionSet(contributions...)
	replacement := wikiContribution("source-e", "module", "topic", "snapshot-e", "snapshot-e",
		strings.Repeat("y", types.SourceWikiContributionMaxBodyBytes), types.SourceWikiContributionCurrent,
		contributionEvidence("e-source-e", "source-e", "snapshot-e", "file-e", "version-e", "pkg/File.java"))

	got, err := ReplaceSourceWikiContribution(input, wikiContributionTarget("source-e", "module", "topic", "snapshot-e"), types.SourceWikiContributionOutcome{
		Kind: types.SourceWikiContributionOutcomeReplace, Replacement: &replacement,
	})
	require.ErrorContains(t, err, "total body limit")
	require.Equal(t, 4, len(got.Contributions), "an over-limit result is rejected atomically")
	require.Equal(t, 4, len(input.Contributions))
}

func cloneWikiContributionSet(input types.SourceWikiContributionSet) types.SourceWikiContributionSet {
	cloned := input
	cloned.Contributions = make([]types.SourceWikiContribution, len(input.Contributions))
	for i, contribution := range input.Contributions {
		cloned.Contributions[i] = contribution
		cloned.Contributions[i].Evidence = make([]types.SourceWikiEvidence, len(contribution.Evidence))
		for j, evidence := range contribution.Evidence {
			cloned.Contributions[i].Evidence[j] = evidence
			cloned.Contributions[i].Evidence[j].Symbols = append([]string(nil), evidence.Symbols...)
			cloned.Contributions[i].Evidence[j].Context = append([]types.SourceContext(nil), evidence.Context...)
			cloned.Contributions[i].Evidence[j].Diagnostics = append([]types.SourceDiagnostic(nil), evidence.Diagnostics...)
			if evidence.Region != nil {
				region := *evidence.Region
				cloned.Contributions[i].Evidence[j].Region = &region
			}
		}
	}
	return cloned
}

func evidenceIDsForContribution(t *testing.T, contribution types.SourceWikiContribution) []string {
	t.Helper()
	ids := make([]string, len(contribution.Evidence))
	for i, evidence := range contribution.Evidence {
		ids[i] = evidence.ID
	}
	return ids
}
