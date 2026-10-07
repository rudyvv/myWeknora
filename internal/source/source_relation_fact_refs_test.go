package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestCorrelateHTTPRouteRetainsOnlyParticipatingConfigurationFacts(t *testing.T) {
	request := relationFact("api_request", "detail", "", 1, 8)
	request.RoutePath, request.HTTPMethod = "/detail", "GET"
	prefix := relationFact("api_prefix", "/app", "", 11, 20)
	prefix.RoutePath, prefix.Certainty = "/app", "uncertain"
	unmatchedPrefix := relationFact("api_prefix", "/unused", "", 21, 30)
	unmatchedPrefix.RoutePath, unmatchedPrefix.Certainty = "/unused", "certain"
	proxy := relationFact("api_proxy", "/app", "", 31, 50)
	proxy.RoutePath, proxy.TargetName, proxy.OwnerName = "/svc", "^/app", "web/app"
	proxy.Certainty = "certain"
	classMapping := relationFact("spring_mapping", "Controller", "demo.Controller", 51, 65)
	classMapping.RoutePath, classMapping.OwnerKind, classMapping.StatementType = "/svc", "type", "type"
	handler := relationFact("spring_mapping", "detail", "demo.Controller", 66, 80)
	handler.RoutePath, handler.OwnerKind, handler.OwnerName, handler.HTTPMethod = "/detail", "method", "detail", "GET"

	members := []SourceRelationMember{
		{Path: "web/app/views/detail.js", FileID: "request-file", VersionID: "request-v1", Facts: []types.ParsedSourceFact{request}},
		{Path: "web/app/api.js", FileID: "prefix-file", VersionID: "prefix-v1", Facts: []types.ParsedSourceFact{prefix, unmatchedPrefix}},
		{Path: "web/app/vue.config.js", FileID: "proxy-file", VersionID: "proxy-v1", Facts: []types.ParsedSourceFact{proxy}},
		{Path: "web/other/vue.config.js", FileID: "sibling-proxy", VersionID: "sibling-proxy-v1", Facts: []types.ParsedSourceFact{proxy}},
		{Path: "server/Controller.java", FileID: "controller-file", VersionID: "controller-v1", Facts: []types.ParsedSourceFact{classMapping, handler}},
	}
	members[3].Facts[0].OwnerName = "web/other"

	relations := CorrelateSourceFacts(1, "source", "snapshot", members)
	var route *types.SourceCodeRelation
	for i := range relations {
		if relations[i].Kind == "http_route" && relations[i].FromFileID == "request-file" {
			route = &relations[i]
			break
		}
	}
	if route == nil {
		t.Fatal("request did not produce its matching backend route")
	}
	if route.Determinacy != "uncertain" || route.ToFileID != "" || route.ToVersionID != "" || route.ToPath != "" {
		t.Fatalf("conditional prefix match became a certain or navigable route: %#v", route)
	}

	var refs []types.SourceRelationFactRef
	if err := json.Unmarshal(route.Context, &refs); err != nil {
		t.Fatalf("route context is not a typed fact-reference array: %v", err)
	}
	if len(refs) != 3 {
		t.Fatalf("route should cite only the matching prefix, proxy, and class mapping; got %#v", refs)
	}
	byRole := make(map[string]types.SourceRelationFactRef, len(refs))
	for _, ref := range refs {
		byRole[ref.Role] = ref
		if ref.DataSourceID != "source" || ref.SnapshotID != "snapshot" {
			t.Errorf("fact reference is not bound to the relation snapshot: %#v", ref)
		}
		if ref.Quality != "structural" {
			t.Errorf("fact reference lost parser quality: %#v", ref)
		}
	}
	want := map[string]struct {
		fileID, versionID, path, kind string
		span                          types.SourceRange
	}{
		"api_prefix":           {"prefix-file", "prefix-v1", "web/app/api.js", "api_prefix", prefix.Range},
		"api_proxy":            {"proxy-file", "proxy-v1", "web/app/vue.config.js", "api_proxy", proxy.Range},
		"spring_class_mapping": {"controller-file", "controller-v1", "server/Controller.java", "spring_mapping", classMapping.Range},
	}
	for role, expected := range want {
		ref, ok := byRole[role]
		if !ok {
			t.Errorf("matching route omitted causal fact role %q", role)
			continue
		}
		if ref.FileID != expected.fileID || ref.FileVersionID != expected.versionID || ref.Path != expected.path || ref.Kind != expected.kind || ref.Range != expected.span {
			t.Errorf("route reference for %q lost exact snapshot identity or range: %#v", role, ref)
		}
	}
}

