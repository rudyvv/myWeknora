package source

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestPlanSourceWikiImpactFindsChangedTransitiveDependencies(t *testing.T) {
	oldMembers := []types.SourceWikiImpactMember{
		impactMember("app/Handler.java", "handler", "handler-v1", "handler", impactFact("java_type", "OrdersController", "", "", 1)),
		impactMember("app/Service.java", "service", "service-v1", "service", impactFact("java_type", "OrdersService", "", "", 2)),
		impactMember("app/Store.java", "store", "store-v1", "store-v1", impactFact("java_type", "OrdersRepository", "", "", 3)),
	}
	newMembers := cloneImpactMembers(oldMembers)
	newMembers[2].FileVersionID = "store-v2"
	newMembers[2].ContentSHA = impactSHA("store-v2-content")
	oldRelations := []types.SourceCodeRelation{
		impactRelation("snapshot-old", "edge-handler-service", oldMembers[0], oldMembers[1], "calls", "certain", nil),
		impactRelation("snapshot-old", "edge-service-store", oldMembers[1], oldMembers[2], "calls", "certain", nil),
	}
	newRelations := []types.SourceCodeRelation{
		impactRelation("snapshot-new", "edge-handler-service-new", newMembers[0], newMembers[1], "calls", "certain", nil),
		impactRelation("snapshot-new", "edge-service-store-new", newMembers[1], newMembers[2], "calls", "certain", nil),
	}
	oldSnapshot := impactSnapshot("snapshot-old", types.SourceWikiImpactPublishedComplete, oldMembers, oldRelations)
	newSnapshot := impactSnapshot("snapshot-new", types.SourceWikiImpactPreparingComplete, newMembers, newRelations)
	oldInventory := impactInventory(oldSnapshot, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview-evidence", nil, false),
		impactTopic("module/app", "module", "app", "module-evidence", []string{"handler", "service", "store"}, true),
		impactTopic("flow/GET /orders", "flow", "", "flow-evidence", []string{"handler"}, true),
	}, []types.SourceWikiImpactModule{impactModule("app", "handler", "service", "store")})
	newInventory := impactInventory(newSnapshot, cloneImpactTopics(oldInventory.Topics), []types.SourceWikiImpactModule{impactModule("app", "handler", "service", "store")})

	plan, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Disposition != types.SourceWikiImpactPlanned {
		t.Fatalf("unexpected disposition: %+v", plan)
	}
	for _, key := range []string{"system", "module/app", "flow/GET /orders"} {
		if !impactHasAffected(plan, key) {
			t.Errorf("transitively affected topic %q missing: %+v", key, plan.Affected)
		}
	}
	if !strings.Contains(strings.Join(impactReasons(plan, "flow/GET /orders"), ","), "transitive_dependency_changed") {
		t.Fatalf("flow does not explain transitive dependency impact: %+v", plan.Affected)
	}
}

func TestPlanSourceWikiImpactIgnoresVersionOnlyRotationAndIsOrderIndependent(t *testing.T) {
	request := impactMember("web/request.js", "request", "request-v1", "request-body", impactFact("api_request", "orders", "", "/orders", 10))
	config := impactMember("web/vue.config.js", "config", "config-v1", "config-body", impactFact("api_proxy", "local", "", "/api", 20))
	controller := impactMember("server/OrdersController.java", "controller", "controller-v1", "controller-body",
		impactFact("spring_mapping", "OrdersController", "", "/orders", 30))
	oldMembers := []types.SourceWikiImpactMember{request, config, controller}
	newMembers := cloneImpactMembers(oldMembers)
	for i := range newMembers {
		newMembers[i].FileVersionID = "rotated-" + newMembers[i].SourceFileID
	}
	oldSnapshotID, newSnapshotID := "snapshot-v1", "snapshot-v2"
	oldRelation := impactRelation(oldSnapshotID, "relation-old", request, controller, "http_route", "certain", []types.SourceRelationFactRef{
		impactFactRef(oldSnapshotID, config, "api_proxy", "api_proxy", 20),
	})
	newRelation := impactRelation(newSnapshotID, "relation-new", newMembers[0], newMembers[2], "http_route", "certain", []types.SourceRelationFactRef{
		impactFactRef(newSnapshotID, newMembers[1], "api_proxy", "api_proxy", 20),
	})
	oldSnapshot := impactSnapshot(oldSnapshotID, types.SourceWikiImpactPublishedComplete, oldMembers, []types.SourceCodeRelation{oldRelation})
	newSnapshot := impactSnapshot(newSnapshotID, types.SourceWikiImpactPreparingComplete, newMembers, []types.SourceCodeRelation{newRelation})
	oldTopics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "old-overview-sha", nil, false),
		impactTopic("module/web", "module", "web", "old-web-sha", []string{"request", "config"}, true),
		impactTopic("flow/GET /orders", "flow", "", "old-flow-sha", []string{"request"}, true),
	}
	oldModules := []types.SourceWikiImpactModule{impactModule("web", "request", "config"), impactModule("server", "controller")}
	oldInventory := impactInventory(oldSnapshot, oldTopics, oldModules)
	newInventory := impactInventory(newSnapshot, cloneImpactTopics(oldTopics), cloneImpactModules(oldModules))

	plan, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Disposition != types.SourceWikiImpactPlanned || len(plan.Affected) != 0 || len(plan.Removed) != 0 {
		t.Fatalf("version-only rotation invalidated applicability: %+v", plan)
	}
	if len(plan.Unaffected) != len(oldTopics) {
		t.Fatalf("expected every stable topic to remain applicable: %+v", plan.Unaffected)
	}
	publishedCandidate := newSnapshot
	publishedCandidate.Stage = types.SourceWikiImpactPublishedComplete
	publishedPlan, err := PlanSourceWikiImpact(oldSnapshot, publishedCandidate, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, publishedPlan) {
		t.Fatalf("preparing and published complete candidates disagree: preparing=%+v published=%+v", plan, publishedPlan)
	}
	for _, unaffected := range plan.Unaffected {
		if len(unaffected.ApplicabilityFingerprint) != 64 || unaffected.EvidenceSHA == "" {
			t.Fatalf("unaffected topic did not preserve its old evidence SHA: %+v", unaffected)
		}
		if strings.HasPrefix(unaffected.TopicKey, "flow/") && unaffected.EvidenceSHA != "old-flow-sha" {
			t.Fatalf("flow evidence SHA changed: %+v", unaffected)
		}
	}

	// Row, fact, module, and topic input order must not affect the plan.
	reverseImpactMembers(newSnapshot.Members)
	reverseImpactRelations(newSnapshot.Relations)
	reverseImpactTopics(newInventory.Topics)
	reverseImpactModules(newInventory.ModuleInventory)
	for i := range newInventory.ModuleInventory {
		reverseStrings(newInventory.ModuleInventory[i].FileIDs)
	}
	permuted, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan, permuted) {
		t.Fatalf("input ordering changed impact result\nfirst:  %+v\nsecond: %+v", plan, permuted)
	}
}

