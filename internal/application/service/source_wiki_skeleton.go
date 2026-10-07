package service

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	sourceWikiTopicSystem      = "system"
	sourceWikiTopicModule      = "module"
	sourceWikiTopicFlow        = "flow"
	sourceWikiMaxInitialTopics = 40
	sourceWikiMaxFlowRelations = 64
	sourceWikiMaxFlowDepth     = 8
)

type SourceWikiTopic = types.SourceWikiTopic

type SourceWikiSkeletonPlan struct {
	Topics         []SourceWikiTopic
	ModuleCount    int
	FlowCount      int
	InitialCount   int
	ExpansionCount int
}

type sourceWikiSkeletonInput struct {
	SourceID   string
	SnapshotID string
	Files      []sourceWikiSkeletonFile
	Relations  []types.SourceCodeRelation
}

type sourceWikiSkeletonFile struct {
	Path      string
	Generated bool
	Facts     []types.ParsedSourceFact
}

type sourceWikiModuleCandidate struct {
	path     string
	priority int
	title    string
}

// buildSourceWikiSkeleton uses architectural declarations and statically
// correlated business entry points, not every directory or method, as topic
// seeds. The initial batch limit is capped here even if a caller misconfigures
// it; later candidates remain visible as expansion work.
func buildSourceWikiSkeleton(input sourceWikiSkeletonInput, initialLimit int) SourceWikiSkeletonPlan {
	if initialLimit < 0 {
		initialLimit = 0
	}
	if initialLimit > sourceWikiMaxInitialTopics {
		initialLimit = sourceWikiMaxInitialTopics
	}

	filesByPath := make(map[string]sourceWikiSkeletonFile, len(input.Files))
	for _, file := range input.Files {
		clean := normalizeSourceWikiPath(file.Path)
		if clean == "" {
			continue
		}
		file.Path = clean
		filesByPath[clean] = file
	}

	modules := sourceWikiModuleCandidates(filesByPath)
	flows := sourceWikiFlowCandidates(input.Relations, filesByPath)
	topics := make([]SourceWikiTopic, 0, 1+len(modules)+len(flows))
	topics = append(topics, SourceWikiTopic{
		SourceID: input.SourceID, SnapshotID: input.SnapshotID, TopicKey: sourceWikiTopicSystem,
		Kind: sourceWikiTopicSystem, Title: "System overview", Priority: 120,
	})
	for _, candidate := range modules {
		topics = append(topics, SourceWikiTopic{
			SourceID: input.SourceID, SnapshotID: input.SnapshotID,
			TopicKey: sourceWikiTopicModule + "/" + candidate.path,
			Kind:     sourceWikiTopicModule, ModulePath: candidate.path, Title: candidate.title,
			Priority: candidate.priority,
		})
	}
	for _, candidate := range flows {
		topics = append(topics, SourceWikiTopic{
			SourceID: input.SourceID, SnapshotID: input.SnapshotID,
			TopicKey: sourceWikiTopicFlow + "/" + candidate.route,
			Kind:     sourceWikiTopicFlow, Title: candidate.route, Priority: candidate.priority,
			Uncertain: len(candidate.reasons) > 0, UncertaintyReasons: candidate.reasons,
			Relations: candidate.relations,
		})
	}
	sort.SliceStable(topics, func(i, j int) bool {
		if topics[i].Priority != topics[j].Priority {
			return topics[i].Priority > topics[j].Priority
		}
		return topics[i].TopicKey < topics[j].TopicKey
	})
	for i := range topics {
		if i < initialLimit {
			topics[i].Status = "planned"
		} else {
			topics[i].Status = "expansion"
		}
	}
	plan := SourceWikiSkeletonPlan{Topics: topics}
	for _, topic := range topics {
		switch topic.Kind {
		case sourceWikiTopicModule:
			plan.ModuleCount++
		case sourceWikiTopicFlow:
			plan.FlowCount++
		}
	}
	plan.InitialCount = min(initialLimit, len(topics))
	plan.ExpansionCount = len(topics) - plan.InitialCount
	return plan
}

