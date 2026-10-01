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
	signatureFacts, signatureTruncated := boundedFactSummaries(types.JSON(`[{"kind":"java_method","parameter_types":["int","java.lang.String"],"signature_certain":true,"is_abstract":false,"is_default":true}]`), 1)
	require.False(t, signatureTruncated)
	require.Equal(t, []interface{}{"int", "java.lang.String"}, signatureFacts[0]["parameter_types"])
	require.Equal(t, true, signatureFacts[0]["signature_certain"])
	require.Equal(t, true, signatureFacts[0]["is_default"])
}

func TestSourceAnalysisKeepsAuthorizedBusinessFlowFactsAndUncertainty(t *testing.T) {
	knowledge := &businessFlowSourceAnalysisKnowledge{test: t}
	ctx, release := source.WithReadScope(context.Background(), types.SourceReadLease{
		ID: "00000000-0000-4000-8000-000000000001", HasSources: true,
	}, nil, func() {})
	defer release()
	analysis, err := readSourceAnalysis(ctx, knowledge, "vue-file", &types.SourceEvidence{
		SnapshotID: "snapshot-one", FileVersionID: "vue-version",
	}, "")
	require.NoError(t, err)
	facts := analysis["facts"].([]map[string]interface{})
	require.Equal(t, "/api/questionnaire/detail", facts[0]["route_path"])
	require.Equal(t, "GET", facts[0]["http_method"])
	require.Equal(t, []interface{}{"demo.IQuestionnaireService"}, facts[1]["super_types"])
	require.NotContains(t, facts[0], "text", "Agent source analysis carries compact facts, not raw parser source text")
	relations := analysis["relations"].([]map[string]interface{})
	require.Len(t, relations, 1)
	require.Equal(t, "uncertain", relations[0]["determinacy"])
	require.Equal(t, "client prefix/proxy target is not statically verified", relations[0]["resolution_reason"])
}

type businessFlowSourceAnalysisKnowledge struct {
	interfaces.KnowledgeService
	test *testing.T
}