func TestPlanSourceWikiImpactDoesNotConfirmRemovalWhenManifestEvidenceIsDowngraded(t *testing.T) {
	tests := []struct {
		name     string
		kind     string
		path     string
		fact     types.ParsedSourceFact
		topicKey string
		module   string
	}{
		{name: "module facts degraded", kind: "module", path: "app/AController.java",
			fact: impactFact("java_type", "AController", "", "", 1), topicKey: "module/app", module: "app"},
		{name: "flow facts degraded", kind: "flow", path: "app/Request.java",
			fact: impactFact("api_request", "orders", "GET", "/orders", 1), topicKey: "flow/GET /orders"},
		{name: "module member excluded", kind: "module", path: "app/AController.java",
			fact: impactFact("java_type", "AController", "", "", 1), topicKey: "module/app", module: "app"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldMembers := []types.SourceWikiImpactMember{impactMember(test.path, "old-file", "old-version", "old", test.fact)}
			oldTopics := []types.SourceWikiImpactTopicDependencies{
				impactTopic("system", "system", "", "overview", nil, false),
				impactTopic(test.topicKey, test.kind, test.module, "old-topic-evidence", []string{"old-file"}, true),
			}
			oldModules := []types.SourceWikiImpactModule(nil)
			if test.module != "" {
				oldModules = []types.SourceWikiImpactModule{impactModule(test.module, "old-file")}
			}
			oldSnapshot := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, oldMembers, nil)
			newMember := firstWithVersion(oldMembers[0], "new-version")
			newMember.ContentSHA = impactSHA("new")
			newMember.Facts = nil
			newMember.ExpectedFactCount = 0
			if strings.Contains(test.name, "excluded") {
				newMember.Status = "excluded"
				newMember.FileVersionID = ""
				newMember.ParserVersion = ""
				newMember.Quality = ""
			}
			if strings.Contains(test.name, "degraded") {
				newMember.Quality = "degraded"
			}
			newSnapshot := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{newMember}, nil)
			newInventory := impactInventory(newSnapshot, []types.SourceWikiImpactTopicDependencies{impactTopic("system", "system", "", "overview", nil, false)}, nil)
			oldInventory := impactInventory(oldSnapshot, oldTopics, oldModules)

			plan, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Removed) != 0 || !impactHasAffected(plan, test.topicKey) {
				t.Fatalf("remaining but downgraded evidence was treated as deletion: %+v", plan)
			}
		})
	}
}

func TestPlanSourceWikiImpactExpandsUncertainChangesToModule(t *testing.T) {
	clientFact := impactFact("api_request", "orders", "", "/orders", 1)
	oldClient := impactMember("app/Request.java", "request", "request-v1", "old-request", clientFact)
	newClient := oldClient
	newClient.FileVersionID = "request-v2"
	newClient.ContentSHA = impactSHA("new-request")
	controllerFact := impactFact("spring_mapping", "OrdersController", "", "/orders", 2)
	controller := impactMember("app/OrdersController.java", "controller", "controller-v1", "controller", controllerFact)
	oldSnapshot := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{oldClient, controller}, []types.SourceCodeRelation{
		impactRelation("old", "uncertain-old", oldClient, types.SourceWikiImpactMember{}, "http_route", "uncertain",
			[]types.SourceRelationFactRef{impactFactRef("old", controller, "spring_mapping", "spring_class_mapping", 2)}),
	})
	newSnapshot := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{newClient, controller}, []types.SourceCodeRelation{
		impactRelation("new", "uncertain-new", newClient, types.SourceWikiImpactMember{}, "http_route", "uncertain",
			[]types.SourceRelationFactRef{impactFactRef("new", controller, "spring_mapping", "spring_class_mapping", 2)}),
	})
	topics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"request", "controller"}, true),
		impactTopic("flow/GET /orders", "flow", "", "flow", []string{"request"}, true),
	}
	oldInventory := impactInventory(oldSnapshot, topics, []types.SourceWikiImpactModule{impactModule("app", "request", "controller")})
	newInventory := impactInventory(newSnapshot, cloneImpactTopics(topics), []types.SourceWikiImpactModule{impactModule("app", "request", "controller")})

	plan, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if !impactHasReason(plan, "module/app", "uncertain_relation_expanded_module") {
		t.Fatalf("uncertain edge did not expand to the bounded module: %+v", plan.Affected)
	}
}

