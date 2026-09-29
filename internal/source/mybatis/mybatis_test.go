package mybatis

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const mapperFixture = `<?xml version="1.0" encoding="UTF-8"?>
<mapper namespace="com.example.PushScheduleMapper">
  <resultMap id="ScheduleMap" type="Schedule"><id column="id" property="id"/></resultMap>
  <sql id="columns">id, student_name</sql>
  <select id="getPushSchedule" resultMap="ScheduleMap">
    SELECT <include refid="columns"/> FROM push_schedule ps
    <where><if test="id != null">ps.id = #{id}</if></where>
  </select>
  <select id="stableList">SELECT id FROM push_schedule_detail</select>
</mapper>
`

func TestParseXMLExtractsStructureTablesAndExactRanges(t *testing.T) {
	doc, err := ParseXML("src/mapper/PushScheduleMapper.xml", []byte(mapperFixture))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Namespace != "com.example.PushScheduleMapper" {
		t.Fatalf("namespace = %q", doc.Namespace)
	}
	if len(doc.Statements) != 2 || len(doc.ResultMaps) != 1 || len(doc.Includes) != 1 {
		t.Fatalf("unexpected structures: %#v", doc)
	}
	statement := doc.Statements[0]
	if !statement.Dynamic || statement.Quality != "structural" {
		t.Fatalf("dynamic statement quality lost: %#v", statement)
	}
	if statement.Range.EndByte <= statement.Range.StartByte || statement.SQLRange.EndByte <= statement.SQLRange.StartByte {
		t.Fatalf("invalid ranges: %#v", statement)
	}
	if got := string([]byte(mapperFixture)[statement.Range.StartByte:statement.Range.EndByte]); !strings.HasPrefix(got, "<select id=\"getPushSchedule\"") || !strings.HasSuffix(strings.TrimSpace(got), "</select>") {
		t.Fatalf("range is not the whole statement: %q", got)
	}
	if len(statement.Tables) != 1 || statement.Tables[0].Name != "push_schedule" || statement.Tables[0].Certain {
		t.Fatalf("dynamic table relation should be uncertain: %#v", statement.Tables)
	}
	if statement.Tables[0].Range.StartLine < statement.Range.StartLine || statement.Tables[0].Range.EndLine > statement.Range.EndLine {
		t.Fatalf("table range escaped statement: %#v", statement.Tables[0].Range)
	}
	if len(statement.ResultMapRefs) != 1 || len(statement.IncludeRefs) != 1 {
		t.Fatalf("references not extracted: %#v", statement)
	}
}

func TestParseXMLRejectsExternalEntityAndPreservesLargeStatements(t *testing.T) {
	if _, err := ParseXML("mapper.xml", []byte(`<!DOCTYPE mapper SYSTEM "http://example.invalid/x"><mapper/>`)); err == nil {
		t.Fatal("external entity was accepted")
	}
	large := strings.Repeat("x", 256*1024)
	raw := []byte(`<mapper namespace="com.example.Large"><select id="large">SELECT ` + large + ` FROM large_table</select></mapper>`)
	doc, err := ParseXML("large.xml", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Statements) != 1 || !strings.HasSuffix(string(raw[doc.Statements[0].Range.StartByte:doc.Statements[0].Range.EndByte]), "</select>") {
		t.Fatalf("large statement was truncated: %#v", doc.Statements)
	}
}

func TestParseJavaMapperAndCorrelateUncertainty(t *testing.T) {
	javaRaw := []byte(`package com.example;
import org.apache.ibatis.annotations.Select;
public interface PushScheduleMapper {
  @Select("SELECT id FROM push_schedule WHERE id = #{id}")
  Schedule getPushSchedule(Long id);
  Schedule missing(Long id);
}`)
	java, err := ParseJavaMapper("src/dao/PushScheduleMapper.java", javaRaw)
	if err != nil {
		t.Fatal(err)
	}
	if java.QualifiedName != "com.example.PushScheduleMapper" || len(java.Methods) != 2 {
		t.Fatalf("mapper parse failed: %#v", java)
	}
	if java.Methods[0].SQL == nil || !java.Methods[0].SQL.Certain || len(java.Methods[0].SQL.Tables) != 1 {
		t.Fatalf("literal SQL was not extracted: %#v", java.Methods[0])
	}
	doc, err := ParseXML("src/mapper/PushScheduleMapper.xml", []byte(strings.Replace(mapperFixture, "com.example.PushScheduleMapper", "com.example.PushScheduleMapper", 1)))
	if err != nil {
		t.Fatal(err)
	}
	relations, warnings := Correlate(CorrelationInput{SnapshotID: "snapshot-1", CommitSHA: "abc123", XML: []XMLDocument{doc}, Java: []JavaMapper{java}})
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	var certain, uncertain bool
	for _, relation := range relations {
		if relation.Kind == RelationMapperStatement && relation.FromKey == "com.example.PushScheduleMapper#getPushSchedule" {
			certain = relation.Determinacy == "certain" && relation.SnapshotID == "snapshot-1" && relation.CommitSHA == "abc123"
		}
		if relation.Kind == RelationMapperStatement && relation.FromKey == "com.example.PushScheduleMapper#missing" {
			uncertain = relation.Determinacy == "uncertain" && relation.Quality == "uncertain"
		}
	}
	if !certain || !uncertain {
		t.Fatalf("mapper relation certainty was not preserved: %#v", relations)
	}
}

