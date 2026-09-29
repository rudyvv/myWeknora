package modelcontext

import (
	"encoding/xml"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestModelToolResultForToolCarriesVueEvidenceIntoModelContext(t *testing.T) {
	type sourceNode struct {
		RegionKind    string `xml:"region_kind,attr"`
		Language      string `xml:"region_language,attr"`
		Quality       string `xml:"quality,attr"`
		RegionQuality string `xml:"region_quality,attr"`
		Symbols       string `xml:"symbols,attr"`
		StartLine     int    `xml:"start_line,attr"`
		EndLine       int    `xml:"end_line,attr"`
	}
	type chunkNode struct {
		Source sourceNode `xml:"source"`
	}
	type documentNode struct {
		Chunks []chunkNode `xml:"chunk"`
	}
	type retrievalNode struct {
		Documents []documentNode `xml:"document"`
	}

	evidence := &types.SourceEvidence{
		DataSourceID: "source-id", SnapshotID: "snapshot-id", FileVersionID: "file-version-id",
		ProjectID: "project-id", CommitSHA: "commit-sha", Path: "src/pages/Confirm.vue",
		Range: types.SourceRange{StartByte: 25, EndByte: 180, StartLine: 7, EndLine: 13},
		Symbols: []string{
			"ReviewPanel.render", "sendAPI", `quote"<&`, strings.Repeat("x", 200),
			"symbol5", "symbol6", "symbol7", "symbol8", "symbol9", "symbol10", "symbol11", "symbol12",
		},
		Quality: "structural", GitLabURL: "https://gitlab.example/project/-/blob/commit-sha/src/pages/Confirm.vue#L7",
		Region: &types.SourceRegion{
			Kind: "template", Language: "pug", Quality: "unknown_preprocess",
		},
	}
	row := map[string]interface{}{
		"chunk_id": "chunk-id", "knowledge_id": "source-file-id", "knowledge_base_id": "kb-id",
		"knowledge_title": "Confirm timetable", "content": "bounded fixture content",
		"source_evidence": evidence,
	}
	for _, test := range []struct {
		toolName   string
		display    string
		rowsField  string
		dataFields map[string]interface{}
	}{
		{toolName: "knowledge_search", display: "search_results", rowsField: "results"},
		{toolName: "grep_chunks", display: "grep_results", rowsField: "chunk_results"},
		{toolName: "list_knowledge_chunks", display: "knowledge_chunks_list", rowsField: "chunks",
			dataFields: map[string]interface{}{"knowledge_id": "source-file-id", "knowledge_title": "Confirm timetable"}},
		{toolName: "wiki_read_source_doc", display: "knowledge_chunks_list", rowsField: "chunks",
			dataFields: map[string]interface{}{"knowledge_id": "source-file-id", "knowledge_title": "Confirm timetable"}},
	} {
		t.Run(test.toolName, func(t *testing.T) {
			data := map[string]interface{}{"display_type": test.display, test.rowsField: []map[string]interface{}{row}}
			for key, value := range test.dataFields {
				data[key] = value
			}
			output := NewRegistry(true).ModelToolResultForTool(test.toolName, &types.ToolResult{Success: true, Data: data})
			require.Contains(t, output, `region_kind="template"`)
			require.Contains(t, output, `region_language="pug"`)
			require.Contains(t, output, `quality="structural"`)
			require.Contains(t, output, `region_quality="unknown_preprocess"`)
			require.Contains(t, output, `start_line="7"`)
			require.Contains(t, output, `end_line="13"`)

			var parsed retrievalNode
			require.NoError(t, xml.Unmarshal([]byte(output), &parsed), "model-facing evidence must remain valid XML")
			require.Len(t, parsed.Documents, 1)
			require.Len(t, parsed.Documents[0].Chunks, 1)
			modelSource := parsed.Documents[0].Chunks[0].Source
			require.Equal(t, "ReviewPanel.render", strings.Split(modelSource.Symbols, " ")[0])
			require.Equal(t, "structural", modelSource.Quality)
			require.Equal(t, "unknown_preprocess", modelSource.RegionQuality)
			require.Contains(t, modelSource.Symbols, "quote\"<&")
			require.LessOrEqual(t, utf8.RuneCountInString(modelSource.Symbols), 512)
			require.LessOrEqual(t, len(strings.Fields(modelSource.Symbols)), 8)
			require.Len(t, strings.Fields(modelSource.Symbols)[3], 80)
		})
	}
}

func TestSourceEvidenceQualityFallsBackToRegionQuality(t *testing.T) {
	attrs := sourceEvidenceAttrs(&types.SourceEvidence{
		Region: &types.SourceRegion{Kind: "style", Language: "pug", Quality: "unknown_preprocess"},
	})
	require.Contains(t, attrs, `quality="unknown_preprocess"`)
	require.NotContains(t, attrs, `region_quality=`)
}

func TestModelToolResultForToolCarriesOnlySafeExternalScriptStatus(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{status: "unchecked", want: `external_status="unchecked"`},
		{status: "rejected", want: `external_status="rejected"`},
		{status: "resolved"},
	} {
		t.Run(test.status, func(t *testing.T) {
			row := map[string]interface{}{
				"chunk_id": "chunk-id", "knowledge_id": "source-file-id", "knowledge_base_id": "kb-id",
				"knowledge_title": "External script", "content": "<script src=\"./api.js\"></script>",
				"source_evidence": &types.SourceEvidence{
					SnapshotID: "snapshot-id", CommitSHA: "commit-sha", Path: "src/Panel.vue",
					Quality: "degraded", Range: types.SourceRange{StartLine: 1, EndLine: 1},
					Region: &types.SourceRegion{Kind: "script", Quality: "degraded", ExternalSource: "./api.js",
						ExternalStatus: test.status, ResolvedPath: "src/api.js"},
				},
			}
			output := NewRegistry(true).ModelToolResultForTool("knowledge_search", &types.ToolResult{
				Success: true, Data: map[string]interface{}{
					"display_type": "search_results", "results": []map[string]interface{}{row},
				},
			})
			if test.want != "" {
				require.Contains(t, output, test.want)
			} else {
				require.NotContains(t, output, `external_status=`)
			}
			require.NotContains(t, output, `external_source=`)
			require.NotContains(t, output, `resolved_path=`)
			require.NotContains(t, output, "src/api.js")
		})
	}
}

