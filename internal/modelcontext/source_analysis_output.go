package modelcontext

import (
	"bytes"
	"encoding/json"
	"strings"
)

const (
	maxModelSourceAnalysisJSONBytes = 12 << 10
	maxModelSourceAnalysisBytes     = 64 << 10
	maxModelSourceFacts             = 30
	maxModelSourceDiagnostics       = 20
	maxModelSourceRelations         = 20
	maxModelSourceTextRunes         = 160
	maxModelSourceMessageRunes      = 400
	maxModelSourceSnippetRunes      = 240
	maxModelSourceCursorBytes       = 4096
)

// appendModelSourceAnalysis renders the typed sidecar through the same model
// boundary as the chunk rows. The ToolResult.Output is a UI representation and
// may contain a sidecar that the structured chunk formatter otherwise drops.
func appendModelSourceAnalysis(output string, raw interface{}) string {
	analysis := modelSourceAnalysis(raw)
	if len(analysis) == 0 {
		return output
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(analysis); err != nil {
		return output
	}
	payload := strings.TrimSuffix(encoded.String(), "\n")
	section := "  <source_analysis>" + escapeText(payload) + "</source_analysis>\n"
	if len(section) > maxModelSourceAnalysisBytes {
		return output
	}
	if end := strings.LastIndex(output, "</retrieval>"); end >= 0 {
		return output[:end] + section + output[end:]
	}
	return output + "\n" + section
}

func modelSourceAnalysis(raw interface{}) map[string]interface{} {
	input := objectValue(raw)
	if input == nil {
		return nil
	}
	output := make(map[string]interface{})
	truncated := false
	for _, key := range []string{"path", "quality", "parser_version", "sha256"} {
		if value, ok := input[key].(string); ok && value != "" {
			output[key], truncated = boundedModelString(value, maxModelSourceTextRunes, truncated)
		}
	}

	facts, factTruncated := boundedModelRows(input["facts"], maxModelSourceFacts, modelFactFields, maxModelSourceTextRunes)
	diagnostics, diagnosticTruncated := boundedModelDiagnostics(input["diagnostics"])
	relations, relationTruncated := boundedModelRelations(input["relations"])
	truncated = truncated || factTruncated || diagnosticTruncated || relationTruncated
	output["facts"], output["diagnostics"], output["relations"] = facts, diagnostics, relations
	output["facts_truncated"] = boolValue(input, "facts_truncated") || factTruncated
	output["diagnostics_truncated"] = boolValue(input, "diagnostics_truncated") || diagnosticTruncated
	output["relations_truncated"] = boolValue(input, "relations_truncated") || relationTruncated
	if cursor, ok := input["relations_next_cursor"].(string); ok && cursor != "" && len(cursor) <= maxModelSourceCursorBytes {
		output["relations_next_cursor"] = cursor
	} else if ok && cursor != "" {
		truncated = true
		output["relations_truncated"] = true
	}

	for {
		encoded, err := json.Marshal(output)
		if err != nil || len(encoded) <= maxModelSourceAnalysisJSONBytes {
			break
		}
		truncated = true
		switch {
		case len(relations) > 0:
			relations = relations[:len(relations)-1]
			output["relations"] = relations
			output["relations_truncated"] = true
		case len(diagnostics) > 0:
			diagnostics = diagnostics[:len(diagnostics)-1]
			output["diagnostics"] = diagnostics
			output["diagnostics_truncated"] = true
		case len(facts) > 0:
			facts = facts[:len(facts)-1]
			output["facts"] = facts
			output["facts_truncated"] = true
		default:
			return nil
		}
	}
	if truncated {
		output["model_output_truncated"] = true
	}
	return output
}

var modelFactFields = []string{
	"kind", "name", "namespace", "statement_type", "statement_id", "method_name", "receiver",
	"type_name", "result_map_refs", "include_refs", "target_namespace", "target_name", "owner_kind",
	"owner_name", "reference_kind", "dynamic", "certainty", "quality", "range",
}

func boundedModelRows(raw interface{}, limit int, fields []string, textLimit int) ([]map[string]interface{}, bool) {
	rows := mapsValue(raw)
	truncated := len(rows) > limit
	if truncated {
		rows = rows[:limit]
	}
	result := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]interface{})
		for _, key := range fields {
			value, ok := row[key]
			if !ok {
				continue
			}
			if key == "range" {
				if span := modelSourceRange(value); span != nil {
					item[key] = span
				}
				continue
			}
			if key == "result_map_refs" || key == "include_refs" {
				values, wasTruncated := boundedModelStringList(value, 8, textLimit)
				item[key], truncated = values, truncated || wasTruncated
				continue
			}
			switch typed := value.(type) {
			case string:
				item[key], truncated = boundedModelString(typed, textLimit, truncated)
			case bool, float64, float32, int, int32, int64, uint, uint32, uint64, json.Number:
				item[key] = typed
			}
		}
		result = append(result, item)
	}
	return result, truncated
}