func (k *businessFlowSourceAnalysisKnowledge) GetSourceFile(ctx context.Context, id string, versionIDs ...string) (*types.SourceFileView, error) {
	require.True(k.test, source.HasReadScope(ctx), "business facts are read only inside the caller's authorized source scope")
	require.Equal(k.test, "vue-file", id)
	require.Equal(k.test, []string{"vue-version"}, versionIDs, "business facts stay pinned to the evidence version")
	return &types.SourceFileView{
		KnowledgeID: id, SnapshotID: "snapshot-one", FileVersionID: "vue-version", SHA256: "vue-sha",
		Path: "src/web/QuestionnaireDetail.vue", RawContent: []byte("GET /api/questionnaire/detail"),
		Facts: types.JSON(`[
			{"kind":"api_request","route_path":"/api/questionnaire/detail","http_method":"GET","dynamic":false,"certainty":"certain","quality":"structural","range":{"start_byte":0,"end_byte":3,"start_line":1,"end_line":1},"text":"fixture source"},
			{"kind":"java_type","name":"QuestionnaireServiceImpl","namespace":"demo.QuestionnaireServiceImpl","super_types":["demo.IQuestionnaireService"],"certainty":"certain","quality":"structural","range":{"start_byte":0,"end_byte":3,"start_line":1,"end_line":1}}
		]`),
		Relations: []types.SourceCodeRelation{{
			ID: "route-candidate", Kind: "http_route", FromFileID: id, FromVersionID: "vue-version",
			FromPath: "src/web/QuestionnaireDetail.vue", FromKey: "GET /api/questionnaire/detail",
			Determinacy: "uncertain", Quality: "structural",
			ResolutionReason: "client prefix/proxy target is not statically verified",
		}},
	}, nil
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

type reverseSourceAnalysisKnowledge struct {
	interfaces.KnowledgeService
	test        *testing.T
	determinacy string
	reads       [][2]string
}

func (k *reverseSourceAnalysisKnowledge) GetSourceFile(ctx context.Context, id string, versionIDs ...string) (*types.SourceFileView, error) {
	require.True(k.test, source.HasReadScope(ctx), "opposite endpoint reads retain the caller's authorized source scope")
	require.Empty(k.test, source.RelationCursorFromContext(ctx), "the source file's relation cursor is not reused for endpoint evidence")
	versionID := ""
	if len(versionIDs) > 0 {
		versionID = versionIDs[0]
	}
	k.reads = append(k.reads, [2]string{id, versionID})
	switch id {
	case "xml-file":
		require.Equal(k.test, "xml-version", versionID)
		determinacy := k.determinacy
		if determinacy == "" {
			determinacy = "certain"
		}
		return &types.SourceFileView{
			KnowledgeID: id, SnapshotID: "snapshot-one", FileVersionID: versionID,
			SHA256: "xml-sha", Path: "src/mapper/PushScheduleMapper.xml",
			RawContent: []byte("<mapper>\n  <select id=\"getPushSchedule\">SELECT 1</select>\n</mapper>"),
			Relations: []types.SourceCodeRelation{{
				ID: "mapper-edge", Kind: "mapper_statement",
				FromFileID: "java-file", FromVersionID: "java-version", FromPath: "src/PushScheduleMapper.java",
				FromRange: types.JSON(`{"start_byte":61,"end_byte":85,"start_line":2,"end_line":2}`),
				ToFileID:  id, ToVersionID: versionID, ToPath: "src/mapper/PushScheduleMapper.xml",
				ToRange:     types.JSON(`{"start_byte":10,"end_byte":20,"start_line":2,"end_line":2}`),
				Determinacy: determinacy, Quality: "structural",
			}},
		}, nil
	case "java-file":
		require.Equal(k.test, "java-version", versionID)
		content := []byte("package demo;\npublic interface PushScheduleMapper { Schedule getPushSchedule(Long id); }\n")
		return &types.SourceFileView{
			KnowledgeID: id, SnapshotID: "snapshot-one", FileVersionID: versionID,
			SHA256: "java-sha", Path: "src/PushScheduleMapper.java", RawContent: content,
		}, nil
	default:
		k.test.Fatalf("unexpected source read %q", id)
		return nil, context.Canceled
	}
}

func TestSourceAnalysisFromReverseEndpointReturnsOppositeJavaEvidence(t *testing.T) {
	knowledge := &reverseSourceAnalysisKnowledge{test: t}
	ctx, release := source.WithReadScope(context.Background(), types.SourceReadLease{
		ID: "00000000-0000-4000-8000-000000000001", HasSources: true,
	}, nil, func() {})
	defer release()

	analysis, err := readSourceAnalysis(ctx, knowledge, "xml-file", &types.SourceEvidence{
		SnapshotID: "snapshot-one", FileVersionID: "xml-version",
	}, "")
	require.NoError(t, err)
	relations := analysis["relations"].([]map[string]interface{})
	require.Len(t, relations, 1)
	evidence, ok := relations[0]["target_evidence"].(map[string]interface{})
	require.True(t, ok, "an XML-side relation exposes its Java counterpart as target evidence")
	require.Equal(t, "java-file", evidence["knowledge_id"])
	require.Equal(t, "java-version", evidence["file_version_id"])
	require.Equal(t, "src/PushScheduleMapper.java", evidence["path"])
	require.Equal(t, "getPushSchedule(Long id)", evidence["snippet"])
	require.Equal(t, [][2]string{{"xml-file", "xml-version"}, {"java-file", "java-version"}}, knowledge.reads)
}

func TestSourceAnalysisDoesNotPromoteUncertainReverseEndpoint(t *testing.T) {
	knowledge := &reverseSourceAnalysisKnowledge{test: t, determinacy: "uncertain"}
	ctx, release := source.WithReadScope(context.Background(), types.SourceReadLease{
		ID: "00000000-0000-4000-8000-000000000001", HasSources: true,
	}, nil, func() {})
	defer release()

	analysis, err := readSourceAnalysis(ctx, knowledge, "xml-file", &types.SourceEvidence{
		SnapshotID: "snapshot-one", FileVersionID: "xml-version",
	}, "")
	require.NoError(t, err)
	relations := analysis["relations"].([]map[string]interface{})
	require.Len(t, relations, 1)
	require.NotContains(t, relations[0], "target_evidence")
	require.Equal(t, [][2]string{{"xml-file", "xml-version"}}, knowledge.reads, "uncertain edges never trigger an opposite-endpoint read")
}
