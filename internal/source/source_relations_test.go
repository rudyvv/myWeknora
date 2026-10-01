package source

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func relationFact(kind, name, namespace string, start, end int) types.ParsedSourceFact {
	return types.ParsedSourceFact{Kind: kind, Name: name, Namespace: namespace, Quality: "structural",
		Range: types.SourceRange{StartByte: start, EndByte: end, StartLine: 1, EndLine: 1}}
}

func TestCorrelateBusinessFlowsAcrossRoutesInjectedServicesAndMapperStatements(t *testing.T) {
	type flowSpec struct {
		label, route, routeSuffix, controller, handler, serviceAPI, serviceImpl, mapper, method string
	}
	flows := []flowSpec{
		{"push", "/api/schedule/push/getPushSchedule", "/push/getPushSchedule", "demo.ScheduleController", "getPushSchedule", "demo.IScheduleService", "demo.ScheduleServiceImpl", "demo.PushScheduleMapper", "getPushSchedule"},
		{"questionnaire", "/api/questionnaire/detail", "/detail", "demo.QuestionnaireController", "getQuestionnaireDetail", "demo.IQuestionnaireService", "demo.QuestionnaireServiceImpl", "demo.QuestionnaireMapper", "getDetail"},
	}
	var members []SourceRelationMember
	for index, flow := range flows {
		base := index * 100
		vue := SourceRelationMember{Path: "web/" + flow.label + ".vue", FileID: flow.label + "-vue", VersionID: flow.label + "-vue-v1", Facts: []types.ParsedSourceFact{
			relationFact("api_request", flow.handler, "", base, base+8),
		}}
		vue.Facts[0].RoutePath, vue.Facts[0].HTTPMethod = flow.route, "GET"
		controller := SourceRelationMember{Path: "server/" + flow.controller + ".java", FileID: flow.label + "-controller", VersionID: flow.label + "-controller-v1", Facts: []types.ParsedSourceFact{
			relationFact("java_type", "Controller", flow.controller, base, base+4),
			relationFact("spring_mapping", "Controller", flow.controller, base+5, base+10),
			relationFact("spring_mapping", flow.handler, flow.controller, base+11, base+16),
			relationFact("java_injection", "service", flow.controller, base+17, base+20),
			relationFact("java_method", flow.handler, flow.controller, base+21, base+25),
			relationFact("java_method_call", flow.method, flow.controller, base+26, base+30),
		}}
		controller.Facts[1].RoutePath = strings.TrimSuffix(flow.route, flow.routeSuffix)
		controller.Facts[1].StatementType, controller.Facts[1].OwnerKind = "type", "type"
		controller.Facts[2].RoutePath = flow.routeSuffix
		controller.Facts[2].StatementType, controller.Facts[2].OwnerKind, controller.Facts[2].OwnerName = "method", "method", flow.handler
		controller.Facts[3].TypeName = flow.serviceAPI
		controller.Facts[5].MethodName, controller.Facts[5].Receiver, controller.Facts[5].TypeName = flow.handler, "service", flow.serviceAPI
		contract := SourceRelationMember{Path: "server/" + flow.serviceAPI + ".java", FileID: flow.label + "-contract", VersionID: flow.label + "-contract-v1", Facts: []types.ParsedSourceFact{
			relationFact("java_type", "Service", flow.serviceAPI, base, base+4),
			relationFact("java_method", flow.method, flow.serviceAPI, base+5, base+10),
		}}
		contract.Facts[0].OwnerKind = "interface"
		implementation := SourceRelationMember{Path: "server/" + flow.serviceImpl + ".java", FileID: flow.label + "-service", VersionID: flow.label + "-service-v1", Facts: []types.ParsedSourceFact{
			relationFact("java_type", "ServiceImpl", flow.serviceImpl, base, base+4),
			relationFact("java_method", flow.method, flow.serviceImpl, base+5, base+10),
			relationFact("java_injection", "mapper", flow.serviceImpl, base+11, base+14),
			relationFact("java_method_call", flow.method, flow.serviceImpl, base+15, base+20),
		}}
		implementation.Facts[0].SuperTypes = []string{flow.serviceAPI}
		implementation.Facts[0].OwnerKind = "class"
		implementation.Facts[2].TypeName = flow.mapper
		implementation.Facts[3].MethodName, implementation.Facts[3].Receiver, implementation.Facts[3].TypeName = flow.method, "mapper", flow.mapper
		mapper := SourceRelationMember{Path: "server/" + flow.mapper + ".java", FileID: flow.label + "-mapper", VersionID: flow.label + "-mapper-v1", Facts: []types.ParsedSourceFact{
			relationFact("java_type", "Mapper", flow.mapper, base, base+4),
			relationFact("java_method", flow.method, flow.mapper, base+5, base+10),
			relationFact("java_mapper_method", flow.method, flow.mapper, base+11, base+16),
		}}
		xml := SourceRelationMember{Path: "server/" + flow.mapper + ".xml", FileID: flow.label + "-xml", VersionID: flow.label + "-xml-v1", Facts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", flow.mapper, base, base+25),
			relationFact("mybatis_statement", flow.method, flow.mapper, base+5, base+20),
		}}
		vue.Facts[0].Text = "GET " + flow.route
		vue.Facts[0].Range = types.SourceRange{StartByte: base, EndByte: base + 8, StartLine: 1, EndLine: 1}
		for _, member := range []SourceRelationMember{vue, controller, contract, implementation, mapper, xml} {
			members = append(members, member)
		}
	}

	relations := CorrelateSourceFacts(1, "source", "snapshot", members)
	find := func(kind, fromPath string) *types.SourceCodeRelation {
		t.Helper()
		for index := range relations {
			if relations[index].Kind == kind && relations[index].FromPath == fromPath {
				return &relations[index]
			}
		}
		return nil
	}
	for _, flow := range flows {
		apiPath := "web/" + flow.label + ".vue"
		controllerPath := "server/" + flow.controller + ".java"
		servicePath := "server/" + flow.serviceImpl + ".java"
		mapperPath := "server/" + flow.mapper + ".java"
		if relation := find("http_route", apiPath); relation == nil || relation.Determinacy != "certain" || relation.ToPath != controllerPath {
			t.Fatalf("API request did not resolve to its same-flow handler: %#v", relation)
		}
		if relation := find("method_call", controllerPath); relation == nil || relation.Determinacy != "certain" || relation.ToPath != "server/"+flow.serviceAPI+".java" {
			t.Fatalf("controller did not resolve through its injected service contract: %#v", relation)
		}
		if relation := find("implements_method", "server/"+flow.serviceAPI+".java"); relation == nil || relation.Determinacy != "certain" || relation.ToPath != servicePath {
			t.Fatalf("service contract did not resolve to its unique implementation: %#v", relation)
		}
		if relation := find("method_call", servicePath); relation == nil || relation.Determinacy != "certain" || relation.ToPath != mapperPath {
			t.Fatalf("service did not resolve through its injected mapper: %#v", relation)
		}
		if relation := find("mapper_statement", mapperPath); relation == nil || relation.Determinacy != "certain" || relation.ToPath != "server/"+flow.mapper+".xml" || relation.SnapshotID != "snapshot" {
			t.Fatalf("mapper statement was not pinned to its flow's XML member: %#v", relation)
		}
	}
}