// validateSourceWikiSkeletonPlan is the independent bounded integrity check
// before a plan is persisted or sent to a model. It verifies ownership,
// stable identity, cap/status accounting, and uncertainty preservation.
func validateSourceWikiSkeletonPlan(plan SourceWikiSkeletonPlan) error {
	if plan.InitialCount < 0 || plan.InitialCount > sourceWikiMaxInitialTopics ||
		plan.ExpansionCount < 0 || plan.InitialCount+plan.ExpansionCount != len(plan.Topics) {
		return fmt.Errorf("source Wiki skeleton plan exceeds its bounded accounting contract")
	}
	seen := make(map[string]bool, len(plan.Topics))
	systems, modules, flows := 0, 0, 0
	for index, topic := range plan.Topics {
		if topic.SourceID == "" || topic.SnapshotID == "" || topic.TopicKey == "" || topic.Title == "" || seen[topic.TopicKey] {
			return fmt.Errorf("source Wiki skeleton contains an invalid or duplicate topic identity")
		}
		seen[topic.TopicKey] = true
		wantStatus := "expansion"
		if index < plan.InitialCount {
			wantStatus = "planned"
		}
		if topic.Status != wantStatus {
			return fmt.Errorf("source Wiki skeleton initial and expansion states do not match the fixed cap")
		}
		switch topic.Kind {
		case sourceWikiTopicSystem:
			systems++
			if topic.TopicKey != "system" {
				return fmt.Errorf("source Wiki system topic identity is invalid")
			}
		case sourceWikiTopicModule:
			modules++
			if topic.ModulePath == "" || topic.TopicKey != "module/"+topic.ModulePath ||
				(topic.ModulePath != "." && normalizeSourceWikiPath(topic.ModulePath) != topic.ModulePath) {
				return fmt.Errorf("source Wiki module topic identity is invalid")
			}
		case sourceWikiTopicFlow:
			flows++
			if !strings.HasPrefix(topic.TopicKey, "flow/") || sourceWikiCanonicalRoute(strings.TrimPrefix(topic.TopicKey, "flow/")) == "" ||
				topic.Uncertain != (len(topic.UncertaintyReasons) > 0) || len(topic.Relations) > sourceWikiMaxFlowRelations+1 {
				return fmt.Errorf("source Wiki flow topic has invalid route or uncertainty metadata")
			}
		default:
			return fmt.Errorf("source Wiki skeleton contains an unsupported topic kind")
		}
	}
	if systems != 1 || modules != plan.ModuleCount || flows != plan.FlowCount {
		return fmt.Errorf("source Wiki skeleton topic counts are inconsistent")
	}
	return nil
}

func sourceWikiModuleCandidates(files map[string]sourceWikiSkeletonFile) []sourceWikiModuleCandidate {
	candidates := map[string]sourceWikiModuleCandidate{}
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	for _, filePath := range paths {
		file := files[filePath]
		if file.Generated {
			continue
		}
		modulePath := path.Dir(filePath)
		candidate := candidates[modulePath]
		candidate.path = modulePath
		for _, fact := range file.Facts {
			priority := sourceWikiModuleFactPriority(fact)
			if priority > candidate.priority {
				candidate.priority = priority
				candidate.title = sourceWikiModuleTitle(modulePath, fact)
			}
		}
		if candidate.priority > 0 {
			candidates[modulePath] = candidate
		}
	}
	result := make([]sourceWikiModuleCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].priority != result[j].priority {
			return result[i].priority > result[j].priority
		}
		return result[i].path < result[j].path
	})
	return result
}

func sourceWikiModuleFactPriority(fact types.ParsedSourceFact) int {
	return source.SourceWikiModuleFactPriority(fact)
}

func sourceWikiModuleTitle(modulePath string, fact types.ParsedSourceFact) string {
	base := path.Base(modulePath)
	if modulePath == "." || base == "." {
		if fact.Name != "" {
			return fact.Name
		}
		return "Root module"
	}
	return strings.ReplaceAll(base, "-", " ")
}

type sourceWikiFlowCandidate struct {
	route     string
	priority  int
	relations []types.SourceCodeRelation
	reasons   []string
}