func TestCorrelateHTTPRouteKeepsMultipleTargetsUncertainAndRetainsTheirFacts(t *testing.T) {
	snapshot, _ := httpRouteFactSnapshot(t)
	secondClass := relationFact("spring_mapping", "Controller", "demo.OtherController", 81, 95)
	secondClass.RoutePath, secondClass.OwnerKind, secondClass.StatementType = "/svc", "type", "type"
	secondHandler := relationFact("spring_mapping", "detail", "demo.OtherController", 96, 110)
	secondHandler.RoutePath, secondHandler.OwnerKind, secondHandler.OwnerName, secondHandler.HTTPMethod = "/detail", "method", "detail", "GET"
	snapshot.Members = append(snapshot.Members, SourceRelationMember{Path: "server/OtherController.java", FileID: "other-controller-file",
		VersionID: "other-controller-v1", Facts: []types.ParsedSourceFact{secondClass, secondHandler}})

	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	var route *types.SourceCodeRelation
	for i := range relations {
		if relations[i].Kind == "http_route" {
			route = &relations[i]
			break
		}
	}
	if route == nil {
		t.Fatal("request did not produce an ambiguous backend candidate")
	}
	if route.Determinacy != "uncertain" || route.ToFileID != "" || route.ToVersionID != "" || route.ToPath != "" {
		t.Fatalf("multiple backend candidates became a certain or navigable route: %#v", route)
	}
	var refs []types.SourceRelationFactRef
	if err := json.Unmarshal(route.Context, &refs); err != nil {
		t.Fatalf("ambiguous route context is not a typed fact-reference array: %v", err)
	}
	classMappingCount := 0
	for _, ref := range refs {
		if ref.Role == "spring_class_mapping" {
			classMappingCount++
		}
	}
	if len(refs) != 4 || classMappingCount != 2 {
		t.Fatalf("ambiguous route should retain both candidates' config facts only; got %#v", refs)
	}
}

func TestCorrelateHTTPRouteOmitsClassMappingCoveredByMethodRange(t *testing.T) {
	snapshot, _ := httpRouteFactSnapshot(t)
	snapshot.Members[3].Facts[0].Range = snapshot.Members[3].Facts[1].Range
	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	for _, relation := range relations {
		if relation.Kind != "http_route" {
			continue
		}
		var refs []types.SourceRelationFactRef
		if err := json.Unmarshal(relation.Context, &refs); err != nil {
			t.Fatalf("route context is not a typed fact-reference array: %v", err)
		}
		for _, ref := range refs {
			if ref.Role == "spring_class_mapping" {
				t.Fatalf("class mapping already covered by the method range was redundantly cited: %#v", refs)
			}
		}
		if len(refs) != 2 {
			t.Fatalf("route should retain only its participating frontend config refs: %#v", refs)
		}
		return
	}
	t.Fatal("fixture did not produce an HTTP route")
}

func TestCorrelateCertainHTTPRouteRetainsParticipatingConfigurationFacts(t *testing.T) {
	snapshot, _ := httpRouteFactSnapshot(t)
	snapshot.Members[1].Facts[0].Certainty = "certain"
	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	for _, relation := range relations {
		if relation.Kind != "http_route" {
			continue
		}
		if relation.Determinacy != "certain" || relation.ToFileID != "controller-file" || relation.ToVersionID != "controller-v1" {
			t.Fatalf("verified route should remain certain and navigable: %#v", relation)
		}
		var refs []types.SourceRelationFactRef
		if err := json.Unmarshal(relation.Context, &refs); err != nil || len(refs) != 3 {
			t.Fatalf("certain route lost its participating config facts: %v, %#v", err, refs)
		}
		return
	}
	t.Fatal("fixture did not produce an HTTP route")
}