func TestModelToolResultForToolExplainsOnlyKnownBoundedSFCDiagnostics(t *testing.T) {
	evidence := &types.SourceEvidence{
		SnapshotID: "snapshot-id", CommitSHA: "commit-sha", Path: "src/Panel.vue",
		Quality: "degraded", Range: types.SourceRange{StartLine: 1, EndLine: 1},
		Region: &types.SourceRegion{Kind: "template", Quality: "degraded"},
		Diagnostics: []types.SourceDiagnostic{
			{Code: "vue_sfc_parse_warning", Range: types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 1, EndLine: 1}},
			{Code: "vue_sfc_duplicate_block", Range: types.SourceRange{StartByte: 40, EndByte: 40, StartLine: 2, EndLine: 2}},
			{Code: `vue_sfc_parse_warning" resolved_path="private.ts`, Range: types.SourceRange{StartByte: 50, EndByte: 55, StartLine: 3, EndLine: 3}},
		},
	}
	row := map[string]interface{}{
		"chunk_id": "chunk-id", "knowledge_id": "source-file-id", "knowledge_base_id": "kb-id",
		"knowledge_title": "Panel", "content": "original source", "source_evidence": evidence,
	}
	output := NewRegistry(true).ModelToolResultForTool("knowledge_search", &types.ToolResult{
		Success: true, Data: map[string]interface{}{
			"display_type": "search_results", "results": []map[string]interface{}{row},
		},
	})
	require.Contains(t, output, `quality="degraded"`)
	require.Contains(t, output, `sfc_diagnostics="vue_sfc_parse_warning@L1-L1: Vue SFC block has a descriptor warning; original bytes are retained.; vue_sfc_duplicate_block@L2-L2: Vue SFC has a duplicate top-level block; original bytes are retained."`)
	require.NotContains(t, output, "private.ts")
	require.NotContains(t, output, "resolved_path=")
}

func TestSourceEvidenceExternalStatusIsBoundedAndNeverResolvedForTheModel(t *testing.T) {
	for _, test := range []struct {
		status string
		want   string
	}{
		{status: "unchecked", want: `external_status="unchecked"`},
		{status: "rejected", want: `external_status="rejected"`},
		{status: "resolved"},
		{status: `unchecked" resolved_path="private.ts`},
	} {
		t.Run(test.status, func(t *testing.T) {
			attrs := sourceEvidenceAttrs(&types.SourceEvidence{Region: &types.SourceRegion{
				Kind: "script", ExternalSource: "./private.js", ExternalStatus: test.status,
				ResolvedPath: "private.ts",
			}})
			if test.want != "" {
				require.Contains(t, attrs, test.want)
			} else {
				require.NotContains(t, attrs, `external_status=`)
			}
			require.NotContains(t, attrs, `external_source=`)
			require.NotContains(t, attrs, `resolved_path=`)
			require.NotContains(t, attrs, "private.js")
			require.NotContains(t, attrs, "private.ts")
		})
	}
	attrs := sourceEvidenceAttrs(&types.SourceEvidence{Region: &types.SourceRegion{
		Kind: "script", ExternalStatus: strings.Repeat("x", 1024),
	}})
	require.NotContains(t, attrs, `external_status=`)
}
