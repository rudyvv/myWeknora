package source

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// SourceRelationMember contains only verified facts from one complete Git
// manifest and the file/version IDs staged for that same snapshot.
type SourceRelationMember struct {
	Path      string
	FileID    string
	VersionID string
	Facts     []types.ParsedSourceFact
}

type factOwner struct {
	member SourceRelationMember
	fact   types.ParsedSourceFact
}

type mapperDocument struct {
	member     SourceRelationMember
	namespace  string
	statements []types.ParsedSourceFact
	resultMaps []types.ParsedSourceFact
	sql        []types.ParsedSourceFact
}

// CorrelateSourceFacts performs snapshot-local identity correlation only. It
// intentionally contains no Java/XML/SQL parsing and never infers a target
// file from a basename or a generated diagnostic.
func CorrelateSourceFacts(tenant uint64, sourceID, snapshotID string, members []SourceRelationMember) []types.SourceCodeRelation {
	documents := map[string][]*mapperDocument{}
	statementsByKey := map[string][]factOwner{}
	resultMapsByKey := map[string][]factOwner{}
	sqlFragmentsByKey := map[string][]factOwner{}
	methods := []factOwner{}
	methodCounts := map[string]int{}
	fields := map[string][]factOwner{}
	calls := []factOwner{}
	var relations []types.SourceCodeRelation

	for _, member := range members {
		if member.FileID == "" || member.VersionID == "" {
			continue
		}
		var doc *mapperDocument
		for _, fact := range member.Facts {
			switch fact.Kind {
			case "mybatis_mapper":
				doc = &mapperDocument{member: member, namespace: fact.Namespace}
				documents[fact.Namespace] = append(documents[fact.Namespace], doc)
			case "java_mapper_method":
				methods = append(methods, factOwner{member, fact})
				methodCounts[fact.Namespace+"#"+fact.Name]++
			case "java_field":
				fields[member.FileID] = append(fields[member.FileID], factOwner{member, fact})
			case "java_mapper_call":
				calls = append(calls, factOwner{member, fact})
			}
		}
		for _, fact := range member.Facts {
			if doc == nil {
				if fact.Kind == "sql_table" {
					relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "table_access", member, fact, tableAccessFromKey(fact), nil, factKey(fact), factDeterminacy(fact), tableAccessReason(fact)))
				}
				continue
			}
			switch fact.Kind {
			case "mybatis_statement":
				doc.statements = append(doc.statements, fact)
				key := relationLookupKey(doc.namespace, fact.Name)
				statementsByKey[key] = append(statementsByKey[key], factOwner{member, fact})
			case "mybatis_result_map":
				doc.resultMaps = append(doc.resultMaps, fact)
				key := relationLookupKey(doc.namespace, fact.Name)
				resultMapsByKey[key] = append(resultMapsByKey[key], factOwner{member, fact})
			case "mybatis_sql_fragment":
				doc.sql = append(doc.sql, fact)
				key := relationLookupKey(doc.namespace, fact.Name)
				sqlFragmentsByKey[key] = append(sqlFragmentsByKey[key], factOwner{member, fact})
			case "sql_table":
				kind := "table_access"
				relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, kind, member, fact, tableAccessFromKey(fact), nil, factKey(fact), factDeterminacy(fact), tableAccessReason(fact)))
			}
		}
	}

	for _, owner := range methods {
		fact := owner.fact
		key := fact.Namespace + "#" + fact.Name
		if methodCounts[key] > 1 {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "mapper_statement", owner.member, fact, key, nil, "", "uncertain", "Java mapper method is overloaded"))
			continue
		}
		candidates := statementsByKey[relationLookupKey(fact.Namespace, fact.Name)]
		relations = append(relations, resolveFactRelation(tenant, sourceID, snapshotID, "mapper_statement", owner, key, candidates,
			len(documents[fact.Namespace]), "mapper statement"))
	}

	for _, docList := range documents {
		for _, doc := range docList {
			for _, statement := range doc.statements {
				for _, ref := range statement.ResultMapRefs {
					ns, name := referenceTarget(doc.namespace, ref)
					targets := resultMapsByKey[relationLookupKey(ns, name)]
					from := factOwner{doc.member, statement}
					relations = append(relations, resolveFactRelation(tenant, sourceID, snapshotID, "result_map", from,
						doc.namespace+"."+statement.Name, targets, len(documents[ns]), "resultMap"))
				}
			}
			for _, owner := range doc.member.Facts {
				if owner.Kind != "mybatis_include" {
					continue
				}
				ns, name := owner.TargetNamespace, owner.TargetName
				if ns == "" {
					ns, name = referenceTarget(doc.namespace, owner.Name)
				}
				targets := sqlFragmentsByKey[relationLookupKey(ns, name)]
				relations = append(relations, resolveFactRelation(tenant, sourceID, snapshotID, "include", factOwner{doc.member, owner},
					doc.namespace+"."+owner.Name, targets, len(documents[ns]), "SQL include"))
			}
		}
	}

	mapperTypes := map[string][]string{}
	for _, owner := range methods {
		parts := strings.Split(owner.fact.Namespace, ".")
		if len(parts) > 0 && parts[len(parts)-1] != "" {
			simple := parts[len(parts)-1]
			if nested := strings.LastIndex(simple, "$"); nested >= 0 {
				simple = simple[nested+1:]
			}
			mapperTypes[simple] = appendUnique(mapperTypes[simple], owner.fact.Namespace)
		}
	}
	for _, call := range calls {
		for _, field := range fields[call.member.FileID] {
			if !receiverMatchesField(call.fact.Receiver, field.fact.Name) {
				continue
			}
			typeName := simpleTypeName(field.fact.TypeName)
			namespaces := mapperTypes[typeName]
			if len(namespaces) == 0 {
				continue
			}
			candidates := []factOwner{}
			key := ""
			if len(namespaces) == 1 {
				key = namespaces[0] + "#" + call.fact.Name
				candidates = statementsByKey[relationLookupKey(namespaces[0], call.fact.Name)]
			} else {
				key = call.fact.Receiver + "#" + call.fact.Name
			}
			count := 0
			if len(namespaces) == 1 {
				count = len(documents[namespaces[0]])
			}
			relations = append(relations, resolveFactRelation(tenant, sourceID, snapshotID, "mapper_call", call, key, candidates,
				count, "mapper call"))
		}
	}

	sort.SliceStable(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		return fmt.Sprintf("%s|%s|%09d|%s|%s", a.FromPath, a.Kind, rangeStart(a.FromRange), a.ToPath, a.ToKey) <
			fmt.Sprintf("%s|%s|%09d|%s|%s", b.FromPath, b.Kind, rangeStart(b.FromRange), b.ToPath, b.ToKey)
	})
	return relations
}

