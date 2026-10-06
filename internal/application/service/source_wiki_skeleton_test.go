package service

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceWikiSkeletonPlansSystemEntrypointModulesAndEvidenceBackedFlows(t *testing.T) {
	input := sourceWikiSkeletonInput{
		SourceID:   "source-a",
		SnapshotID: "snapshot-7",
		Files: []sourceWikiSkeletonFile{
			{Path: "server/orders/OrderController.java", Facts: []types.ParsedSourceFact{
				{Kind: "java_type", Name: "OrderController", Namespace: "demo.orders.OrderController", OwnerKind: "class", Quality: "structural"},
				{Kind: "spring_mapping", RoutePath: "/orders", HTTPMethod: "GET", Quality: "structural"},
			}},
			{Path: "server/orders/OrderService.java", Facts: []types.ParsedSourceFact{
				{Kind: "java_type", Name: "OrderService", Namespace: "demo.orders.OrderService", OwnerKind: "interface", Quality: "structural"},
			}},
			{Path: "server/orders/OrderServiceImpl.java", Facts: []types.ParsedSourceFact{
				{Kind: "java_type", Name: "OrderServiceImpl", Namespace: "demo.orders.OrderServiceImpl", OwnerKind: "class", Quality: "structural"},
			}},
			{Path: "server/orders/OrderMapper.java", Facts: []types.ParsedSourceFact{
				{Kind: "mybatis_mapper", Namespace: "demo.orders.OrderMapper", Quality: "structural"},
			}},
			{Path: "server/generated/GeneratedController.java", Generated: true, Facts: []types.ParsedSourceFact{
				{Kind: "java_type", Name: "GeneratedController", Namespace: "demo.generated.GeneratedController", OwnerKind: "class", Quality: "structural"},
			}},
			{Path: "web/orders.ts", Facts: []types.ParsedSourceFact{
				{Kind: "api_request", Name: "loadOrders", RoutePath: "/api/orders", HTTPMethod: "GET", Quality: "structural"},
			}},
		},
		Relations: []types.SourceCodeRelation{
			{Kind: "http_route", FromPath: "web/orders.ts", FromKey: "GET /api/orders", FromFileID: "web-orders", ToPath: "server/orders/OrderController.java", ToFileID: "controller", ToKey: "demo.orders.OrderController#list", Determinacy: "certain", Quality: "structural"},
			{Kind: "method_call", FromPath: "server/orders/OrderController.java", FromKey: "demo.orders.OrderController#list -> service.list", FromFileID: "controller", ToPath: "server/orders/OrderServiceImpl.java", ToFileID: "service", ToKey: "demo.orders.OrderServiceImpl#list", Determinacy: "certain", Quality: "structural"},
			{Kind: "mapper_statement", FromPath: "server/orders/OrderServiceImpl.java", FromKey: "demo.orders.OrderServiceImpl#list -> mapper.list", FromFileID: "service", ToPath: "server/orders/OrderMapper.xml", ToFileID: "mapper-xml", ToKey: "demo.orders.OrderMapper#list", Determinacy: "uncertain", Quality: "structural", ResolutionReason: "Mapper statement is ambiguous"},
		},
	}

	plan := buildSourceWikiSkeleton(input, 40)
	byKey := make(map[string]SourceWikiTopic, len(plan.Topics))
	for _, topic := range plan.Topics {
		byKey[topic.TopicKey] = topic
		require.Equal(t, "source-a", topic.SourceID)
		require.Equal(t, "snapshot-7", topic.SnapshotID)
	}
	require.Contains(t, byKey, "system")
	require.Contains(t, byKey, "module/server/orders")
	require.Contains(t, byKey, "flow/GET /api/orders")
	require.Equal(t, 1, plan.ModuleCount, "related controller, service, and mapper files form one topic, not a directory-tree mirror")
	require.Equal(t, 1, plan.FlowCount)
	require.True(t, byKey["flow/GET /api/orders"].Uncertain,
		"one uncertain static edge makes the flow's relationship claim uncertain")
	require.Contains(t, byKey["flow/GET /api/orders"].UncertaintyReasons, "Mapper statement is ambiguous")
	require.NotContains(t, byKey, "module/server/generated")
	require.Equal(t, "planned", byKey["system"].Status)
}

