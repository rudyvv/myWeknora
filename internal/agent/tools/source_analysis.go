package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	maxAgentSourceFacts       = 30
	maxAgentSourceDiagnostics = 20
	maxAgentRelationPageSize  = 20
	maxAgentSnippetRunes      = 240
	maxAgentSummaryRunes      = 128
	maxAgentSummaryListItems  = 8
)

// readSourceAnalysis only runs inside a question's already-authorized source
// scope. It reads the exact chunk-pinned version, then re-reads every verified
// cross-file endpoint through the same scope before returning source evidence.
func readSourceAnalysis(ctx context.Context, knowledge interfaces.KnowledgeService, knowledgeID string, evidence *types.SourceEvidence, relationCursor string) (map[string]interface{}, error) {
	if knowledge == nil || evidence == nil || !source.HasReadScope(ctx) {
		return nil, nil
	}
	ctx = source.WithRelationCursor(ctx, relationCursor)
	ctx = source.WithRelationPageSize(ctx, maxAgentRelationPageSize)
	file, err := knowledge.GetSourceFile(ctx, knowledgeID, evidence.FileVersionID)
	if err != nil {
		return nil, err
	}
	if file.KnowledgeID != knowledgeID || file.FileVersionID != evidence.FileVersionID ||
		file.SnapshotID != evidence.SnapshotID || file.SHA256 == "" {
		return nil, fmt.Errorf("source evidence no longer matches its pinned file version")
	}

	facts, factsTruncated := boundedFactSummaries(file.Facts, maxAgentSourceFacts)
	diagnostics, diagnosticsTruncated := boundedDiagnostics(file.Diagnostics, maxAgentSourceDiagnostics)
	relations := make([]map[string]interface{}, 0, len(file.Relations))
	targetFiles := map[sourceVersionKey]*types.SourceFileView{{knowledgeID, file.FileVersionID}: file}
	for _, relation := range file.Relations {
		item := map[string]interface{}{
			"id": relation.ID, "kind": relation.Kind, "from_file_id": relation.FromFileID,
			"from_version_id": relation.FromVersionID, "from_path": relation.FromPath,
			"from_key": relation.FromKey, "from_range": sourceRangeValue(relation.FromRange),
			"to_file_id": relation.ToFileID, "to_version_id": relation.ToVersionID,
			"to_path": relation.ToPath, "to_key": relation.ToKey,
			"to_range": sourceRangeValue(relation.ToRange), "determinacy": relation.Determinacy,
			"quality": relation.Quality, "resolution_reason": relation.ResolutionReason,
		}
		for _, key := range []string{"kind", "from_path", "from_key", "to_path", "to_key", "resolution_reason"} {
			if value, ok := item[key].(string); ok {
				item[key] = truncateAgentRunes(value, maxAgentSummaryRunes)
			}
		}
		endpoint, endpointOK := oppositeSourceRelationEndpoint(relation, file.KnowledgeID, file.FileVersionID)
		if relation.Determinacy == "certain" && endpointOK {
			key := sourceVersionKey{endpoint.knowledgeID, endpoint.versionID}
			target, ok := targetFiles[key]
			if !ok {
				targetCtx := source.WithoutRelationCursor(ctx)
				target, err = knowledge.GetSourceFile(targetCtx, endpoint.knowledgeID, endpoint.versionID)
				if err != nil {
					return nil, err
				}
				if target.KnowledgeID == endpoint.knowledgeID && target.FileVersionID == endpoint.versionID && target.SnapshotID == file.SnapshotID && target.Path == endpoint.path {
					targetFiles[key] = target
				}
			}
			if target.KnowledgeID == endpoint.knowledgeID && target.FileVersionID == endpoint.versionID && target.SnapshotID == file.SnapshotID && target.Path == endpoint.path {
				if location, snippet, snippetTruncated, ok := sourceRangeSnippet(target.RawContent, endpoint.rangeValue); ok {
					item["target_evidence"] = map[string]interface{}{
						"knowledge_id": target.KnowledgeID, "snapshot_id": target.SnapshotID,
						"file_version_id": target.FileVersionID, "sha256": target.SHA256,
						"path": target.Path, "range": location, "snippet": snippet,
						"snippet_truncated": snippetTruncated,
					}
				}
			}
		}
		relations = append(relations, item)
	}

	return map[string]interface{}{
		"knowledge_id": file.KnowledgeID, "snapshot_id": file.SnapshotID,
		"file_version_id": file.FileVersionID, "sha256": file.SHA256,
		"path": file.Path, "quality": file.Quality, "parser_version": file.ParserVersion,
		"facts": facts, "facts_truncated": factsTruncated,
		"diagnostics": diagnostics, "diagnostics_truncated": diagnosticsTruncated,
		"relations": relations, "relations_truncated": file.RelationsTruncated,
		"relations_next_cursor": file.RelationsNextCursor,
	}, nil
}

type sourceVersionKey struct{ knowledgeID, versionID string }

type sourceRelationEndpoint struct {
	knowledgeID string
	versionID   string
	path        string
	rangeValue  types.JSON
}

