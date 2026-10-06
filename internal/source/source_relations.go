package source

import (
	"encoding/json"
	"fmt"
	"path"
	"slices"
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

type sourceRouteEndpoint struct {
	owner            factOwner
	path             string
	methods          []string
	methodRestricted bool
	methodUncertain  bool
	uncertain        bool
	supportingFacts  []types.SourceRelationFactRef
}

type sourceRequestRoute struct {
	path            string
	uncertain       bool
	reason          string
	supportingFacts []types.SourceRelationFactRef
}

type mapperDocument struct {
	member     SourceRelationMember
	namespace  string
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
				key := relationLookupKey(doc.namespace, fact.Name)
				statementsByKey[key] = append(statementsByKey[key], factOwner{member, fact})
				for _, reference := range fact.ResultMapRefs {
					targetNS, targetName := referenceTarget(doc.namespace, reference)
					referenceFact := fact
					referenceFact.Kind = "mybatis_result_map_reference"
					referenceFact.Name = reference
					referenceFact.TargetNamespace, referenceFact.TargetName = targetNS, targetName
					referenceFact.OwnerKind, referenceFact.OwnerName = "mybatis_statement", fact.Name
					referenceFact.ReferenceKind = "statement"
					referenceFact.Dynamic = strings.Contains(reference, "${")
					references = append(references, factOwner{member, referenceFact})
				}
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
			switch fact.OwnerKind {
			case "mybatis_statement":
				resolution.ownerCount = len(statementsByKey[relationLookupKey(fact.Namespace, fact.OwnerName)])
			case "mybatis_result_map":
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
	relations = append(relations, correlateStaticBusinessFlow(tenant, sourceID, snapshotID, members)...)

	sort.SliceStable(relations, func(i, j int) bool {
		a, b := relations[i], relations[j]
		return fmt.Sprintf("%s|%s|%09d|%s|%s", a.FromPath, a.Kind, rangeStart(a.FromRange), a.ToPath, a.ToKey) <
			fmt.Sprintf("%s|%s|%09d|%s|%s", b.FromPath, b.Kind, rangeStart(b.FromRange), b.ToPath, b.ToKey)
	})
	return relations
}

func correlateStaticBusinessFlow(tenant uint64, sourceID, snapshotID string, members []SourceRelationMember) []types.SourceCodeRelation {
	typeDeclarations := map[string][]factOwner{}
	methods := map[string][]factOwner{}
	methodsByType := map[string][]factOwner{}
	injections := map[string][]factOwner{}
	importsByFile := map[string][]string{}
	var supertypeReferences []factOwner
	var calls, mappings, requests, apiPrefixes, apiProxies []factOwner
	for _, member := range members {
		if member.FileID == "" || member.VersionID == "" {
			continue
		}
		for _, fact := range member.Facts {
			owner := factOwner{member, fact}
			switch fact.Kind {
			case "java_type":
				typeDeclarations[fact.Namespace] = append(typeDeclarations[fact.Namespace], owner)
			case "java_supertype_reference":
				supertypeReferences = append(supertypeReferences, owner)
			case "java_import":
				key := sourceMemberVersionKey(member)
				importsByFile[key] = append(importsByFile[key], fact.Name)
			case "java_method":
				methods[relationLookupKey(fact.Namespace, fact.Name)] = append(methods[relationLookupKey(fact.Namespace, fact.Name)], owner)
				methodsByType[fact.Namespace] = append(methodsByType[fact.Namespace], owner)
			case "java_injection":
				injections[relationLookupKey(fact.Namespace, fact.Name)] = append(injections[relationLookupKey(fact.Namespace, fact.Name)], owner)
			case "java_method_call":
				calls = append(calls, owner)
			case "spring_mapping":
				mappings = append(mappings, owner)
			case "api_request":
				requests = append(requests, owner)
			case "api_prefix":
				apiPrefixes = append(apiPrefixes, owner)
			case "api_proxy":
				apiProxies = append(apiProxies, owner)
			}
		}
	}
	implementersByType := map[string][]factOwner{}
	for _, declarations := range typeDeclarations {
		for _, declaration := range declarations {
			if declaration.fact.Certainty == "uncertain" || declaration.fact.OwnerKind != "class" {
				continue
			}
			for _, superType := range declaration.fact.SuperTypes {
				implementersByType[superType] = append(implementersByType[superType], declaration)
			}
		}
	}
	var relations []types.SourceCodeRelation
	for _, reference := range supertypeReferences {
		if reference.fact.Certainty != "uncertain" || reference.fact.TargetName == "" {
			continue
		}
		candidateNamespaces := map[string]bool{}
		for _, declarations := range typeDeclarations {
			for _, candidate := range declarations {
				if javaSupertypeMayReference(reference, candidate.fact, importsByFile) {
					candidateNamespaces[candidate.fact.Namespace] = true
				}
			}
		}
		fromKey := reference.fact.Namespace + " -> " + reference.fact.TargetName
		reason := reference.fact.Reason
		if reason == "" {
			reason = "Java supertype identity is unresolved"
		}
		if len(candidateNamespaces) > 1 {
			reason = "Java supertype name matches multiple imported types"
		}
		if len(candidateNamespaces) == 0 {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "type_supertype",
				reference.member, reference.fact, fromKey, nil, reference.fact.TargetName, "uncertain", reason))
			continue
		}
		for namespace := range candidateNamespaces {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "type_supertype",
				reference.member, reference.fact, fromKey, nil, namespace, "uncertain", reason))
		}
	}

	for _, injection := range flattenFactOwners(injections) {
		fact := injection.fact
		typeName, resolved := resolveSnapshotJavaType(injection, fact, typeDeclarations, importsByFile)
		if !resolved {
			if fact.TargetName != "" {
				relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "dependency_injection",
					injection.member, fact, fact.Namespace+"."+fact.Name, nil, "", "uncertain",
					"injected Java type is unresolved or ambiguous in the snapshot"))
			}
			continue
		}
		targets := typeDeclarations[typeName]
		key := fact.Namespace + "." + fact.Name
		if len(targets) == 1 && targets[0].fact.Certainty != "uncertain" {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "dependency_injection",
				injection.member, fact, key, &targets[0], typeFactKey(targets[0].fact), "certain", ""))
			continue
		}
		reason := "injected Java type is missing"
		if len(targets) > 1 {
			reason = "injected Java type is ambiguous"
		}
		relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "dependency_injection",
			injection.member, fact, key, nil, "", "uncertain", reason))
	}

	// Preserve Java's declared type relation without inventing implementations for interfaces.
	for _, declarations := range typeDeclarations {
		for _, declaration := range declarations {
			fact := declaration.fact
			for _, superType := range fact.SuperTypes {
				targets := typeDeclarations[superType]
				key := typeFactKey(fact) + " -> " + superType
				if fact.Certainty != "uncertain" && len(targets) == 1 && targets[0].fact.Certainty != "uncertain" {
					relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "type_supertype",
						declaration.member, fact, key, &targets[0], typeFactKey(targets[0].fact), "certain", ""))
					continue
				}
				reason := "Java supertype is missing or unresolved"
				if len(targets) > 1 {
					reason = "Java supertype is ambiguous"
				}
				relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "type_supertype",
					declaration.member, fact, key, nil, "", "uncertain", reason))
			}
		}
	}

	for _, call := range calls {
		fact := call.fact
		receiver := strings.TrimPrefix(fact.Receiver, "this.")
		bindings := injections[relationLookupKey(fact.Namespace, receiver)]
		if receiver == "" || len(bindings) == 0 {
			continue
		}
		key := fact.Namespace + "#" + fact.MethodName + " -> " + receiver + "." + fact.Name
		binding := bindings[0].fact
		callType, callResolved := resolveSnapshotJavaType(call, fact, typeDeclarations, importsByFile)
		bindingType, bindingResolved := resolveSnapshotJavaType(bindings[0], binding, typeDeclarations, importsByFile)
		if !callResolved || !bindingResolved || callType != bindingType || len(bindings) != 1 {
			reason := "Java call receiver or injected type is unresolved"
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "method_call",
				call.member, fact, key, nil, "", "uncertain", reason))
			continue
		}
		targets := methods[relationLookupKey(callType, fact.Name)]
		if len(targets) == 1 {
			relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "method_call",
				call.member, fact, key, &targets[0], methodFactKey(targets[0].fact), "certain", ""))
			continue
		}
		reason := "declared Java method target is missing"
		if len(targets) > 1 {
			reason = "declared Java method target is overloaded or ambiguous"
		}
		relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "method_call",
			call.member, fact, key, nil, "", "uncertain", reason))
	}

	for _, interfaceDeclarations := range typeDeclarations {
		for _, contract := range interfaceDeclarations {
			if contract.fact.OwnerKind != "interface" || contract.fact.Certainty == "uncertain" {
				continue
			}
			implementations := implementersByType[contract.fact.Namespace]
			for _, method := range methodsByType[contract.fact.Namespace] {
				var unresolvedMethodCandidates []struct {
					implementation factOwner
					reference      factOwner
				}
				for _, reference := range supertypeReferences {
					if reference.fact.ReferenceKind != "implements" || !javaSupertypeMayReference(reference, contract.fact, importsByFile) {
						continue
					}
					for _, implementationType := range typeDeclarations[reference.fact.Namespace] {
						if implementationType.member.FileID != reference.member.FileID || implementationType.member.VersionID != reference.member.VersionID || implementationType.fact.OwnerKind != "class" {
							continue
						}
						for _, candidate := range methodsByType[implementationType.fact.Namespace] {
							if candidate.member.FileID != implementationType.member.FileID || candidate.member.VersionID != implementationType.member.VersionID ||
								candidate.fact.Name != method.fact.Name || candidate.fact.IsAbstract {
								continue
							}
							if method.fact.SignatureCertain && candidate.fact.SignatureCertain &&
								!slices.Equal(method.fact.ParameterTypes, candidate.fact.ParameterTypes) {
								continue
							}
							unresolvedMethodCandidates = append(unresolvedMethodCandidates, struct {
								implementation factOwner
								reference      factOwner
							}{candidate, reference})
						}
					}
				}
				if len(unresolvedMethodCandidates) > 0 {
					seenCandidates := map[struct {
						fileID    string
						versionID string
						startByte int
						endByte   int
					}]bool{}
					appendUncertainCandidate := func(candidate factOwner, reason string) {
						key := struct {
							fileID    string
							versionID string
							startByte int
							endByte   int
						}{candidate.member.FileID, candidate.member.VersionID, candidate.fact.Range.StartByte, candidate.fact.Range.EndByte}
						if seenCandidates[key] {
							return
						}
						seenCandidates[key] = true
						if !method.fact.SignatureCertain || !candidate.fact.SignatureCertain {
							reason = "Java method parameter signature is unresolved"
						}
						relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "implements_method",
							method.member, method.fact, methodFactKey(method.fact), nil,
							methodFactKey(candidate.fact), "uncertain", reason))
					}
					for _, candidate := range unresolvedMethodCandidates {
						reason := "Java implementation is only a candidate because its supertype identity is unresolved"
						if candidate.reference.fact.Reason != "" {
							reason = "Java implementation candidate: " + candidate.reference.fact.Reason
						}
						appendUncertainCandidate(candidate.implementation, reason)
					}
					for _, implementation := range implementations {
						for _, candidate := range methods[relationLookupKey(implementation.fact.Namespace, method.fact.Name)] {
							if candidate.fact.IsAbstract || method.fact.SignatureCertain && candidate.fact.SignatureCertain &&
								!slices.Equal(method.fact.ParameterTypes, candidate.fact.ParameterTypes) {
								continue
							}
							appendUncertainCandidate(candidate,
								"Java implementation is not unique because an unresolved supertype candidate may implement the same contract")
						}
					}
					continue
				}
				var namedCandidates []factOwner
				for _, implementation := range implementations {
					namedCandidates = append(namedCandidates, methods[relationLookupKey(implementation.fact.Namespace, method.fact.Name)]...)
				}
				var exactCandidates []factOwner
				var abstractExact bool
				var unresolvedCandidate bool
				for _, candidate := range namedCandidates {
					if !method.fact.SignatureCertain || !candidate.fact.SignatureCertain {
						unresolvedCandidate = true
						continue
					}
					if !slices.Equal(method.fact.ParameterTypes, candidate.fact.ParameterTypes) {
						continue
					}
					if candidate.fact.IsAbstract {
						abstractExact = true
						continue
					}
					exactCandidates = append(exactCandidates, candidate)
				}
				key := methodFactKey(method.fact)
				if len(implementations) == 1 && len(exactCandidates) == 1 && len(typeDeclarations[contract.fact.Namespace]) == 1 && !unresolvedCandidate {
					relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "implements_method",
						method.member, method.fact, key, &exactCandidates[0], methodFactKey(exactCandidates[0].fact), "certain", ""))
					continue
				}
				if method.fact.IsDefault && len(exactCandidates) == 0 && !abstractExact && !unresolvedCandidate {
					continue
				}
				reason := "Java service method implementation is missing"
				if !method.fact.SignatureCertain || unresolvedCandidate {
					reason = "Java method parameter signature is unresolved"
				} else if abstractExact {
					reason = "Java service method has only an abstract declaration"
				} else if len(namedCandidates) > 0 && len(exactCandidates) == 0 {
					reason = "Java service method parameter signatures do not match"
				} else if len(implementations) > 1 || len(exactCandidates) > 1 || len(typeDeclarations[contract.fact.Namespace]) > 1 {
					reason = "Java service has multiple possible implementations"
				}
				relations = append(relations, sourceFactRelation(tenant, sourceID, snapshotID, "implements_method",
					method.member, method.fact, key, nil, "", "uncertain", reason))
			}
		}
	}

	classRoutes := map[string][]factOwner{}
	methodRoutes := []factOwner{}
	for _, mapping := range mappings {
		if mapping.fact.StatementType == "type" || mapping.fact.OwnerKind == "type" {
			classRoutes[mapping.fact.Namespace] = append(classRoutes[mapping.fact.Namespace], mapping)
		} else {
			methodRoutes = append(methodRoutes, mapping)
		}
	}
	var endpoints []sourceRouteEndpoint
	for _, mapping := range methodRoutes {
		fact := mapping.fact
		if fact.RoutePath == "" || fact.OwnerName == "" {
			continue
		}
		methodSet, methodRestricted, methodUncertain := springMethodConstraint(fact)
		prefixes := classRoutes[fact.Namespace]
		if len(prefixes) == 0 {
			endpoints = append(endpoints, sourceRouteEndpoint{owner: mapping, path: normalizeSourceRoute(fact.RoutePath),
				methods: methodSet, methodRestricted: methodRestricted, methodUncertain: methodUncertain,
				uncertain: fact.Dynamic || fact.Certainty == "uncertain"})
			continue
		}
		for _, prefix := range prefixes {
			if prefix.fact.RoutePath == "" {
				continue
			}
			classMethods, classRestricted, classUncertain := springMethodConstraint(prefix.fact)
			combinedMethods, combinedRestricted, combinedUncertain, hasMethods := combineSpringMethodConstraints(
				classMethods, classRestricted, classUncertain, methodSet, methodRestricted, methodUncertain)
			if !hasMethods {
				continue
			}
			var supportingFacts []types.SourceRelationFactRef
			if !sourceFactRangeCoveredBySameMember(mapping, prefix) {
				supportingFacts = []types.SourceRelationFactRef{sourceRelationFactRef(sourceID, snapshotID, prefix, "spring_class_mapping")}
			}
			endpoints = append(endpoints, sourceRouteEndpoint{owner: mapping,
				path:    normalizeSourceRoute(joinSourceRoute(prefix.fact.RoutePath, fact.RoutePath)),
				methods: combinedMethods, methodRestricted: combinedRestricted, methodUncertain: combinedUncertain,
				uncertain:       prefix.fact.Dynamic || prefix.fact.Certainty == "uncertain" || fact.Dynamic || fact.Certainty == "uncertain",
				supportingFacts: supportingFacts})
		}
	}
	for _, request := range requests {
		fact := request.fact
		if fact.RoutePath == "" || fact.HTTPMethod == "" {
			continue
		}
		fromKey := strings.ToUpper(fact.HTTPMethod) + " " + fact.RoutePath
		requestOnlyRoute := func(reason string) types.SourceCodeRelation {
			return sourceFactRelation(tenant, sourceID, snapshotID, "http_route", request.member, fact,
				fromKey, nil, "", "uncertain", reason)
		}
		if fact.Dynamic {
			relations = append(relations, requestOnlyRoute("No statically validated backend route relationship was found for this request"))
			continue
		}
		requestRoutes := []sourceRequestRoute{{path: normalizeSourceRoute(fact.RoutePath)}}
		var moduleProxies []factOwner
		for _, proxy := range apiProxies {
			root := proxy.fact.OwnerName
			if root != "" && sourcePathWithin(request.member.Path, root) {
				moduleProxies = append(moduleProxies, proxy)
			}
		}
		if len(moduleProxies) > 0 {
			// A proxy fact scopes frontend URL transforms to its containing module.
			// Configurations in sibling applications must not rewrite this request.
			requestRoutes = nil
			for _, prefix := range apiPrefixes {
				prefixFact := prefix.fact
				if prefixFact.RoutePath == "" || prefixFact.Dynamic || !strings.HasPrefix(prefixFact.RoutePath, "/") {
					continue
				}
				for _, proxy := range moduleProxies {
					proxyFact := proxy.fact
					if !sourcePathWithin(prefix.member.Path, proxyFact.OwnerName) {
						continue
					}
					browserPath := normalizeSourceRoute(joinSourceRoute(prefixFact.RoutePath, fact.RoutePath))
					rewritePrefix := strings.TrimPrefix(proxyFact.TargetName, "^")
					if proxyFact.Dynamic || proxyFact.Certainty == "uncertain" || proxyFact.Name == "" ||
						proxyFact.RoutePath == "" || rewritePrefix == "" || rewritePrefix != prefixFact.RoutePath ||
						!strings.HasPrefix(browserPath, proxyFact.Name) {
						continue
					}
					remainder := strings.TrimPrefix(browserPath, rewritePrefix)
					if remainder != "" && !strings.HasPrefix(remainder, "/") {
						continue
					}
					resolvedPath := normalizeSourceRoute(joinSourceRoute(proxyFact.RoutePath,
						joinSourceRoute(proxyFact.Namespace, remainder)))
					requestRoutes = append(requestRoutes, sourceRequestRoute{path: resolvedPath, uncertain: prefixFact.Certainty != "certain",
						reason: "frontend prefix or proxy transformation is conditional or unverified",
						supportingFacts: []types.SourceRelationFactRef{
							sourceRelationFactRef(sourceID, snapshotID, prefix, "api_prefix"),
							sourceRelationFactRef(sourceID, snapshotID, proxy, "api_proxy"),
						}})
				}
			}
			requestRoutes = uniqueRequestRoutes(requestRoutes)
		}
		if len(requestRoutes) == 0 {
			relations = append(relations, requestOnlyRoute("No statically validated backend route relationship was found for this request"))
			continue
		}
		var candidates []sourceRouteEndpoint
		for _, requestRoute := range requestRoutes {
			for _, endpoint := range endpoints {
				exactRoute := endpoint.path == requestRoute.path
				legacySuffix := endpoint.path == normalizeSourceRoute(strings.TrimSuffix(requestRoute.path, ".do"))
				methodMatches, methodUncertain := springRouteMethodMatch(endpoint, fact.HTTPMethod)
				if (!exactRoute && !legacySuffix) || !methodMatches {
					continue
				}
				candidate := endpoint
				candidate.uncertain = endpoint.uncertain || requestRoute.uncertain || methodUncertain || (legacySuffix && !exactRoute)
				candidate.supportingFacts = mergeSourceRelationFactRefs(endpoint.supportingFacts, requestRoute.supportingFacts)
				candidates = append(candidates, candidate)
			}
		}
		candidates = uniqueRouteEndpoints(candidates)
		if len(candidates) == 0 {
			relations = append(relations, requestOnlyRoute("No statically validated backend route relationship was found for this request"))
			continue
		}
		if len(candidates) == 1 && !candidates[0].uncertain {
			relation := sourceFactRelation(tenant, sourceID, snapshotID, "http_route",
				request.member, fact, fromKey,
				&candidates[0].owner, springEndpointKey(candidates[0].owner.fact, candidates[0].path), "certain", "")
			setSourceRelationFactRefs(&relation, candidates[0].supportingFacts)
			relations = append(relations, relation)
			continue
		}
		if len(candidates) > 0 {
			reason := "Spring route target is ambiguous or depends on unresolved mapping metadata"
			if len(requestRoutes) == 1 && requestRoutes[0].uncertain {
				reason = requestRoutes[0].reason
			} else if len(candidates) == 1 && candidates[0].path == normalizeSourceRoute(strings.TrimSuffix(requestRoutes[0].path, ".do")) {
				reason = "legacy .do route suffix matching is not verified by source configuration"
			}
			relation := sourceFactRelation(tenant, sourceID, snapshotID, "http_route", request.member, fact,
				fromKey, nil, "", "uncertain", reason)
			relation.ToKey = springEndpointKey(candidates[0].owner.fact, candidates[0].path)
			var supportingFacts []types.SourceRelationFactRef
			for _, candidate := range candidates {
				supportingFacts = mergeSourceRelationFactRefs(supportingFacts, candidate.supportingFacts)
			}
			setSourceRelationFactRefs(&relation, supportingFacts)
			relations = append(relations, relation)
		}
	}
	return relations
}

