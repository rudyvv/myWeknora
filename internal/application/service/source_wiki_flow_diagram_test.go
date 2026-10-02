package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func flowRangeJSON(t *testing.T, sourceRange types.SourceRange) types.JSON {
	t.Helper()
	data, err := json.Marshal(sourceRange)
	require.NoError(t, err)
	return types.JSON(data)
}

func flowEvidence(id, path, version string, start, end, startLine, endLine int) types.SourceWikiEvidence {
	return types.SourceWikiEvidence{
		ID: id,
		SourceEvidence: types.SourceEvidence{
			DataSourceID: "source-1", SnapshotID: "snapshot-1", FileVersionID: version, Path: path,
			Range: types.SourceRange{StartByte: start, EndByte: end, StartLine: startLine, EndLine: endLine},
		},
	}
}

type flowFactRefFixture struct {
	DataSourceID  string            `json:"data_source_id"`
	SnapshotID    string            `json:"snapshot_id"`
	FileID        string            `json:"file_id"`
	FileVersionID string            `json:"file_version_id"`
	Path          string            `json:"path"`
	Kind          string            `json:"kind"`
	Role          string            `json:"role"`
	Quality       string            `json:"quality"`
	Range         types.SourceRange `json:"range"`
}

func flowFactRefsJSON(t *testing.T, refs ...flowFactRefFixture) types.JSON {
	t.Helper()
	data, err := json.Marshal(refs)
	require.NoError(t, err)
	return types.JSON(data)
}

func flowFactEvidence(id, fileID, path, version string, start, end, startLine, endLine int) types.SourceWikiEvidence {
	evidence := flowEvidence(id, path, version, start, end, startLine, endLine)
	evidence.KnowledgeID = fileID
	return evidence
}

func flowRelation(t *testing.T, kind, fromFile, fromVersion, fromPath string, fromRange types.SourceRange,
	toFile, toVersion, toPath, toKey string, toRange types.SourceRange,
) types.SourceCodeRelation {
	t.Helper()
	return types.SourceCodeRelation{
		ID: "relation-1", DataSourceID: "source-1", SnapshotID: "snapshot-1", Kind: kind,
		FromFileID: fromFile, FromVersionID: fromVersion, FromPath: fromPath, FromKey: "Controller.get",
		FromRange: flowRangeJSON(t, fromRange), ToFileID: toFile, ToVersionID: toVersion, ToPath: toPath,
		ToKey: toKey, ToRange: flowRangeJSON(t, toRange), Determinacy: "certain",
	}
}

func TestBuildSourceWikiFlowDiagramRendersEvidenceBackedStaticChain(t *testing.T) {
	first := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
	second := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
	third := types.SourceRange{StartByte: 80, EndByte: 96, StartLine: 12, EndLine: 12}
	relations := []types.SourceCodeRelation{
		flowRelation(t, "method_call", "file-a", "version-a", "api/Controller.java", first,
			"file-b", "version-b", "svc/Service.java", "Service.run", second),
		flowRelation(t, "mapper_statement", "file-b", "version-b", "svc/Service.java", second,
			"file-c", "version-c", "db/Mapper.xml", "Mapper.find", third),
	}
	evidence := []types.SourceWikiEvidence{
		flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
		flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
		flowEvidence("ev-c", "db/Mapper.xml", "version-c", 75, 110, 11, 15),
	}

	got, err := BuildSourceWikiFlowDiagram(relations, evidence)
	require.NoError(t, err)
	require.Equal(t, []string{"ev-a", "ev-b", "ev-c"}, got.EvidenceIDs)
	require.False(t, got.Uncertain)
	require.Contains(t, got.Markdown, "```mermaid\nflowchart TD")
	require.Contains(t, got.Markdown, "Static source relations")
	require.Contains(t, got.Markdown, "-->")
	require.Contains(t, got.Markdown, "method call")
	require.Contains(t, got.Markdown, "mapper statement")
	require.Equal(t, 2, strings.Count(got.Markdown, " -->|"))
	require.NotContains(t, got.Markdown, "relation-1", "renderer uses generated node IDs, not persisted IDs")
	reversed, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relations[1], relations[0]}, evidence)
	require.NoError(t, err)
	require.Equal(t, got.Markdown, reversed.Markdown, "diagram output is stable regardless of input relation order")
	require.Equal(t, got.EvidenceIDs, reversed.EvidenceIDs)
}

