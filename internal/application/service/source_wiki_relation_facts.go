package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
)

type sourceWikiRelationFactResolverCache struct {
	mu       sync.Mutex
	key      string
	resolver *source.SourceRelationFactRefResolver
}

func sourceWikiRelationFactSnapshot(tenantID uint64, snapshot *repository.SourceWikiSkeletonSnapshot) source.SourceRelationFactSnapshot {
	result := source.SourceRelationFactSnapshot{}
	if snapshot == nil {
		return result
	}
	result = source.SourceRelationFactSnapshot{
		TenantID: tenantID, DataSourceID: snapshot.DataSourceID, SnapshotID: snapshot.SnapshotID,
		Complete: snapshot.Complete, Members: make([]source.SourceRelationMember, 0, len(snapshot.Members)),
	}
	for _, member := range snapshot.Members {
		result.Members = append(result.Members, source.SourceRelationMember{
			Path: member.Path, FileID: member.FileID, VersionID: member.VersionID, Facts: member.Facts,
		})
	}
	return result
}

func (s *sourceWikiService) relationFactResolver(
	ctx context.Context,
	tenantID uint64,
	knowledgeBaseID string,
	sourceID string,
	snapshotID string,
	loaded *repository.SourceWikiSkeletonSnapshot,
) (*source.SourceRelationFactRefResolver, error) {
	if s == nil || s.db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return nil, fmt.Errorf("source relation facts require a fixed tenant and snapshot")
	}
	key := fmt.Sprintf("%d\x00%s\x00%s\x00%s", tenantID, knowledgeBaseID, sourceID, snapshotID)
	s.relationFacts.mu.Lock()
	defer s.relationFacts.mu.Unlock()
	if s.relationFacts.key == key && s.relationFacts.resolver != nil {
		return s.relationFacts.resolver, nil
	}
	if loaded == nil {
		var err error
		loaded, err = repository.LoadSourceWikiSkeletonSnapshot(ctx, s.db, tenantID, knowledgeBaseID, sourceID, snapshotID)
		if err != nil {
			return nil, err
		}
	}
	if loaded.TenantID != tenantID || loaded.DataSourceID != sourceID || loaded.SnapshotID != snapshotID {
		return nil, fmt.Errorf("source relation facts do not match the fixed snapshot")
	}
	resolver := source.NewSourceRelationFactRefResolver(sourceWikiRelationFactSnapshot(tenantID, loaded))
	s.relationFacts.key = key
	s.relationFacts.resolver = resolver
	return resolver, nil
}

func (s *sourceWikiService) resolveSourceWikiRelations(
	ctx context.Context,
	tenantID uint64,
	knowledgeBaseID string,
	sourceID string,
	snapshotID string,
	loaded *repository.SourceWikiSkeletonSnapshot,
	relations []types.SourceCodeRelation,
) ([]types.SourceCodeRelation, error) {
	needsResolver := false
	for _, relation := range relations {
		if relation.Kind == "http_route" {
			needsResolver = true
			break
		}
	}
	resolved := append([]types.SourceCodeRelation(nil), relations...)
	if !needsResolver {
		return resolved, nil
	}
	resolver, err := s.relationFactResolver(ctx, tenantID, knowledgeBaseID, sourceID, snapshotID, loaded)
	if err != nil {
		return nil, err
	}
	return sourceWikiResolveRelationFactRefs(resolver, relations)
}

func sourceWikiResolveRelationFactRefs(resolver *source.SourceRelationFactRefResolver, relations []types.SourceCodeRelation) ([]types.SourceCodeRelation, error) {
	resolved := append([]types.SourceCodeRelation(nil), relations...)
	for _, relation := range resolved {
		if relation.Kind == "http_route" && resolver == nil {
			return nil, fmt.Errorf("HTTP route relation lacks a fixed source-fact resolver")
		}
	}
	for i := range resolved {
		if resolved[i].Kind != "http_route" {
			continue
		}
		resolution := resolver.Resolve(resolved[i])
		if resolution.Err != nil {
			return nil, fmt.Errorf("HTTP route relation reference resolution failed: %w", resolution.Err)
		}
		if resolution.Status != source.SourceRelationFactRefsVerified && resolution.Status != source.SourceRelationFactRefsReplayed {
			return nil, fmt.Errorf("HTTP route relation lacks verifiable exact source-fact references")
		}
		if len(resolution.Context) == 0 {
			return nil, fmt.Errorf("HTTP route relation reference context is unavailable")
		}
		resolved[i].Context = resolution.Context
	}
	return resolved, nil
}

func sourceWikiValidateDiagramFactEvidence(relations []types.SourceCodeRelation, evidence []types.SourceWikiEvidence, diagram SourceWikiFlowDiagram) error {
	evidenceIDs := make(map[string]struct{}, len(evidence))
	for _, item := range evidence {
		if item.ID == "" {
			return fmt.Errorf("flow diagram evidence has no stable ID")
		}
		evidenceIDs[item.ID] = struct{}{}
	}
	diagramIDs := make(map[string]struct{}, len(diagram.EvidenceIDs))
	for _, id := range diagram.EvidenceIDs {
		if _, ok := evidenceIDs[id]; !ok {
			return fmt.Errorf("flow diagram references unowned evidence")
		}
		diagramIDs[id] = struct{}{}
	}
	for _, relation := range relations {
		if relation.Kind != "http_route" {
			continue
		}
		var refs []types.SourceRelationFactRef
		if len(relation.Context) == 0 || json.Unmarshal(relation.Context, &refs) != nil || refs == nil {
			return fmt.Errorf("HTTP route relation fact references are invalid")
		}
		for _, ref := range refs {
			cited := false
			for _, item := range evidence {
				if item.DataSourceID != ref.DataSourceID || item.SnapshotID != ref.SnapshotID || item.KnowledgeID != ref.FileID ||
					item.FileVersionID != ref.FileVersionID || item.Path != ref.Path || !sourceWikiFlowRangeCovers(item.Range, ref.Range) {
					continue
				}
				if _, ok := diagramIDs[item.ID]; ok {
					cited = true
					break
				}
			}
			if !cited {
				return fmt.Errorf("flow diagram does not cite exact evidence for a referenced route fact")
			}
		}
	}
	return nil
}