func TestPlanSourceWikiImpactConfigChangesAffectModuleAndOverview(t *testing.T) {
	oldMember := impactMember("app/OrdersController.java", "controller", "v1", "old", impactFact("spring_mapping", "OrdersController", "", "/orders", 1))
	newMember := impactMember("app/OrdersController.java", "controller", "v2", "new", impactFact("spring_mapping", "OrdersController", "", "/purchases", 1))
	oldSnapshot := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{oldMember}, nil)
	newSnapshot := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{newMember}, nil)
	topics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview-sha", nil, false),
		impactTopic("module/app", "module", "app", "module-sha", []string{"controller"}, true),
	}
	oldInventory := impactInventory(oldSnapshot, topics, []types.SourceWikiImpactModule{impactModule("app", "controller")})
	newInventory := impactInventory(newSnapshot, cloneImpactTopics(topics), []types.SourceWikiImpactModule{impactModule("app", "controller")})

	plan, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"system", "module/app"} {
		if !impactHasReason(plan, key, "public_interface_or_config_changed") {
			t.Errorf("public/config impact missing for %q: %+v", key, plan.Affected)
		}
	}
	if !plan.RescanSkeleton {
		t.Fatal("interface change did not request skeleton rescan")
	}
}

func TestPlanSourceWikiImpactIncludesCrossFileCausalFactsInFlowFingerprint(t *testing.T) {
	request := impactMember("web/requests.js", "request", "request-v1", "request", impactFact("api_request", "orders", "GET", "/orders", 1))
	oldPrefix := impactMember("web/api.js", "prefix", "prefix-v1", "old-prefix", impactFact("api_prefix", "api", "", "/api", 10))
	newPrefix := firstWithVersion(oldPrefix, "prefix-v2")
	newPrefix.ContentSHA = impactSHA("new-prefix")
	newPrefix.Facts = []types.ParsedSourceFact{impactFact("api_prefix", "api", "", "/v2", 10)}
	controller := impactMember("server/OrdersController.java", "controller", "controller-v1", "controller", impactFact("spring_mapping", "OrdersController", "", "/orders", 20))
	oldSnapshotID, newSnapshotID := "old", "new"
	oldRelation := impactRelation(oldSnapshotID, "route-old", request, controller, "http_route", "certain",
		[]types.SourceRelationFactRef{impactFactRef(oldSnapshotID, oldPrefix, "api_prefix", "api_prefix", 10)})
	newRelation := impactRelation(newSnapshotID, "route-new", request, controller, "http_route", "certain",
		[]types.SourceRelationFactRef{impactFactRef(newSnapshotID, newPrefix, "api_prefix", "api_prefix", 10)})
	previous := impactSnapshot(oldSnapshotID, types.SourceWikiImpactPublishedComplete,
		[]types.SourceWikiImpactMember{request, oldPrefix, controller}, []types.SourceCodeRelation{oldRelation})
	next := impactSnapshot(newSnapshotID, types.SourceWikiImpactPreparingComplete,
		[]types.SourceWikiImpactMember{request, newPrefix, controller}, []types.SourceCodeRelation{newRelation})
	topics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview-sha", nil, false),
		impactTopic("module/web", "module", "web", "web-sha", []string{"request", "prefix"}, true),
		impactTopic("module/server", "module", "server", "server-sha", []string{"controller"}, true),
		impactTopic("flow/GET /orders", "flow", "", "flow-sha", []string{"request"}, true),
	}
	modules := []types.SourceWikiImpactModule{impactModule("web", "request", "prefix"), impactModule("server", "controller")}
	oldInventory := impactInventory(previous, topics, modules)
	newInventory := impactInventory(next, cloneImpactTopics(topics), cloneImpactModules(modules))

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if !impactHasAffected(plan, "flow/GET /orders") {
		t.Fatalf("causally participating cross-file config change did not affect its flow: %+v", plan)
	}
}