func TestBuildSourceWikiFlowDiagramRetainsOnlyFullyBackedContextFactEdges(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
	toRange := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
	relation := flowRelation(t, "method_call", "file-a", "version-a", "api/Controller.java", fromRange,
		"file-b", "version-b", "svc/Service.java", "Service.run", toRange)
	refs := []flowFactRefFixture{
		{DataSourceID: "source-1", SnapshotID: "snapshot-1", FileID: "file-prefix", FileVersionID: "prefix-v1", Path: "api/RoutePrefix.java", Kind: "api_prefix", Role: "api_prefix", Quality: "high", Range: types.SourceRange{StartByte: 4, EndByte: 12, StartLine: 1, EndLine: 1}},
		{DataSourceID: "source-1", SnapshotID: "snapshot-1", FileID: "file-proxy", FileVersionID: "proxy-v1", Path: "svc/ServiceProxy.java", Kind: "api_proxy", Role: "api_proxy", Quality: "high", Range: types.SourceRange{StartByte: 30, EndByte: 42, StartLine: 4, EndLine: 4}},
		{DataSourceID: "source-1", SnapshotID: "snapshot-1", FileID: "file-a", FileVersionID: "version-a", Path: "api/Controller.java", Kind: "spring_mapping", Role: "spring_class_mapping", Quality: "high", Range: types.SourceRange{StartByte: 0, EndByte: 9, StartLine: 1, EndLine: 1}},
	}
	relation.Context = flowFactRefsJSON(t, refs...)
	evidence := []types.SourceWikiEvidence{
		flowFactEvidence("ev-a", "file-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
		flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
		flowFactEvidence("ev-prefix", "file-prefix", "api/RoutePrefix.java", "prefix-v1", 0, 20, 1, 2),
		flowFactEvidence("ev-proxy", "file-proxy", "svc/ServiceProxy.java", "proxy-v1", 25, 50, 3, 6),
		flowFactEvidence("ev-class-prefix", "file-a", "api/Controller.java", "version-a", 0, 9, 1, 1),
	}

	got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, evidence)
	require.NoError(t, err)
	require.Equal(t, []string{"ev-a", "ev-b", "ev-class-prefix", "ev-prefix", "ev-proxy"}, got.EvidenceIDs,
		"all causal facts, including cross-file contributors, must be retained as exact Wiki evidence")
	require.False(t, got.Uncertain)
	require.Contains(t, got.Markdown, " -->|method call|")

	uncertain := relation
	uncertain.Determinacy = "uncertain"
	got, err = BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{uncertain}, evidence)
	require.NoError(t, err)
	require.True(t, got.Uncertain, "causal references must not upgrade uncertain relations")
	require.Contains(t, got.Markdown, "-.->|uncertain method call|")
	require.NotContains(t, got.Markdown, " -->|method call|")

	legacy := relation
	legacy.Context = types.JSON("null")
	got, err = BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{legacy}, evidence[:2])
	require.NoError(t, err)
	require.NotEmpty(t, got.Markdown, "legacy relations without fact refs retain endpoint-only diagrams")
	require.Equal(t, []string{"ev-a", "ev-b"}, got.EvidenceIDs)

	for _, tc := range []struct {
		name      string
		ref       flowFactRefFixture
		evidence  []types.SourceWikiEvidence
		context   types.JSON
		wantError bool
	}{
		{
			name: "uncovered causal range cannot leave the edge verified",
			ref:  refs[0],
			evidence: []types.SourceWikiEvidence{
				flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
				flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
			},
		},
		{
			name:      "cross-snapshot causal reference is rejected",
			ref:       func() flowFactRefFixture { ref := refs[0]; ref.SnapshotID = "snapshot-old"; return ref }(),
			wantError: true,
		},
		{
			name:      "cross-source causal reference is rejected",
			ref:       func() flowFactRefFixture { ref := refs[0]; ref.DataSourceID = "source-other"; return ref }(),
			wantError: true,
		},
		{
			name: "wrong file identity cannot borrow path evidence",
			ref:  func() flowFactRefFixture { ref := refs[0]; ref.FileID = "another-file"; return ref }(),
			evidence: []types.SourceWikiEvidence{
				flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
				flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
				flowFactEvidence("ev-prefix", "file-prefix", "api/RoutePrefix.java", "prefix-v1", 0, 20, 1, 2),
			},
		},
		{
			name: "evidence from another source does not cover a causal ref",
			ref:  refs[0],
			evidence: func() []types.SourceWikiEvidence {
				prefix := flowFactEvidence("ev-prefix", "file-prefix", "api/RoutePrefix.java", "prefix-v1", 0, 20, 1, 2)
				prefix.DataSourceID = "source-other"
				return []types.SourceWikiEvidence{
					flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
					flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
					prefix,
				}
			}(),
		},
		{
			name: "evidence from another snapshot does not cover a causal ref",
			ref:  refs[0],
			evidence: func() []types.SourceWikiEvidence {
				prefix := flowFactEvidence("ev-prefix", "file-prefix", "api/RoutePrefix.java", "prefix-v1", 0, 20, 1, 2)
				prefix.SnapshotID = "snapshot-old"
				return []types.SourceWikiEvidence{
					flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
					flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
					prefix,
				}
			}(),
		},
		{
			name: "evidence with another file version does not cover a causal ref",
			ref:  refs[0],
			evidence: []types.SourceWikiEvidence{
				flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
				flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
				flowFactEvidence("ev-prefix", "file-prefix", "api/RoutePrefix.java", "old-version", 0, 20, 1, 2),
			},
		},
		{
			name: "evidence with another path does not cover a causal ref",
			ref:  refs[0],
			evidence: []types.SourceWikiEvidence{
				flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
				flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
				flowFactEvidence("ev-prefix", "file-prefix", "api/OtherPrefix.java", "prefix-v1", 0, 20, 1, 2),
			},
		},
		{
			name: "evidence with a partial range does not cover a causal ref",
			ref:  refs[0],
			evidence: []types.SourceWikiEvidence{
				flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
				flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
				flowFactEvidence("ev-prefix", "file-prefix", "api/RoutePrefix.java", "prefix-v1", 4, 11, 1, 1),
			},
		},
		{
			name:      "mismatched role and fact kind are rejected",
			ref:       func() flowFactRefFixture { ref := refs[0]; ref.Role = "api_proxy"; return ref }(),
			wantError: true,
		},
		{
			name:      "duplicate causal reference is rejected",
			context:   flowFactRefsJSON(t, refs[0], refs[0]),
			wantError: true,
		},
		{
			name:      "malformed context JSON is rejected",
			context:   types.JSON(`{"file_id":"file-prefix"`),
			wantError: true,
		},
		{
			name:      "incomplete causal reference is rejected",
			ref:       flowFactRefFixture{DataSourceID: "source-1", SnapshotID: "snapshot-1", FileID: "file-prefix"},
			wantError: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := relation
			if tc.context != nil {
				candidate.Context = tc.context
			} else {
				candidate.Context = flowFactRefsJSON(t, tc.ref)
			}
			candidateEvidence := tc.evidence
			if candidateEvidence == nil {
				candidateEvidence = evidence
			}
			got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{candidate}, candidateEvidence)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Empty(t, got.Markdown, "an edge missing exact evidence for any causal ref must not remain in the diagram")
			require.Empty(t, got.EvidenceIDs)
		})
	}
}

