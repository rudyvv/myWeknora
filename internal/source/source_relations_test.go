package source

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func relationFact(kind, name, namespace string, start, end int) types.ParsedSourceFact {
	return types.ParsedSourceFact{Kind: kind, Name: name, Namespace: namespace, Quality: "structural",
		Range: types.SourceRange{StartByte: start, EndByte: end, StartLine: 1, EndLine: 1}}
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
