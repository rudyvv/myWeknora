package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
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

// GetSourceWikiCoverageSummary returns counts only when a completed plan holds
// an exact, complete inventory for the requested published snapshot. Any
// ambiguous or unknown state is omitted rather than inferred as zero.
func (s *sourceWikiService) GetSourceWikiCoverageSummary(ctx context.Context, knowledgeBaseID, sourceID, snapshotID string) (*types.SourceWikiCoverageTelemetry, error) {
	if knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return nil, fmt.Errorf("knowledge base, source, and snapshot are required")
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
	permission := source.SourcePermissionSQL(ctx, "source_wiki_update_plans.source_id", "NULL")
	var plan types.SourceWikiUpdatePlan
	err = s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND snapshot_id = ? AND status = ?",
		kb.TenantID, knowledgeBaseID, sourceID, snapshotID, "completed").
		Where(permission).Order("updated_at DESC, id DESC").Take(&plan).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	var inventory types.SourceWikiImpactTopicInventory
	if len(plan.NextInventory) == 0 || json.Unmarshal(plan.NextInventory, &inventory) != nil ||
		!inventory.Complete || inventory.TenantID != kb.TenantID || inventory.KnowledgeBaseID != knowledgeBaseID ||
		inventory.SourceID != sourceID || inventory.SnapshotID != snapshotID || inventory.ExpectedTopicCount < 0 ||
		inventory.ExpectedTopicCount != len(inventory.Topics) || inventory.ExpectedTopicCount > types.SourceWikiSkeletonMaxRelations {
		return nil, nil
	}
	expected := make(map[string]types.SourceWikiImpactTopicDependencies, len(inventory.Topics))
	for _, topic := range inventory.Topics {
		if topic.TopicKey == "" {
			return nil, nil
		}
		if _, duplicate := expected[topic.TopicKey]; duplicate {
			return nil, nil
		}
		expected[topic.TopicKey] = topic
	}

	coveragePermission := source.SourcePermissionSQL(ctx, "source_wiki_topics.source_id", "NULL")
	var topics []types.SourceWikiCoverageTopic
	if len(expected) > 0 {
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND topic_key IN ?",
			kb.TenantID, knowledgeBaseID, sourceID, sourceWikiSortedMapKeys(expected)).
			Where(coveragePermission).Find(&topics).Error; err != nil {
			return nil, err
		}
	}
	byKey := make(map[string]types.SourceWikiCoverageTopic, len(topics))
	for _, topic := range topics {
		if _, ok := expected[topic.TopicKey]; !ok {
			return nil, nil
		}
		if _, duplicate := byKey[topic.TopicKey]; duplicate {
			return nil, nil
		}
		byKey[topic.TopicKey] = topic
	}
	counts := types.SourceWikiCoverageTelemetry{}
	eligible, ready, stale, failed, ungenerated, deferred := int64(len(expected)), int64(0), int64(0), int64(0), int64(0), int64(0)
	for key := range expected {
		topic, exists := byKey[key]
		if !exists {
			ungenerated++
			continue
		}
		switch topic.Status {
		case "ready":
			if topic.LastReadySnapshotID == snapshotID {
				ready++
			} else {
				stale++
			}
		case "stale":
			stale++
		case "failed", "insufficient_evidence":
			failed++
		case "planned":
			if topic.Initial {
				ungenerated++
			} else {
				deferred++
			}
		case "expansion", "draft":
			deferred++
		default:
			return nil, nil
		}
	}
	counts.EligibleTopics, counts.ReadyTopics, counts.StaleTopics = &eligible, &ready, &stale
	counts.FailedTopics, counts.UngeneratedTopics, counts.DeferredTopics = &failed, &ungenerated, &deferred
	return &counts, nil
}

func sourceWikiSortedMapKeys(values map[string]types.SourceWikiImpactTopicDependencies) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