func TestBuildSourceWikiFlowDiagramOmitsUncoveredOrCrossSnapshotEndpoints(t *testing.T) {
	relation := flowRelation(t, "method_call", "file-a", "version-a", "api/Controller.java",
		types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2},
		"file-b", "version-b", "svc/Service.java", "Service.run",
		types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7})
	for name, evidence := range map[string][]types.SourceWikiEvidence{
		"source range not covered": {
			flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 23, 1, 4),
			flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
		},
		"target belongs to another snapshot": {
			flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
			{ID: "ev-b", SourceEvidence: types.SourceEvidence{DataSourceID: "source-1", SnapshotID: "snapshot-old", FileVersionID: "version-b", Path: "svc/Service.java", Range: types.SourceRange{StartByte: 35, EndByte: 70, StartLine: 6, EndLine: 9}}},
		},
		"target path and version do not match": {
			flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
			flowEvidence("ev-b", "svc/Service.java", "old-version", 35, 70, 6, 9),
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, evidence)
			require.NoError(t, err)
			require.Empty(t, got.Markdown, "unsupported relations must not become diagram edges")
			require.Empty(t, got.EvidenceIDs)
		})
	}
}

func TestBuildSourceWikiFlowDiagramRejectsMixedRelationSnapshots(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
	toRange := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
	first := flowRelation(t, "method_call", "file-a", "version-a", "api/Controller.java", fromRange,
		"file-b", "version-b", "svc/Service.java", "Service.run", toRange)
	second := first
	second.ID = "relation-2"
	second.SnapshotID = "snapshot-2"
	_, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{first, second}, []types.SourceWikiEvidence{
		flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
		flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
	})
	require.ErrorContains(t, err, "cross source or snapshot boundaries")
}