func TestParserHTTPRequestFactFlowsIntoSnapshotBoundRouteRefs(t *testing.T) {
	snapshot, _ := httpRouteFactSnapshot(t)
	raw := []byte("client.get('/detail')")
	digest := sha256.Sum256(raw)
	requestFact := relationFact("api_request", "detail", "", 0, len(raw))
	requestFact.RoutePath, requestFact.HTTPMethod, requestFact.Certainty = "/detail", "GET", "certain"
	requestFact.Text = string(raw)
	parsed := types.ParsedSourceFile{ParserVersion: "fixture-parser", SHA256: hex.EncodeToString(digest[:]),
		ByteLength: len(raw), Encoding: "utf-8", Quality: "structural", Facts: []types.ParsedSourceFact{requestFact},
		Chunks: []types.ParsedSourceChunk{{Content: string(raw), Range: types.SourceRange{StartByte: 0, EndByte: len(raw), StartLine: 1, EndLine: 1}, Quality: "structural"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(parsed)
	}))
	defer server.Close()
	requestResult, err := ParseFile(context.Background(), server.URL, snapshot.Members[0].Path, raw)
	if err != nil {
		t.Fatalf("parser HTTP rejected request facts needed for route correlation: %v", err)
	}
	snapshot.Members[0].Facts = requestResult.Facts
	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	for _, relation := range relations {
		if relation.Kind != "http_route" {
			continue
		}
		resolution := NewSourceRelationFactRefResolver(snapshot).Resolve(relation)
		if resolution.Status != SourceRelationFactRefsVerified || len(resolution.Refs) != 3 {
			t.Fatalf("parser HTTP facts did not survive correlation and snapshot verification: %#v", resolution)
		}
		return
	}
	t.Fatal("parser HTTP request fact did not produce a route relation")
}

func TestResolveSourceRelationFactRefsVerifiesPersistedSnapshotFacts(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	resolution := NewSourceRelationFactRefResolver(snapshot).Resolve(relation)
	if resolution.Status != SourceRelationFactRefsVerified || len(resolution.Refs) != 3 {
		t.Fatalf("persisted fact refs were not verified against the fixed snapshot: %#v", resolution)
	}
}

func TestResolveSourceRelationFactRefsReplaysLegacyRouteAgainstCompleteSnapshot(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	relation.Context = types.JSON("[]")
	resolution := NewSourceRelationFactRefResolver(snapshot).Resolve(relation)
	if resolution.Status != SourceRelationFactRefsReplayed || len(resolution.Refs) != 3 {
		t.Fatalf("legacy route did not recover its exact configuration refs: %#v", resolution)
	}
}

func TestBoundedSourceRelationReplayDefersBeforeRefBudgetIsExceeded(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	relation.Context = types.JSON(`[]`)
	resolver := newSourceRelationFactRefResolverWithLimits(snapshot, 2, 1<<20)
	resolution := resolver.Resolve(relation)
	var capacityErr *SourceRelationFactCapacityError
	if !errors.As(resolution.Err, &capacityErr) || capacityErr.Budget != "cumulative refs" || capacityErr.Used > capacityErr.Limit {
		t.Fatalf("legacy replay must stop at the cumulative ref bound: %#v", resolution)
	}
	if len(resolution.Refs) != 0 {
		t.Fatalf("an over-budget replay must not return a partial ref set: %#v", resolution.Refs)
	}
	if len(resolution.Context) != 0 {
		t.Fatalf("an over-budget replay must not return a partial serialized context: %q", resolution.Context)
	}

	resolver = newSourceRelationFactRefResolverWithLimits(snapshot, 100, 1)
	resolution = resolver.Resolve(relation)
	if !errors.As(resolution.Err, &capacityErr) || capacityErr.Budget != "serialized context bytes" {
		t.Fatalf("legacy replay must check serialized bytes before marshaling: %#v", resolution)
	}
}

func TestSourceRelationRefMergeChecksCapacityBeforeAppending(t *testing.T) {
	first := types.SourceRelationFactRef{FileID: "first"}
	second := types.SourceRelationFactRef{FileID: "second"}
	third := types.SourceRelationFactRef{FileID: "third"}
	budget := newSourceRelationFactCapacityBudget(2, 1<<20)
	merged, err := sourceRelationFactRefsWithBudget(budget, first)
	if err != nil || len(merged) != 1 {
		t.Fatalf("initial bounded ref allocation failed: %#v, %v", merged, err)
	}
	_, err = mergeSourceRelationFactRefsWithBudget(merged, []types.SourceRelationFactRef{second, third}, budget)
	var capacityErr *SourceRelationFactCapacityError
	if !errors.As(err, &capacityErr) || capacityErr.Budget != "cumulative refs" {
		t.Fatalf("merge must fail before retaining a ref beyond the budget: %v", err)
	}
	if budget.refs != 2 || len(merged) > 2 {
		t.Fatalf("failed merge exceeded retained-ref budget: used=%d refs=%d", budget.refs, len(merged))
	}
}

