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