func TestBuildSourceWikiFlowDiagramRendersSQLTableFromSourceEvidenceOnly(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 90, EndByte: 132, StartLine: 11, EndLine: 13}
	relation := flowRelation(t, "table_access", "mapper-file", "mapper-version", "sql/PushMapper.xml", fromRange,
		"", "", "", "push_schedule", types.SourceRange{})
	relation.Determinacy = "certain"
	got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, []types.SourceWikiEvidence{
		flowEvidence("sql-evidence", "sql/PushMapper.xml", "mapper-version", 80, 150, 10, 14),
	})
	require.NoError(t, err)
	require.Equal(t, []string{"sql-evidence"}, got.EvidenceIDs)
	require.Contains(t, got.Markdown, "table push_schedule")
	require.Contains(t, got.Markdown, "table access")
	require.False(t, got.Uncertain)
}

func TestBuildSourceWikiFlowDiagramKeepsUncertainAndDynamicEdgesDashed(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
	toRange := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
	uncertain := flowRelation(t, "method_call", "file-a", "version-a", "api/Controller.java", fromRange,
		"", "", "", "Service.run candidate", types.SourceRange{})
	uncertain.Determinacy = "uncertain"
	dynamic := flowRelation(t, "method_call", "file-a", "version-a", "api/Controller.java", fromRange,
		"file-b", "version-b", "svc/Service.java", "Service.dynamic", toRange)
	dynamic.Determinacy = "dynamic"
	evidence := []types.SourceWikiEvidence{
		flowEvidence("ev-a", "api/Controller.java", "version-a", 0, 35, 1, 4),
		flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
	}

	got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{uncertain, dynamic}, evidence)
	require.NoError(t, err)
	require.True(t, got.Uncertain)
	require.Contains(t, got.Markdown, "-.->|uncertain method call|")
	require.NotContains(t, got.Markdown, " -->|method call|")
	require.Equal(t, []string{"ev-a", "ev-b"}, got.EvidenceIDs)
}