func TestSourceWikiSkeletonCapsInitialBatchAndKeepsStableSourceTopicKeys(t *testing.T) {
	input := sourceWikiSkeletonInput{SourceID: "source-a", SnapshotID: "snapshot-9"}
	for i := 0; i < 55; i++ {
		input.Files = append(input.Files, sourceWikiSkeletonFile{
			Path:  fmt.Sprintf("src/module-%02d/ModuleController.java", i),
			Facts: []types.ParsedSourceFact{{Kind: "java_type", Name: "ModuleController", Namespace: fmt.Sprintf("demo.module%d.ModuleController", i), OwnerKind: "class", Quality: "structural"}},
		})
	}
	plan := buildSourceWikiSkeleton(input, 40)
	require.Len(t, plan.Topics, 56, "the full bounded plan exposes expansion candidates after the initial batch")
	require.Equal(t, 40, plan.InitialCount)
	require.Equal(t, 16, plan.ExpansionCount)
	initial, expansion := 0, 0
	keys := map[string]bool{}
	for _, topic := range plan.Topics {
		require.False(t, keys[topic.TopicKey], "topic keys are unique within a source")
		keys[topic.TopicKey] = true
		if topic.Status == "planned" {
			initial++
		}
		if topic.Status == "expansion" {
			expansion++
		}
	}
	require.Equal(t, 40, initial)
	require.Equal(t, 16, expansion)

	otherSource := input
	otherSource.SourceID = "source-b"
	other := buildSourceWikiSkeleton(otherSource, 40)
	require.Equal(t, plan.Topics[1].TopicKey, other.Topics[1].TopicKey,
		"canonical theme identity stays stable across publications; source ID is its owner namespace")
	require.NotEqual(t, plan.Topics[1].SourceID, other.Topics[1].SourceID)
}

func TestSourceWikiSkeletonDoesNotPromoteUncertainRouteIntoCertainFlow(t *testing.T) {
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: "source-a", SnapshotID: "snapshot-1",
		Files:     []sourceWikiSkeletonFile{{Path: "web/app.ts", Facts: []types.ParsedSourceFact{{Kind: "api_request", RoutePath: "/orders", HTTPMethod: "GET", Quality: "structural"}}}},
		Relations: []types.SourceCodeRelation{{Kind: "http_route", FromPath: "web/app.ts", FromKey: "GET /orders", FromFileID: "web", ToKey: "Controller#list", Determinacy: "uncertain", Quality: "degraded", ResolutionReason: "route mapping is conditional"}},
	}, 40)
	require.Len(t, plan.Topics, 2, "system and one evidence-backed entry-point topic")
	flow := plan.Topics[1]
	require.Equal(t, "flow/GET /orders", flow.TopicKey)
	require.True(t, flow.Uncertain)
	require.Contains(t, flow.UncertaintyReasons, "route mapping is conditional")
	require.NotEqual(t, "certain", flow.Relations[0].Determinacy)
}

func TestSourceWikiSkeletonDoesNotInventRelationForUnregisteredEntrypoint(t *testing.T) {
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: "source-a", SnapshotID: "snapshot-2",
		Files: []sourceWikiSkeletonFile{{Path: "web/orders.ts", Facts: []types.ParsedSourceFact{{
			Kind: "api_request", RoutePath: "/orders", HTTPMethod: "POST", Quality: "structural",
		}}}},
	}, 40)
	require.Len(t, plan.Topics, 2)
	flow := plan.Topics[1]
	require.Equal(t, "flow/POST /orders", flow.TopicKey)
	require.True(t, flow.Uncertain, "client route alone is not an end-to-end validated backend flow")
	require.Contains(t, flow.UncertaintyReasons, "No statically validated backend route relationship was found for this request")
	require.Empty(t, flow.Relations)
}