func TestCorrelateFrontendPrefixAndProxyOnlyProducesUncertainRouteCandidate(t *testing.T) {
	request := relationFact("api_request", "detail", "", 1, 10)
	request.RoutePath, request.HTTPMethod = "/questionnaire/detail.do", "GET"
	prefix := relationFact("api_prefix", "/apiroot", "", 1, 12)
	prefix.RoutePath, prefix.Certainty = "/apiroot", "uncertain"
	proxy := relationFact("api_proxy", "/api", "", 1, 70)
	proxy.RoutePath, proxy.TargetName, proxy.Namespace, proxy.Certainty = "/api", "^/apiroot", "", "certain"
	controllerType := relationFact("java_type", "Controller", "demo.QuestionnaireController", 1, 5)
	controllerType.OwnerKind = "class"
	classMapping := relationFact("spring_mapping", "Controller", "demo.QuestionnaireController", 6, 15)
	classMapping.RoutePath, classMapping.OwnerKind, classMapping.StatementType = "/api", "type", "type"
	handler := relationFact("spring_mapping", "detail", "demo.QuestionnaireController", 16, 30)
	handler.RoutePath, handler.OwnerKind, handler.OwnerName, handler.HTTPMethod = "/questionnaire/detail", "method", "detail", "GET"
	members := []SourceRelationMember{
		{Path: "web/detail.vue", FileID: "request-file", VersionID: "request-v1", Facts: []types.ParsedSourceFact{request}},
		{Path: "web/interceptor.js", FileID: "prefix-file", VersionID: "prefix-v1", Facts: []types.ParsedSourceFact{prefix}},
		{Path: "web/config.js", FileID: "proxy-file", VersionID: "proxy-v1", Facts: []types.ParsedSourceFact{proxy}},
		{Path: "server/QuestionnaireController.java", FileID: "controller-file", VersionID: "controller-v1", Facts: []types.ParsedSourceFact{controllerType, classMapping, handler}},
	}
	findRoute := func(values []SourceRelationMember) *types.SourceCodeRelation {
		t.Helper()
		for _, relation := range CorrelateSourceFacts(1, "source", "snapshot", values) {
			if relation.Kind == "http_route" {
				return &relation
			}
		}
		return nil
	}

	relation := findRoute(members)
	if relation == nil || relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ToVersionID != "" ||
		relation.ToPath != "" || relation.ToKey == "" || !strings.Contains(relation.ToKey, "/api/questionnaire/detail") ||
		relation.ResolutionReason != "frontend prefix or proxy transformation is conditional or unverified" {
		t.Fatalf("conditional frontend rewrite became a navigable route or lost its candidate: %#v", relation)
	}

	// If the prefix has no corresponding proxy rewrite, even a backend path
	// equal to the raw call literal must not be linked as a direct route.
	withoutProxy := append([]SourceRelationMember(nil), members[:2]...)
	withoutProxy = append(withoutProxy, members[3])
	withoutProxy[2].Facts = []types.ParsedSourceFact{controllerType, handler}
	if relation := findRoute(withoutProxy); relation != nil {
		t.Fatalf("request bypassed incomplete prefix/proxy evidence: %#v", relation)
	}

	mismatchedProxy := proxy
	mismatchedProxy.TargetName = "^/other"
	mismatched := append([]SourceRelationMember(nil), members...)
	mismatched[2].Facts = []types.ParsedSourceFact{mismatchedProxy}
	mismatched[3].Facts = []types.ParsedSourceFact{controllerType, handler}
	if relation := findRoute(mismatched); relation != nil {
		t.Fatalf("request bypassed mismatched prefix rewrite evidence: %#v", relation)
	}
}