func oppositeSourceRelationEndpoint(relation types.SourceCodeRelation, knowledgeID, versionID string) (sourceRelationEndpoint, bool) {
	atFrom := relation.FromFileID == knowledgeID && relation.FromVersionID == versionID
	atTo := relation.ToFileID == knowledgeID && relation.ToVersionID == versionID
	var endpoint sourceRelationEndpoint
	switch {
	case atFrom:
		endpoint = sourceRelationEndpoint{relation.ToFileID, relation.ToVersionID, relation.ToPath, relation.ToRange}
	case atTo:
		endpoint = sourceRelationEndpoint{relation.FromFileID, relation.FromVersionID, relation.FromPath, relation.FromRange}
	default:
		return sourceRelationEndpoint{}, false
	}
	return endpoint, endpoint.knowledgeID != "" && endpoint.versionID != "" && endpoint.path != ""
}

func sourceRangeValue(raw types.JSON) interface{} {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var location types.SourceRange
	if json.Unmarshal(raw, &location) != nil {
		return nil
	}
	return location
}

func boundedFactSummaries(raw types.JSON, limit int) ([]map[string]interface{}, bool) {
	var values []map[string]interface{}
	if json.Unmarshal(raw, &values) != nil {
		return []map[string]interface{}{}, false
	}
	truncated := len(values) > limit
	if truncated {
		values = values[:limit]
	}
	allowed := map[string]bool{
		"kind": true, "name": true, "namespace": true, "statement_type": true,
		"statement_id": true, "method_name": true, "receiver": true, "type_name": true,
		"route_path": true, "http_method": true, "http_methods": true,
		"http_methods_specified": true, "http_methods_certain": true, "super_types": true,
		"parameter_types": true, "signature_certain": true, "is_abstract": true, "is_default": true,
		"result_map_refs": true, "include_refs": true, "target_namespace": true,
		"target_name": true, "owner_kind": true, "dynamic": true, "certainty": true,
		"quality": true, "range": true,
	}
	result := make([]map[string]interface{}, 0, len(values))
	for _, value := range values {
		fact := make(map[string]interface{})
		for key, item := range value {
			if allowed[key] {
				bounded, fieldTruncated := boundedFactValue(item)
				fact[key] = bounded
				truncated = truncated || fieldTruncated
			}
		}
		result = append(result, fact)
	}
	return result, truncated
}

func boundedFactValue(value interface{}) (interface{}, bool) {
	switch item := value.(type) {
	case string:
		bounded := truncateAgentRunes(item, maxAgentSummaryRunes)
		return bounded, bounded != item
	case []interface{}:
		truncated := len(item) > maxAgentSummaryListItems
		if len(item) > maxAgentSummaryListItems {
			item = item[:maxAgentSummaryListItems]
		}
		bounded := make([]interface{}, len(item))
		for i, value := range item {
			if text, ok := value.(string); ok {
				bounded[i] = truncateAgentRunes(text, maxAgentSummaryRunes/2)
				truncated = truncated || bounded[i] != text
			} else {
				bounded[i] = value
			}
		}
		return bounded, truncated
	default:
		return value, false
	}
}

func truncateAgentRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

func boundedDiagnostics(raw types.JSON, limit int) ([]types.ParsedSourceDiagnostic, bool) {
	var values []types.ParsedSourceDiagnostic
	if json.Unmarshal(raw, &values) != nil {
		return []types.ParsedSourceDiagnostic{}, false
	}
	truncated := len(values) > limit
	if truncated {
		values = values[:limit]
	}
	for i := range values {
		message := truncateAgentRunes(values[i].Message, 500)
		truncated = truncated || message != values[i].Message
		values[i].Message = message
	}
	return values, truncated
}

func sourceRangeSnippet(content []byte, raw types.JSON) (types.SourceRange, string, bool, bool) {
	var location types.SourceRange
	if json.Unmarshal(raw, &location) != nil || location.StartByte < 0 || location.EndByte < location.StartByte || location.EndByte > len(content) ||
		location.StartLine < 1 || location.EndLine < location.StartLine {
		return types.SourceRange{}, "", false, false
	}
	snippet := string(content[location.StartByte:location.EndByte])
	if !utf8.ValidString(snippet) || location.StartLine != bytes.Count(content[:location.StartByte], []byte{'\n'})+1 {
		return types.SourceRange{}, "", false, false
	}
	last := location.EndByte - 1
	if last < location.StartByte {
		last = location.StartByte
	}
	if location.EndLine != bytes.Count(content[:last], []byte{'\n'})+1 {
		return types.SourceRange{}, "", false, false
	}
	truncated := false
	if runes := []rune(snippet); len(runes) > maxAgentSnippetRunes {
		snippet = string(runes[:maxAgentSnippetRunes]) + "…"
		truncated = true
	}
	return location, snippet, truncated, true
}

func sourceAnalysisXML(analysis map[string]interface{}) string {
	if analysis == nil {
		return ""
	}
	encoded, err := json.Marshal(analysis)
	if err != nil {
		return ""
	}
	return "<source_analysis>" + xmlEscape(string(encoded)) + "</source_analysis>\n"
}

func appendSourceAnalysis(output string, analysis map[string]interface{}) string {
	encoded := sourceAnalysisXML(analysis)
	if encoded == "" {
		return output
	}
	end := strings.LastIndex(output, "</knowledge_chunks>")
	if end < 0 {
		return output + "\n" + encoded
	}
	return output[:end] + encoded + output[end:]
}