func TestSourceWikiRequestOnlyFlowRetainsExactEvidenceAndUncertainDiagram(t *testing.T) {
	request := types.ParsedSourceFact{
		Kind: "api_request", Name: "loadOrders", RoutePath: "/orders", HTTPMethod: "GET", Quality: "structural",
		Range: types.SourceRange{StartByte: 20, EndByte: 38, StartLine: 2, EndLine: 2},
	}
	member := source.SourceRelationMember{Path: "web/orders.ts", FileID: "orders-file", VersionID: "orders-v1", Facts: []types.ParsedSourceFact{request}}
	relations := source.CorrelateSourceFacts(7, "source-a", "snapshot-a", []source.SourceRelationMember{member})
	require.Len(t, relations, 1)
	resolver := source.NewSourceRelationFactRefResolver(source.SourceRelationFactSnapshot{
		TenantID: 7, DataSourceID: "source-a", SnapshotID: "snapshot-a", Complete: true,
		Members: []source.SourceRelationMember{member},
	})
	resolvedRelations, err := sourceWikiResolveRelationFactRefs(resolver, relations)
	require.NoError(t, err, "the request-only anchor must be accepted only after exact snapshot replay")

	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: "source-a", SnapshotID: "snapshot-a",
		Files:     []sourceWikiSkeletonFile{{Path: member.Path, Facts: member.Facts}},
		Relations: resolvedRelations,
	}, 40)
	require.Len(t, plan.Topics, 2)
	flow := plan.Topics[1]
	require.Equal(t, "flow/GET /orders", flow.TopicKey)
	require.True(t, flow.Uncertain)
	require.Contains(t, flow.UncertaintyReasons, "No statically validated backend route relationship was found for this request")
	require.Len(t, flow.Relations, 1)
	require.Equal(t, "uncertain", flow.Relations[0].Determinacy)
	require.Empty(t, flow.Relations[0].ToFileID, "a request-only anchor must not claim a backend file")
	require.Empty(t, flow.Relations[0].ToKey, "a request-only anchor must not name a backend candidate")

	targets, err := sourceWikiFlowEvidenceRanges(flow.Relations, "source-a", "snapshot-a")
	require.NoError(t, err)
	require.Equal(t, []types.SourceRange{request.Range}, targets[sourceWikiFlowEvidenceTarget{FileID: member.FileID, VersionID: member.VersionID, Path: member.Path}])

	evidence := []types.SourceWikiEvidence{{ID: "e-request", KnowledgeID: member.FileID,
		SourceEvidence: types.SourceEvidence{DataSourceID: "source-a", SnapshotID: "snapshot-a", FileVersionID: member.VersionID, Path: member.Path, Range: request.Range}}}
	diagram, err := BuildSourceWikiFlowDiagram(flow.Relations, evidence)
	require.NoError(t, err)
	require.True(t, diagram.Uncertain)
	require.Equal(t, []string{"e-request"}, diagram.EvidenceIDs)
	require.Contains(t, diagram.Markdown, "-.->|uncertain HTTP route|")
	require.Contains(t, diagram.Markdown, "unresolved endpoint")
	require.NoError(t, sourceWikiValidateDiagramFactEvidence(flow.Relations, evidence, diagram))
}

func TestSourceWikiBatchCandidateCardRetainsCompleteDraftAndEvidence(t *testing.T) {
	draft := sourceWikiDraft{Summary: strings.Repeat("s", 400), Sections: make([]sourceWikiSection, 9)}
	for i := range draft.Sections {
		draft.Sections[i] = sourceWikiSection{Text: fmt.Sprintf("section-%d:%s", i, strings.Repeat("x", 240)), EvidenceIDs: []string{fmt.Sprintf("e%03d", i)}}
	}
	evidence := make([]map[string]any, 7)
	for i := range evidence {
		evidence[i] = map[string]any{"record": types.SourceWikiEvidence{ID: fmt.Sprintf("e%03d", i)}, "text": fmt.Sprintf("evidence-%d:%s", i, strings.Repeat("y", 220))}
	}
	identity := map[string]any{"topic_key": "module/orders"}
	relations := []types.SourceCodeRelation{{Kind: "method_call", FromKey: "A#orders -> service"}}
	diagram := SourceWikiFlowDiagram{Markdown: "full flow diagram", EvidenceIDs: []string{"e000", "e006"}, Uncertain: true}

	card := sourceWikiBatchCandidateCard(identity, draft, evidence, relations, diagram)
	gotDraft, ok := card["draft"].(sourceWikiDraft)
	require.True(t, ok)
	require.Equal(t, draft.Summary, gotDraft.Summary)
	require.Equal(t, draft.Sections, gotDraft.Sections, "group QA must see sections beyond the former eight-section clip in full")
	gotEvidence, ok := card["evidence"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, gotEvidence, len(evidence), "group QA must see every verified evidence item")
	for i, item := range gotEvidence {
		require.Equal(t, evidence[i]["text"], item["text"], "evidence excerpts must not be clipped")
	}
	require.Equal(t, identity, card["identity"])
	require.Equal(t, relations, card["relations"])
	require.Equal(t, diagram.Markdown, card["flow_diagram"].(map[string]any)["markdown"])
}