func sourcePathWithin(filePath, directory string) bool {
	cleanFile := path.Clean(strings.TrimPrefix(strings.ReplaceAll(filePath, "\\", "/"), "/"))
	cleanDirectory := path.Clean(strings.TrimPrefix(strings.ReplaceAll(directory, "\\", "/"), "/"))
	if cleanFile == "." || cleanDirectory == "." || cleanFile == ".." || cleanDirectory == ".." ||
		strings.HasPrefix(cleanFile, "../") || strings.HasPrefix(cleanDirectory, "../") {
		return false
	}
	return cleanFile == cleanDirectory || strings.HasPrefix(cleanFile, strings.TrimRight(cleanDirectory, "/")+"/")
}

func springMethodConstraint(fact types.ParsedSourceFact) ([]string, bool, bool) {
	if fact.HTTPMethodsSpecified {
		methods := append([]string(nil), fact.HTTPMethods...)
		if len(methods) == 0 && fact.HTTPMethod != "" {
			methods = []string{fact.HTTPMethod}
		}
		return methods, true, !fact.HTTPMethodsCertain
	}
	if fact.HTTPMethod != "" {
		return []string{fact.HTTPMethod}, true, false
	}
	return nil, false, false
}

func combineSpringMethodConstraints(left []string, leftRestricted, leftUncertain bool,
	right []string, rightRestricted, rightUncertain bool) ([]string, bool, bool, bool) {
	if !leftRestricted {
		return right, rightRestricted, rightUncertain, true
	}
	if !rightRestricted {
		return left, leftRestricted, leftUncertain, true
	}
	if leftUncertain && rightUncertain {
		return nil, true, true, true
	}
	if leftUncertain {
		return right, true, true, true
	}
	if rightUncertain {
		return left, true, true, true
	}
	allowed := make([]string, 0, len(left))
	for _, method := range left {
		if containsSourceMethod(right, method) {
			allowed = append(allowed, method)
		}
	}
	return allowed, true, false, len(allowed) > 0
}

