package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	sourceWikiFlowDiagramMaxEdges = 64
	sourceWikiFlowDiagramMaxLabel = 160
)

var errSourceWikiFlowDiagramLimit = errors.New("source Wiki flow diagram exceeds its 64-edge limit")

// SourceWikiFlowDiagram is a bounded static view of source relations. Evidence
// IDs refer to the original source ranges; consumers retain their normal
// evidence renderer and must not treat this diagram as a replacement citation.
type SourceWikiFlowDiagram struct {
	Markdown    string
	EvidenceIDs []string
	Uncertain   bool
}

type sourceWikiFlowEndpoint struct {
	identity sourceWikiFlowNodeKey
	label    string
}

type sourceWikiFlowNodeKey struct {
	fileID         string
	versionID      string
	path           string
	key            string
	unresolvedKind string
}

type sourceWikiFlowEvidenceKey struct {
	sourceID   string
	snapshotID string
	versionID  string
	path       string
}

type sourceWikiFlowEdge struct {
	from        sourceWikiFlowEndpoint
	to          sourceWikiFlowEndpoint
	kind        string
	uncertain   bool
	evidenceIDs []string
}

// BuildSourceWikiFlowDiagram renders only allowlisted, snapshot-local static
// relations whose original endpoint ranges are covered by registered evidence.
func BuildSourceWikiFlowDiagram(relations []types.SourceCodeRelation, evidence []types.SourceWikiEvidence) (SourceWikiFlowDiagram, error) {
	if len(relations) == 0 {
		return SourceWikiFlowDiagram{}, nil
	}
	seenEvidenceIDs := make(map[string]struct{}, len(evidence))
	evidenceByEndpoint := make(map[sourceWikiFlowEvidenceKey][]types.SourceWikiEvidence, len(evidence))
	for _, item := range evidence {
		if item.ID == "" {
			continue
		}
		if _, exists := seenEvidenceIDs[item.ID]; exists {
			return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow evidence IDs must be unique")
		}
		seenEvidenceIDs[item.ID] = struct{}{}
		key := sourceWikiFlowEvidenceKey{sourceID: item.DataSourceID, snapshotID: item.SnapshotID, versionID: item.FileVersionID, path: item.Path}
		evidenceByEndpoint[key] = append(evidenceByEndpoint[key], item)
	}

	var sourceID, snapshotID string
	edges := make([]sourceWikiFlowEdge, 0, min(len(relations), sourceWikiFlowDiagramMaxEdges))
	for _, relation := range relations {
		if !sourceWikiFlowDiagramKindAllowed(relation.Kind) {
			continue
		}
		if relation.DataSourceID == "" || relation.SnapshotID == "" {
			return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relation lacks source/snapshot identity")
		}
		if sourceID == "" {
			sourceID, snapshotID = relation.DataSourceID, relation.SnapshotID
		} else if relation.DataSourceID != sourceID || relation.SnapshotID != snapshotID {
			return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relations cross source or snapshot boundaries")
		}
		if relation.FromFileID == "" || relation.FromVersionID == "" || relation.FromPath == "" {
			return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relation has an incomplete source endpoint")
		}
		fromRange, err := parseSourceWikiFlowRange(relation.FromRange)
		if err != nil {
			return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relation has an invalid source range: %w", err)
		}
		fromEvidence := sourceWikiFlowEvidenceIDs(evidenceByEndpoint[sourceWikiFlowEvidenceKey{
			sourceID: relation.DataSourceID, snapshotID: relation.SnapshotID, versionID: relation.FromVersionID, path: relation.FromPath,
		}], fromRange)
		if len(fromEvidence) == 0 {
			continue
		}

		toFileFields := relation.ToFileID != "" || relation.ToVersionID != "" || relation.ToPath != ""
		var toEvidence []string
		toIdentity := sourceWikiFlowNodeKey{}
		toLabel := ""
		if toFileFields {
			if relation.ToFileID == "" || relation.ToVersionID == "" || relation.ToPath == "" {
				return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relation has an incomplete target endpoint")
			}
			toRange, err := parseSourceWikiFlowRange(relation.ToRange)
			if err != nil {
				return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relation has an invalid target range: %w", err)
			}
			toEvidence = sourceWikiFlowEvidenceIDs(evidenceByEndpoint[sourceWikiFlowEvidenceKey{
				sourceID: relation.DataSourceID, snapshotID: relation.SnapshotID, versionID: relation.ToVersionID, path: relation.ToPath,
			}], toRange)
			if len(toEvidence) == 0 {
				continue
			}
			toIdentity = sourceWikiFlowNodeKey{fileID: relation.ToFileID, versionID: relation.ToVersionID, path: relation.ToPath, key: relation.ToKey}
			toLabel = sourceWikiFlowEndpointLabel(relation.ToPath, relation.ToKey)
		} else {
			if !sourceWikiFlowHasNoTargetRange(relation.ToRange) {
				return SourceWikiFlowDiagram{}, fmt.Errorf("source Wiki flow relation has a target range without a target endpoint")
			}
			if relation.Kind != "table_access" && relation.Determinacy == "certain" {
				return SourceWikiFlowDiagram{}, fmt.Errorf("certain source Wiki flow relation lacks a target endpoint")
			}
			toIdentity = sourceWikiFlowNodeKey{key: relation.ToKey, unresolvedKind: relation.Kind}
			toLabel = sourceWikiFlowEndpointLabel("", relation.ToKey)
			if relation.Kind == "table_access" {
				if strings.TrimSpace(relation.ToKey) == "" {
					continue
				}
				toIdentity.path = "table"
				toLabel = sourceWikiFlowEndpointLabel("table", relation.ToKey)
			}
		}

		allEvidence := append(fromEvidence, toEvidence...)
		sort.Strings(allEvidence)
		allEvidence = uniqueSourceWikiFlowStrings(allEvidence)
		edges = append(edges, sourceWikiFlowEdge{
			from: sourceWikiFlowEndpoint{
				identity: sourceWikiFlowNodeKey{fileID: relation.FromFileID, versionID: relation.FromVersionID, path: relation.FromPath, key: relation.FromKey},
				label:    sourceWikiFlowEndpointLabel(relation.FromPath, relation.FromKey),
			},
			to:          sourceWikiFlowEndpoint{identity: toIdentity, label: toLabel},
			kind:        relation.Kind,
			uncertain:   relation.Determinacy != "certain",
			evidenceIDs: allEvidence,
		})
		if len(edges) > sourceWikiFlowDiagramMaxEdges {
			return SourceWikiFlowDiagram{}, errSourceWikiFlowDiagramLimit
		}
	}
	if len(edges) == 0 {
		return SourceWikiFlowDiagram{}, nil
	}

	sort.Slice(edges, func(i, j int) bool {
		a, b := edges[i], edges[j]
		if a.from.identity != b.from.identity {
			return sourceWikiFlowNodeKeyLess(a.from.identity, b.from.identity)
		}
		if a.kind != b.kind {
			return a.kind < b.kind
		}
		if a.to.identity != b.to.identity {
			return sourceWikiFlowNodeKeyLess(a.to.identity, b.to.identity)
		}
		if a.uncertain != b.uncertain {
			return !a.uncertain
		}
		for k := 0; k < min(len(a.evidenceIDs), len(b.evidenceIDs)); k++ {
			if a.evidenceIDs[k] != b.evidenceIDs[k] {
				return a.evidenceIDs[k] < b.evidenceIDs[k]
			}
		}
		return len(a.evidenceIDs) < len(b.evidenceIDs)
	})

	nodeLabels := make(map[sourceWikiFlowNodeKey]string, len(edges)*2)
	for _, edge := range edges {
		nodeLabels[edge.from.identity] = edge.from.label
		nodeLabels[edge.to.identity] = edge.to.label
	}
	nodeKeys := make([]sourceWikiFlowNodeKey, 0, len(nodeLabels))
	for key := range nodeLabels {
		nodeKeys = append(nodeKeys, key)
	}
	sort.Slice(nodeKeys, func(i, j int) bool { return sourceWikiFlowNodeKeyLess(nodeKeys[i], nodeKeys[j]) })
	nodeIDs := make(map[sourceWikiFlowNodeKey]string, len(nodeKeys))
	for i, key := range nodeKeys {
		nodeIDs[key] = fmt.Sprintf("n%d", i)
	}

	var markdown strings.Builder
	markdown.WriteString("Static source relations (not a complete execution trace).\n\n```mermaid\nflowchart TD\n")
	for _, key := range nodeKeys {
		fmt.Fprintf(&markdown, "  %s[\"%s\"]\n", nodeIDs[key], escapeSourceWikiFlowLabel(nodeLabels[key]))
	}
	usedEvidence := make(map[string]struct{})
	uncertain := false
	for _, edge := range edges {
		if edge.uncertain {
			uncertain = true
			fmt.Fprintf(&markdown, "  %s -.->|uncertain %s| %s\n", nodeIDs[edge.from.identity], sourceWikiFlowKindLabel(edge.kind), nodeIDs[edge.to.identity])
		} else {
			fmt.Fprintf(&markdown, "  %s -->|%s| %s\n", nodeIDs[edge.from.identity], sourceWikiFlowKindLabel(edge.kind), nodeIDs[edge.to.identity])
		}
		for _, id := range edge.evidenceIDs {
			usedEvidence[id] = struct{}{}
		}
	}
	markdown.WriteString("```\n")
	evidenceIDs := make([]string, 0, len(usedEvidence))
	for id := range usedEvidence {
		evidenceIDs = append(evidenceIDs, id)
	}
	sort.Strings(evidenceIDs)
	return SourceWikiFlowDiagram{Markdown: markdown.String(), EvidenceIDs: evidenceIDs, Uncertain: uncertain}, nil
}