func relationLookupKey(namespace, name string) string {
	return namespace + "\x00" + name
}

func referenceTarget(defaultNamespace, reference string) (string, string) {
	if split := strings.LastIndex(reference, "."); split >= 0 {
		return reference[:split], reference[split+1:]
	}
	return defaultNamespace, reference
}

func resolveFactRelation(tenant uint64, sourceID, snapshotID, kind string, from factOwner, key string,
	targets []factOwner, namespaceCount int, targetLabel string) types.SourceCodeRelation {
	if namespaceCount == 1 && len(targets) == 1 {
		return sourceFactRelation(tenant, sourceID, snapshotID, kind, from.member, from.fact, key, &targets[0], factKey(targets[0].fact), "certain", "")
	}
	reason := targetLabel + " target is missing"
	if namespaceCount > 1 {
		reason = targetLabel + " namespace is ambiguous"
	} else if namespaceCount == 1 && len(targets) > 1 {
		reason = targetLabel + " target is duplicated"
	}
	return sourceFactRelation(tenant, sourceID, snapshotID, kind, from.member, from.fact, key, nil, "", "uncertain", reason)
}

func sourceFactRelation(tenant uint64, sourceID, snapshotID, kind string, fromMember SourceRelationMember,
	from types.ParsedSourceFact, fromKey string, to *factOwner, toKey, determinacy, reason string) types.SourceCodeRelation {
	fromRange, _ := json.Marshal(from.Range)
	relation := types.SourceCodeRelation{ID: uuid.NewString(), TenantID: tenant, DataSourceID: sourceID,
		SnapshotID: snapshotID, Kind: kind, FromFileID: fromMember.FileID, FromVersionID: fromMember.VersionID,
		FromPath: fromMember.Path, FromKey: fromKey, FromRange: types.JSON(fromRange), ToKey: toKey,
		Determinacy: determinacy, Quality: from.Quality, ResolutionReason: reason, Context: types.JSON("[]")}
	if to != nil {
		toRange, _ := json.Marshal(to.fact.Range)
		relation.ToFileID, relation.ToVersionID, relation.ToPath = to.member.FileID, to.member.VersionID, to.member.Path
		relation.ToRange, relation.ToKey = types.JSON(toRange), toKey
	} else {
		relation.ToRange = types.JSON(`{"start_byte":0,"end_byte":0,"start_line":0,"end_line":0}`)
	}
	return relation
}

func factKey(fact types.ParsedSourceFact) string {
	if fact.Kind == "sql_table" {
		return fact.Name
	}
	if fact.Namespace != "" && fact.Name != "" {
		return fact.Namespace + "." + fact.Name
	}
	return fact.Name
}

func tableAccessFromKey(fact types.ParsedSourceFact) string {
	if fact.StatementID == "" {
		return fact.Namespace
	}
	if fact.Namespace == "" {
		return fact.StatementID
	}
	return fact.Namespace + "#" + fact.StatementID
}

func factDeterminacy(fact types.ParsedSourceFact) string {
	if fact.Dynamic || fact.Certainty == "uncertain" {
		return "uncertain"
	}
	return "certain"
}

func tableAccessReason(fact types.ParsedSourceFact) string {
	if factDeterminacy(fact) == "uncertain" {
		return "SQL table access is branch-dependent or contains a dynamic identifier"
	}
	return ""
}

func simpleTypeName(typeName string) string {
	if angle := strings.IndexByte(typeName, '<'); angle >= 0 {
		typeName = typeName[:angle]
	}
	typeName = strings.TrimSpace(strings.TrimSuffix(typeName, "[]"))
	parts := strings.Split(typeName, ".")
	return parts[len(parts)-1]
}

func receiverMatchesField(receiver, field string) bool {
	return receiver == field || strings.HasSuffix(receiver, "."+field)
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func rangeStart(raw types.JSON) int {
	var span types.SourceRange
	_ = json.Unmarshal(raw, &span)
	return span.StartByte
}