func springRouteMethodMatch(endpoint sourceRouteEndpoint, requestMethod string) (bool, bool) {
	if !endpoint.methodRestricted {
		return true, false
	}
	if endpoint.methodUncertain || len(endpoint.methods) == 0 || requestMethod == "" {
		return true, true
	}
	return containsSourceMethod(endpoint.methods, strings.ToUpper(requestMethod)), false
}

func containsSourceMethod(methods []string, expected string) bool {
	for _, method := range methods {
		if strings.EqualFold(method, expected) {
			return true
		}
	}
	return false
}

func resolveSnapshotJavaType(owner factOwner, fact types.ParsedSourceFact,
	declarations map[string][]factOwner, importsByFile map[string][]string) (string, bool) {
	if fact.TypeName != "" && fact.Certainty != "uncertain" && !fact.Dynamic {
		return fact.TypeName, true
	}
	simpleName := fact.TargetName
	if simpleName == "" {
		return "", false
	}
	if strings.Contains(simpleName, ".") {
		targets := declarations[simpleName]
		return simpleName, len(targets) == 1
	}

	packageName := ""
	if separator := strings.LastIndex(fact.Namespace, "."); separator >= 0 {
		packageName = fact.Namespace[:separator]
	}

	var explicitImports []string
	for _, imported := range importsByFile[sourceMemberVersionKey(owner.member)] {
		if !strings.HasSuffix(imported, ".*") && imported[strings.LastIndex(imported, ".")+1:] == simpleName {
			explicitImports = append(explicitImports, imported)
		}
	}
	if len(explicitImports) > 0 {
		if len(explicitImports) != 1 || len(declarations[explicitImports[0]]) != 1 {
			return "", false
		}
		return explicitImports[0], true
	}
	if packageName != "" {
		candidate := packageName + "." + simpleName
		if len(declarations[candidate]) == 1 {
			return candidate, true
		}
	}

	candidates := map[string]struct{}{}
	for _, imported := range importsByFile[sourceMemberVersionKey(owner.member)] {
		if !strings.HasSuffix(imported, ".*") {
			continue
		}
		candidate := strings.TrimSuffix(imported, ".*") + "." + simpleName
		if len(declarations[candidate]) > 0 {
			candidates[candidate] = struct{}{}
		}
	}
	if len(candidates) != 1 {
		return "", false
	}
	for candidate := range candidates {
		if len(declarations[candidate]) == 1 {
			return candidate, true
		}
	}
	return "", false
}

