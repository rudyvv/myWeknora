package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const sourceWikiUpdateDispatchBatchSize = types.SourceWikiBatchMaxInitialTopics

var ErrSourceWikiUpdateBaseVersionChanged = errors.New("source Wiki update target page version changed")

// dispatchPendingSourceWikiRegeneration durably hands at most one fixed-size
// slice of pending impact items to the existing epoch-fenced T17 batch runner.
func (s *sourceWikiService) dispatchPendingSourceWikiRegeneration(ctx context.Context, payload types.SourceWikiUpdatePayload) error {
	if s == nil || s.db == nil || s.kb == nil || s.models == nil || s.wiki == nil {
		return fmt.Errorf("source Wiki regeneration dispatcher is not configured")
	}
	var plan types.SourceWikiUpdatePlan
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND knowledge_base_id=? AND source_id=? AND snapshot_id=? AND config_generation=?",
		payload.TenantID, payload.KnowledgeBaseID, payload.DataSourceID, payload.SnapshotID, payload.ConfigGeneration).Take(&plan).Error; err != nil {
		return err
	}
	if plan.Status != "completed" {
		return nil
	}
	if plan.SourceWideStale {
		if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&types.SourceWikiUpdatePlanItem{}).Where("plan_id=? AND action IN ? AND state='pending'",
				plan.ID, []string{"regenerate", "skeleton"}).Updates(map[string]any{
				"state": "failed", "reason_code": "bounded_inventory_unavailable",
				"reason":     "The source-wide conservative fallback has no complete candidate inventory; automatic regeneration was not started.",
				"updated_at": time.Now().UTC(),
			}).Error; err != nil {
				return err
			}
			return nil
		}); err != nil {
			return err
		}
		return nil
	}
	if err := s.processPendingSourceWikiSkeleton(ctx, plan, payload); err != nil {
		return err
	}
	var pending []types.SourceWikiUpdatePlanItem
	if err := s.db.WithContext(ctx).Where("plan_id=? AND action='regenerate' AND state='pending'", plan.ID).
		Order("topic_key ASC").Limit(sourceWikiUpdateDispatchBatchSize + 1).Find(&pending).Error; err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	if len(pending) > sourceWikiUpdateDispatchBatchSize {
		pending = pending[:sourceWikiUpdateDispatchBatchSize]
	}
	eligible := pending[:0]
	for _, item := range pending {
		var details struct {
			PageCount int `json:"page_count"`
		}
		if len(item.Details) > 0 && json.Unmarshal(item.Details, &details) == nil && details.PageCount > 1 {
			if err := s.db.WithContext(ctx).Model(&types.SourceWikiUpdatePlanItem{}).Where("id=? AND state='pending'", item.ID).
				Updates(map[string]any{"state": "failed", "reason_code": "regeneration_page_ambiguous",
					"reason": "Multiple current contribution pages share this topic key; no unique page target can be regenerated safely.", "updated_at": time.Now().UTC()}).Error; err != nil {
				return err
			}
			continue
		}
		eligible = append(eligible, item)
	}
	pending = eligible
	if len(pending) == 0 {
		return nil
	}
	kb, err := s.kb.GetKnowledgeBaseByIDOnly(ctx, plan.KnowledgeBaseID)
	if err != nil {
		return err
	}
	taskCtx, err := access.WithKBTaskWrite(ctx, kb, plan.TenantID)
	if err != nil {
		return err
	}
	preflight, err := s.PreflightSourceWikiBatch(taskCtx, plan.KnowledgeBaseID, plan.SourceID, types.SourceWikiBatchPreflightRequest{})
	if err != nil {
		if errors.Is(err, repository.ErrSourceWikiBatchAlreadyActive) {
			return nil
		}
		if errors.Is(err, repository.ErrSourceWikiBatchInvalidState) || errors.Is(err, repository.ErrSourceWikiBatchNotFound) {
			return s.failPendingSourceWikiUpdateItems(ctx, plan.ID, pending, "regeneration_binding_changed", "The fixed source, snapshot, model, or candidate binding is no longer available.")
		}
		return err
	}
	if !preflight.PreflightPassed || !preflight.StartAvailable || !preflight.ModelContextKnown || len(preflight.PlannedTopics) == 0 {
		return s.failPendingSourceWikiUpdateItems(ctx, plan.ID, pending, "regeneration_preflight_rejected", "The current source publication cannot satisfy the bounded generation preflight.")
	}
	if preflight.SnapshotID != plan.SnapshotID {
		return s.supersedeSourceWikiUpdateItems(ctx, plan.ID, "regeneration_snapshot_changed")
	}
	byKey := make(map[string]types.SourceWikiTopic, len(preflight.PlannedTopics))
	for _, topic := range preflight.PlannedTopics {
		if topic.SourceID == plan.SourceID && topic.SnapshotID == plan.SnapshotID {
			byKey[topic.TopicKey] = topic
		}
	}
	selected := make([]types.SourceWikiTopic, 0, len(pending))
	selectedKeys := make(map[string]bool, len(pending))
	for _, item := range pending {
		topic, ok := byKey[item.TopicKey]
		if !ok {
			continue
		}
		topic.Status = "planned"
		selected = append(selected, topic)
		selectedKeys[item.TopicKey] = true
	}
	if len(selected) == 0 {
		return s.failPendingSourceWikiUpdateItems(ctx, plan.ID, pending, "regeneration_topic_not_in_snapshot", "The affected topic is not present in the current complete source candidate inventory.")
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].TopicKey < selected[j].TopicKey })
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := &types.SourceWikiBatch{
		ID: uuid.NewString(), TenantID: plan.TenantID, KnowledgeBaseID: plan.KnowledgeBaseID, SourceID: plan.SourceID,
		SnapshotID: preflight.SnapshotID, SourceConfigFingerprint: preflight.SourceConfigFingerprint,
		SourceUpdatedAt: preflight.SourceUpdatedAt, ModelID: preflight.ModelID,
		ModelSettingsFingerprint: preflight.ModelSettingsFingerprint, ModelContextWindow: preflight.ModelContextWindow,
		MaxCompletionTokens: preflight.MaxCompletionTokens, Status: "running", Phase: "skeleton",
		MaxCalls: preflight.MaxCalls, MaxTokens: preflight.MaxTokens, MaxElapsedMS: preflight.MaxElapsedMS,
		MaxInitialTopics: preflight.MaxInitialTopics, SkeletonMaxCalls: preflight.SkeletonMaxCalls,
		SkeletonMaxTokens: preflight.SkeletonMaxTokens, QAMaxCalls: preflight.QAMaxCalls,
		QAMaxTokens: preflight.QAMaxTokens, CreatedAt: now, UpdatedAt: now,
		DeadlineAt: now.Add(time.Duration(preflight.MaxElapsedMS) * time.Millisecond),
	}
	var batchID string
	err = s.db.WithContext(taskCtx).Transaction(func(tx *gorm.DB) error {
		var currentPlan types.SourceWikiUpdatePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", plan.ID).Take(&currentPlan).Error; err != nil {
			return err
		}
		if currentPlan.Status != "completed" || currentPlan.ConfigGeneration != payload.ConfigGeneration || currentPlan.SnapshotID != payload.SnapshotID {
			return nil
		}
		if err := validateSourceWikiUpdateFenceInTx(tx, payload, preflight); err != nil {
			return err
		}
		var currentItems []types.SourceWikiUpdatePlanItem
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("plan_id=? AND action='regenerate' AND state='pending' AND topic_key IN ?",
			plan.ID, keysOfSourceWikiUpdateItems(pending)).Order("topic_key ASC").Limit(sourceWikiUpdateDispatchBatchSize + 1).Find(&currentItems).Error; err != nil {
			return err
		}
		if len(currentItems) == 0 {
			return nil
		}
		currentTopicPlan := make([]types.SourceWikiTopic, 0, len(currentItems))
		for _, item := range currentItems {
			topic, ok := byKey[item.TopicKey]
			if !ok {
				continue
			}
			topic.Status = "planned"
			currentTopicPlan = append(currentTopicPlan, topic)
		}
		if len(currentTopicPlan) == 0 {
			return nil
		}
		batch.CreatedAt, batch.UpdatedAt, batch.DeadlineAt = now, now, now.Add(time.Duration(preflight.MaxElapsedMS)*time.Millisecond)
		ledger := repository.NewSourceWikiBatchLedger(tx)
		if err := ledger.CreateWithPlan(taskCtx, batch, currentTopicPlan, now); err != nil {
			return err
		}
		for _, item := range currentItems {
			if !selectedKeys[item.TopicKey] {
				continue
			}
			var details map[string]any
			if len(item.Details) > 0 && json.Unmarshal(item.Details, &details) != nil {
				details = map[string]any{}
			}
			if details == nil {
				details = map[string]any{}
			}
			details["generation_batch_id"] = batch.ID
			details["generation_snapshot_id"] = batch.SnapshotID
			encoded, err := json.Marshal(details)
			if err != nil {
				return err
			}
			result := tx.Model(&types.SourceWikiUpdatePlanItem{}).Where("id=? AND state='pending'", item.ID).Updates(map[string]any{
				"state": "running", "details": types.JSON(encoded), "updated_at": now,
			})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("source Wiki update item changed during generation handoff")
			}
		}
		batchID = batch.ID
		return nil
	})
	if err != nil {
		if errors.Is(err, repository.ErrSourceWikiBatchAlreadyActive) || sourceWikiBatchNowActive(ctx, s.db, plan) {
			return nil
		}
		return err
	}
	if batchID != "" {
		s.ResumeSourceWikiBatch(taskCtx, batchID)
	}
	return nil
}