func sourceWikiFlowCandidates(relations []types.SourceCodeRelation, files map[string]sourceWikiSkeletonFile) []sourceWikiFlowCandidate {
	ordered := append([]types.SourceCodeRelation(nil), relations...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := ordered[i], ordered[j]
		return strings.Join([]string{a.FromPath, a.Kind, a.FromKey, a.ToPath, a.ToKey}, "\x00") <
			strings.Join([]string{b.FromPath, b.Kind, b.FromKey, b.ToPath, b.ToKey}, "\x00")
	})
	adjacency := make(map[string][]types.SourceCodeRelation)
	for _, relation := range ordered {
		if !sourceWikiFlowEdgeAllowed(relation.Kind) || sourceWikiRelationTouchesGenerated(relation, files) {
			continue
		}
		for _, from := range sourceWikiRelationNodes(relation.FromFileID, relation.FromPath) {
			adjacency[from] = append(adjacency[from], relation)
		}
	}

	candidates := map[string]sourceWikiFlowCandidate{}
	for _, routeEdge := range ordered {
		if routeEdge.Kind != "http_route" || sourceWikiRelationTouchesGenerated(routeEdge, files) {
			continue
		}
		route := sourceWikiCanonicalRoute(routeEdge.FromKey)
		if route == "" {
			route = sourceWikiRouteFromFacts(routeEdge.FromPath, files)
		}
		if route == "" {
			continue
		}
		chain := sourceWikiTraceFlow(routeEdge, adjacency)
		reasons := make([]string, 0)
		seenReasons := map[string]bool{}
		allRelations := append([]types.SourceCodeRelation{routeEdge}, chain...)
		for _, edge := range allRelations {
			if edge.Determinacy != "certain" || edge.Quality != "structural" {
				reason := strings.TrimSpace(edge.ResolutionReason)
				if reason == "" {
					reason = "A source relationship is unresolved or has degraded evidence quality"
				}
				if !seenReasons[reason] {
					seenReasons[reason] = true
					reasons = append(reasons, reason)
				}
			}
		}
		sort.Strings(reasons)
		candidate := candidates[route]
		candidate.route = route
		candidate.priority = 100
		candidate.relations = sourceWikiMergeRelations(candidate.relations, allRelations)
		for _, reason := range reasons {
			if !sourceWikiReasonIncluded(candidate.reasons, reason) {
				candidate.reasons = append(candidate.reasons, reason)
			}
		}
		sort.Strings(candidate.reasons)
		candidates[route] = candidate
	}
	// Frontend API requests remain useful flow entrypoints even when static
	// correlation found no backend endpoint. Keep them in coverage as uncertain
	// rather than implying a complete call chain from the client request alone.
	filePaths := make([]string, 0, len(files))
	for filePath := range files {
		filePaths = append(filePaths, filePath)
	}
	sort.Strings(filePaths)
	for _, filePath := range filePaths {
		file := files[filePath]
		if file.Generated {
			continue
		}
		for _, fact := range file.Facts {
			if fact.Kind != "api_request" || fact.RoutePath == "" || fact.HTTPMethod == "" {
				continue
			}
			route := sourceWikiCanonicalRoute(fact.HTTPMethod + " " + fact.RoutePath)
			if route == "" {
				continue
			}
			candidate := candidates[route]
			candidate.route = route
			candidate.priority = 100
			if fact.Dynamic || fact.Certainty == "uncertain" || fact.Quality != "structural" {
				reason := strings.TrimSpace(fact.Reason)
				if reason == "" {
					reason = "Frontend request path or configuration is dynamic or degraded"
				}
				if !sourceWikiReasonIncluded(candidate.reasons, reason) {
					candidate.reasons = append(candidate.reasons, reason)
				}
			}
			if len(candidate.relations) == 0 {
				reason := "No statically validated backend route relationship was found for this request"
				if !sourceWikiReasonIncluded(candidate.reasons, reason) {
					candidate.reasons = append(candidate.reasons, reason)
				}
			}
			sort.Strings(candidate.reasons)
			candidates[route] = candidate
		}
	}
	result := make([]sourceWikiFlowCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		result = append(result, candidate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].route < result[j].route })
	return result
}

func sourceWikiCanonicalRoute(key string) string {
	fields := strings.Fields(strings.TrimSpace(key))
	if len(fields) != 2 || !strings.HasPrefix(fields[1], "/") {
		return ""
	}
	method := strings.ToUpper(fields[0])
	if method == "" {
		return ""
	}
	return method + " " + fields[1]
}