func sourceMemberVersionKey(member SourceRelationMember) string {
	return member.FileID + "\x00" + member.VersionID
}

func uniqueRequestRoutes(routes []sourceRequestRoute) []sourceRequestRoute {
	seen := make(map[string]int, len(routes))
	unique := make([]sourceRequestRoute, 0, len(routes))
	for _, route := range routes {
		key := route.path
		if index, exists := seen[key]; exists {
			unique[index].uncertain = unique[index].uncertain || route.uncertain
			if unique[index].reason == "" {
				unique[index].reason = route.reason
			}
			unique[index].supportingFacts = mergeSourceRelationFactRefs(unique[index].supportingFacts, route.supportingFacts)
			continue
		}
		seen[key] = len(unique)
		unique = append(unique, route)
	}
	return unique
}

func uniqueRouteEndpoints(endpoints []sourceRouteEndpoint) []sourceRouteEndpoint {
	seen := make(map[string]int, len(endpoints))
	unique := make([]sourceRouteEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		key := endpoint.owner.member.FileID + "\x00" + endpoint.owner.fact.Namespace + "\x00" + endpoint.owner.fact.OwnerName + "\x00" + endpoint.path + "\x00" +
			strings.Join(endpoint.methods, ",") + fmt.Sprint(endpoint.methodRestricted) + fmt.Sprint(endpoint.methodUncertain)
		if index, exists := seen[key]; exists {
			unique[index].uncertain = unique[index].uncertain || endpoint.uncertain
			unique[index].supportingFacts = mergeSourceRelationFactRefs(unique[index].supportingFacts, endpoint.supportingFacts)
			continue
		}
		seen[key] = len(unique)
		unique = append(unique, endpoint)
	}
	return unique
}