func TestResolveSourceRelationFactRefsReplayCanProveNoConfigurationRefs(t *testing.T) {
	request := relationFact("api_request", "detail", "", 1, 8)
	request.RoutePath, request.HTTPMethod = "/detail", "GET"
	handler := relationFact("spring_mapping", "detail", "demo.Controller", 20, 30)
	handler.RoutePath, handler.OwnerKind, handler.OwnerName, handler.HTTPMethod = "/detail", "method", "detail", "GET"
	snapshot := SourceRelationFactSnapshot{TenantID: 1, DataSourceID: "source", SnapshotID: "snapshot", Complete: true,
		Members: []SourceRelationMember{
			{Path: "web/detail.js", FileID: "request-file", VersionID: "request-v1", Facts: []types.ParsedSourceFact{request}},
			{Path: "server/Controller.java", FileID: "controller-file", VersionID: "controller-v1", Facts: []types.ParsedSourceFact{handler}},
		}}
	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	if len(relations) != 1 || relations[0].Kind != "http_route" {
		t.Fatalf("fixture should have one direct HTTP route, got %#v", relations)
	}
	relations[0].Context = types.JSON("[]")
	resolution := NewSourceRelationFactRefResolver(snapshot).Resolve(relations[0])
	if resolution.Status != SourceRelationFactRefsReplayed || len(resolution.Refs) != 0 {
		t.Fatalf("complete replay should distinguish a valid no-config route from missing evidence: %#v", resolution)
	}
}

func TestResolveSourceRelationFactRefsFailsClosedForIncompleteOrMismatchedSnapshots(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	incomplete := snapshot
	incomplete.Complete = false
	if got := NewSourceRelationFactRefResolver(incomplete).Resolve(relation); got.Status != SourceRelationFactRefsUnavailable || len(got.Refs) != 0 {
		t.Errorf("incomplete snapshot should not authorize refs: %#v", got)
	}
	wrongSnapshot := relation
	wrongSnapshot.SnapshotID = "another-snapshot"
	if got := NewSourceRelationFactRefResolver(snapshot).Resolve(wrongSnapshot); got.Status != SourceRelationFactRefsUnavailable || len(got.Refs) != 0 {
		t.Errorf("relation from another snapshot should not authorize refs: %#v", got)
	}
}

func TestResolveSourceRelationFactRefsRejectsStaleAndMalformedContext(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	var refs []types.SourceRelationFactRef
	if err := json.Unmarshal(relation.Context, &refs); err != nil || len(refs) == 0 {
		t.Fatalf("fixture does not contain typed refs: %v, %#v", err, relation.Context)
	}
	refs[0].FileVersionID = "stale-version"
	relation.Context, _ = json.Marshal(refs)
	if got := NewSourceRelationFactRefResolver(snapshot).Resolve(relation); got.Status != SourceRelationFactRefsUnavailable || len(got.Refs) != 0 {
		t.Errorf("stale fact identity should not authorize reads: %#v", got)
	}

	_, malformedRelation := httpRouteFactSnapshot(t)
	malformedRelation.Context = types.JSON("{not-an-array}")
	if got := NewSourceRelationFactRefResolver(snapshot).Resolve(malformedRelation); got.Status != SourceRelationFactRefsUnavailable || len(got.Refs) != 0 {
		t.Errorf("malformed context should not fall back to an unverified ref set: %#v", got)
	}
}

func TestResolveSourceRelationFactRefsRequiresExactLegacyRelationIdentity(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	relation.Context = types.JSON("[]")
	relation.FromKey += " changed"
	if got := NewSourceRelationFactRefResolver(snapshot).Resolve(relation); got.Status != SourceRelationFactRefsUnavailable || len(got.Refs) != 0 {
		t.Fatalf("legacy replay should not bind refs to a merely similar relation: %#v", got)
	}
}