func TestCorrelateStaticRouteWithUnresolvedSpringIdentityIsNotNavigable(t *testing.T) {
	request := relationFact("api_request", "detail", "", 1, 10)
	request.RoutePath, request.HTTPMethod = "/api/detail", "GET"
	controllerType := relationFact("java_type", "Controller", "demo.DetailController", 1, 5)
	controllerType.OwnerKind = "class"
	classMapping := relationFact("spring_mapping", "Controller", "demo.DetailController", 6, 15)
	classMapping.RoutePath, classMapping.OwnerKind, classMapping.StatementType = "/api", "type", "type"
	classMapping.Dynamic, classMapping.Certainty = true, "uncertain"
	handler := relationFact("spring_mapping", "detail", "demo.DetailController", 16, 30)
	handler.RoutePath, handler.OwnerKind, handler.OwnerName, handler.HTTPMethod = "/detail", "method", "detail", "GET"
	relations := CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{
		{Path: "web/detail.js", FileID: "request-file", VersionID: "request-v1", Facts: []types.ParsedSourceFact{request}},
		{Path: "server/DetailController.java", FileID: "controller-file", VersionID: "controller-v1", Facts: []types.ParsedSourceFact{controllerType, classMapping, handler}},
	})
	for _, relation := range relations {
		if relation.Kind != "http_route" {
			continue
		}
		if relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ToVersionID != "" ||
			relation.ToPath != "" || relation.ToKey == "" || relation.ResolutionReason == "" {
			t.Fatalf("unresolved Spring identity became a navigable route: %#v", relation)
		}
		return
	}
	t.Fatal("literal Spring route with unresolved mapping identity was discarded")
}