func sourceRelationFactRef(sourceID, snapshotID string, owner factOwner, role string) types.SourceRelationFactRef {
	return types.SourceRelationFactRef{DataSourceID: sourceID, SnapshotID: snapshotID,
		FileID: owner.member.FileID, FileVersionID: owner.member.VersionID, Path: owner.member.Path,
		Kind: owner.fact.Kind, Role: role, Quality: owner.fact.Quality, Range: owner.fact.Range}
}

func sourceFactRangeCoveredBySameMember(outer, inner factOwner) bool {
	if outer.member.FileID != inner.member.FileID || outer.member.VersionID != inner.member.VersionID ||
		outer.member.Path != inner.member.Path {
		return false
	}
	return outer.fact.Range.StartByte <= inner.fact.Range.StartByte && outer.fact.Range.EndByte >= inner.fact.Range.EndByte &&
		outer.fact.Range.StartLine <= inner.fact.Range.StartLine && outer.fact.Range.EndLine >= inner.fact.Range.EndLine
}

func mergeSourceRelationFactRefs(existing, incoming []types.SourceRelationFactRef) []types.SourceRelationFactRef {
	if len(incoming) == 0 {
		return existing
	}
	seen := make(map[types.SourceRelationFactRef]bool, len(existing)+len(incoming))
	merged := make([]types.SourceRelationFactRef, 0, len(existing)+len(incoming))
	for _, ref := range append(append([]types.SourceRelationFactRef(nil), existing...), incoming...) {
		if seen[ref] {
			continue
		}
		seen[ref] = true
		merged = append(merged, ref)
	}
	return merged
}