func TestRootT15PersistedRefMustCauseItsSpecificRoute(t *testing.T) {
	snapshot, relation := httpRouteFactSnapshot(t)
	unused := relationFact("api_prefix", "/unused", "", 88, 99)
	unused.RoutePath, unused.Certainty = "/unused", "certain"
	snapshot.Members[1].Facts = append(snapshot.Members[1].Facts, unused)
	var refs []types.SourceRelationFactRef
	if err := json.Unmarshal(relation.Context, &refs); err != nil {
		t.Fatal(err)
	}
	changed := false
	for i := range refs {
		if refs[i].Role == "api_prefix" {
			refs[i].Range = unused.Range
			changed = true
		}
	}
	if !changed {
		t.Fatal("fixture did not cite the matching prefix")
	}
	relation.Context, _ = json.Marshal(refs)
	got := NewSourceRelationFactRefResolver(snapshot).Resolve(relation)
	if got.Status != SourceRelationFactRefsUnavailable {
		t.Fatalf("same-snapshot but noncausal prefix passed exact route validation: %#v", got)
	}
}

func TestRootT15CoverageCoordinatesCannotCrossConfigurationFiles(t *testing.T) {
	snapshot, _ := httpRouteFactSnapshot(t)
	classFact := snapshot.Members[3].Facts[0]
	handler := snapshot.Members[3].Facts[1]
	handler.Range = classFact.Range
	snapshot.Members[3].Facts = []types.ParsedSourceFact{handler}
	snapshot.Members = append(snapshot.Members, SourceRelationMember{Path: "server/config/Controller.java", FileID: "class-file",
		VersionID: "class-v1", Facts: []types.ParsedSourceFact{classFact}})
	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	for _, relation := range relations {
		if relation.Kind != "http_route" {
			continue
		}
		var refs []types.SourceRelationFactRef
		if err := json.Unmarshal(relation.Context, &refs); err != nil {
			t.Fatal(err)
		}
		for _, ref := range refs {
			if ref.Role == "spring_class_mapping" && ref.FileID == "class-file" {
				return
			}
		}
		t.Fatalf("matching file-local ranges in separate files suppressed the causal class mapping: %#v", refs)
	}
	t.Fatal("fixture did not produce the joined route")
}

func httpRouteFactSnapshot(t *testing.T) (SourceRelationFactSnapshot, types.SourceCodeRelation) {
	t.Helper()
	request := relationFact("api_request", "detail", "", 1, 8)
	request.RoutePath, request.HTTPMethod = "/detail", "GET"
	prefix := relationFact("api_prefix", "/app", "", 11, 20)
	prefix.RoutePath, prefix.Certainty = "/app", "uncertain"
	proxy := relationFact("api_proxy", "/app", "", 31, 50)
	proxy.RoutePath, proxy.TargetName, proxy.OwnerName, proxy.Certainty = "/svc", "^/app", "web/app", "certain"
	classMapping := relationFact("spring_mapping", "Controller", "demo.Controller", 51, 65)
	classMapping.RoutePath, classMapping.OwnerKind, classMapping.StatementType = "/svc", "type", "type"
	handler := relationFact("spring_mapping", "detail", "demo.Controller", 66, 80)
	handler.RoutePath, handler.OwnerKind, handler.OwnerName, handler.HTTPMethod = "/detail", "method", "detail", "GET"
	snapshot := SourceRelationFactSnapshot{TenantID: 1, DataSourceID: "source", SnapshotID: "snapshot", Complete: true,
		Members: []SourceRelationMember{
			{Path: "web/app/views/detail.js", FileID: "request-file", VersionID: "request-v1", Facts: []types.ParsedSourceFact{request}},
			{Path: "web/app/api.js", FileID: "prefix-file", VersionID: "prefix-v1", Facts: []types.ParsedSourceFact{prefix}},
			{Path: "web/app/vue.config.js", FileID: "proxy-file", VersionID: "proxy-v1", Facts: []types.ParsedSourceFact{proxy}},
			{Path: "server/Controller.java", FileID: "controller-file", VersionID: "controller-v1", Facts: []types.ParsedSourceFact{classMapping, handler}},
		}}
	relations := CorrelateSourceFacts(snapshot.TenantID, snapshot.DataSourceID, snapshot.SnapshotID, snapshot.Members)
	for _, relation := range relations {
		if relation.Kind == "http_route" {
			return snapshot, relation
		}
	}
	t.Fatal("fixture did not produce an HTTP route")
	return SourceRelationFactSnapshot{}, types.SourceCodeRelation{}
}