func TestPlanSourceWikiImpactRescansForAddedDeletedRenamedAndParserChanges(t *testing.T) {
	tests := []struct {
		name       string
		previous   []types.SourceWikiImpactMember
		next       []types.SourceWikiImpactMember
		oldTopics  []types.SourceWikiImpactTopicDependencies
		newTopics  []types.SourceWikiImpactTopicDependencies
		oldModules []types.SourceWikiImpactModule
		newModules []types.SourceWikiImpactModule
		wantReason string
		wantRemove bool
	}{
		{
			name:     "added member",
			previous: []types.SourceWikiImpactMember{impactMember("app/A.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))},
			next: []types.SourceWikiImpactMember{
				impactMember("app/A.java", "a", "a-v1-new", "a", impactFact("java_type", "AController", "", "", 1)),
				impactMember("app/B.java", "b", "b-v1", "b", impactFact("java_type", "BService", "", "", 2)),
			},
			oldTopics:  []types.SourceWikiImpactTopicDependencies{impactTopic("system", "system", "", "overview", nil, false), impactTopic("module/app", "module", "app", "module", []string{"a"}, true)},
			newTopics:  []types.SourceWikiImpactTopicDependencies{impactTopic("system", "system", "", "overview", nil, false), impactTopic("module/app", "module", "app", "module", []string{"a", "b"}, true)},
			oldModules: []types.SourceWikiImpactModule{impactModule("app", "a")},
			newModules: []types.SourceWikiImpactModule{impactModule("app", "a", "b")},
			wantReason: "file_added",
		},
		{
			name:     "deleted source-owned module contribution",
			previous: []types.SourceWikiImpactMember{impactMember("app/AController.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))},
			next:     nil,
			oldTopics: []types.SourceWikiImpactTopicDependencies{
				impactTopic("system", "system", "", "overview", nil, false),
				impactTopic("module/app", "module", "app", "module", []string{"a"}, true),
			},
			newTopics:  []types.SourceWikiImpactTopicDependencies{impactTopic("system", "system", "", "overview", nil, false)},
			oldModules: []types.SourceWikiImpactModule{impactModule("app", "a")},
			wantReason: "file_deleted",
			wantRemove: true,
		},
		{
			name:     "renamed member",
			previous: []types.SourceWikiImpactMember{impactMember("app/AController.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))},
			next:     []types.SourceWikiImpactMember{impactMember("app/RenamedController.java", "a", "a-v2", "a", impactFact("java_type", "AController", "", "", 1))},
			oldTopics: []types.SourceWikiImpactTopicDependencies{
				impactTopic("system", "system", "", "overview", nil, false), impactTopic("module/app", "module", "app", "module", []string{"a"}, true),
			},
			newTopics: []types.SourceWikiImpactTopicDependencies{
				impactTopic("system", "system", "", "overview", nil, false), impactTopic("module/app", "module", "app", "module", []string{"a"}, true),
			},
			oldModules: []types.SourceWikiImpactModule{impactModule("app", "a")},
			newModules: []types.SourceWikiImpactModule{impactModule("app", "a")},
			wantReason: "file_renamed",
		},
		{
			name:     "parser quality change",
			previous: []types.SourceWikiImpactMember{impactMember("app/AController.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))},
			next: []types.SourceWikiImpactMember{func() types.SourceWikiImpactMember {
				member := impactMember("app/AController.java", "a", "a-v2", "a", impactFact("java_type", "AController", "", "", 1))
				member.Quality = "degraded"
				return member
			}()},
			oldTopics: []types.SourceWikiImpactTopicDependencies{
				impactTopic("system", "system", "", "overview", nil, false), impactTopic("module/app", "module", "app", "module", []string{"a"}, true),
			},
			newTopics: []types.SourceWikiImpactTopicDependencies{
				impactTopic("system", "system", "", "overview", nil, false), impactTopic("module/app", "module", "app", "module", []string{"a"}, true),
			},
			oldModules: []types.SourceWikiImpactModule{impactModule("app", "a")},
			newModules: []types.SourceWikiImpactModule{impactModule("app", "a")},
			wantReason: "parser_or_quality_changed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			oldSnapshot := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, test.previous, nil)
			newSnapshot := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, test.next, nil)
			oldInventory := impactInventory(oldSnapshot, test.oldTopics, test.oldModules)
			newInventory := impactInventory(newSnapshot, test.newTopics, test.newModules)
			plan, err := PlanSourceWikiImpact(oldSnapshot, newSnapshot, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
			if err != nil {
				t.Fatal(err)
			}
			if !plan.RescanSkeleton {
				t.Fatalf("change did not request skeleton rescan: %+v", plan)
			}
			if !strings.Contains(strings.Join(plan.RescanReasons, ","), test.wantReason) {
				t.Fatalf("rescan reason %q missing: %+v", test.wantReason, plan.RescanReasons)
			}
			if test.wantRemove != (len(plan.Removed) == 1) {
				t.Fatalf("confirmed removal mismatch: %+v", plan.Removed)
			}
		})
	}
}

func TestPlanSourceWikiImpactDoesNotTreatInventoryOmissionAsRemoval(t *testing.T) {
	member := impactMember("app/AController.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{member}, nil)
	nextMember := member
	nextMember.FileVersionID = "a-v2"
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{nextMember}, nil)
	oldInventory := impactInventory(previous, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"a"}, true),
	}, []types.SourceWikiImpactModule{impactModule("app", "a")})
	newInventory := impactInventory(next, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
	}, []types.SourceWikiImpactModule{impactModule("app", "a")})

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removed) != 0 || !impactHasReason(plan, "module/app", "topic_missing_next_inventory_removal_unproven") {
		t.Fatalf("topic inventory omission was mistaken for source deletion: %+v", plan)
	}
}

func TestPlanSourceWikiImpactRejectsSamePathReplacementIdentity(t *testing.T) {
	oldMember := impactMember("app/A.java", "old-file-id", "old-version", "same-bytes", impactFact("java_type", "AController", "", "", 1))
	newMember := impactMember("app/A.java", "new-file-id", "new-version", "same-bytes", impactFact("java_type", "AController", "", "", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{oldMember}, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{newMember}, nil)
	topics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"old-file-id"}, true),
	}
	oldInventory := impactInventory(previous, topics, []types.SourceWikiImpactModule{impactModule("app", "old-file-id")})
	newInventory := impactInventory(next, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"new-file-id"}, true),
	}, []types.SourceWikiImpactModule{impactModule("app", "new-file-id")})

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if impactHasUnaffected(plan, "module/app") || !impactHasReason(plan, "module/app", "source_file_identity_replaced") {
		t.Fatalf("new source-file identity inherited old applicability: %+v", plan)
	}
}