func setSourceRelationFactRefs(relation *types.SourceCodeRelation, refs []types.SourceRelationFactRef) {
	if relation == nil || len(refs) == 0 {
		return
	}
	ordered := append([]types.SourceRelationFactRef(nil), refs...)
	sort.Slice(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		for _, field := range [][2]string{
			{a.Role, b.Role}, {a.DataSourceID, b.DataSourceID}, {a.SnapshotID, b.SnapshotID},
			{a.FileID, b.FileID}, {a.FileVersionID, b.FileVersionID}, {a.Path, b.Path},
			{a.Kind, b.Kind}, {a.Quality, b.Quality},
		} {
			if field[0] != field[1] {
				return field[0] < field[1]
			}
		}
		for _, field := range [][2]int{
			{a.Range.StartByte, b.Range.StartByte}, {a.Range.EndByte, b.Range.EndByte},
			{a.Range.StartLine, b.Range.StartLine}, {a.Range.EndLine, b.Range.EndLine},
		} {
			if field[0] != field[1] {
				return field[0] < field[1]
			}
		}
		return false
	})
	encoded, err := json.Marshal(ordered)
	if err == nil {
		relation.Context = types.JSON(encoded)
	}
}

func flattenFactOwners(values map[string][]factOwner) []factOwner {
	var result []factOwner
	for _, owners := range values {
		result = append(result, owners...)
	}
	return result
}

