package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestSourceWikiRelationFactResolutionHydratesVerifiedRefsAndFailsClosed(t *testing.T) {
	rangeAt := func(start int) types.SourceRange {
		return types.SourceRange{StartByte: start, EndByte: start + 8, StartLine: 1, EndLine: 1}
	}
	request := types.ParsedSourceFact{Kind: "api_request", Name: "detail", RoutePath: "/detail", HTTPMethod: "GET", Quality: "structural", Range: rangeAt(1)}
	prefix := types.ParsedSourceFact{Kind: "api_prefix", Name: "/app", RoutePath: "/app", Certainty: "certain", Quality: "structural", Range: rangeAt(11)}
	proxy := types.ParsedSourceFact{Kind: "api_proxy", Name: "/app", RoutePath: "/svc", TargetName: "^/app", OwnerName: "web/app", Certainty: "certain", Quality: "structural", Range: rangeAt(21)}
	classMapping := types.ParsedSourceFact{Kind: "spring_mapping", Name: "Controller", Namespace: "demo.Controller", RoutePath: "/svc",
		StatementType: "type", OwnerKind: "type", Quality: "structural", Range: rangeAt(31)}
	handlerMapping := types.ParsedSourceFact{Kind: "spring_mapping", Name: "detail", Namespace: "demo.Controller", RoutePath: "/detail",
		StatementType: "method", OwnerKind: "method", OwnerName: "detail", HTTPMethod: "GET", Quality: "structural", Range: rangeAt(41)}
	members := []source.SourceRelationMember{
		{Path: "web/app/views/detail.js", FileID: "request-file", VersionID: "request-v1", Facts: []types.ParsedSourceFact{request}},
		{Path: "web/app/api.js", FileID: "prefix-file", VersionID: "prefix-v1", Facts: []types.ParsedSourceFact{prefix}},
		{Path: "web/app/vue.config.js", FileID: "proxy-file", VersionID: "proxy-v1", Facts: []types.ParsedSourceFact{proxy}},
		{Path: "server/Controller.java", FileID: "controller-file", VersionID: "controller-v1", Facts: []types.ParsedSourceFact{classMapping, handlerMapping}},
	}
	relations := source.CorrelateSourceFacts(9, "source", "snapshot", members)
	var relation types.SourceCodeRelation
	for _, candidate := range relations {
		if candidate.Kind == "http_route" {
			relation = candidate
			break
		}
	}
	if relation.Kind != "http_route" {
		t.Fatal("fixed facts did not produce an HTTP route relation")
	}
	resolver := source.NewSourceRelationFactRefResolver(source.SourceRelationFactSnapshot{
		TenantID: 9, DataSourceID: "source", SnapshotID: "snapshot", Complete: true,
		Members: members,
	})

	resolved, err := sourceWikiResolveRelationFactRefs(resolver, []types.SourceCodeRelation{relation})
	if err != nil {
		t.Fatalf("valid exact fact ref was rejected: %v", err)
	}
	var got []types.SourceRelationFactRef
	if err := json.Unmarshal(resolved[0].Context, &got); err != nil {
		t.Fatalf("resolved context is not a typed ref list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("resolved route should retain only its matching prefix, proxy, and class mapping: %#v", got)
	}

	legacy := relation
	legacy.Context = types.JSON(`[]`)
	replayed, err := sourceWikiResolveRelationFactRefs(resolver, []types.SourceCodeRelation{legacy})
	if err != nil {
		t.Fatalf("exact legacy relation replay was rejected: %v", err)
	}
	var replayedRefs []types.SourceRelationFactRef
	if err := json.Unmarshal(replayed[0].Context, &replayedRefs); err != nil || len(replayedRefs) != 3 {
		t.Fatalf("legacy relation did not persist its replayed exact refs: %#v, %v", replayedRefs, err)
	}
	if replayed[0].Determinacy != relation.Determinacy {
		t.Fatal("fact-reference replay changed the relation determinacy")
	}

	unmatched := legacy
	unmatched.FromKey += " changed"
	if _, err := sourceWikiResolveRelationFactRefs(resolver, []types.SourceCodeRelation{unmatched}); err == nil {
		t.Fatal("an HTTP route with no exact legacy replay was treated as having no config facts")
	}

	incomplete := source.NewSourceRelationFactRefResolver(source.SourceRelationFactSnapshot{
		TenantID: 9, DataSourceID: "source", SnapshotID: "snapshot", Complete: false,
		Members: members,
	})
	if _, err := sourceWikiResolveRelationFactRefs(incomplete, []types.SourceCodeRelation{relation}); err == nil {
		t.Fatal("an incomplete snapshot was accepted as verified configuration evidence")
	}
	oversized := relation
	oversized.Context = types.JSON(bytes.Repeat([]byte{' '}, (1<<20)+1))
	partial, err := sourceWikiResolveRelationFactRefs(resolver, []types.SourceCodeRelation{relation, oversized})
	var capacityErr *source.SourceRelationFactCapacityError
	if !errors.As(err, &capacityErr) {
		t.Fatalf("oversized persisted route context should preserve its typed capacity error: %v", err)
	}
	if partial != nil {
		t.Fatalf("relation resolution must discard prior work instead of returning a partial slice: %#v", partial)
	}

	ordinary := types.SourceCodeRelation{Kind: "method_call", Context: types.JSON(`{"legacy":"context"}`)}
	unchanged, err := sourceWikiResolveRelationFactRefs(nil, []types.SourceCodeRelation{ordinary})
	if err != nil || !reflect.DeepEqual(unchanged, []types.SourceCodeRelation{ordinary}) {
		t.Fatalf("non-route relations should retain their existing validation path: %#v, %v", unchanged, err)
	}
}

func TestSourceWikiRelationResolutionCapacityIsDeferred(t *testing.T) {
	capacityErr := &source.SourceRelationFactCapacityError{Budget: "cumulative refs", Requested: 1, Limit: 0}
	deferred := sourceWikiRelationResolutionError(capacityErr)
	if !errors.Is(deferred, repository.ErrSourceWikiDerivationDeferred) || errors.Is(deferred, repository.ErrSourceWikiDerivationUnavailable) {
		t.Fatalf("capacity exhaustion must defer Wiki derivation: %v", deferred)
	}
	var got *source.SourceRelationFactCapacityError
	if !errors.As(deferred, &got) || got != capacityErr {
		t.Fatalf("deferred error should preserve the typed capacity cause: %#v", deferred)
	}
	unavailable := sourceWikiRelationResolutionError(errors.New("reference identity mismatch"))
	if !errors.Is(unavailable, repository.ErrSourceWikiDerivationUnavailable) || errors.Is(unavailable, repository.ErrSourceWikiDerivationDeferred) {
		t.Fatalf("invalid reference evidence must remain unavailable: %v", unavailable)
	}
}

func TestSourceWikiFlowEvidenceRangesIncludeOnlyExactRouteFactRefs(t *testing.T) {
	fromRange := types.SourceRange{StartByte: 0, EndByte: 8, StartLine: 1, EndLine: 1}
	toRange := types.SourceRange{StartByte: 10, EndByte: 22, StartLine: 2, EndLine: 2}
	configRange := types.SourceRange{StartByte: 4, EndByte: 15, StartLine: 1, EndLine: 1}
	context, err := json.Marshal([]types.SourceRelationFactRef{{
		DataSourceID: "source", SnapshotID: "snapshot", FileID: "config", FileVersionID: "config-v1",
		Path: "web/proxy.js", Kind: "api_proxy", Role: "api_proxy", Quality: "structural", Range: configRange,
	}})
	if err != nil {
		t.Fatal(err)
	}
	fromRaw, _ := json.Marshal(fromRange)
	toRaw, _ := json.Marshal(toRange)
	relation := types.SourceCodeRelation{
		DataSourceID: "source", SnapshotID: "snapshot", Kind: "http_route",
		FromFileID: "request", FromVersionID: "request-v1", FromPath: "web/orders.ts", FromRange: types.JSON(fromRaw),
		ToFileID: "controller", ToVersionID: "controller-v1", ToPath: "server/Orders.java", ToRange: types.JSON(toRaw),
		Context: types.JSON(context),
	}
	targets, err := sourceWikiFlowEvidenceRanges([]types.SourceCodeRelation{relation}, "source", "snapshot")
	if err != nil {
		t.Fatalf("exact relation and fact ranges were rejected: %v", err)
	}
	if len(targets) != 3 {
		t.Fatalf("expected the request, handler, and exact proxy fact files; got %#v", targets)
	}
	configKey := sourceWikiFlowEvidenceTarget{FileID: "config", VersionID: "config-v1", Path: "web/proxy.js"}
	if !reflect.DeepEqual(targets[configKey], []types.SourceRange{configRange}) {
		t.Fatalf("the exact config-fact range was not added to flow evidence: %#v", targets[configKey])
	}
	evidence := []types.SourceWikiEvidence{
		{ID: "e1", KnowledgeID: "request", SourceEvidence: types.SourceEvidence{DataSourceID: "source", SnapshotID: "snapshot", FileVersionID: "request-v1", Path: "web/orders.ts", Range: fromRange}},
		{ID: "e2", KnowledgeID: "controller", SourceEvidence: types.SourceEvidence{DataSourceID: "source", SnapshotID: "snapshot", FileVersionID: "controller-v1", Path: "server/Orders.java", Range: toRange}},
		{ID: "e3", KnowledgeID: "config", SourceEvidence: types.SourceEvidence{DataSourceID: "source", SnapshotID: "snapshot", FileVersionID: "config-v1", Path: "web/proxy.js", Range: configRange}},
	}
	if err := sourceWikiValidateDiagramFactEvidence([]types.SourceCodeRelation{relation}, evidence, SourceWikiFlowDiagram{EvidenceIDs: []string{"e1", "e2", "e3"}}); err != nil {
		t.Fatalf("diagram with exact config-fact evidence was rejected: %v", err)
	}
	if err := sourceWikiValidateDiagramFactEvidence([]types.SourceCodeRelation{relation}, evidence, SourceWikiFlowDiagram{EvidenceIDs: []string{"e1", "e2"}}); err == nil {
		t.Fatal("diagram omitted the exact ID for its route config fact")
	}

	bad := relation
	var refs []types.SourceRelationFactRef
	if err := json.Unmarshal(bad.Context, &refs); err != nil {
		t.Fatal(err)
	}
	refs[0].SnapshotID = "other-snapshot"
	bad.Context, _ = json.Marshal(refs)
	if _, err := sourceWikiFlowEvidenceRanges([]types.SourceCodeRelation{bad}, "source", "snapshot"); err == nil {
		t.Fatal("a cross-snapshot config reference was accepted")
	}
}