func TestPlanSourceWikiImpactUsesOldAndNewReverseRelations(t *testing.T) {
	first := impactMember("app/A.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))
	second := impactMember("app/B.java", "b", "b-v1", "b", impactFact("java_type", "BService", "", "", 2))
	certain := impactRelation("old", "edge-old", first, second, "calls", "certain", nil)
	old := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{first, second}, []types.SourceCodeRelation{certain})
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{firstWithVersion(first, "a-v2"), firstWithVersion(second, "b-v2")}, nil)
	oldTopics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"a", "b"}, true),
		impactTopic("flow/GET /a", "flow", "", "flow", []string{"a"}, true),
	}
	newTopics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"a", "b"}, true),
		impactTopic("flow/GET /a", "flow", "", "flow", []string{"a"}, true),
	}
	oldInventory := impactInventory(old, oldTopics, []types.SourceWikiImpactModule{impactModule("app", "a", "b")})
	newInventory := impactInventory(next, newTopics, []types.SourceWikiImpactModule{impactModule("app", "a", "b")})

	plan, err := PlanSourceWikiImpact(old, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if impactHasUnaffected(plan, "flow/GET /a") || !impactHasAffected(plan, "flow/GET /a") {
		t.Fatalf("removed old dependency edge did not stale its dependent topic: %+v", plan)
	}
}

func TestPlanSourceWikiImpactFallsBackWithoutPartialUnaffectedOnIncompleteOrOverbound(t *testing.T) {
	member := impactMember("app/A.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{member}, nil)
	baseNext := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{firstWithVersion(member, "a-v2")}, nil)
	oldInventory := impactInventory(previous, []types.SourceWikiImpactTopicDependencies{impactTopic("system", "system", "", "overview", nil, false)}, nil)
	newInventory := impactInventory(baseNext, cloneImpactTopics(oldInventory.Topics), nil)

	tests := []struct {
		name   string
		mutate func(*types.SourceWikiImpactSnapshot, *types.SourceWikiImpactTopicInventory)
	}{
		{name: "incomplete facts", mutate: func(_ *types.SourceWikiImpactSnapshot, _ *types.SourceWikiImpactTopicInventory) {}},
		{name: "incomplete next manifest", mutate: func(snapshot *types.SourceWikiImpactSnapshot, _ *types.SourceWikiImpactTopicInventory) {
			snapshot.ManifestComplete = false
		}},
		{name: "overbound member manifest", mutate: func(snapshot *types.SourceWikiImpactSnapshot, _ *types.SourceWikiImpactTopicInventory) {
			snapshot.ExpectedMemberCount = types.SourceWikiSkeletonMaxFiles + 1
		}},
		{name: "incomplete topic inventory", mutate: func(_ *types.SourceWikiImpactSnapshot, inventory *types.SourceWikiImpactTopicInventory) {
			inventory.Complete = false
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			next := baseNext
			inventory := newInventory
			if test.name == "incomplete facts" {
				members := cloneImpactMembers(next.Members)
				members[0].FactsComplete = false
				next.Members = members
			}
			test.mutate(&next, &inventory)
			plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, inventory})
			if err != nil {
				t.Fatal(err)
			}
			if plan.Disposition != types.SourceWikiImpactSourceWideStale || len(plan.Unaffected) != 0 || len(plan.Affected) != 0 || len(plan.Removed) != 0 {
				t.Fatalf("incomplete/overbound input returned partial applicability: %+v", plan)
			}
		})
	}
}

func TestPlanSourceWikiImpactEnforcesAggregateReferenceBound(t *testing.T) {
	members := []types.SourceWikiImpactMember{
		impactMember("app/A.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1)),
		impactMember("app/B.java", "b", "b-v1", "b", impactFact("java_type", "BService", "", "", 2)),
	}
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, members, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{firstWithVersion(members[0], "a-v2"), firstWithVersion(members[1], "b-v2")}, nil)
	buildLargeInventory := func(snapshot types.SourceWikiImpactSnapshot, prefix string) types.SourceWikiImpactTopicInventory {
		topics := make([]types.SourceWikiImpactTopicDependencies, 100_000)
		for i := range topics {
			topics[i] = impactTopic(fmt.Sprintf("%s/%05d", prefix, i), "flow", "", "evidence", []string{"a", "b"}, true)
		}
		return impactInventory(snapshot, topics, nil)
	}
	oldInventory := buildLargeInventory(previous, "old")
	newInventory := buildLargeInventory(next, "new")

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Disposition != types.SourceWikiImpactSourceWideStale || plan.FallbackReason != "topic_dependency_reference_bound_exceeded" ||
		len(plan.Unaffected) != 0 || len(plan.Affected) != 0 || len(plan.Removed) != 0 {
		t.Fatalf("aggregate reference overflow did not fail closed atomically: disposition=%s fallback=%s affected=%d unaffected=%d removed=%d",
			plan.Disposition, plan.FallbackReason, len(plan.Affected), len(plan.Unaffected), len(plan.Removed))
	}
}