func TestSourceWikiSkeletonIntegrityCheckRejectsCorruptCoverageAccounting(t *testing.T) {
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: "source-a", SnapshotID: "snapshot-3",
		Files: []sourceWikiSkeletonFile{{Path: "src/orders/OrderService.java", Facts: []types.ParsedSourceFact{{
			Kind: "java_type", Name: "OrderService", OwnerKind: "class", Quality: "structural",
		}}}},
	}, 40)
	require.NoError(t, validateSourceWikiSkeletonPlan(plan))

	corrupt := plan
	corrupt.Topics = append([]SourceWikiTopic(nil), plan.Topics...)
	corrupt.Topics[1].Status = "expansion"
	require.Error(t, validateSourceWikiSkeletonPlan(corrupt), "a topic beyond the batch prefix cannot silently become expansion")

	corrupt = plan
	corrupt.FlowCount++
	require.Error(t, validateSourceWikiSkeletonPlan(corrupt), "persisted counters must match topic kinds")
}

func TestSourceWikiBuildPageRetainsFlowDiagramEvidenceLinks(t *testing.T) {
	attempt := &types.SourceWikiAttempt{
		TenantID: 1, SourceID: "source-a", SnapshotID: "snapshot-7", TopicKind: "flow",
		TopicKey: "flow/GET /orders", Slug: "concept/source-a/flow-orders",
	}
	evidence := collectedWikiEvidence{Evidence: types.SourceWikiEvidence{
		ID: "e001", KnowledgeID: "knowledge-a", SourceEvidence: types.SourceEvidence{
			DataSourceID: "source-a", SnapshotID: "snapshot-7", FileVersionID: "version-a", Path: "src/OrderController.java",
			CommitSHA: "123456789abcdef", Range: types.SourceRange{StartByte: 0, EndByte: 20, StartLine: 1, EndLine: 2},
		},
	}}
	registry := map[string]collectedWikiEvidence{"e001": evidence}
	diagram := SourceWikiFlowDiagram{Markdown: "```mermaid\nflowchart TD\n  n0 --> n1\n```\n", EvidenceIDs: []string{"e001"}}
	page := sourceWikiBuildPage("kb-a", attempt, sourceWikiDraft{
		Title: "Orders flow", Summary: "An orders request follows the cited route.",
		Sections: []sourceWikiSection{{Text: "The route is declared.", EvidenceIDs: []string{"e001"}}},
	}, []collectedWikiEvidence{evidence}, registry, 0, diagram)
	require.Contains(t, page.Content, "flowchart TD")
	require.Contains(t, page.Content, "evidence_id=e001", "diagram edges retain the exact source evidence link")
	require.NotNil(t, page.SourceProvenance)
	require.Len(t, page.SourceProvenance.Evidence, 1)
}

func TestSourceWikiFlowEvidenceWindowsCoverExactUTF8RangesAndFailClosedAtLimit(t *testing.T) {
	raw := make([]byte, 12000)
	for i := range raw {
		raw[i] = 'x'
		if i%100 == 99 {
			raw[i] = '\n'
		}
	}
	target := sourceWikiRangeForBytes(raw, 2500, 2650)
	windows, err := sourceWikiFlowEvidenceWindows(raw, []types.SourceRange{target})
	require.NoError(t, err)
	require.Len(t, windows, 1)
	require.LessOrEqual(t, windows[0].EndByte-windows[0].StartByte, sourceWikiFlowEvidenceWindowBytes)
	require.True(t, sourceWikiFlowRangeCovers(windows[0], target))
	require.Equal(t, 1+bytes.Count(raw[:windows[0].StartByte], []byte("\n")), windows[0].StartLine)
	require.Equal(t, 1+bytes.Count(raw[:windows[0].EndByte-1], []byte("\n")), windows[0].EndLine)

	oversized := sourceWikiRangeForBytes(raw, 1000, 1000+sourceWikiFlowEvidenceWindowBytes+1)
	_, err = sourceWikiFlowEvidenceWindows(raw, []types.SourceRange{oversized})
	require.ErrorContains(t, err, "exceeds the bounded")

	wrongCoordinates := target
	wrongCoordinates.StartLine++
	_, err = sourceWikiFlowEvidenceWindows(raw, []types.SourceRange{wrongCoordinates})
	require.ErrorContains(t, err, "exact UTF-8 source coordinates")
}
