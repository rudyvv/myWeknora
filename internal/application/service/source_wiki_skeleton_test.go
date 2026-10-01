package service

import (
	"fmt"
	"testing"

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

func TestSourceWikiSkeletonKeepsUnmatchedEntrypointAsExplicitlyUncertain(t *testing.T) {
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
