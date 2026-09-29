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

type sourceReference struct {
	owner                factOwner
	relationKind         string
	targetNS             string
	targetName           string
	targets              []factOwner
	sourceNamespaceCount int
	namespaceCount       int
	ownerCount           int
	fromKey              string
	label                string
	cycleSource          string
	cycleTarget          string
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
	references := []factOwner{}
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
			case "mybatis_include", "mybatis_result_map_reference":
				references = append(references, factOwner{member, fact})
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
		}
	}

	resolvedReferences := make([]sourceReference, 0, len(references))
	cycleGraph := map[string][]string{}
	for _, ref := range references {
		fact := ref.fact
		targetNS, targetName := fact.TargetNamespace, fact.TargetName
		if targetNS == "" {
			targetNS, targetName = referenceTarget(fact.Namespace, fact.Name)
		}
		resolution := sourceReference{
			owner: ref, targetNS: targetNS, targetName: targetName,
			sourceNamespaceCount: len(documents[fact.Namespace]),
			namespaceCount:       len(documents[targetNS]), ownerCount: -1, fromKey: referenceFromKey(fact),
		}
		if fact.Kind == "mybatis_include" {
			resolution.relationKind, resolution.label = "include", "SQL include"
			resolution.targets = sqlFragmentsByKey[relationLookupKey(targetNS, targetName)]
			ownerName := fact.OwnerName
			if ownerName == "" {
				ownerName = fact.StatementID
			}
			switch fact.OwnerKind {
			case "mybatis_statement":
				resolution.ownerCount = len(statementsByKey[relationLookupKey(fact.Namespace, ownerName)])
			case "mybatis_sql_fragment":
				resolution.ownerCount = len(sqlFragmentsByKey[relationLookupKey(fact.Namespace, ownerName)])
			}
			if fact.OwnerKind == "mybatis_sql_fragment" && fact.OwnerName != "" {
				sourceKey := relationLookupKey(fact.Namespace, fact.OwnerName)
				if len(documents[fact.Namespace]) == 1 && len(sqlFragmentsByKey[sourceKey]) == 1 &&
					len(resolution.targets) == 1 && !fact.Dynamic {
					resolution.cycleSource = referenceCycleNode("include", fact.Namespace, fact.OwnerName)
					resolution.cycleTarget = referenceCycleNode("include", targetNS, targetName)
					cycleGraph[resolution.cycleSource] = append(cycleGraph[resolution.cycleSource], resolution.cycleTarget)
				}
			}
		} else {
			resolution.relationKind, resolution.label = "result_map", "resultMap"
			resolution.targets = resultMapsByKey[relationLookupKey(targetNS, targetName)]
			if fact.OwnerKind == "mybatis_result_map" {
				resolution.ownerCount = len(resultMapsByKey[relationLookupKey(fact.Namespace, fact.OwnerName)])
			}
			if fact.OwnerKind == "mybatis_result_map" && fact.OwnerName != "" {
				sourceKey := relationLookupKey(fact.Namespace, fact.OwnerName)
				if len(documents[fact.Namespace]) == 1 && len(resultMapsByKey[sourceKey]) == 1 &&
					len(resolution.targets) == 1 && !fact.Dynamic {
					resolution.cycleSource = referenceCycleNode("result_map", fact.Namespace, fact.OwnerName)
					resolution.cycleTarget = referenceCycleNode("result_map", targetNS, targetName)
					cycleGraph[resolution.cycleSource] = append(cycleGraph[resolution.cycleSource], resolution.cycleTarget)
				}
			}
		}
		resolvedReferences = append(resolvedReferences, resolution)
	}
	for _, ref := range resolvedReferences {
		fact := ref.owner.fact
		if fact.Dynamic {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
				ref.owner.member, fact, ref.fromKey, nil, "", "uncertain", "MyBatis reference contains a dynamic identifier"))
			continue
		}
		ownerName := fact.OwnerName
		if ownerName == "" {
			ownerName = fact.StatementID
		}
		if fact.Namespace == "" || ref.targetNS == "" || ref.targetName == "" || (ownerName == "" && fact.OwnerKind != "") {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
				ref.owner.member, fact, ref.fromKey, nil, "", "uncertain", "MyBatis reference has a missing namespace or ID"))
			continue
		}
		if ref.sourceNamespaceCount != 1 {
			reason := "MyBatis source namespace is missing"
			if ref.sourceNamespaceCount > 1 {
				reason = "MyBatis source namespace is ambiguous"
			}
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
				ref.owner.member, fact, ref.fromKey, nil, "", "uncertain", reason))
			continue
		}
		if ref.ownerCount == 0 {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
				ref.owner.member, fact, ref.fromKey, nil, "", "uncertain", "MyBatis reference owner is missing"))
			continue
		}
		if ref.ownerCount > 1 {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
				ref.owner.member, fact, ref.fromKey, nil, "", "uncertain", "MyBatis reference owner is duplicated"))
			continue
		}
		relation := resolveFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
			ref.owner, ref.fromKey, ref.targets, ref.namespaceCount, ref.label)
		if relation.Determinacy == "certain" && ref.cycleSource != "" && hasReferencePath(cycleGraph, ref.cycleTarget, ref.cycleSource) {
			relation = sourceFactRelation(tenant, sourceID, snapshotID, ref.relationKind,
				ref.owner.member, fact, ref.fromKey, nil, "", "uncertain", "cyclic MyBatis references are not resolved")
		}
		relations = append(relations, relation)
	}

	sort.SliceStable(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		return fmt.Sprintf("%s|%s|%09d|%s|%s", a.FromPath, a.Kind, rangeStart(a.FromRange), a.ToPath, a.ToKey) <
			fmt.Sprintf("%s|%s|%09d|%s|%s", b.FromPath, b.Kind, rangeStart(b.FromRange), b.ToPath, b.ToKey)
	})
	return relations
}

func referenceFromKey(fact types.ParsedSourceFact) string {
	owner := fact.OwnerName
	if owner == "" {
		owner = fact.StatementID
	}
	if owner == "" {
		return fact.Namespace + "." + fact.Name
	}
	if fact.ReferenceKind == "" {
		return fact.Namespace + "." + owner + " -> " + fact.Name
	}
	return fact.Namespace + "." + owner + " -> " + fact.ReferenceKind + " " + fact.Name
}

func referenceCycleNode(kind, namespace, name string) string {
	return kind + "\x00" + relationLookupKey(namespace, name)
}

func hasReferencePath(graph map[string][]string, from, target string) bool {
	if from == "" || target == "" {
		return false
	}
	stack := []string{from}
	visited := map[string]bool{}
	for len(stack) > 0 {
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		if node == target {
			return true
		}
		if visited[node] {
			continue
		}
		visited[node] = true
		stack = append(stack, graph[node]...)
	}
	return false
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

func rangeStart(raw types.JSON) int {
	var span types.SourceRange
	_ = json.Unmarshal(raw, &span)
	return span.StartByte
}