func TestPlanSourceWikiImpactGraphWorkLimitClearsPartialResults(t *testing.T) {
	const nodeCount = 1800
	const topicCount = 350
	oldMembers := make([]types.SourceWikiImpactMember, nodeCount)
	newMembers := make([]types.SourceWikiImpactMember, nodeCount)
	for i := range oldMembers {
		path := fmt.Sprintf("app/File%04d.java", i)
		oldMembers[i] = impactMember(path, fmt.Sprintf("file-%04d", i), fmt.Sprintf("v1-%04d", i), path)
		newMembers[i] = firstWithVersion(oldMembers[i], fmt.Sprintf("v2-%04d", i))
	}
	oldRelations := make([]types.SourceCodeRelation, 0, nodeCount-1)
	newRelations := make([]types.SourceCodeRelation, 0, nodeCount-1)
	for i := 0; i < nodeCount-1; i++ {
		oldRelations = append(oldRelations, impactRelation("old", fmt.Sprintf("old-edge-%d", i), oldMembers[i], oldMembers[i+1], "calls", "certain", nil))
		newRelations = append(newRelations, impactRelation("new", fmt.Sprintf("new-edge-%d", i), newMembers[i], newMembers[i+1], "calls", "certain", nil))
	}
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, oldMembers, oldRelations)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, newMembers, newRelations)
	topics := make([]types.SourceWikiImpactTopicDependencies, topicCount)
	for i := range topics {
		topics[i] = impactTopic(fmt.Sprintf("flow/GET /chain/%03d", i), "flow", "", "evidence", []string{"file-0000"}, true)
	}
	oldInventory := impactInventory(previous, topics, nil)
	newInventory := impactInventory(next, cloneImpactTopics(topics), nil)

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Disposition != types.SourceWikiImpactSourceWideStale || plan.FallbackReason != "graph_work_limit_exceeded" ||
		len(plan.Unaffected) != 0 || len(plan.Affected) != 0 || len(plan.Removed) != 0 {
		t.Fatalf("graph work overflow returned partial results: disposition=%s fallback=%s affected=%d unaffected=%d removed=%d",
			plan.Disposition, plan.FallbackReason, len(plan.Affected), len(plan.Unaffected), len(plan.Removed))
	}
}

func TestPlanSourceWikiImpactRejectsCrossSourceAndDuplicateIdentities(t *testing.T) {
	member := impactMember("app/AController.java", "a", "a-v1", "a", impactFact("java_type", "AController", "", "", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{member}, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{firstWithVersion(member, "a-v2")}, nil)
	oldInventory := impactInventory(previous, []types.SourceWikiImpactTopicDependencies{impactTopic("system", "system", "", "old", nil, false)}, nil)
	newInventory := impactInventory(next, cloneImpactTopics(oldInventory.Topics), nil)

	otherSource := next
	otherSource.SourceID = "source-two"
	_, err := PlanSourceWikiImpact(previous, otherSource, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err == nil {
		t.Fatal("cross-source same-path identities were accepted")
	}
	duplicate := cloneImpactMembers(previous.Members)
	duplicate = append(duplicate, duplicate[0])
	previousWithDuplicate := previous
	previousWithDuplicate.ExpectedMemberCount = len(duplicate)
	previousWithDuplicate.Members = duplicate
	_, err = PlanSourceWikiImpact(previousWithDuplicate, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err == nil {
		t.Fatal("duplicate member identity was accepted")
	}
}

func TestPlanSourceWikiImpactStableModuleInventoryDoesNotRescan(t *testing.T) {
	member := impactMember("app/OrdersController.java", "controller", "controller-v1", "controller",
		impactFact("java_type", "OrdersController", "", "", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{member}, nil)
	nextMember := firstWithVersion(member, "controller-v2")
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{nextMember}, nil)
	topics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"controller"}, true),
	}
	modules := []types.SourceWikiImpactModule{impactModule("app", "controller")}
	oldInventory := impactInventory(previous, topics, modules)
	newInventory := impactInventory(next, cloneImpactTopics(topics), cloneImpactModules(modules))

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RescanSkeleton || containsString(plan.RescanReasons, "module_inventory_changed") {
		t.Fatalf("stable module inventory spuriously rescanned: %+v", plan)
	}
}

func TestPlanSourceWikiImpactChangedModuleInventoryRescans(t *testing.T) {
	controller := impactMember("app/OrdersController.java", "controller", "controller-v1", "controller",
		impactFact("java_type", "OrdersController", "", "", 1))
	note := impactMember("app/README.md", "note", "note-v1", "note")
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{controller, note}, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{controller, note}, nil)
	oldTopics := []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"controller"}, true),
	}
	newTopics := cloneImpactTopics(oldTopics)
	newTopics[1] = impactTopic("module/app", "module", "app", "module", []string{"controller", "note"}, true)
	oldInventory := impactInventory(previous, oldTopics, []types.SourceWikiImpactModule{impactModule("app", "controller")})
	newInventory := impactInventory(next, newTopics, []types.SourceWikiImpactModule{impactModule("app", "controller", "note")})

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if !plan.RescanSkeleton || !containsString(plan.RescanReasons, "module_inventory_changed") {
		t.Fatalf("changed module inventory missed skeleton rescan: %+v", plan)
	}
}