func sourceWikiFlowDiagramKindAllowed(kind string) bool {
	switch kind {
	case "dependency_injection", "http_route", "implements_method", "include", "mapper_statement", "method_call", "result_map", "table_access", "type_supertype":
		return true
	default:
		return false
	}
}

func parseSourceWikiFlowRange(raw types.JSON) (types.SourceRange, error) {
	var sourceRange types.SourceRange
	if len(raw) == 0 || json.Unmarshal(raw, &sourceRange) != nil || sourceRange.StartByte < 0 || sourceRange.EndByte <= sourceRange.StartByte ||
		sourceRange.StartLine < 1 || sourceRange.EndLine < sourceRange.StartLine {
		return types.SourceRange{}, fmt.Errorf("range must have positive lines and a non-empty byte interval")
	}
	return sourceRange, nil
}

func sourceWikiFlowEvidenceIDs(evidence []types.SourceWikiEvidence, covered types.SourceRange) []string {
	var ids []string
	for _, item := range evidence {
		if item.ID == "" || !sourceWikiFlowRangeCovers(item.Range, covered) {
			continue
		}
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return uniqueSourceWikiFlowStrings(ids)
}

func sourceWikiFlowHasNoTargetRange(raw types.JSON) bool {
	if len(raw) == 0 {
		return true
	}
	var sourceRange types.SourceRange
	if json.Unmarshal(raw, &sourceRange) != nil {
		return false
	}
	return sourceRange == (types.SourceRange{})
}

func sourceWikiFlowNodeKeyLess(a, b sourceWikiFlowNodeKey) bool {
	if a.fileID != b.fileID {
		return a.fileID < b.fileID
	}
	if a.versionID != b.versionID {
		return a.versionID < b.versionID
	}
	if a.path != b.path {
		return a.path < b.path
	}
	if a.key != b.key {
		return a.key < b.key
	}
	return a.unresolvedKind < b.unresolvedKind
}

func sourceWikiFlowRangeCovers(evidence, relation types.SourceRange) bool {
	return evidence.StartByte >= 0 && evidence.EndByte > evidence.StartByte && evidence.StartLine > 0 && evidence.EndLine >= evidence.StartLine &&
		evidence.StartByte <= relation.StartByte && evidence.EndByte >= relation.EndByte &&
		evidence.StartLine <= relation.StartLine && evidence.EndLine >= relation.EndLine
}

func sourceWikiFlowEndpointLabel(path, key string) string {
	label := strings.TrimSpace(strings.TrimSpace(path) + " " + strings.TrimSpace(key))
	if label == "" {
		label = "unresolved endpoint"
	}
	runes := []rune(label)
	if len(runes) > sourceWikiFlowDiagramMaxLabel {
		label = string(runes[:sourceWikiFlowDiagramMaxLabel-3]) + "..."
	}
	return label
}

func escapeSourceWikiFlowLabel(label string) string {
	var safe strings.Builder
	for _, char := range label {
		switch {
		case unicode.IsLetter(char) || unicode.IsNumber(char) || strings.ContainsRune(" _./-", char):
			safe.WriteRune(char)
		default:
			safe.WriteByte('_')
		}
	}
	return safe.String()
}

func sourceWikiFlowKindLabel(kind string) string {
	switch kind {
	case "dependency_injection":
		return "injected dependency"
	case "http_route":
		return "HTTP route"
	case "implements_method":
		return "implements"
	case "include":
		return "SQL include"
	case "mapper_statement":
		return "mapper statement"
	case "method_call":
		return "method call"
	case "result_map":
		return "result map"
	case "table_access":
		return "table access"
	case "type_supertype":
		return "supertype"
	default:
		return "static relation"
	}
}

func uniqueSourceWikiFlowStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}