func sourceWikiRouteFromFacts(filePath string, files map[string]sourceWikiSkeletonFile) string {
	file, ok := files[normalizeSourceWikiPath(filePath)]
	if !ok || file.Generated {
		return ""
	}
	for _, fact := range file.Facts {
		if fact.Kind != "api_request" || fact.RoutePath == "" || fact.HTTPMethod == "" {
			continue
		}
		if route := sourceWikiCanonicalRoute(fact.HTTPMethod + " " + fact.RoutePath); route != "" {
			return route
		}
	}
	return ""
}

func sourceWikiTraceFlow(route types.SourceCodeRelation, adjacency map[string][]types.SourceCodeRelation) []types.SourceCodeRelation {
	type nodeDepth struct {
		node  string
		depth int
	}
	starts := sourceWikiRelationNodes(route.ToFileID, route.ToPath)
	if len(starts) == 0 {
		return nil
	}
	queue := make([]nodeDepth, 0, len(starts))
	seen := make(map[string]bool, len(starts))
	for _, start := range starts {
		queue = append(queue, nodeDepth{node: start})
		seen[start] = true
	}
	visitedEdges := map[string]bool{}
	result := make([]types.SourceCodeRelation, 0)
	for len(queue) > 0 && len(result) < sourceWikiMaxFlowRelations {
		current := queue[0]
		queue = queue[1:]
		if current.depth >= sourceWikiMaxFlowDepth {
			continue
		}
		for _, edge := range adjacency[current.node] {
			identity := strings.Join([]string{edge.Kind, edge.FromFileID, edge.FromKey, edge.ToFileID, edge.ToKey}, "\x00")
			if visitedEdges[identity] {
				continue
			}
			visitedEdges[identity] = true
			result = append(result, edge)
			for _, next := range sourceWikiRelationNodes(edge.ToFileID, edge.ToPath) {
				if !seen[next] {
					seen[next] = true
					queue = append(queue, nodeDepth{node: next, depth: current.depth + 1})
				}
			}
			if len(result) >= sourceWikiMaxFlowRelations {
				break
			}
		}
	}
	return result
}

func sourceWikiFlowEdgeAllowed(kind string) bool {
	switch kind {
	case "method_call", "dependency_injection", "implements_method", "type_supertype",
		"mapper_statement", "include", "result_map", "table_access":
		return true
	default:
		return false
	}
}

func sourceWikiRelationTouchesGenerated(relation types.SourceCodeRelation, files map[string]sourceWikiSkeletonFile) bool {
	from, fromOK := files[normalizeSourceWikiPath(relation.FromPath)]
	to, toOK := files[normalizeSourceWikiPath(relation.ToPath)]
	return fromOK && from.Generated || toOK && to.Generated
}

func sourceWikiRelationNodes(fileID, filePath string) []string {
	var nodes []string
	if fileID != "" {
		nodes = append(nodes, "id:"+fileID)
	}
	if normalized := normalizeSourceWikiPath(filePath); normalized != "" {
		nodes = append(nodes, "path:"+normalized)
	}
	return nodes
}

func sourceWikiMergeRelations(existing, incoming []types.SourceCodeRelation) []types.SourceCodeRelation {
	merged := append([]types.SourceCodeRelation(nil), existing...)
	seen := make(map[string]bool, len(existing)+len(incoming))
	identity := func(edge types.SourceCodeRelation) string {
		return strings.Join([]string{edge.Kind, edge.FromFileID, edge.FromVersionID, edge.FromPath, edge.FromKey,
			edge.ToFileID, edge.ToVersionID, edge.ToPath, edge.ToKey, edge.Determinacy, edge.ResolutionReason}, "\x00")
	}
	for _, edge := range existing {
		seen[identity(edge)] = true
	}
	for _, edge := range incoming {
		key := identity(edge)
		if seen[key] {
			continue
		}
		seen[key] = true
		merged = append(merged, edge)
		if len(merged) >= sourceWikiMaxFlowRelations+1 {
			break
		}
	}
	return merged
}

func normalizeSourceWikiPath(filePath string) string {
	filePath = strings.ReplaceAll(strings.TrimSpace(filePath), "\\", "/")
	filePath = strings.TrimPrefix(filePath, "./")
	if filePath == "" || strings.HasPrefix(filePath, "/") {
		return ""
	}
	clean := path.Clean(filePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return ""
	}
	return clean
}

func sourceWikiReasonIncluded(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}