func TestBuildSourceWikiFlowDiagramEscapesUntrustedLabels(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
	toRange := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
	relation := flowRelation(t, "method_call", "file-a", "version-a",
		"api/Controller\"]\nclick n0 \"https://evil.test\"\n%%{init: {\"theme\":\"dark\"}}%%<script>", fromRange,
		"file-b", "version-b", "svc/Service.java", "Service.run", toRange)
	got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, []types.SourceWikiEvidence{
		flowEvidence("ev-a", relation.FromPath, "version-a", 0, 35, 1, 4),
		flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
	})
	require.NoError(t, err)
	require.NotContains(t, got.Markdown, "\nclick")
	require.NotContains(t, got.Markdown, "https://")
	require.NotContains(t, got.Markdown, "<script>")
	require.NotContains(t, got.Markdown, "%%{init")
	require.NotContains(t, got.Markdown, "\n]")
	require.LessOrEqual(t, strings.Count(got.Markdown, "\n"), 8, "untrusted labels cannot create extra Mermaid directives")
}

func TestBuildSourceWikiFlowDiagramBoundsNodeLabels(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
	toRange := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
	longPath := strings.Repeat("A", 220)
	relation := flowRelation(t, "method_call", "file-a", "version-a", longPath, fromRange,
		"file-b", "version-b", "svc/Service.java", "Service.run", toRange)
	got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, []types.SourceWikiEvidence{
		flowEvidence("ev-a", longPath, "version-a", 0, 35, 1, 4),
		flowEvidence("ev-b", "svc/Service.java", "version-b", 35, 70, 6, 9),
	})
	require.NoError(t, err)
	for _, line := range strings.Split(got.Markdown, "\n") {
		if !strings.Contains(line, "[\"") {
			continue
		}
		start := strings.Index(line, "[\"") + 2
		end := strings.LastIndex(line, "\"]")
		require.GreaterOrEqual(t, end, start)
		require.LessOrEqual(t, len([]rune(line[start:end])), 160)
	}
}

func TestBuildSourceWikiFlowDiagramRejectsMoreThan64SupportedEdges(t *testing.T) {
	var relations []types.SourceCodeRelation
	var evidence []types.SourceWikiEvidence
	for i := 0; i < 65; i++ {
		fromPath, toPath := fmt.Sprintf("from/%d.java", i), fmt.Sprintf("to/%d.java", i)
		fromVersion, toVersion := fmt.Sprintf("from-v%d", i), fmt.Sprintf("to-v%d", i)
		fromRange := types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2}
		toRange := types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7}
		relations = append(relations, flowRelation(t, "method_call", fmt.Sprintf("from-file-%d", i), fromVersion, fromPath, fromRange,
			fmt.Sprintf("to-file-%d", i), toVersion, toPath, "Target.run", toRange))
		evidence = append(evidence,
			flowEvidence(fmt.Sprintf("from-ev-%d", i), fromPath, fromVersion, 0, 35, 1, 4),
			flowEvidence(fmt.Sprintf("to-ev-%d", i), toPath, toVersion, 35, 70, 6, 9),
		)
	}
	got, err := BuildSourceWikiFlowDiagram(relations, evidence)
	require.Error(t, err)
	require.ErrorContains(t, err, "64-edge limit")
	require.Empty(t, got.Markdown, "oversized input must error instead of returning a silently incomplete graph")
}

func TestBuildSourceWikiFlowDiagramReturnsEmptyWhenNoAllowlistedEvidenceBackedEdges(t *testing.T) {
	relation := flowRelation(t, "runtime_guess", "file-a", "version-a", "api/Controller.java",
		types.SourceRange{StartByte: 10, EndByte: 24, StartLine: 2, EndLine: 2},
		"file-b", "version-b", "svc/Service.java", "Service.run",
		types.SourceRange{StartByte: 40, EndByte: 56, StartLine: 7, EndLine: 7})
	got, err := BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, nil)
	require.NoError(t, err)
	require.Empty(t, got.Markdown)
	require.Empty(t, got.EvidenceIDs)
	require.False(t, got.Uncertain)

	relation.Kind = "method_call"
	got, err = BuildSourceWikiFlowDiagram([]types.SourceCodeRelation{relation}, nil)
	require.NoError(t, err)
	require.Empty(t, got.Markdown, "a known relation without exact evidence is not rendered")
}