func boundedModelDiagnostics(raw interface{}) ([]map[string]interface{}, bool) {
	rows := mapsValue(raw)
	truncated := len(rows) > maxModelSourceDiagnostics
	if truncated {
		rows = rows[:maxModelSourceDiagnostics]
	}
	result := make([]map[string]interface{}, 0, len(rows))
	for _, row := range rows {
		item := make(map[string]interface{})
		for _, key := range []string{"code", "message"} {
			if value, ok := row[key].(string); ok {
				limit := maxModelSourceTextRunes
				if key == "message" {
					limit = maxModelSourceMessageRunes
				}
				item[key], truncated = boundedModelString(value, limit, truncated)
			}
		}
		if span := modelSourceRange(row["range"]); span != nil {
			item["range"] = span
		}
		result = append(result, item)
	}
	return result, truncated
}

func boundedModelRelations(raw interface{}) ([]map[string]interface{}, bool) {
	rows := mapsValue(raw)
	truncated := len(rows) > maxModelSourceRelations
	if truncated {
		rows = rows[:maxModelSourceRelations]
	}
	result := make([]map[string]interface{}, 0, len(rows))
	fields := []string{"id", "kind", "from_file_id", "to_file_id", "from_path", "from_key", "to_path", "to_key",
		"determinacy", "quality", "resolution_reason"}
	for _, row := range rows {
		item := make(map[string]interface{})
		for _, key := range fields {
			if value, ok := row[key].(string); ok {
				item[key], truncated = boundedModelString(value, maxModelSourceTextRunes, truncated)
			}
		}
		for _, key := range []string{"from_range", "to_range"} {
			if span := modelSourceRange(row[key]); span != nil {
				item[key] = span
			}
		}
		if evidence := objectValue(row["target_evidence"]); evidence != nil {
			target := make(map[string]interface{})
			for _, key := range []string{"knowledge_id", "snapshot_id", "file_version_id", "sha256", "path"} {
				if value, ok := evidence[key].(string); ok {
					target[key], truncated = boundedModelString(value, maxModelSourceTextRunes, truncated)
				}
			}
			if span := modelSourceRange(evidence["range"]); span != nil {
				target["range"] = span
			}
			if snippet, ok := evidence["snippet"].(string); ok {
				target["snippet"], truncated = boundedModelString(snippet, maxModelSourceSnippetRunes, truncated)
			}
			if value, ok := evidence["snippet_truncated"].(bool); ok {
				target["snippet_truncated"] = value
			}
			item["target_evidence"] = target
		}
		result = append(result, item)
	}
	return result, truncated
}

func modelSourceRange(raw interface{}) map[string]interface{} {
	input := objectValue(raw)
	if input == nil {
		return nil
	}
	span := make(map[string]interface{}, 4)
	for _, key := range []string{"start_byte", "end_byte", "start_line", "end_line"} {
		switch value := input[key].(type) {
		case float64, float32, int, int32, int64, uint, uint32, uint64, json.Number:
			span[key] = value
		}
	}
	if len(span) == 0 {
		return nil
	}
	return span
}

func boundedModelStringList(raw interface{}, limit, textLimit int) ([]string, bool) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return []string{}, false
	}
	var values []string
	if json.Unmarshal(encoded, &values) != nil {
		return []string{}, false
	}
	truncated := len(values) > limit
	if truncated {
		values = values[:limit]
	}
	for index, value := range values {
		bounded, wasTruncated := truncateModelString(value, textLimit)
		values[index], truncated = bounded, truncated || wasTruncated
	}
	return values, truncated
}

func objectValue(raw interface{}) map[string]interface{} {
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var result map[string]interface{}
	if json.Unmarshal(encoded, &result) != nil {
		return nil
	}
	return result
}

func boundedModelString(value string, limit int, truncated bool) (string, bool) {
	bounded, shortened := truncateModelString(value, limit)
	return bounded, truncated || shortened
}

func truncateModelString(value string, limit int) (string, bool) {
	runes := []rune(value)
	if len(runes) <= limit {
		return value, false
	}
	return string(runes[:limit]) + "…", true
}