func TestCorrelateResolvesWildcardImportedTypesOnlyWhenSnapshotCandidateIsUnique(t *testing.T) {
	service := SourceRelationMember{Path: "service/Service.java", FileID: "service-file", VersionID: "service-v1", Facts: []types.ParsedSourceFact{
		relationFact("java_import", "demo.dao.*", "demo.service", 1, 15),
		relationFact("java_import", "demo.model.*", "demo.service", 16, 30),
		relationFact("java_injection", "mapper", "demo.service.Service", 31, 40),
		relationFact("java_method", "run", "demo.service.Service", 41, 50),
		relationFact("java_method_call", "find", "demo.service.Service", 51, 60),
	}}
	service.Facts[2].TargetName, service.Facts[2].Certainty, service.Facts[2].Dynamic = "Mapper", "uncertain", true
	service.Facts[4].TargetName, service.Facts[4].Certainty, service.Facts[4].Dynamic = "Mapper", "uncertain", true
	service.Facts[4].Receiver, service.Facts[4].MethodName = "mapper", "run"
	mapper := SourceRelationMember{Path: "dao/Mapper.java", FileID: "mapper-file", VersionID: "mapper-v1", Facts: []types.ParsedSourceFact{
		relationFact("java_type", "Mapper", "demo.dao.Mapper", 1, 8),
		relationFact("java_method", "find", "demo.dao.Mapper", 9, 18),
	}}
	other := SourceRelationMember{Path: "model/Other.java", FileID: "other-file", VersionID: "other-v1", Facts: []types.ParsedSourceFact{
		relationFact("java_type", "Other", "demo.model.Other", 1, 8),
	}}
	findRelations := func(members ...SourceRelationMember) map[string]types.SourceCodeRelation {
		t.Helper()
		result := map[string]types.SourceCodeRelation{}
		for _, relation := range CorrelateSourceFacts(1, "source", "snapshot", members) {
			result[relation.Kind] = relation
		}
		return result
	}

	unique := findRelations(service, mapper, other)
	if relation := unique["dependency_injection"]; relation.Determinacy != "certain" || relation.ToFileID != mapper.FileID || relation.ToVersionID != mapper.VersionID {
		t.Fatalf("unique imported snapshot type was not resolved: %#v", relation)
	}
	if relation := unique["method_call"]; relation.Determinacy != "certain" || relation.ToFileID != mapper.FileID || relation.ToVersionID != mapper.VersionID {
		t.Fatalf("call through a uniquely imported snapshot type was not resolved: %#v", relation)
	}

	ambiguousMapper := SourceRelationMember{Path: "model/Mapper.java", FileID: "model-mapper-file", VersionID: "model-mapper-v1", Facts: []types.ParsedSourceFact{
		relationFact("java_type", "Mapper", "demo.model.Mapper", 1, 8),
		relationFact("java_method", "find", "demo.model.Mapper", 9, 18),
	}}
	ambiguous := findRelations(service, mapper, ambiguousMapper)
	for _, kind := range []string{"dependency_injection", "method_call"} {
		if relation := ambiguous[kind]; relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ToVersionID != "" || relation.ResolutionReason == "" {
			t.Fatalf("ambiguous wildcard type %s became navigable: %#v", kind, relation)
		}
	}
}

func TestCorrelateSourceFactsBindsOnlyUniqueSameSnapshotMembers(t *testing.T) {
	java := SourceRelationMember{Path: "src/M.java", FileID: "java-file", VersionID: "java-version", Facts: []types.ParsedSourceFact{
		relationFact("java_mapper_method", "find", "demo.M", 10, 20),
		relationFact("java_mapper_method", "missing", "demo.M", 21, 32),
		relationFact("java_mapper_method", "overloaded", "demo.M", 33, 44),
		relationFact("java_mapper_method", "overloaded", "demo.M", 45, 57),
	}}
	xml := SourceRelationMember{Path: "src/M.xml", FileID: "xml-file", VersionID: "xml-version", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.M", 0, 100),
		relationFact("mybatis_statement", "find", "demo.M", 30, 50),
		relationFact("mybatis_result_map", "Result", "demo.M", 55, 65),
		relationFact("mybatis_include", "demo.Common.columns", "demo.M", 70, 80),
		relationFact("sql_table", "orders", "demo.M", 30, 50),
	}}
	common := SourceRelationMember{Path: "src/Common.xml", FileID: "common-file", VersionID: "common-version", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.Common", 0, 40),
		relationFact("mybatis_sql_fragment", "columns", "demo.Common", 10, 30),
	}}
	relations := CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{java, xml, common})
	var mapper, missing, overloaded, include *types.SourceCodeRelation
	for i := range relations {
		r := &relations[i]
		switch {
		case r.Kind == "mapper_statement" && r.FromKey == "demo.M#find":
			mapper = r
		case r.Kind == "mapper_statement" && r.FromKey == "demo.M#missing":
			missing = r
		case r.Kind == "mapper_statement" && r.FromKey == "demo.M#overloaded":
			overloaded = r
		case r.Kind == "include":
			include = r
		}
	}
	if mapper == nil || mapper.Determinacy != "certain" || mapper.ToFileID != "xml-file" || mapper.ToVersionID != "xml-version" || mapper.SnapshotID != "snapshot" {
		t.Fatalf("unique mapper target was not snapshot-bound: %#v", mapper)
	}
	if missing == nil || missing.Determinacy != "uncertain" || missing.ToFileID != "" || missing.ResolutionReason == "" {
		t.Fatalf("missing target became readable evidence: %#v", missing)
	}
	if overloaded == nil || overloaded.Determinacy != "uncertain" || overloaded.ResolutionReason != "Java mapper method is overloaded" {
		t.Fatalf("overload became a certain mapper edge: %#v", overloaded)
	}
	if include == nil || include.Determinacy != "certain" || include.ToFileID != "common-file" || include.ToVersionID != "common-version" {
		t.Fatalf("verified cross-namespace include was not bound: %#v", include)
	}
	var table *types.SourceCodeRelation
	for i := range relations {
		if relations[i].Kind == "table_access" {
			table = &relations[i]
		}
	}
	if table == nil || table.ToKey != "orders" || table.ToFileID != "" || table.Determinacy != "certain" {
		t.Fatalf("table access identity is wrong: %#v", table)
	}
}

