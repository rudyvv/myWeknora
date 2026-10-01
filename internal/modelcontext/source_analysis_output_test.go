package modelcontext

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModelSourceAnalysisIsBoundedAndPreservesContinuationCursor(t *testing.T) {
	long := strings.Repeat("&<>", 100)
	facts := make([]map[string]interface{}, 30)
	for index := range facts {
		facts[index] = map[string]interface{}{
			"kind": long, "name": long, "namespace": long, "statement_type": long,
			"statement_id": long, "method_name": long, "receiver": long,
			"type_name": long, "target_namespace": long, "target_name": long,
			"owner_kind": long, "owner_name": long, "reference_kind": long,
			"quality": "structural", "range": map[string]interface{}{"start_byte": index, "end_byte": index + 1},
		}
	}
	diagnostics := make([]map[string]interface{}, 20)
	for index := range diagnostics {
		diagnostics[index] = map[string]interface{}{"code": long, "message": long}
	}
	relations := make([]map[string]interface{}, 20)
	for index := range relations {
		relations[index] = map[string]interface{}{
			"id": long, "kind": long, "from_file_id": long, "to_file_id": long,
			"from_path": long, "from_key": long, "to_path": long, "to_key": long,
			"determinacy": long, "quality": long, "resolution_reason": long,
			"target_evidence": map[string]interface{}{"snippet": long},
		}
	}
	const cursor = "opaque-cursor-that-must-remain-verbatim"
	analysis := modelSourceAnalysis(map[string]interface{}{
		"path": long, "quality": "structural", "parser_version": "parser-v1", "sha256": strings.Repeat("a", 64),
		"facts": facts, "diagnostics": diagnostics, "relations": relations,
		"relations_next_cursor": cursor,
	})
	encoded, err := json.Marshal(analysis)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), maxModelSourceAnalysisJSONBytes)
	require.Equal(t, cursor, analysis["relations_next_cursor"])
	require.Equal(t, true, analysis["model_output_truncated"])
	require.Len(t, analysis["relations"], len(relations))
	require.Equal(t, false, analysis["relations_truncated"], "shortened relation context is not a missing relation page")

	output := appendModelSourceAnalysis(`<retrieval></retrieval>`, map[string]interface{}{
		"path": "src/Mapper.xml", "relations_next_cursor": cursor,
		"relations": []map[string]interface{}{{"kind": "include", "resolution_reason": "<unsafe>&"}},
	})
	start, end := strings.Index(output, "<source_analysis>"), strings.Index(output, "</source_analysis>")
	require.GreaterOrEqual(t, start, 0)
	require.Greater(t, end, start)
	require.LessOrEqual(t, len(output[start:end+len("</source_analysis>")]), maxModelSourceAnalysisBytes)
	require.Contains(t, output, "&lt;unsafe&gt;&amp;")
	require.Contains(t, output, cursor)
}

func TestModelSourceAnalysisPreservesBoundedBusinessFlowFields(t *testing.T) {
	analysis := modelSourceAnalysis(map[string]interface{}{
		"facts": []map[string]interface{}{{
			"kind": "api_request", "route_path": "/api/questionnaire/detail", "http_method": "GET",
			"super_types": []string{"demo.IQuestionnaireService"}, "quality": "structural",
		}, map[string]interface{}{
			"kind": "java_method", "parameter_types": []string{"int", "java.lang.String"},
			"signature_certain": true, "is_abstract": true, "is_default": false, "quality": "structural",
		}},
	})
	facts := analysis["facts"].([]map[string]interface{})
	require.Len(t, facts, 2)
	require.Equal(t, "/api/questionnaire/detail", facts[0]["route_path"])
	require.Equal(t, "GET", facts[0]["http_method"])
	require.Equal(t, []string{"demo.IQuestionnaireService"}, facts[0]["super_types"])
	require.Equal(t, []string{"int", "java.lang.String"}, facts[1]["parameter_types"])
	require.Equal(t, true, facts[1]["signature_certain"])
	require.Equal(t, true, facts[1]["is_abstract"])
}