func typeFactKey(fact types.ParsedSourceFact) string {
	return fact.Namespace
}

func methodFactKey(fact types.ParsedSourceFact) string {
	return fact.Namespace + "#" + fact.Name
}

func javaSupertypeMayReference(reference factOwner, target types.ParsedSourceFact, importsByFile map[string][]string) bool {
	name := reference.fact.TargetName
	if name == "" {
		name = reference.fact.Name
	}
	if name == "" || name != target.Name || target.Namespace == "" {
		return false
	}
	targetPackage := ""
	if separator := strings.LastIndex(target.Namespace, "."); separator >= 0 {
		targetPackage = target.Namespace[:separator]
	}
	for _, imported := range importsByFile[sourceMemberVersionKey(reference.member)] {
		if imported == target.Namespace || targetPackage != "" && imported == targetPackage+".*" {
			return true
		}
	}
	return false
}

func springEndpointKey(fact types.ParsedSourceFact, route string) string {
	return fact.Namespace + "#" + fact.OwnerName + " " + route
}

func joinSourceRoute(prefix, suffix string) string {
	left, right := strings.TrimRight(prefix, "/"), strings.TrimLeft(suffix, "/")
	if left == "" {
		return "/" + right
	}
	if right == "" {
		return left
	}
	return left + "/" + right
}

func normalizeSourceRoute(route string) string {
	if route == "" {
		return "/"
	}
	return "/" + strings.Trim(strings.TrimSpace(route), "/")
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