func TestCorrelateSourceFactsLeavesDuplicateMapperNamespaceUnresolved(t *testing.T) {
	java := SourceRelationMember{Path: "M.java", FileID: "jf", VersionID: "jv", Facts: []types.ParsedSourceFact{
		relationFact("java_mapper_method", "find", "demo.M", 1, 8),
	}}
	first := SourceRelationMember{Path: "one.xml", FileID: "x1", VersionID: "v1", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.M", 0, 30), relationFact("mybatis_statement", "find", "demo.M", 10, 20),
	}}
	second := SourceRelationMember{Path: "two.xml", FileID: "x2", VersionID: "v2", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.M", 0, 30),
	}}
	for _, relation := range CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{java, first, second}) {
		if relation.Kind == "mapper_statement" {
			if relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ResolutionReason != "mapper statement namespace is ambiguous" {
				t.Fatalf("duplicate namespace selected one XML path: %#v", relation)
			}
			return
		}
	}
	t.Fatal("mapper relation not produced")
}

func TestCorrelateDoesNotInferMapperCallsFromNamesOrSimpleTypes(t *testing.T) {
	java := SourceRelationMember{Path: "Mapper.java", FileID: "jf", VersionID: "jv", Facts: []types.ParsedSourceFact{
		relationFact("java_mapper_method", "find", "demo.PushScheduleMapper", 1, 8),
		relationFact("java_mapper_method", "overloaded", "demo.PushScheduleMapper", 9, 18),
		relationFact("java_mapper_method", "overloaded", "demo.PushScheduleMapper", 19, 29),
		{Kind: "java_field", Name: "otherMapper", TypeName: "unrelated.PushScheduleMapper", Quality: "structural"},
		{Kind: "java_field", Name: "mapper", TypeName: "demo.PushScheduleMapper", Quality: "structural"},
		{Kind: "java_mapper_call", Name: "find", Receiver: "otherMapper", Quality: "structural"},
		{Kind: "java_mapper_call", Name: "find", Receiver: "service.mapper", Quality: "structural"},
		{Kind: "java_mapper_call", Name: "find", Receiver: "mapper", Quality: "structural"},
		{Kind: "java_mapper_call", Name: "find", Receiver: "mapper", Quality: "structural"},
		{Kind: "java_mapper_call", Name: "overloaded", Receiver: "mapper", Quality: "structural"},
	}}
	xml := SourceRelationMember{Path: "Mapper.xml", FileID: "xf", VersionID: "xv", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.PushScheduleMapper", 0, 40),
		relationFact("mybatis_statement", "find", "demo.PushScheduleMapper", 10, 20),
		relationFact("mybatis_statement", "overloaded", "demo.PushScheduleMapper", 21, 30),
	}}
	relations := CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{java, xml})
	var verifiedMethodEdge, overloadedMethodEdge *types.SourceCodeRelation
	for i := range relations {
		if relations[i].Kind == "mapper_call" {
			t.Fatalf("lexical receiver/name was promoted to a mapper-call edge: %#v", relations[i])
		}
		if relations[i].Kind == "mapper_statement" && relations[i].FromKey == "demo.PushScheduleMapper#find" {
			verifiedMethodEdge = &relations[i]
		} else if relations[i].Kind == "mapper_statement" && relations[i].FromKey == "demo.PushScheduleMapper#overloaded" {
			overloadedMethodEdge = &relations[i]
		}
	}
	if verifiedMethodEdge == nil || verifiedMethodEdge.FromKey != "demo.PushScheduleMapper#find" || verifiedMethodEdge.Determinacy != "certain" || verifiedMethodEdge.ToFileID != "xf" {
		t.Fatalf("verified interface method to XML statement edge was lost: %#v", verifiedMethodEdge)
	}
	if overloadedMethodEdge == nil || overloadedMethodEdge.Determinacy != "uncertain" || overloadedMethodEdge.ToFileID != "" {
		t.Fatalf("overloaded interface method became a readable source relation: %#v", overloadedMethodEdge)
	}
}