func (s *sourceWikiService) processPendingSourceWikiSkeleton(ctx context.Context, plan types.SourceWikiUpdatePlan, payload types.SourceWikiUpdatePayload) error {
	var item types.SourceWikiUpdatePlanItem
	err := s.db.WithContext(ctx).Where("plan_id=? AND topic_key='source-skeleton' AND action='skeleton' AND state='pending'", plan.ID).Take(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	kb, err := s.kb.GetKnowledgeBaseByIDOnly(ctx, plan.KnowledgeBaseID)
	if err != nil {
		return err
	}
	taskCtx, err := access.WithKBTaskWrite(ctx, kb, plan.TenantID)
	if err != nil {
		return err
	}
	preflight, err := s.PreflightSourceWikiBatch(taskCtx, plan.KnowledgeBaseID, plan.SourceID, types.SourceWikiBatchPreflightRequest{})
	if err != nil {
		if errors.Is(err, repository.ErrSourceWikiBatchAlreadyActive) {
			return nil
		}
		if errors.Is(err, repository.ErrSourceWikiBatchInvalidState) || errors.Is(err, repository.ErrSourceWikiBatchNotFound) {
			return s.setSourceWikiUpdateItemState(ctx, item.ID, "failed", "skeleton_preflight_rejected", "The current source snapshot cannot produce a complete bounded topic inventory.")
		}
		return err
	}
	if !preflight.PreflightPassed || preflight.SnapshotID != plan.SnapshotID || preflight.CandidateCount != len(preflight.PlannedTopics) ||
		preflight.CandidateCount > types.SourceWikiBatchMaxCandidates {
		if preflight.SnapshotID != plan.SnapshotID {
			return s.supersedeSourceWikiUpdateItems(ctx, plan.ID, "skeleton_snapshot_changed")
		}
		return s.setSourceWikiUpdateItemState(ctx, item.ID, "failed", "skeleton_inventory_incomplete", "The source skeleton did not provide a complete inventory within the candidate bound.")
	}
	now := time.Now().UTC()
	return s.db.WithContext(taskCtx).Transaction(func(tx *gorm.DB) error {
		var currentPlan types.SourceWikiUpdatePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", plan.ID).Take(&currentPlan).Error; err != nil {
			return err
		}
		if currentPlan.Status != "completed" || currentPlan.SnapshotID != payload.SnapshotID || currentPlan.ConfigGeneration != payload.ConfigGeneration {
			return nil
		}
		if err := validateSourceWikiUpdateFenceInTx(tx, payload, preflight); err != nil {
			return err
		}
		for start := 0; start < len(preflight.PlannedTopics); start += 500 {
			end := min(start+500, len(preflight.PlannedTopics))
			rows := make([]types.SourceWikiCoverageTopic, 0, end-start)
			for _, topic := range preflight.PlannedTopics[start:end] {
				if topic.SourceID != plan.SourceID || topic.SnapshotID != plan.SnapshotID || topic.TopicKey == "" || topic.Title == "" ||
					(topic.Status != "planned" && topic.Status != "expansion") {
					return fmt.Errorf("source Wiki skeleton candidate identity or state is invalid")
				}
				uncertaintyReasons := topic.UncertaintyReasons
				if uncertaintyReasons == nil {
					uncertaintyReasons = []string{}
				}
				reasons, err := json.Marshal(uncertaintyReasons)
				if err != nil {
					return err
				}
				topicRelations := topic.Relations
				if topicRelations == nil {
					topicRelations = []types.SourceCodeRelation{}
				}
				relations, err := json.Marshal(topicRelations)
				if err != nil {
					return err
				}
				rows = append(rows, types.SourceWikiCoverageTopic{
					ID: uuid.NewString(), TenantID: plan.TenantID, KnowledgeBaseID: plan.KnowledgeBaseID,
					SourceID: topic.SourceID, TopicKey: topic.TopicKey, SnapshotID: topic.SnapshotID,
					Kind: topic.Kind, ModulePath: topic.ModulePath, Title: topic.Title, Priority: topic.Priority,
					Status: topic.Status, Initial: topic.Status == "planned", Uncertain: topic.Uncertain,
					UncertaintyReasons: types.JSON(reasons), Relations: types.JSON(relations),
					WikiSlug: sourceWikiAttemptSlug(topic.SourceID, topic.Kind, topic.TopicKey, topic.ModulePath), UpdatedAt: now,
				})
			}
			if len(rows) == 0 {
				continue
			}
			updates := map[string]any{
				"knowledge_base_id": gorm.Expr("EXCLUDED.knowledge_base_id"), "snapshot_id": gorm.Expr("EXCLUDED.snapshot_id"),
				"kind": gorm.Expr("EXCLUDED.kind"), "module_path": gorm.Expr("EXCLUDED.module_path"), "title": gorm.Expr("EXCLUDED.title"),
				"priority": gorm.Expr("EXCLUDED.priority"), "initial": gorm.Expr("EXCLUDED.initial"),
				"status":    gorm.Expr("CASE WHEN source_wiki_topics.status='ready' AND source_wiki_topics.last_ready_snapshot_id=EXCLUDED.snapshot_id THEN 'ready' ELSE EXCLUDED.status END"),
				"uncertain": gorm.Expr("EXCLUDED.uncertain"), "uncertainty_reasons": gorm.Expr("EXCLUDED.uncertainty_reasons"),
				"relations":  gorm.Expr("EXCLUDED.relations"),
				"batch_id":   gorm.Expr("CASE WHEN source_wiki_topics.status='ready' AND source_wiki_topics.last_ready_snapshot_id=EXCLUDED.snapshot_id THEN source_wiki_topics.batch_id ELSE NULL END"),
				"attempt_id": gorm.Expr("CASE WHEN source_wiki_topics.status='ready' AND source_wiki_topics.last_ready_snapshot_id=EXCLUDED.snapshot_id THEN source_wiki_topics.attempt_id ELSE NULL END"),
				"wiki_slug":  gorm.Expr("CASE WHEN source_wiki_topics.wiki_slug='' THEN EXCLUDED.wiki_slug ELSE source_wiki_topics.wiki_slug END"),
				"reason":     gorm.Expr("CASE WHEN source_wiki_topics.status='ready' AND source_wiki_topics.last_ready_snapshot_id=EXCLUDED.snapshot_id THEN source_wiki_topics.reason ELSE '' END"),
				"updated_at": gorm.Expr("EXCLUDED.updated_at"),
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_id"}, {Name: "topic_key"}}, DoUpdates: clause.Assignments(updates)}).Create(&rows).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&types.SourceWikiUpdatePlanItem{}).Where("id=? AND state='pending'", item.ID).Updates(map[string]any{
			"state": "completed", "reason_code": "skeleton_inventory_refreshed",
			"reason": "The complete bounded topic inventory for this published snapshot was persisted.", "updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return nil
		}
		return nil
	})
}

func (s *sourceWikiService) setSourceWikiUpdateItemState(ctx context.Context, itemID int64, state, reasonCode, reason string) error {
	return s.db.WithContext(ctx).Model(&types.SourceWikiUpdatePlanItem{}).Where("id=? AND state='pending'", itemID).
		Updates(map[string]any{"state": state, "reason_code": reasonCode, "reason": reason, "updated_at": time.Now().UTC()}).Error
}

func validateSourceWikiUpdateFenceInTx(tx *gorm.DB, payload types.SourceWikiUpdatePayload, preflight *types.SourceWikiBatchPreflight) error {
	if tx == nil || preflight == nil || payload.TenantID == 0 || payload.DataSourceID == "" || payload.KnowledgeBaseID == "" ||
		payload.SnapshotID == "" || payload.ConfigGeneration <= 0 {
		return fmt.Errorf("source Wiki update generation fence is incomplete")
	}
	var syncState struct {
		TenantID                 uint64 `gorm:"column:tenant_id"`
		ConfigGeneration         int64  `gorm:"column:config_generation"`
		LastSuccessfulSnapshotID string `gorm:"column:last_successful_snapshot_id"`
	}
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Table("source_sync_states").
		Select("tenant_id,config_generation,last_successful_snapshot_id").
		Where("data_source_id=? AND tenant_id=?", payload.DataSourceID, payload.TenantID).Take(&syncState).Error; err != nil {
		return err
	}
	var publication types.SourcePublication
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("data_source_id=? AND tenant_id=? AND knowledge_base_id=?",
		payload.DataSourceID, payload.TenantID, payload.KnowledgeBaseID).Take(&publication).Error; err != nil {
		return err
	}
	var snapshot types.SourceSnapshot
	if err := tx.Where("id=? AND data_source_id=? AND tenant_id=? AND knowledge_base_id=? AND state='published' AND manifest_complete=TRUE",
		payload.SnapshotID, payload.DataSourceID, payload.TenantID, payload.KnowledgeBaseID).Take(&snapshot).Error; err != nil {
		return err
	}
	var outbox struct {
		ID               string     `gorm:"column:id"`
		TenantID         uint64     `gorm:"column:tenant_id"`
		KnowledgeBaseID  string     `gorm:"column:knowledge_base_id"`
		DataSourceID     string     `gorm:"column:data_source_id"`
		SnapshotID       string     `gorm:"column:snapshot_id"`
		EventType        string     `gorm:"column:event_type"`
		ConfigGeneration int64      `gorm:"column:config_generation"`
		Payload          types.JSON `gorm:"column:payload"`
	}
	if err := tx.Table("source_publication_outbox").Where("id=?", payload.EventID).Take(&outbox).Error; err != nil {
		return err
	}
	var accepted types.SourceWikiUpdatePayload
	if json.Unmarshal(outbox.Payload, &accepted) != nil || accepted != payload || outbox.ID != payload.EventID ||
		outbox.TenantID != payload.TenantID || outbox.KnowledgeBaseID != payload.KnowledgeBaseID ||
		outbox.DataSourceID != payload.DataSourceID || outbox.SnapshotID != payload.SnapshotID ||
		outbox.EventType != "source.wiki.update" || outbox.ConfigGeneration != payload.ConfigGeneration {
		return fmt.Errorf("source Wiki update no longer matches its accepted publication event")
	}
	var currentSource types.DataSource
	if err := tx.Where("id=? AND tenant_id=? AND knowledge_base_id=? AND deleted_at IS NULL", payload.DataSourceID, payload.TenantID, payload.KnowledgeBaseID).
		Take(&currentSource).Error; err != nil {
		return err
	}
	if syncState.TenantID != payload.TenantID || syncState.ConfigGeneration != payload.ConfigGeneration ||
		syncState.LastSuccessfulSnapshotID != payload.SnapshotID || publication.SnapshotID != payload.SnapshotID ||
		preflight.SnapshotID != payload.SnapshotID || sourceWikiSourceFingerprint(&currentSource) != preflight.SourceConfigFingerprint ||
		!currentSource.UpdatedAt.Equal(preflight.SourceUpdatedAt) || payload.CommitSHA != "" && snapshot.CommitSHA != payload.CommitSHA {
		return repository.ErrSourceWikiBatchInvalidState
	}
	return nil
}

func sourceWikiBatchNowActive(ctx context.Context, db *gorm.DB, plan types.SourceWikiUpdatePlan) bool {
	var active types.SourceWikiBatch
	err := db.WithContext(ctx).Where("tenant_id=? AND knowledge_base_id=? AND source_id=? AND status IN ?",
		plan.TenantID, plan.KnowledgeBaseID, plan.SourceID, []string{"queued", "running"}).Take(&active).Error
	return err == nil
}

func (s *sourceWikiService) failPendingSourceWikiUpdateItems(ctx context.Context, planID string, items []types.SourceWikiUpdatePlanItem, reasonCode, reason string) error {
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	if len(ids) == 0 {
		return nil
	}
	return s.db.WithContext(ctx).Model(&types.SourceWikiUpdatePlanItem{}).Where("plan_id=? AND id IN ? AND state='pending'", planID, ids).
		Updates(map[string]any{"state": "failed", "reason_code": reasonCode, "reason": reason, "updated_at": time.Now().UTC()}).Error
}

func (s *sourceWikiService) validateSourceWikiUpdateGenerationBase(ctx context.Context, batchID, topicKey, snapshotID string,
	existing *types.WikiPage, baseVersion int) error {
	var item types.SourceWikiUpdatePlanItem
	err := s.db.WithContext(ctx).Where("state='running' AND topic_key=? AND details->>'generation_batch_id'=?", topicKey, batchID).Take(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var details struct {
		SnapshotID string `json:"generation_snapshot_id"`
	}
	if json.Unmarshal(item.Details, &details) != nil || details.SnapshotID != snapshotID {
		return ErrSourceWikiUpdateBaseVersionChanged
	}
	if item.PageID == nil {
		if existing != nil || item.ExpectedPageVersion != 0 || baseVersion != 0 {
			return ErrSourceWikiUpdateBaseVersionChanged
		}
		return nil
	}
	if existing == nil || existing.ID != *item.PageID || existing.Version != item.ExpectedPageVersion || baseVersion != item.ExpectedPageVersion {
		return ErrSourceWikiUpdateBaseVersionChanged
	}
	return nil
}

func (s *sourceWikiService) supersedeSourceWikiUpdateItems(ctx context.Context, planID, reason string) error {
	return s.db.WithContext(ctx).Model(&types.SourceWikiUpdatePlanItem{}).Where("plan_id=? AND action='regenerate' AND state IN ?", planID, []string{"pending", "running"}).
		Updates(map[string]any{"state": "superseded", "reason_code": reason, "reason": "A newer publication superseded this regeneration target.", "updated_at": time.Now().UTC()}).Error
}

func keysOfSourceWikiUpdateItems(items []types.SourceWikiUpdatePlanItem) []string {
	keys := make([]string, 0, len(items))
	for _, item := range items {
		keys = append(keys, item.TopicKey)
	}
	return keys
}

// ResumePendingSourceWikiUpdates is called by lifecycle recovery. It repairs
// terminal batch/item links first, then dispatches a bounded number of pending
// plans so a crash between impact persistence and T17 handoff is recoverable.
func (s *sourceWikiService) ResumePendingSourceWikiUpdates(ctx context.Context) error {
	if s == nil || s.db == nil {
		return nil
	}
	var batchIDs []string
	if err := s.db.WithContext(ctx).Table("source_wiki_update_plan_items").Distinct("details->>'generation_batch_id'").
		Where("state='running' AND details->>'generation_batch_id' IS NOT NULL AND details->>'generation_batch_id'<>''").
		Limit(sourceWikiUpdateRecoveryPlanLimit).Pluck("details->>'generation_batch_id'", &batchIDs).Error; err != nil {
		return err
	}
	for _, batchID := range batchIDs {
		if err := s.reconcileSourceWikiUpdateBatch(ctx, batchID); err != nil {
			return err
		}
	}
	type pendingPlan struct {
		Payload types.JSON `gorm:"column:payload"`
	}
	var plans []pendingPlan
	err := s.db.WithContext(ctx).Table("source_wiki_update_plans p").
		Select("o.payload").
		Joins("JOIN source_publication_outbox o ON o.tenant_id=p.tenant_id AND o.knowledge_base_id=p.knowledge_base_id AND o.data_source_id=p.source_id AND o.snapshot_id=p.snapshot_id AND o.config_generation=p.config_generation AND o.event_type='source.wiki.update'").
		Where("p.status='completed' AND EXISTS (SELECT 1 FROM source_wiki_update_plan_items i WHERE i.plan_id=p.id AND i.action='regenerate' AND i.state='pending')").
		Order("p.updated_at ASC, p.id ASC").Limit(sourceWikiUpdateRecoveryPlanLimit).Scan(&plans).Error
	if err != nil {
		return err
	}
	for _, pending := range plans {
		var payload types.SourceWikiUpdatePayload
		if err := json.Unmarshal(pending.Payload, &payload); err != nil {
			return fmt.Errorf("decode recovered source Wiki update payload: %w", err)
		}
		if payload.SchemaVersion != 1 || payload.EventID == "" || payload.TenantID == 0 || payload.KnowledgeBaseID == "" ||
			payload.DataSourceID == "" || payload.SnapshotID == "" || payload.ConfigGeneration <= 0 {
			return fmt.Errorf("recovered source Wiki update payload identity is incomplete")
		}
		ctx := types.WithExecutionTenant(ctx, payload.TenantID)
		if err := s.dispatchPendingSourceWikiRegeneration(ctx, payload); err != nil {
			return err
		}
	}
	return nil
}

const sourceWikiUpdateRecoveryPlanLimit = 8

func (s *sourceWikiService) reconcileSourceWikiUpdateBatch(ctx context.Context, batchID string) error {
	var batch types.SourceWikiBatch
	if err := s.db.WithContext(ctx).Where("id=?", batchID).Take(&batch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if batch.Status == "running" || batch.Status == "queued" {
		return nil
	}
	var items []types.SourceWikiUpdatePlanItem
	if err := s.db.WithContext(ctx).Where("state='running' AND details->>'generation_batch_id'=?", batchID).Order("plan_id ASC, topic_key ASC").Find(&items).Error; err != nil {
		return err
	}
	for _, item := range items {
		var topic types.SourceWikiCoverageTopic
		err := s.db.WithContext(ctx).Where("batch_id=? AND topic_key=? AND source_id=? AND snapshot_id=?", batch.ID, item.TopicKey, batch.SourceID, batch.SnapshotID).Take(&topic).Error
		completed := err == nil && topic.Status == "ready" && topic.LastReadySnapshotID == batch.SnapshotID
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		state, reasonCode, reason := "failed", "regeneration_batch_failed", batch.Reason
		if completed {
			state, reasonCode, reason = "completed", "regeneration_completed", "The fixed-snapshot generation batch published this affected topic."
		} else if reason == "" {
			reason = "The bounded generation batch ended without publishing this topic; its previous contribution remains stale."
		}
		result := s.db.WithContext(ctx).Model(&types.SourceWikiUpdatePlanItem{}).Where("id=? AND state='running' AND details->>'generation_batch_id'=?", item.ID, batchID).
			Updates(map[string]any{"state": state, "reason_code": reasonCode, "reason": reason, "updated_at": time.Now().UTC()})
		if result.Error != nil {
			return result.Error
		}
	}
	return nil
}

var _ interface{ ResumePendingSourceWikiUpdates(context.Context) error } = (*sourceWikiService)(nil)
