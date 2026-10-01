package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
)

func (s *sourceWikiService) ListSourceWikiBatches(ctx context.Context, knowledgeBaseID, sourceID string) ([]types.SourceWikiBatch, error) {
	if knowledgeBaseID == "" || sourceID == "" {
		return nil, fmt.Errorf("knowledge base and source are required")
	}
	ctx, release, err := beginSourceRead(ctx, s.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: knowledgeBaseID, SourceIDs: []string{sourceID},
	}})
	if err != nil {
		return nil, err
	}
	defer release()
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	var batches []types.SourceWikiBatch
	permission := source.SourcePermissionSQL(ctx, "source_wiki_batches.source_id", "NULL")
	err = s.db.WithContext(ctx).Where("knowledge_base_id = ? AND tenant_id = ? AND source_id = ?", knowledgeBaseID, kb.TenantID, sourceID).
		Where(permission).Order("created_at DESC, id DESC").Limit(50).Find(&batches).Error
	if err != nil {
		return nil, err
	}
	return batches, nil
}

func (s *sourceWikiService) GetSourceWikiBatch(ctx context.Context, knowledgeBaseID, sourceID, batchID string) (*types.SourceWikiBatch, error) {
	if knowledgeBaseID == "" || sourceID == "" || batchID == "" {
		return nil, repository.ErrSourceWikiBatchNotFound
	}
	ctx, release, err := beginSourceRead(ctx, s.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: knowledgeBaseID, SourceIDs: []string{sourceID},
	}})
	if err != nil {
		return nil, err
	}
	defer release()
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	var batch types.SourceWikiBatch
	permission := source.SourcePermissionSQL(ctx, "source_wiki_batches.source_id", "NULL")
	if err = s.db.WithContext(ctx).Where("knowledge_base_id = ? AND tenant_id = ? AND source_id = ? AND id = ?", knowledgeBaseID, kb.TenantID, sourceID, batchID).
		Where(permission).Take(&batch).Error; err != nil {
		return nil, repository.ErrSourceWikiBatchNotFound
	}
	return &batch, nil
}

func (s *sourceWikiService) ListSourceWikiCoverage(ctx context.Context, knowledgeBaseID, sourceID string) ([]types.SourceWikiCoverageTopic, error) {
	if knowledgeBaseID == "" || sourceID == "" {
		return nil, fmt.Errorf("knowledge base and source are required")
	}
	ctx, release, err := beginSourceRead(ctx, s.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: knowledgeBaseID, SourceIDs: []string{sourceID},
	}})
	if err != nil {
		return nil, err
	}
	defer release()
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	var topics []types.SourceWikiCoverageTopic
	permission := source.SourcePermissionSQL(ctx, "source_wiki_topics.source_id", "NULL")
	err = s.db.WithContext(ctx).Where("knowledge_base_id = ? AND tenant_id = ? AND source_id = ?", knowledgeBaseID, kb.TenantID, sourceID).
		Where(permission).Order("priority DESC, topic_key ASC").Find(&topics).Error
	if err != nil {
		return nil, err
	}
	return topics, nil
}