func TestCorrelateResolvesOwnedMyBatisReferencesAndRejectsAmbiguousDynamicOrCyclicEdges(t *testing.T) {
	ref := func(kind, ownerKind, ownerName, referenceKind, name, targetName string, start int) types.ParsedSourceFact {
		fact := relationFact(kind, name, "demo.M", start, start+5)
		fact.OwnerKind, fact.OwnerName, fact.ReferenceKind = ownerKind, ownerName, referenceKind
		fact.TargetNamespace, fact.TargetName = "demo.M", targetName
		return fact
	}
	fragments := []types.ParsedSourceFact{
		relationFact("mybatis_sql_fragment", "outer", "demo.M", 1, 8),
		relationFact("mybatis_sql_fragment", "inner", "demo.M", 9, 16),
		relationFact("mybatis_sql_fragment", "cycleA", "demo.M", 17, 24),
		relationFact("mybatis_sql_fragment", "cycleB", "demo.M", 25, 32),
		relationFact("mybatis_sql_fragment", "duplicate", "demo.M", 33, 40),
		relationFact("mybatis_sql_fragment", "duplicate", "demo.M", 41, 48),
	}
	resultMaps := []types.ParsedSourceFact{
		relationFact("mybatis_result_map", "Base", "demo.M", 49, 56),
		relationFact("mybatis_result_map", "Derived", "demo.M", 57, 64),
		relationFact("mybatis_result_map", "cycleA", "demo.M", 65, 72),
		relationFact("mybatis_result_map", "cycleB", "demo.M", 73, 80),
		relationFact("mybatis_result_map", "duplicateMap", "demo.M", 81, 88),
		relationFact("mybatis_result_map", "duplicateMap", "demo.M", 89, 96),
	}
	fragmentRefs := []types.ParsedSourceFact{
		ref("mybatis_include", "mybatis_sql_fragment", "outer", "include", "inner", "inner", 100),
		ref("mybatis_include", "mybatis_sql_fragment", "cycleA", "include", "cycleB", "cycleB", 110),
		ref("mybatis_include", "mybatis_sql_fragment", "cycleB", "include", "cycleA", "cycleA", 120),
		ref("mybatis_include", "mybatis_sql_fragment", "outer", "include", "${prefix}", "${prefix}", 130),
		ref("mybatis_include", "mybatis_sql_fragment", "outer", "include", "missing", "missing", 140),
		ref("mybatis_include", "mybatis_sql_fragment", "outer", "include", "duplicate", "duplicate", 150),
	}
	fragmentRefs[3].Dynamic = true
	resultMapRefs := []types.ParsedSourceFact{
		ref("mybatis_result_map_reference", "mybatis_result_map", "Derived", "extends", "Base", "Base", 160),
		ref("mybatis_result_map_reference", "mybatis_result_map", "Derived", "association", "Base", "Base", 170),
		ref("mybatis_result_map_reference", "mybatis_result_map", "cycleA", "extends", "cycleB", "cycleB", 180),
		ref("mybatis_result_map_reference", "mybatis_result_map", "cycleB", "collection", "cycleA", "cycleA", 190),
		ref("mybatis_result_map_reference", "mybatis_result_map", "Derived", "collection", "duplicateMap", "duplicateMap", 200),
		ref("mybatis_result_map_reference", "mybatis_result_map", "Derived", "extends", "missingMap", "missingMap", 210),
	}
	facts := []types.ParsedSourceFact{relationFact("mybatis_mapper", "", "demo.M", 0, 250)}
	facts = append(facts, fragments...)
	facts = append(facts, resultMaps...)
	facts = append(facts, fragmentRefs...)
	facts = append(facts, resultMapRefs...)
	member := SourceRelationMember{Path: "src/M.xml", FileID: "xml-file", VersionID: "xml-version", Facts: facts}

	relations := CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{member})
	byFromKey := make(map[string]types.SourceCodeRelation, len(relations))
	for _, relation := range relations {
		byFromKey[relation.FromKey] = relation
	}
	for _, key := range []string{"demo.M.outer -> include inner", "demo.M.Derived -> extends Base", "demo.M.Derived -> association Base"} {
		relation := byFromKey[key]
		if relation.Determinacy != "certain" || relation.ToFileID != "xml-file" || relation.ToVersionID != "xml-version" {
			t.Fatalf("unique reference %q was not pinned to its target: %#v", key, relation)
		}
	}
	for _, key := range []string{
		"demo.M.cycleA -> include cycleB", "demo.M.cycleB -> include cycleA",
		"demo.M.cycleA -> extends cycleB", "demo.M.cycleB -> collection cycleA",
	} {
		relation := byFromKey[key]
		if relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ResolutionReason != "cyclic MyBatis references are not resolved" {
			t.Fatalf("cyclic reference %q became certain: %#v", key, relation)
		}
	}
	for _, key := range []string{
		"demo.M.outer -> include ${prefix}", "demo.M.outer -> include missing",
		"demo.M.outer -> include duplicate", "demo.M.Derived -> collection duplicateMap",
		"demo.M.Derived -> extends missingMap",
	} {
		relation := byFromKey[key]
		if relation.Determinacy != "uncertain" || relation.ToFileID != "" {
			t.Fatalf("unresolved reference %q became certain: %#v", key, relation)
		}
	}
	if relation := byFromKey["demo.M.outer -> include inner"]; rangeStart(relation.FromRange) != 100 || rangeStart(relation.ToRange) != 9 {
		t.Fatalf("include owner/target ranges were not retained: %#v", relation)
	}
}