func TestJavaConcatenatedSQLIsNotCertain(t *testing.T) {
	raw := []byte(`package com.example; interface DynamicMapper {
  String table = "push_" + suffix;
  String query() { return "SELECT id FROM push_schedule WHERE id = " + value; }
}`)
	mapper, err := ParseJavaMapper("DynamicMapper.java", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapper.EmbeddedSQL) != 1 || mapper.EmbeddedSQL[0].Certain || !mapper.EmbeddedSQL[0].Dynamic {
		t.Fatalf("concatenated Java SQL was treated as certain: %#v", mapper.EmbeddedSQL)
	}
}

func TestDuplicateNamespaceAndStatementStayUncertain(t *testing.T) {
	one, err := ParseXML("one.xml", []byte(`<mapper namespace="dup"><select id="same">SELECT 1 FROM one</select></mapper>`))
	if err != nil {
		t.Fatal(err)
	}
	two, err := ParseXML("two.xml", []byte(`<mapper namespace="dup"><select id="same">SELECT 1 FROM two</select></mapper>`))
	if err != nil {
		t.Fatal(err)
	}
	java, err := ParseJavaMapper("M.java", []byte(`package dup; interface M { void same(); }`))
	if err != nil {
		t.Fatal(err)
	}
	relations, warnings := Correlate(CorrelationInput{XML: []XMLDocument{one, two}, Java: []JavaMapper{java}})
	if len(warnings) == 0 || len(relations) != 1 || relations[0].Determinacy != "uncertain" {
		t.Fatalf("ambiguity was guessed: relations=%#v warnings=%v", relations, warnings)
	}
}

func TestDuplicateResultMapAndIncludeIDsCarryQualityWarning(t *testing.T) {
	raw := []byte(`<mapper namespace="dup"><resultMap id="r"/><resultMap id="r"/><sql id="s"/><sql id="s"/><select id="q">SELECT 1 FROM t</select></mapper>`)
	doc, err := ParseXML("dup.xml", raw)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Quality != "uncertain" || !strings.Contains(strings.Join(doc.Warnings, " "), "duplicate resultMap") || !strings.Contains(strings.Join(doc.Warnings, " "), "duplicate sql include") {
		t.Fatalf("duplicate auxiliary IDs lost quality warnings: %#v", doc)
	}
}

func TestStoredRelationRequiresVersionedSourceIdentity(t *testing.T) {
	relation := Relation{Kind: RelationMapperStatement, FromPath: "M.java", FromKey: "M#q", ToPath: "M.xml", ToKey: "q"}
	if _, err := relation.ToStoredRelation("snap", "source", "", "version", "file", "version"); err == nil {
		t.Fatal("relation accepted an empty source file ID")
	}
	if _, err := relation.ToStoredRelation("snap", "source", "file", "version", "", ""); err == nil {
		t.Fatal("non-table relation accepted an empty target version")
	}
	table := Relation{Kind: RelationTableAccess, FromPath: "M.xml", FromKey: "q", ToKey: "table"}
	if _, err := table.ToStoredRelation("snap", "source", "file", "version", "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestRepresentativeMapperFilesWhenConfigured(t *testing.T) {
	root := os.Getenv("SOURCE_REPRESENTATIVE_ROOT")
	if root == "" {
		t.Skip("SOURCE_REPRESENTATIVE_ROOT is not set")
	}
	paths := []string{
		"nsb/src/main/java/com/nsb/zy/dao/mapper/BusinessPushScheduleMapper.xml",
		"nsb/src/main/java/com/nsb/zy/dao/mapper/BusinessPushScheduleDetailedMapper.xml",
		"nsb/src/main/java/com/nsb/zy/dao/mapper/BusinessFreeTutorMapper.xml",
	}
	javaPaths := []string{
		"nsb/src/main/java/com/nsb/zy/dao/BusinessPushScheduleMapper.java",
		"nsb/src/main/java/com/nsb/zy/dao/BusinessPushScheduleDetailedMapper.java",
		"nsb/src/main/java/com/nsb/zy/dao/BusinessFreeTutorMapper.java",
	}
	var docs []XMLDocument
	for _, relative := range paths {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		doc, err := ParseXML(relative, raw)
		if err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
		if len(doc.Statements) == 0 {
			t.Fatalf("%s: no statements", relative)
		}
		docs = append(docs, doc)
		for _, statement := range doc.Statements {
			if statement.Range.EndByte > len(raw) || statement.SQLRange.EndByte > len(raw) {
				t.Fatalf("%s: invalid statement range for %s", relative, statement.ID)
			}
		}
	}
	var mappers []JavaMapper
	for _, relative := range javaPaths {
		raw, err := os.ReadFile(filepath.Join(root, relative))
		if err != nil {
			t.Fatal(err)
		}
		mapper, err := ParseJavaMapper(relative, raw)
		if err != nil {
			t.Fatalf("%s: %v", relative, err)
		}
		if len(mapper.Methods) == 0 {
			t.Fatalf("%s: no mapper methods", relative)
		}
		mappers = append(mappers, mapper)
	}
	relations, warnings := Correlate(CorrelationInput{SnapshotID: "representative-snapshot", CommitSHA: "representative-commit", XML: docs, Java: mappers})
	if len(relations) == 0 {
		t.Fatal("representative mapper files produced no relations")
	}
	if len(warnings) > 0 {
		t.Fatalf("representative mapper ambiguity: %v", warnings)
	}
}