func TestPlanSourceWikiImpactDoesNotRemoveModuleWhenStructuralMemberRemains(t *testing.T) {
	helper := impactMember("app/Helper.java", "helper", "helper-v1", "helper",
		impactFact("java_type", "Helper", "", "", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{helper}, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete,
		[]types.SourceWikiImpactMember{firstWithVersion(helper, "helper-v2")}, nil)
	oldInventory := impactInventory(previous, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("module/app", "module", "app", "module", []string{"helper"}, true),
	}, []types.SourceWikiImpactModule{impactModule("app", "helper")})
	newInventory := impactInventory(next, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
	}, nil)

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removed) != 0 || !impactHasReason(plan, "module/app", "topic_missing_next_inventory_removal_unproven") {
		t.Fatalf("retained structural member was treated as module deletion: %+v", plan)
	}
}

func TestPlanSourceWikiImpactDoesNotRemoveFlowWhenEntryMemberRemains(t *testing.T) {
	request := impactMember("web/orders.js", "request", "request-v1", "request",
		impactFact("api_request", "orders", "GET", "/orders", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{request}, nil)
	nextRequest := firstWithVersion(request, "request-v2")
	nextRequest.Facts = nil
	nextRequest.ExpectedFactCount = 0
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, []types.SourceWikiImpactMember{nextRequest}, nil)
	oldInventory := impactInventory(previous, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("flow/GET /orders", "flow", "", "flow", []string{"request"}, true),
	}, nil)
	newInventory := impactInventory(next, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
	}, nil)

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removed) != 0 || !impactHasReason(plan, "flow/GET /orders", "topic_missing_next_inventory_removal_unproven") {
		t.Fatalf("retained flow entry member was treated as route deletion: %+v", plan)
	}
}

func TestPlanSourceWikiImpactRemovesFlowWhenEntryMemberDisappears(t *testing.T) {
	request := impactMember("web/orders.js", "request", "request-v1", "request",
		impactFact("api_request", "orders", "GET", "/orders", 1))
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{request}, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, nil, nil)
	oldInventory := impactInventory(previous, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
		impactTopic("flow/GET /orders", "flow", "", "flow", []string{"request"}, true),
	}, nil)
	newInventory := impactInventory(next, []types.SourceWikiImpactTopicDependencies{
		impactTopic("system", "system", "", "overview", nil, false),
	}, nil)

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Removed) != 1 || plan.Removed[0].TopicKey != "flow/GET /orders" {
		t.Fatalf("physically removed flow entry was not confirmed: %+v", plan)
	}
}

func TestPlanSourceWikiImpactBoundsRepeatedCanonicalFlowRemovalWork(t *testing.T) {
	facts := make([]types.ParsedSourceFact, 10_000)
	for i := range facts {
		facts[i] = impactFact("api_request", "orders", "GET", "/orders", i*10+1)
	}
	request := impactMember("web/request.js", "request", "request-v1", "request", facts...)
	previous := impactSnapshot("old", types.SourceWikiImpactPublishedComplete, []types.SourceWikiImpactMember{request}, nil)
	next := impactSnapshot("new", types.SourceWikiImpactPreparingComplete, nil, nil)
	topics := make([]types.SourceWikiImpactTopicDependencies, 1_000)
	for i := range topics {
		topics[i] = impactTopic("flow/GET"+strings.Repeat(" ", i+1)+"/orders", "flow", "", "flow", []string{"request"}, true)
	}
	oldInventory := impactInventory(previous, topics, nil)
	newInventory := impactInventory(next, nil, nil)

	plan, err := PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{oldInventory, newInventory})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Disposition != types.SourceWikiImpactSourceWideStale || plan.FallbackReason != "graph_work_limit_exceeded" ||
		len(plan.Removed) != 0 || len(plan.Affected) != 0 || len(plan.Unaffected) != 0 {
		t.Fatalf("over-budget route-removal work returned a partial plan: disposition=%s fallback=%q affected=%d unaffected=%d removed=%d",
			plan.Disposition, plan.FallbackReason, len(plan.Affected), len(plan.Unaffected), len(plan.Removed))
	}
}

func impactSnapshot(snapshotID string, stage types.SourceWikiImpactSnapshotStage, members []types.SourceWikiImpactMember,
	relations []types.SourceCodeRelation) types.SourceWikiImpactSnapshot {
	return types.SourceWikiImpactSnapshot{
		TenantID: 7, KnowledgeBaseID: "kb-one", SourceID: "source-one", SnapshotID: snapshotID, Stage: stage,
		ManifestComplete: true, ExpectedMemberCount: len(members), RelationsComplete: true,
		ExpectedRelationCount: len(relations), Members: cloneImpactMembers(members), Relations: append([]types.SourceCodeRelation(nil), relations...),
	}
}

func impactMember(filePath, fileID, versionID, content string, facts ...types.ParsedSourceFact) types.SourceWikiImpactMember {
	return types.SourceWikiImpactMember{
		Path: filePath, SourceFileID: fileID, FileVersionID: versionID, ContentSHA: impactSHA(content), Status: "parsed",
		ParserVersion: "parser-v1", Quality: "structural", FactsComplete: true, ExpectedFactCount: len(facts), Facts: append([]types.ParsedSourceFact(nil), facts...),
	}
}

func impactFact(kind, name, method, route string, start int) types.ParsedSourceFact {
	return types.ParsedSourceFact{Kind: kind, Name: name, HTTPMethod: method, RoutePath: route, Quality: "structural",
		Range: types.SourceRange{StartByte: start, EndByte: start + 5, StartLine: start + 1, EndLine: start + 1}, Text: name + route}
}