func TestStatementResultMapReferencesRequireUniqueSourceAndOwner(t *testing.T) {
	statement := func(reference string, start int) types.ParsedSourceFact {
		fact := relationFact("mybatis_statement", "find", "demo.M", start, start+10)
		fact.ResultMapRefs = []string{reference}
		return fact
	}
	resultMap := relationFact("mybatis_result_map", "Base", "demo.Common", 40, 60)
	common := SourceRelationMember{Path: "Common.xml", FileID: "common-file", VersionID: "common-version", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.Common", 0, 80), resultMap,
	}}
	findResultMap := func(members []SourceRelationMember) *types.SourceCodeRelation {
		for _, relation := range CorrelateSourceFacts(1, "source", "snapshot", members) {
			if relation.Kind == "result_map" {
				return &relation
			}
		}
		return nil
	}
	assertUnresolved := func(t *testing.T, name string, relation *types.SourceCodeRelation) {
		t.Helper()
		if relation == nil || relation.Determinacy != "uncertain" || relation.ToFileID != "" || relation.ToVersionID != "" || relation.ToPath != "" || relation.ToKey != "" || relation.ResolutionReason == "" {
			t.Fatalf("%s resultMap reference became readable evidence: %#v", name, relation)
		}
	}

	t.Run("duplicate statement owner", func(t *testing.T) {
		source := SourceRelationMember{Path: "M.xml", FileID: "source-file", VersionID: "source-version", Facts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.M", 0, 100), statement("demo.Common.Base", 10), statement("demo.Common.Base", 21),
		}}
		var found int
		for _, relation := range CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{source, common}) {
			if relation.Kind == "result_map" {
				found++
				assertUnresolved(t, "duplicate owner", &relation)
			}
		}
		if found != 2 {
			t.Fatalf("expected both duplicate owner references to remain visible, got %d", found)
		}
	})

	t.Run("duplicate source namespace", func(t *testing.T) {
		first := SourceRelationMember{Path: "one.xml", FileID: "source-one", VersionID: "version-one", Facts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.M", 0, 100), statement("demo.Common.Base", 10),
		}}
		second := SourceRelationMember{Path: "two.xml", FileID: "source-two", VersionID: "version-two", Facts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.M", 0, 100),
		}}
		assertUnresolved(t, "duplicate namespace", findResultMap([]SourceRelationMember{first, second, common}))
	})

	t.Run("unique cross-namespace target", func(t *testing.T) {
		source := SourceRelationMember{Path: "M.xml", FileID: "source-file", VersionID: "source-version", Facts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.M", 0, 100), statement("demo.Common.Base", 10),
		}}
		relation := findResultMap([]SourceRelationMember{source, common})
		if relation == nil || relation.Determinacy != "certain" || relation.ToFileID != "common-file" || relation.ToVersionID != "common-version" || relation.ToPath != "Common.xml" {
			t.Fatalf("unique cross-namespace resultMap was not bound: %#v", relation)
		}
	})

	for _, test := range []struct {
		name        string
		reference   string
		targetFacts []types.ParsedSourceFact
	}{
		{name: "missing target", reference: "demo.Common.Missing", targetFacts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.Common", 0, 80),
		}},
		{name: "dynamic target", reference: "demo.Common.${map}", targetFacts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.Common", 0, 80), resultMap,
		}},
		{name: "duplicate target", reference: "demo.Common.Base", targetFacts: []types.ParsedSourceFact{
			relationFact("mybatis_mapper", "", "demo.Common", 0, 80), resultMap,
			relationFact("mybatis_result_map", "Base", "demo.Common", 61, 79),
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := SourceRelationMember{Path: "M.xml", FileID: "source-file", VersionID: "source-version", Facts: []types.ParsedSourceFact{
				relationFact("mybatis_mapper", "", "demo.M", 0, 100), statement(test.reference, 10),
			}}
			commonMember := SourceRelationMember{Path: "Common.xml", FileID: "common-file", VersionID: "common-version", Facts: test.targetFacts}
			assertUnresolved(t, test.name, findResultMap([]SourceRelationMember{source, commonMember}))
		})
	}
}

