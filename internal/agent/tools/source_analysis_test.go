package tools

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestSourceAnalysisFactSummariesKeepTypedEvidenceAndBoundPayload(t *testing.T) {
	facts, truncated := boundedFactSummaries(types.JSON(`[
		{"kind":"mybatis_statement","name":"find","namespace":"demo.Mapper","quality":"structural","range":{"start_byte":10,"end_byte":20,"start_line":2,"end_line":2},"text":"unbounded statement body","sql":"SELECT * FROM private_table"},
		{"kind":"java_mapper_method","method_name":"find","certainty":"certain","quality":"structural"}
	]`), 1)
	require.True(t, truncated)
	require.Len(t, facts, 1)
	require.Equal(t, "mybatis_statement", facts[0]["kind"])
	require.Equal(t, "demo.Mapper", facts[0]["namespace"])
	require.NotContains(t, facts[0], "text")
	require.NotContains(t, facts[0], "sql")
	longFacts, fieldsTruncated := boundedFactSummaries(types.JSON(`[{"kind":"fact","name":"`+strings.Repeat("x", maxAgentSummaryRunes+4)+`","include_refs":["a","b","c","d","e","f","g","h","i"]}]`), 1)
	require.True(t, fieldsTruncated)
	require.LessOrEqual(t, utf8.RuneCountInString(longFacts[0]["name"].(string)), maxAgentSummaryRunes+1)
	require.Len(t, longFacts[0]["include_refs"], maxAgentSummaryListItems)
}

func TestSourceAnalysisRangeSnippetUsesExactVersionBytesAndRejectsInvalidBounds(t *testing.T) {
	content := []byte("prefix\nSELECT * FROM customers;\nsuffix")
	rangeJSON := types.JSON(`{"start_byte":7,"end_byte":31,"start_line":2,"end_line":2}`)
	location, snippet, truncated, ok := sourceRangeSnippet(content, rangeJSON)
	require.True(t, ok)
	require.False(t, truncated)
	require.Equal(t, types.SourceRange{StartByte: 7, EndByte: 31, StartLine: 2, EndLine: 2}, location)
	require.Equal(t, "SELECT * FROM customers;", snippet)
	_, _, _, ok = sourceRangeSnippet(content, types.JSON(`{"start_byte":7,"end_byte":99}`))
	require.False(t, ok)
	_, _, _, ok = sourceRangeSnippet(content, types.JSON(`{"start_byte":8,"end_byte":7}`))
	require.False(t, ok)
	longContent := []byte(strings.Repeat("x", 500))
	_, longSnippet, longTruncated, ok := sourceRangeSnippet(longContent, types.JSON(`{"start_byte":0,"end_byte":500,"start_line":1,"end_line":1}`))
	require.True(t, ok)
	require.True(t, longTruncated)
	require.Equal(t, maxAgentSnippetRunes+1, utf8.RuneCountInString(longSnippet))
}

type pagedSourceAnalysisKnowledge struct {
	interfaces.KnowledgeService
	test         *testing.T
	targetCursor string
	targetRead   bool
}

func (k *pagedSourceAnalysisKnowledge) GetSourceFile(ctx context.Context, id string, versionIDs ...string) (*types.SourceFileView, error) {
	require.True(k.test, source.HasReadScope(ctx), "main and target reads retain the same authorized source scope")
	require.Equal(k.test, 20, source.RelationPageSizeFromContext(ctx, 100))
	versionID := "main-version"
	if len(versionIDs) > 0 {
		versionID = versionIDs[0]
	}
	if id == "main-file" {
		require.Equal(k.test, "cursor-main-next", source.RelationCursorFromContext(ctx))
		return &types.SourceFileView{
			KnowledgeID: id, SnapshotID: "snapshot-one", FileVersionID: versionID, SHA256: "a",
			RelationsTruncated: true, RelationsNextCursor: "cursor-main-next",
			Relations: []types.SourceCodeRelation{{
				ID: "edge", Kind: "mapper_statement", FromFileID: id, FromVersionID: versionID,
				ToFileID: "target-file", ToVersionID: "target-version", ToPath: "src/Target.xml",
				Determinacy: "certain", Quality: "structural",
				ToRange: types.JSON(`{"start_byte":0,"end_byte":6,"start_line":1,"end_line":1}`),
			}},
		}, nil
	}
	k.targetRead = true
	k.targetCursor = source.RelationCursorFromContext(ctx)
	if k.targetCursor != "" {
		return nil, context.Canceled // Simulate a repository rejecting a main-file cursor for another file.
	}
	return &types.SourceFileView{
		KnowledgeID: id, SnapshotID: "snapshot-one", FileVersionID: versionID,
		SHA256: "b", Path: "src/Target.xml", RawContent: []byte("target"),
	}, nil
}

func TestSourceAnalysisContinuationReadsCrossFileTargetWithoutReusingCursor(t *testing.T) {
	knowledge := &pagedSourceAnalysisKnowledge{test: t}
	ctx, release := source.WithReadScope(context.Background(), types.SourceReadLease{
		ID: "00000000-0000-4000-8000-000000000001", HasSources: true,
	}, nil, func() {})
	defer release()

	analysis, err := readSourceAnalysis(ctx, knowledge, "main-file", &types.SourceEvidence{
		SnapshotID: "snapshot-one", FileVersionID: "main-version",
	}, "cursor-main-next")
	require.NoError(t, err)
	require.True(t, knowledge.targetRead)
	require.Empty(t, knowledge.targetCursor)
	relations := analysis["relations"].([]map[string]interface{})
	require.Len(t, relations, 1)
	target := relations[0]["target_evidence"].(map[string]interface{})
	require.Equal(t, "target-version", target["file_version_id"])
	require.Equal(t, "target", target["snippet"])
	require.Equal(t, "cursor-main-next", analysis["relations_next_cursor"])
}