func impactRelation(snapshotID, relationID string, from, to types.SourceWikiImpactMember, kind, determinacy string,
	causal []types.SourceRelationFactRef) types.SourceCodeRelation {
	fromRange, _ := json.Marshal(types.SourceRange{StartByte: 1, EndByte: 4, StartLine: 1, EndLine: 1})
	toRange, _ := json.Marshal(types.SourceRange{StartByte: 5, EndByte: 8, StartLine: 2, EndLine: 2})
	if to.SourceFileID == "" {
		toRange = []byte(`{"start_byte":0,"end_byte":0,"start_line":0,"end_line":0}`)
	}
	context, _ := json.Marshal(causal)
	relation := types.SourceCodeRelation{
		ID: relationID, TenantID: 7, DataSourceID: "source-one", SnapshotID: snapshotID, Kind: kind,
		FromFileID: from.SourceFileID, FromVersionID: from.FileVersionID, FromPath: from.Path, FromKey: "GET /orders",
		FromRange: fromRange, Determinacy: determinacy, Quality: "structural", Context: context, CreatedAt: time.Unix(100, 0),
		ToRange: toRange,
	}
	if to.SourceFileID != "" {
		relation.ToFileID, relation.ToVersionID, relation.ToPath, relation.ToKey = to.SourceFileID, to.FileVersionID, to.Path, "target"
	}
	return relation
}

func impactFactRef(snapshotID string, member types.SourceWikiImpactMember, kind, role string, start int) types.SourceRelationFactRef {
	return types.SourceRelationFactRef{
		DataSourceID: "source-one", SnapshotID: snapshotID, FileID: member.SourceFileID,
		FileVersionID: member.FileVersionID, Path: member.Path, Kind: kind, Role: role, Quality: "structural",
		Range: types.SourceRange{StartByte: start, EndByte: start + 5, StartLine: start + 1, EndLine: start + 1},
	}
}

func impactTopic(key, kind, modulePath, evidenceSHA string, dependencyIDs []string, sourceOwned bool) types.SourceWikiImpactTopicDependencies {
	return types.SourceWikiImpactTopicDependencies{
		TopicKey: key, Kind: kind, ModulePath: modulePath, SourceOwned: sourceOwned, EvidenceSHA: evidenceSHA,
		ExpectedDependencyFileCount: len(dependencyIDs), DependencyFileIDs: append([]string(nil), dependencyIDs...),
	}
}

func impactModule(modulePath string, fileIDs ...string) types.SourceWikiImpactModule {
	return types.SourceWikiImpactModule{Path: modulePath, ExpectedFileCount: len(fileIDs), FileIDs: append([]string(nil), fileIDs...)}
}

func impactInventory(snapshot types.SourceWikiImpactSnapshot, topics []types.SourceWikiImpactTopicDependencies,
	modules []types.SourceWikiImpactModule) types.SourceWikiImpactTopicInventory {
	return types.SourceWikiImpactTopicInventory{
		TenantID: snapshot.TenantID, KnowledgeBaseID: snapshot.KnowledgeBaseID, SourceID: snapshot.SourceID, SnapshotID: snapshot.SnapshotID,
		Complete: true, ExpectedTopicCount: len(topics), ExpectedModuleCount: len(modules),
		Topics: cloneImpactTopics(topics), ModuleInventory: cloneImpactModules(modules),
	}
}

func impactSHA(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func firstWithVersion(member types.SourceWikiImpactMember, versionID string) types.SourceWikiImpactMember {
	member.FileVersionID = versionID
	return member
}

func cloneImpactMembers(values []types.SourceWikiImpactMember) []types.SourceWikiImpactMember {
	result := append([]types.SourceWikiImpactMember(nil), values...)
	for i := range result {
		result[i].Facts = append([]types.ParsedSourceFact(nil), values[i].Facts...)
	}
	return result
}

func cloneImpactTopics(values []types.SourceWikiImpactTopicDependencies) []types.SourceWikiImpactTopicDependencies {
	result := append([]types.SourceWikiImpactTopicDependencies(nil), values...)
	for i := range result {
		result[i].DependencyFileIDs = append([]string(nil), values[i].DependencyFileIDs...)
	}
	return result
}

func cloneImpactModules(values []types.SourceWikiImpactModule) []types.SourceWikiImpactModule {
	result := append([]types.SourceWikiImpactModule(nil), values...)
	for i := range result {
		result[i].FileIDs = append([]string(nil), values[i].FileIDs...)
	}
	return result
}

func reverseImpactMembers(values []types.SourceWikiImpactMember) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseImpactRelations(values []types.SourceCodeRelation) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseImpactTopics(values []types.SourceWikiImpactTopicDependencies) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseImpactModules(values []types.SourceWikiImpactModule) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func reverseStrings(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func impactHasAffected(plan types.SourceWikiImpactPlan, topicKey string) bool {
	for _, topic := range plan.Affected {
		if topic.TopicKey == topicKey {
			return true
		}
	}
	return false
}

func impactHasUnaffected(plan types.SourceWikiImpactPlan, topicKey string) bool {
	for _, topic := range plan.Unaffected {
		if topic.TopicKey == topicKey {
			return true
		}
	}
	return false
}

func impactHasReason(plan types.SourceWikiImpactPlan, topicKey, reason string) bool {
	return strings.Contains(strings.Join(impactReasons(plan, topicKey), ","), reason)
}

func impactReasons(plan types.SourceWikiImpactPlan, topicKey string) []string {
	for _, topic := range plan.Affected {
		if topic.TopicKey == topicKey {
			return topic.Reasons
		}
	}
	return nil
}