func TestCorrelateDoesNotCertifyReferencesWithMissingOwnersOrDuplicateNamespaces(t *testing.T) {
	missingOwner := relationFact("mybatis_include", "inner", "demo.M", 20, 28)
	missingOwner.OwnerKind, missingOwner.TargetNamespace, missingOwner.TargetName = "mybatis_sql_fragment", "demo.M", "inner"
	first := SourceRelationMember{Path: "one.xml", FileID: "x1", VersionID: "v1", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.M", 0, 40),
		relationFact("mybatis_sql_fragment", "Derived", "demo.M", 5, 12),
		relationFact("mybatis_sql_fragment", "inner", "demo.M", 13, 19),
		missingOwner,
	}}
	second := SourceRelationMember{Path: "two.xml", FileID: "x2", VersionID: "v2", Facts: []types.ParsedSourceFact{
		relationFact("mybatis_mapper", "", "demo.M", 0, 30),
		relationFact("mybatis_result_map", "Base", "demo.M", 10, 20),
	}}
	owner := relationFact("mybatis_result_map_reference", "Base", "demo.M", 30, 35)
	owner.OwnerKind, owner.OwnerName, owner.ReferenceKind = "mybatis_result_map", "Derived", "extends"
	owner.TargetNamespace, owner.TargetName = "demo.M", "Base"
	first.Facts = append(first.Facts,
		relationFact("mybatis_result_map", "Derived", "demo.M", 21, 29), owner)

	var missing, ambiguous *types.SourceCodeRelation
	for _, relation := range CorrelateSourceFacts(1, "source", "snapshot", []SourceRelationMember{first, second}) {
		switch relation.FromKey {
		case "demo.M.inner":
			missing = &relation
		case "demo.M.Derived -> extends Base":
			ambiguous = &relation
		}
	}
	if missing == nil || missing.Determinacy != "uncertain" || missing.ToFileID != "" {
		t.Fatalf("reference without an owning fragment ID became certain: %#v", missing)
	}
	if ambiguous == nil || ambiguous.Determinacy != "uncertain" || ambiguous.ToFileID != "" || ambiguous.ResolutionReason != "MyBatis source namespace is ambiguous" {
		t.Fatalf("duplicate mapper namespace became certain: %#v", ambiguous)
	}
}
