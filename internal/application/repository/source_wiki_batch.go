package repository

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSourceWikiBatchNotFound         = errors.New("source Wiki batch not found")
	ErrSourceWikiBatchDeadline         = errors.New("source Wiki batch deadline exceeded")
	ErrSourceWikiBatchBudgetExhausted  = errors.New("source Wiki batch budget exhausted")
	ErrSourceWikiBatchInvalidState     = errors.New("invalid source Wiki batch state")
	ErrSourceWikiBatchAlreadyActive    = errors.New("a source Wiki batch is already active")
	ErrSourceWikiBatchQACursorChanged  = errors.New("source Wiki batch QA cursor changed")
	ErrSourceWikiBatchQATimeout        = errors.New("source Wiki batch QA deadline exceeded")
	ErrSourceWikiDerivationDeferred    = errors.New("source Wiki derivation deferred by capacity")
	ErrSourceWikiDerivationUnavailable = errors.New("source Wiki derivation is unavailable")
)

// SourceWikiBatchLedger owns the durable batch parent, stable topic coverage,
// and parent-level call reservations. Child integrations must call
// ReserveCallInTx before locking/updating the T17 child attempt in the same
// transaction; this establishes the single parent->child lock order.
type SourceWikiBatchLedger struct {
	db *gorm.DB
}

func NewSourceWikiBatchLedger(db *gorm.DB) *SourceWikiBatchLedger {
	return &SourceWikiBatchLedger{db: db}
}

func (l *SourceWikiBatchLedger) Create(ctx context.Context, batch *types.SourceWikiBatch) error {
	if l == nil || l.db == nil || !validSourceWikiBatch(batch) {
		return fmt.Errorf("%w: invalid immutable batch target or limits", ErrSourceWikiBatchInvalidState)
	}
	return l.db.WithContext(ctx).Create(batch).Error
}

// CreateWithPlan binds the immutable parent and its entire server-generated
// candidate set in one transaction. A process crash cannot leave a live batch
// without its frozen coverage inventory, or publish a partial candidate plan.
func (l *SourceWikiBatchLedger) CreateWithPlan(ctx context.Context, batch *types.SourceWikiBatch, topics []types.SourceWikiTopic, now time.Time) error {
	if l == nil || l.db == nil || !validSourceWikiBatch(batch) || now.IsZero() || len(topics) == 0 {
		return fmt.Errorf("%w: batch start requires a valid immutable parent and complete plan", ErrSourceWikiBatchInvalidState)
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		transactional := NewSourceWikiBatchLedger(tx)
		if err := transactional.Create(ctx, batch); err != nil {
			return err
		}
		return transactional.SavePlan(ctx, batch.ID, topics, now)
	})
}

func validSourceWikiBatch(batch *types.SourceWikiBatch) bool {
	if batch == nil || batch.ID == "" || batch.TenantID == 0 || batch.KnowledgeBaseID == "" || batch.SourceID == "" ||
		batch.SnapshotID == "" || batch.SourceConfigFingerprint == "" || batch.SourceUpdatedAt.IsZero() || batch.ModelID == "" ||
		batch.ModelSettingsFingerprint == "" || batch.ModelContextWindow < 0 || batch.MaxCompletionTokens <= 0 ||
		batch.MaxCompletionTokens > types.SourceWikiBatchMaxCompletionTokens || batch.Status != "running" || batch.Phase != "skeleton" ||
		batch.Cursor != 0 || batch.CandidateCount != 0 || batch.InitialCount != 0 || batch.CallsReserved != 0 || batch.TokensReserved != 0 ||
		batch.SkeletonCallsReserved != 0 || batch.SkeletonTokensReserved != 0 || batch.QACallsReserved != 0 || batch.QATokensReserved != 0 ||
		batch.MaxCalls <= 0 || batch.MaxCalls > types.SourceWikiBatchMaxCalls || batch.MaxTokens <= 0 || batch.MaxTokens > types.SourceWikiBatchMaxTokens ||
		batch.MaxElapsedMS <= 0 || batch.MaxElapsedMS > types.SourceWikiBatchMaxElapsed.Milliseconds() ||
		batch.MaxInitialTopics <= 0 || batch.MaxInitialTopics > types.SourceWikiBatchMaxInitialTopics ||
		batch.SkeletonMaxCalls < 0 || batch.SkeletonMaxCalls > types.SourceWikiBatchSkeletonMaxCalls ||
		batch.SkeletonMaxTokens < 0 || batch.SkeletonMaxTokens > types.SourceWikiBatchSkeletonMaxTokens ||
		batch.QAMaxCalls < 0 || batch.QAMaxCalls > types.SourceWikiBatchQAMaxCalls ||
		batch.QAMaxTokens < 0 || batch.QAMaxTokens > types.SourceWikiBatchQAMaxTokens || batch.CreatedAt.IsZero() || batch.UpdatedAt.IsZero() || batch.DeadlineAt.IsZero() ||
		!batch.DeadlineAt.Equal(batch.CreatedAt.Add(time.Duration(batch.MaxElapsedMS)*time.Millisecond)) {
		return false
	}
	return true
}

// SavePlan persists the complete candidate plan atomically with its fixed
// parent snapshot. Ready cards retain their attempt link only while they
// remain current for that snapshot; stale or failed cards start without an
// attempt in the new batch.
func (l *SourceWikiBatchLedger) SavePlan(ctx context.Context, batchID string, topics []types.SourceWikiTopic, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || now.IsZero() {
		return fmt.Errorf("%w: missing batch, topic plan, or timestamp", ErrSourceWikiBatchInvalidState)
	}
	deadlineReached := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		if batch.Status != "running" || batch.Phase != "skeleton" {
			return ErrSourceWikiBatchInvalidState
		}
		if !now.Before(batch.DeadlineAt) {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
			return nil
		}
		if len(topics) == 0 || len(topics) > types.SourceWikiBatchMaxCandidates {
			return fmt.Errorf("%w: candidate plan size is outside the supported range", ErrSourceWikiBatchInvalidState)
		}
		planned, seen := 0, make(map[string]bool, len(topics))
		for _, topic := range topics {
			if topic.SourceID != batch.SourceID || topic.SnapshotID != batch.SnapshotID || topic.TopicKey == "" || topic.Title == "" || seen[topic.TopicKey] ||
				(topic.Status != "planned" && topic.Status != "expansion") || (topic.Kind != "system" && topic.Kind != "module" && topic.Kind != "flow") {
				return fmt.Errorf("%w: topic identity does not match the fixed batch snapshot", ErrSourceWikiBatchInvalidState)
			}
			seen[topic.TopicKey] = true
			if topic.Status == "planned" {
				planned++
			}
			reasonValues := topic.UncertaintyReasons
			if reasonValues == nil {
				reasonValues = []string{}
			}
			relationValues := topic.Relations
			if relationValues == nil {
				relationValues = []types.SourceCodeRelation{}
			}
			reasons, err := json.Marshal(reasonValues)
			if err != nil {
				return err
			}
			relations, err := json.Marshal(relationValues)
			if err != nil {
				return err
			}
			row := types.SourceWikiCoverageTopic{
				ID: uuid.NewString(), TenantID: batch.TenantID, KnowledgeBaseID: batch.KnowledgeBaseID,
				SourceID: topic.SourceID, TopicKey: topic.TopicKey, SnapshotID: topic.SnapshotID,
				Kind: topic.Kind, ModulePath: topic.ModulePath, Title: topic.Title, Priority: topic.Priority,
				Status: topic.Status, Uncertain: topic.Uncertain, UncertaintyReasons: types.JSON(reasons), Relations: types.JSON(relations),
				Initial: topic.Status == "planned",
				BatchID: &batch.ID, WikiSlug: sourceWikiTopicSlug(topic), UpdatedAt: now,
			}
			updates := map[string]any{
				"knowledge_base_id": row.KnowledgeBaseID, "snapshot_id": row.SnapshotID, "kind": row.Kind,
				"module_path": row.ModulePath, "title": row.Title, "priority": row.Priority,
				"initial":    row.Initial,
				"status":     gorm.Expr("CASE WHEN source_wiki_topics.status = 'ready' AND source_wiki_topics.last_ready_snapshot_id = EXCLUDED.snapshot_id THEN 'ready' ELSE EXCLUDED.status END"),
				"attempt_id": gorm.Expr("CASE WHEN source_wiki_topics.status = 'ready' AND source_wiki_topics.last_ready_snapshot_id = EXCLUDED.snapshot_id THEN source_wiki_topics.attempt_id ELSE NULL END"),
				"uncertain":  row.Uncertain, "uncertainty_reasons": row.UncertaintyReasons, "relations": row.Relations,
				"batch_id": row.BatchID, "wiki_slug": gorm.Expr("CASE WHEN source_wiki_topics.wiki_slug = '' THEN EXCLUDED.wiki_slug ELSE source_wiki_topics.wiki_slug END"),
				"reason":     gorm.Expr("CASE WHEN source_wiki_topics.status = 'ready' AND source_wiki_topics.last_ready_snapshot_id = EXCLUDED.snapshot_id THEN source_wiki_topics.reason ELSE '' END"),
				"updated_at": row.UpdatedAt,
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "source_id"}, {Name: "topic_key"}}, DoUpdates: clause.Assignments(updates)}).Create(&row).Error; err != nil {
				return err
			}
		}
		if planned > batch.MaxInitialTopics {
			return fmt.Errorf("%w: initial topic count exceeds the frozen cap", ErrSourceWikiBatchInvalidState)
		}
		return tx.Model(&batch).Updates(map[string]any{
			"phase": "cards", "candidate_count": len(topics), "initial_count": planned,
			"cursor": 0, "current_topic_key": "", "updated_at": now,
		}).Error
	})
	if err != nil {
		return err
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	return nil
}

// UpdateProgress advances a durable batch without changing its absolute
// deadline or any reserved budget. Terminal failures retain their draft and
// reason for inspection; a later manual retry must create another batch.
func (l *SourceWikiBatchLedger) UpdateProgress(ctx context.Context, batchID, phase, status, currentTopicKey, reason string, cursor int, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || now.IsZero() || cursor < 0 ||
		(phase != "skeleton" && phase != "cards" && phase != "batch_qa" && phase != "publishing" && phase != "finished") ||
		(status != "running" && status != "failed") {
		return fmt.Errorf("%w: invalid batch progress", ErrSourceWikiBatchInvalidState)
	}
	deadlineReached := false
	qaDeadlineReached := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		if batch.Status != "running" || cursor < batch.Cursor || cursor > batch.InitialCount || sourceWikiBatchPhaseOrder(phase) < sourceWikiBatchPhaseOrder(batch.Phase) {
			return ErrSourceWikiBatchInvalidState
		}
		if !now.Before(batch.DeadlineAt) {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
			return nil
		}
		if batch.Phase == "batch_qa" && batch.QADueAt != nil && !now.Before(*batch.QADueAt) {
			if err := failSourceWikiBatchInTx(tx, &batch, "batch QA phase deadline exhausted", now); err != nil {
				return err
			}
			qaDeadlineReached = true
			return nil
		}
		if status == "failed" && reason == "" {
			return ErrSourceWikiBatchInvalidState
		}
		if status == "failed" {
			return failSourceWikiBatchInTx(tx, &batch, reason, now)
		}
		updates := map[string]any{
			"phase": phase, "status": status, "cursor": cursor,
			"current_topic_key": currentTopicKey, "reason": reason, "updated_at": now,
		}
		if phase == "batch_qa" && batch.QADueAt == nil {
			qaDueAt := now.Add(types.SourceWikiBatchQAMaxElapsed)
			if qaDueAt.After(batch.DeadlineAt) {
				qaDueAt = batch.DeadlineAt
			}
			updates["qa_deadline_at"] = qaDueAt
		}
		if status != "running" {
			updates["finished_at"] = now
		}
		return tx.Model(&batch).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	if qaDeadlineReached {
		return ErrSourceWikiBatchQATimeout
	}
	return nil
}

func sourceWikiBatchPhaseOrder(phase string) int {
	switch phase {
	case "skeleton":
		return 0
	case "cards":
		return 1
	case "batch_qa":
		return 2
	case "publishing":
		return 3
	case "finished":
		return 4
	default:
		return -1
	}
}

func expireSourceWikiBatchInTx(tx *gorm.DB, batch *types.SourceWikiBatch, now time.Time) error {
	const reason = "batch absolute time budget exhausted"
	if err := tx.Model(batch).Updates(map[string]any{
		"status": "expired", "phase": "finished", "reason": reason,
		"finished_at": now, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := failRunningBatchChildrenInTx(tx, batch.ID, reason, now); err != nil {
		return err
	}
	return tx.Model(&types.SourceWikiCoverageTopic{}).
		Where("batch_id = ? AND status = 'planned'", batch.ID).
		Updates(map[string]any{"status": "failed", "reason": reason, "updated_at": now}).Error
}

func failRunningBatchChildrenInTx(tx *gorm.DB, batchID, reason string, now time.Time) error {
	if err := tx.Model(&types.SourceWikiAttempt{}).
		Where("batch_id = ? AND status IN ('running', 'staged')", batchID).
		Updates(map[string]any{"status": "failed", "reason": reason, "lease_owner": "", "lease_expires_at": nil, "updated_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Model(&types.SourceWikiAttemptCall{}).
		Where("attempt_id IN (SELECT id FROM source_wiki_attempts WHERE batch_id = ?) AND outcome = 'reserved'", batchID).
		Updates(map[string]any{"outcome": "unknown", "completed_at": now}).Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM source_wiki_attempt_evidence_refs WHERE attempt_id IN (SELECT id FROM source_wiki_attempts WHERE batch_id = ?)", batchID).Error; err != nil {
		return err
	}
	return tx.Model(&types.SourceWikiBatchCallReservation{}).
		Where("batch_id = ? AND outcome = 'reserved'", batchID).
		Updates(map[string]any{"outcome": "unknown", "completed_at": now}).Error
}

func failSourceWikiBatchInTx(tx *gorm.DB, batch *types.SourceWikiBatch, reason string, now time.Time) error {
	if err := tx.Model(batch).Updates(map[string]any{
		"status": "failed", "phase": "finished", "reason": reason, "finished_at": now, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := failRunningBatchChildrenInTx(tx, batch.ID, reason, now); err != nil {
		return err
	}
	return tx.Model(&types.SourceWikiCoverageTopic{}).
		Where("batch_id = ? AND status IN ('planned', 'draft')", batch.ID).
		Updates(map[string]any{"status": "failed", "reason": reason, "updated_at": now}).Error
}

// Expire records an absolute-deadline transition once, marks unanswered
// reservations unknown without refunding them, and retains the reason on
// topics that had not started. Draft and ready states are left intact.
func (l *SourceWikiBatchLedger) Expire(ctx context.Context, batchID string, now time.Time) (bool, error) {
	if l == nil || l.db == nil || batchID == "" || now.IsZero() {
		return false, fmt.Errorf("%w: invalid expiration request", ErrSourceWikiBatchInvalidState)
	}
	expired := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		if batch.Status != "running" || now.Before(batch.DeadlineAt) {
			return nil
		}
		if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
			return err
		}
		expired = true
		return nil
	})
	return expired, err
}

// UpdateTopic records card state only for the topic and fixed snapshot owned
// by this batch. It locks the parent first so it composes with provider-call
// reservation and recovery using one lock order.
func (l *SourceWikiBatchLedger) UpdateTopic(ctx context.Context, batchID, topicKey, status, attemptID, reason, readySnapshotID string, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || topicKey == "" || now.IsZero() ||
		(status != "planned" && status != "ready" && status != "draft" && status != "failed" && status != "insufficient_evidence" && status != "expansion") ||
		(status == "ready" && readySnapshotID == "") || ((status == "failed" || status == "insufficient_evidence") && reason == "") {
		return fmt.Errorf("%w: invalid topic state update", ErrSourceWikiBatchInvalidState)
	}
	deadlineReached := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		if !now.Before(batch.DeadlineAt) && batch.Status == "running" {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
			return nil
		}
		if batch.Status != "running" {
			return ErrSourceWikiBatchInvalidState
		}
		var topic types.SourceWikiCoverageTopic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("source_id = ? AND topic_key = ? AND batch_id = ?", batch.SourceID, topicKey, batch.ID).Take(&topic).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchInvalidState
			}
			return err
		}
		if topic.SnapshotID != batch.SnapshotID || status == "ready" && readySnapshotID != batch.SnapshotID {
			return ErrSourceWikiBatchInvalidState
		}
		updates := map[string]any{"status": status, "reason": reason, "updated_at": now}
		if attemptID != "" {
			updates["attempt_id"] = attemptID
		}
		if status == "ready" {
			updates["last_ready_snapshot_id"] = readySnapshotID
			if topic.WikiSlug == "" {
				return ErrSourceWikiBatchInvalidState
			}
		}
		return tx.Model(&topic).Updates(updates).Error
	})
	if err != nil {
		return err
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	return nil
}

func sourceWikiTopicSlug(topic types.SourceWikiTopic) string {
	if topic.Kind == "module" && topic.ModulePath != "" {
		return sourceWikiModuleSlug(topic.SourceID, topic.ModulePath)
	}
	hash := sha256.Sum256([]byte(topic.TopicKey))
	return "concept/source-" + topic.SourceID + "/topic-" + hex.EncodeToString(hash[:8])
}

func sourceWikiModuleSlug(sourceID, modulePath string) string {
	// Keep the pre-existing module page identity so old directory cards and
	// their inbound links remain usable after adding topic coverage.
	hash := sha256.Sum256([]byte(modulePath))
	return "concept/source-" + sourceID + "/module-" + hex.EncodeToString(hash[:8])
}

func (l *SourceWikiBatchLedger) Get(ctx context.Context, knowledgeBaseID, batchID string) (*types.SourceWikiBatch, error) {
	var batch types.SourceWikiBatch
	if err := l.db.WithContext(ctx).Where("knowledge_base_id = ? AND id = ?", knowledgeBaseID, batchID).Take(&batch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSourceWikiBatchNotFound
		}
		return nil, err
	}
	return &batch, nil
}

func (l *SourceWikiBatchLedger) List(ctx context.Context, knowledgeBaseID, sourceID string, limit int) ([]types.SourceWikiBatch, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	var batches []types.SourceWikiBatch
	if err := l.db.WithContext(ctx).Where("knowledge_base_id = ? AND source_id = ?", knowledgeBaseID, sourceID).
		Order("created_at DESC, id DESC").Limit(limit).Find(&batches).Error; err != nil {
		return nil, err
	}
	return batches, nil
}

func (l *SourceWikiBatchLedger) Coverage(ctx context.Context, knowledgeBaseID, sourceID string) ([]types.SourceWikiCoverageTopic, error) {
	var topics []types.SourceWikiCoverageTopic
	if err := l.db.WithContext(ctx).Where("knowledge_base_id = ? AND source_id = ?", knowledgeBaseID, sourceID).
		Order("priority DESC, topic_key ASC").Find(&topics).Error; err != nil {
		return nil, err
	}
	return topics, nil
}

// CandidateDigest binds the immutable candidate set and every staged draft to
// QA approval. It intentionally excludes mutable child/topic statuses so the
// digest remains stable as already-approved pages are published.
func (l *SourceWikiBatchLedger) CandidateDigest(ctx context.Context, batchID string) (string, error) {
	if l == nil || l.db == nil || batchID == "" {
		return "", ErrSourceWikiBatchInvalidState
	}
	var batch types.SourceWikiBatch
	if err := l.db.WithContext(ctx).Where("id = ?", batchID).Take(&batch).Error; err != nil {
		return "", err
	}
	return SourceWikiBatchCandidateDigestInTx(l.db.WithContext(ctx), &batch)
}

type sourceWikiBatchDigestCandidate struct {
	TopicID, TopicKey, Kind, ModulePath, Title, WikiSlug, SnapshotID string
	Priority                                                         int
	Uncertain                                                        bool
	UncertaintyReasons                                               json.RawMessage
	Relations                                                        json.RawMessage
	AttemptID                                                        string
	DraftSHA256, CheckpointSHA256                                    string
	BasePageVersion, StagedPageVersion                               int
	ExistingPageSHA256                                               string
}

func SourceWikiBatchCandidateDigestInTx(tx *gorm.DB, batch *types.SourceWikiBatch) (string, error) {
	if tx == nil || batch == nil || batch.ID == "" {
		return "", ErrSourceWikiBatchInvalidState
	}
	var topics []types.SourceWikiCoverageTopic
	if err := tx.Where("batch_id = ? AND initial = TRUE", batch.ID).
		Order("priority DESC, topic_key ASC").Find(&topics).Error; err != nil {
		return "", err
	}
	if len(topics) != batch.InitialCount || len(topics) == 0 {
		return "", ErrSourceWikiBatchInvalidState
	}
	candidates := make([]sourceWikiBatchDigestCandidate, 0, len(topics))
	for _, topic := range topics {
		candidate := sourceWikiBatchDigestCandidate{
			TopicID: topic.ID, TopicKey: topic.TopicKey, Kind: topic.Kind, ModulePath: topic.ModulePath,
			Title: topic.Title, WikiSlug: topic.WikiSlug, SnapshotID: topic.SnapshotID, Priority: topic.Priority,
			Uncertain: topic.Uncertain, UncertaintyReasons: append(json.RawMessage(nil), topic.UncertaintyReasons...),
			Relations: append(json.RawMessage(nil), topic.Relations...),
		}
		if topic.AttemptID != nil && *topic.AttemptID != "" {
			var attempt types.SourceWikiAttempt
			if err := tx.Where("id = ?", *topic.AttemptID).Take(&attempt).Error; err == nil && attempt.BatchID == batch.ID {
				if attempt.TenantID != batch.TenantID || attempt.KnowledgeBaseID != batch.KnowledgeBaseID ||
					attempt.SnapshotID != batch.SnapshotID || attempt.SourceID != batch.SourceID ||
					attempt.TopicKind != topic.Kind || attempt.TopicKey != topic.TopicKey || attempt.Title != topic.Title ||
					attempt.ModulePath != topic.ModulePath || attempt.Slug != topic.WikiSlug ||
					(attempt.Status != "staged" && attempt.Status != "ready") || len(attempt.Draft) == 0 || len(attempt.Checkpoint) == 0 {
					return "", ErrSourceWikiBatchInvalidState
				}
				if attempt.Status == "staged" {
					var evidenceOwnerCount int64
					if err := tx.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", attempt.ID).Count(&evidenceOwnerCount).Error; err != nil {
						return "", err
					}
					if evidenceOwnerCount == 0 {
						return "", ErrSourceWikiBatchInvalidState
					}
				}
				candidate.AttemptID = attempt.ID
				candidate.DraftSHA256 = sourceWikiBatchHash(attempt.Draft)
				candidate.CheckpointSHA256 = sourceWikiBatchHash(attempt.Checkpoint)
				candidate.BasePageVersion = attempt.BasePageVersion
				candidate.StagedPageVersion = attempt.StagedPageVersion
				candidates = append(candidates, candidate)
				continue
			} else if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return "", err
			}
		}
		if topic.Status != "ready" || topic.LastReadySnapshotID != batch.SnapshotID || topic.WikiSlug == "" {
			return "", ErrSourceWikiBatchInvalidState
		}
		var page types.WikiPage
		if err := tx.Where("knowledge_base_id = ? AND slug = ?", batch.KnowledgeBaseID, topic.WikiSlug).Take(&page).Error; err != nil {
			return "", err
		}
		pageBytes, err := json.Marshal(struct {
			ID, Slug, Title, Summary, Content, Status string
			Version                                   int
			Provenance                                *types.SourceWikiProvenance
		}{page.ID, page.Slug, page.Title, page.Summary, page.Content, page.Status, page.Version, page.SourceProvenance})
		if err != nil {
			return "", err
		}
		candidate.ExistingPageSHA256 = sourceWikiBatchHash(pageBytes)
		candidate.BasePageVersion = page.Version
		candidates = append(candidates, candidate)
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return "", err
	}
	return sourceWikiBatchHash(encoded), nil
}

func sourceWikiBatchHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

type SourceWikiBatchQATopicResult struct {
	TopicKey string
	Ready    bool
	Reason   string
}

// CompleteBatchQACall atomically settles one local QA group and advances only
// the QA cursor. Passing a local group never changes coverage to ready and
// never publishes a page; the later whole-candidate check approves publication.
func (l *SourceWikiBatchLedger) CompleteBatchQACall(ctx context.Context, batchID, reservationID string, actualTokens *int, results []SourceWikiBatchQATopicResult, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || reservationID == "" || now.IsZero() || len(results) == 0 || len(results) > 4 {
		return fmt.Errorf("%w: invalid batch QA settlement", ErrSourceWikiBatchInvalidState)
	}
	deadlineReached := false
	qaDeadlineReached := false
	cursorChanged := false
	transactionErr := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		if batch.Status != "running" || batch.Phase != "batch_qa" {
			return ErrSourceWikiBatchInvalidState
		}
		if !now.Before(batch.DeadlineAt) {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
			return nil
		}
		if batch.QADueAt == nil || !now.Before(*batch.QADueAt) {
			if batch.QADueAt == nil {
				return ErrSourceWikiBatchInvalidState
			}
			if err := failSourceWikiBatchInTx(tx, &batch, "batch QA phase deadline exhausted", now); err != nil {
				return err
			}
			qaDeadlineReached = true
			return nil
		}
		var reservation types.SourceWikiBatchCallReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND batch_id = ? AND phase = 'batch_qa'", reservationID, batch.ID).Take(&reservation).Error; err != nil {
			return ErrSourceWikiBatchInvalidState
		}
		if reservation.ExpectedQACursor == nil || *reservation.ExpectedQACursor < 0 {
			return ErrSourceWikiBatchInvalidState
		}
		if err := l.RecordCallInTx(tx, reservation.ID, "succeeded", actualTokens, now); err != nil {
			return err
		}
		if batch.Phase != "batch_qa" {
			cursorChanged = true
			return nil
		}
		if *reservation.ExpectedQACursor != batch.QACursor {
			cursorChanged = true
			return nil
		}
		groupSize := batch.InitialCount - batch.QACursor
		if groupSize > 4 {
			groupSize = 4
		}
		if groupSize <= 0 || len(results) != groupSize {
			return ErrSourceWikiBatchInvalidState
		}
		seen := make(map[string]bool, len(results))
		for _, result := range results {
			if result.TopicKey == "" || seen[result.TopicKey] || (!result.Ready && result.Reason == "") {
				return ErrSourceWikiBatchInvalidState
			}
			seen[result.TopicKey] = true
		}
		var expectedTopics []types.SourceWikiCoverageTopic
		if err := tx.Where("batch_id = ? AND initial = TRUE", batch.ID).
			Order("priority DESC, topic_key ASC").Offset(batch.QACursor).Limit(len(results)).Find(&expectedTopics).Error; err != nil {
			return err
		}
		if len(expectedTopics) != len(results) {
			return ErrSourceWikiBatchInvalidState
		}
		for i, result := range results {
			if expectedTopics[i].TopicKey != result.TopicKey {
				return ErrSourceWikiBatchInvalidState
			}
		}
		nextCursor := batch.QACursor + len(results)
		if nextCursor > batch.InitialCount {
			return ErrSourceWikiBatchInvalidState
		}
		for _, result := range results {
			if !result.Ready {
				return failSourceWikiBatchInTx(tx, &batch, "whole-batch QA rejected one or more cards", now)
			}
		}
		return tx.Model(&batch).Updates(map[string]any{"qa_cursor": nextCursor, "updated_at": now}).Error
	})
	if transactionErr != nil {
		return transactionErr
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	if qaDeadlineReached {
		return ErrSourceWikiBatchQATimeout
	}
	if cursorChanged {
		return ErrSourceWikiBatchQACursorChanged
	}
	return nil
}

// CompleteBatchConsistencyCall records the full-candidate QA result and binds
// approval to the exact durable candidate digest before entering publishing.
func (l *SourceWikiBatchLedger) CompleteBatchConsistencyCall(ctx context.Context, batchID, reservationID, digest string, actualTokens *int, supported bool, reason string, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || reservationID == "" || now.IsZero() || (supported && digest == "") || (!supported && reason == "") {
		return fmt.Errorf("%w: invalid whole-candidate QA settlement", ErrSourceWikiBatchInvalidState)
	}
	deadlineReached, qaDeadlineReached, stale := false, false, false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			return err
		}
		if batch.Status != "running" {
			return ErrSourceWikiBatchInvalidState
		}
		if !now.Before(batch.DeadlineAt) {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
			return nil
		}
		if batch.QADueAt == nil || !now.Before(*batch.QADueAt) {
			if batch.QADueAt == nil {
				return ErrSourceWikiBatchInvalidState
			}
			if err := failSourceWikiBatchInTx(tx, &batch, "batch QA phase deadline exhausted", now); err != nil {
				return err
			}
			qaDeadlineReached = true
			return nil
		}
		var reservation types.SourceWikiBatchCallReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND batch_id = ? AND phase = 'batch_qa'", reservationID, batch.ID).Take(&reservation).Error; err != nil {
			return ErrSourceWikiBatchInvalidState
		}
		if err := l.RecordCallInTx(tx, reservation.ID, "succeeded", actualTokens, now); err != nil {
			return err
		}
		if batch.Phase != "batch_qa" || batch.QACursor != batch.InitialCount || reservation.ExpectedQACursor == nil || *reservation.ExpectedQACursor != batch.InitialCount {
			stale = true
			return nil
		}
		if !supported {
			return failSourceWikiBatchInTx(tx, &batch, reason, now)
		}
		currentDigest, err := SourceWikiBatchCandidateDigestInTx(tx, &batch)
		if err != nil {
			return err
		}
		if currentDigest != digest {
			return failSourceWikiBatchInTx(tx, &batch, "whole-candidate QA approval no longer matches the staged candidate set", now)
		}
		approvedAt := now
		return tx.Model(&batch).Updates(map[string]any{
			"phase": "publishing", "qa_approved_at": approvedAt,
			"qa_approval_digest": digest, "updated_at": now,
		}).Error
	})
	if err != nil {
		return err
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	if qaDeadlineReached {
		return ErrSourceWikiBatchQATimeout
	}
	if stale {
		return ErrSourceWikiBatchQACursorChanged
	}
	return nil
}

// RebuildPublishCursor recovers a cursor from committed page transactions. A
// cursor is only advanced across a contiguous prefix whose pages and attempts
// are already ready for this exact snapshot.
func (l *SourceWikiBatchLedger) RebuildPublishCursor(ctx context.Context, batchID string, now time.Time) (int, error) {
	if l == nil || l.db == nil || batchID == "" || now.IsZero() {
		return 0, ErrSourceWikiBatchInvalidState
	}
	cursor := 0
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			return err
		}
		if batch.Status != "running" || batch.Phase != "publishing" || batch.QAApprovedAt == nil || batch.QAApprovalDigest == "" || !now.Before(batch.DeadlineAt) {
			return ErrSourceWikiBatchInvalidState
		}
		digest, err := SourceWikiBatchCandidateDigestInTx(tx, &batch)
		if err != nil || digest != batch.QAApprovalDigest {
			return ErrSourceWikiBatchInvalidState
		}
		var topics []types.SourceWikiCoverageTopic
		if err := tx.Where("batch_id = ? AND initial = TRUE", batch.ID).
			Order("priority DESC, topic_key ASC").Find(&topics).Error; err != nil {
			return err
		}
		if len(topics) != batch.InitialCount {
			return ErrSourceWikiBatchInvalidState
		}
		for _, topic := range topics {
			if topic.Status != "ready" || topic.LastReadySnapshotID != batch.SnapshotID {
				break
			}
			if topic.AttemptID != nil && *topic.AttemptID != "" {
				var attempt types.SourceWikiAttempt
				if err := tx.Where("id = ?", *topic.AttemptID).Take(&attempt).Error; err == nil {
					if attempt.BatchID == batch.ID && (attempt.Status != "ready" || attempt.SnapshotID != batch.SnapshotID) {
						break
					}
				} else if !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
			}
			cursor++
		}
		if cursor < batch.PublishCursor {
			return ErrSourceWikiBatchInvalidState
		}
		if cursor > batch.PublishCursor {
			if err := tx.Model(&batch).Updates(map[string]any{"publish_cursor": cursor, "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return cursor, err
}

// CompletePublishedBatch commits terminal success only after all approved
// candidates have page/coverage transactions committed in frozen order.
func (l *SourceWikiBatchLedger) CompletePublishedBatch(ctx context.Context, batchID string, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || now.IsZero() {
		return ErrSourceWikiBatchInvalidState
	}
	deadlineReached := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			return err
		}
		if batch.Status != "running" || batch.Phase != "publishing" || batch.PublishCursor != batch.InitialCount || batch.QAApprovalDigest == "" || batch.QAApprovedAt == nil {
			return ErrSourceWikiBatchInvalidState
		}
		if !now.Before(batch.DeadlineAt) {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
			return nil
		}
		digest, err := SourceWikiBatchCandidateDigestInTx(tx, &batch)
		if err != nil || digest != batch.QAApprovalDigest {
			return ErrSourceWikiBatchInvalidState
		}
		var ready int64
		if err := tx.Model(&types.SourceWikiCoverageTopic{}).
			Where("batch_id = ? AND initial = TRUE AND status = 'ready' AND last_ready_snapshot_id = ?", batch.ID, batch.SnapshotID).
			Count(&ready).Error; err != nil {
			return err
		}
		if int(ready) != batch.InitialCount {
			return ErrSourceWikiBatchInvalidState
		}
		if err := tx.Model(&batch).Updates(map[string]any{
			"phase": "finished", "status": "completed", "reason": "", "finished_at": now, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&types.SourceWikiBatchCallReservation{}).
			Where("batch_id = ? AND outcome = 'reserved'", batch.ID).
			Updates(map[string]any{"outcome": "unknown", "completed_at": now}).Error
	})
	if err != nil {
		return err
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	return nil
}

// BeginBatchRevalidation reopens one staged child under its original immutable
// attempt budget after a compatible page-version conflict. Approval is
// invalidated, but QA deadline, child charges, parent budget and published
// prefix are preserved.
func (l *SourceWikiBatchLedger) BeginBatchRevalidation(ctx context.Context, batchID, attemptID string, expectedCheckpoint, nextCheckpoint []byte, pageVersion int, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || attemptID == "" || len(expectedCheckpoint) == 0 || len(nextCheckpoint) == 0 || pageVersion < 0 || now.IsZero() {
		return ErrSourceWikiBatchInvalidState
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			return err
		}
		if batch.Status != "running" || batch.Phase != "publishing" || batch.QAApprovedAt == nil || batch.QAApprovalDigest == "" ||
			batch.QADueAt == nil || !now.Before(*batch.QADueAt) || !now.Before(batch.DeadlineAt) || batch.RevalidationAttemptID != "" {
			return ErrSourceWikiBatchInvalidState
		}
		approvedDigest, err := SourceWikiBatchCandidateDigestInTx(tx, &batch)
		if err != nil || approvedDigest != batch.QAApprovalDigest {
			return ErrSourceWikiBatchInvalidState
		}
		requiredQACalls := (batch.InitialCount+3)/4 + 1
		if batch.QACallsReserved+requiredQACalls > batch.QAMaxCalls || batch.QATokensReserved >= batch.QAMaxTokens {
			return ErrSourceWikiBatchBudgetExhausted
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND batch_id = ?", attemptID, batch.ID).Take(&attempt).Error; err != nil {
			return err
		}
		if attempt.Status != "staged" || attempt.StagedAt == nil || attempt.StagedPageVersion == pageVersion ||
			!now.Before(attempt.DeadlineAt) || attempt.Calls >= attempt.MaxCalls || attempt.Tokens >= attempt.MaxTokens ||
			!bytes.Equal(attempt.Checkpoint, expectedCheckpoint) {
			return ErrSourceWikiBatchInvalidState
		}
		var topic types.SourceWikiCoverageTopic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("batch_id = ? AND attempt_id = ? AND initial = TRUE AND status = 'draft'", batch.ID, attempt.ID).Take(&topic).Error; err != nil {
			return ErrSourceWikiBatchInvalidState
		}
		if err := tx.Model(&attempt).Updates(map[string]any{
			"status": "running", "phase": "merge", "checkpoint": types.JSON(nextCheckpoint),
			"staged_at": nil, "staged_page_version": 0, "lease_owner": "", "lease_expires_at": nil, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return tx.Model(&batch).Updates(map[string]any{
			"phase": "cards", "revalidation_attempt_id": attempt.ID,
			"qa_cursor": 0, "qa_approved_at": nil, "qa_approval_digest": "", "updated_at": now,
		}).Error
	})
}

// CompleteBatchRevalidationCandidate returns the parent to the original QA
// phase after its reopened child is safely staged again. The absolute QA
// deadline and all counters remain unchanged.
func (l *SourceWikiBatchLedger) CompleteBatchRevalidationCandidate(ctx context.Context, batchID, attemptID string, now time.Time) error {
	if l == nil || l.db == nil || batchID == "" || attemptID == "" || now.IsZero() {
		return ErrSourceWikiBatchInvalidState
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			return err
		}
		if batch.Status != "running" || batch.Phase != "cards" || batch.RevalidationAttemptID != attemptID ||
			batch.QADueAt == nil || !now.Before(*batch.QADueAt) || !now.Before(batch.DeadlineAt) {
			return ErrSourceWikiBatchInvalidState
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Where("id = ? AND batch_id = ? AND status = 'staged'", attemptID, batch.ID).Take(&attempt).Error; err != nil {
			return ErrSourceWikiBatchInvalidState
		}
		return tx.Model(&batch).Updates(map[string]any{
			"phase": "batch_qa", "revalidation_attempt_id": "", "qa_cursor": 0,
			"qa_approved_at": nil, "qa_approval_digest": "", "updated_at": now,
		}).Error
	})
}

// SourceWikiSkeletonFactMember binds parser facts to one exact parsed member
// in a published snapshot. It is deliberately repository-owned data; the
// source relation resolver consumes the same identity without broadening the
// files that a topic may read.
type SourceWikiSkeletonFactMember struct {
	FileID    string
	VersionID string
	Path      string
	Generated bool
	Facts     []types.ParsedSourceFact
}

// SourceWikiSkeletonSnapshot is a complete, bounded read of the parser facts
// and relations used to plan source Wiki topics. Complete is true only after
// every parsed member was returned and its path and immutable file identity
// were verified as unique.
type SourceWikiSkeletonSnapshot struct {
	TenantID     uint64
	DataSourceID string
	SnapshotID   string
	Complete     bool
	Members      []SourceWikiSkeletonFactMember
	Files        []types.SourceWikiSkeletonFile
	Relations    []types.SourceCodeRelation
}

// LoadSourceWikiSkeletonEvidence preserves the original API for callers that
// only need files and relations. New fact-reference consumers should use
// LoadSourceWikiSkeletonSnapshot so they also receive exact member identities
// and an explicit completeness result.
func LoadSourceWikiSkeletonEvidence(ctx context.Context, db *gorm.DB, tenantID uint64, knowledgeBaseID, sourceID, snapshotID string) ([]types.SourceWikiSkeletonFile, []types.SourceCodeRelation, error) {
	evidence, err := LoadSourceWikiSkeletonSnapshot(ctx, db, tenantID, knowledgeBaseID, sourceID, snapshotID)
	if err != nil {
		return nil, nil, err
	}
	return evidence.Files, evidence.Relations, nil
}

// LoadSourceWikiSkeletonSnapshot reads parser-authored facts and static
// relations only from the requested published snapshot. Hard row bounds make
// the local scan finite; exceeding them fails the skeleton rather than
// silently omitting components, business flows, or fact-reference members.
func LoadSourceWikiSkeletonSnapshot(ctx context.Context, db *gorm.DB, tenantID uint64, knowledgeBaseID, sourceID, snapshotID string) (*SourceWikiSkeletonSnapshot, error) {
	if db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return nil, fmt.Errorf("source Wiki skeleton requires a fixed source snapshot")
	}
	var publishedSnapshot types.SourceSnapshot
	if err := db.WithContext(ctx).Where("id = ? AND data_source_id = ? AND tenant_id = ? AND knowledge_base_id = ? AND state = 'published' AND manifest_complete = TRUE",
		snapshotID, sourceID, tenantID, knowledgeBaseID).Take(&publishedSnapshot).Error; err != nil {
		return nil, fmt.Errorf("source Wiki skeleton snapshot is not a complete published snapshot")
	}
	if publishedSnapshot.WikiDerivationState == "deferred_capacity" {
		return nil, fmt.Errorf("%w: the published source snapshot is available for search but not Wiki generation", ErrSourceWikiDerivationDeferred)
	}
	if publishedSnapshot.WikiDerivationState != "complete" || !publishedSnapshot.RelationsStaged {
		return nil, fmt.Errorf("%w: source Wiki derivation is not complete", ErrSourceWikiDerivationUnavailable)
	}
	var publication types.SourcePublication
	if err := db.WithContext(ctx).Where("snapshot_id = ? AND data_source_id = ? AND tenant_id = ? AND knowledge_base_id = ?",
		snapshotID, sourceID, tenantID, knowledgeBaseID).Take(&publication).Error; err != nil {
		return nil, fmt.Errorf("source Wiki skeleton snapshot is not the published source")
	}
	type factRow struct {
		Path                      string
		SourceFileID              string
		FileVersionID             string
		JoinedFileID              string
		JoinedFileTenantID        uint64
		JoinedFileKnowledgeBaseID string
		JoinedFileDataSourceID    string
		JoinedSourceFileID        string
		JoinedVersionID           string
		Generated                 bool
		Facts                     types.JSON
	}
	var rows []factRow
	err := db.WithContext(ctx).Table("source_snapshot_members sm").
		Joins("LEFT JOIN source_files sf ON sf.id = sm.source_file_id").
		Joins("LEFT JOIN source_file_versions sv ON sv.id = sm.file_version_id AND sv.snapshot_id = sm.snapshot_id").
		Select("sm.path, sm.source_file_id, sm.file_version_id, sf.id AS joined_file_id, sf.tenant_id AS joined_file_tenant_id, sf.knowledge_base_id AS joined_file_knowledge_base_id, sf.data_source_id AS joined_file_data_source_id, sv.source_file_id AS joined_source_file_id, sv.id AS joined_version_id, sm.generated, sv.facts").
		Joins("JOIN source_snapshots ss ON ss.id = sm.snapshot_id AND ss.data_source_id = ? AND ss.tenant_id = ? AND ss.knowledge_base_id = ? AND ss.state = 'published'", sourceID, tenantID, knowledgeBaseID).
		Joins("JOIN source_publications sp ON sp.snapshot_id = ss.id AND sp.data_source_id = ss.data_source_id AND sp.tenant_id = ss.tenant_id AND sp.knowledge_base_id = ss.knowledge_base_id").
		Where("sm.snapshot_id = ? AND sm.status = 'parsed'", snapshotID).
		Order("sm.path ASC").Limit(types.SourceWikiSkeletonMaxFiles + 1).Find(&rows).Error
	if err != nil {
		return nil, err
	}
	if len(rows) > types.SourceWikiSkeletonMaxFiles {
		return nil, fmt.Errorf("source inventory exceeds the %d-file skeleton scan bound", types.SourceWikiSkeletonMaxFiles)
	}
	snapshot := &SourceWikiSkeletonSnapshot{
		TenantID:     tenantID,
		DataSourceID: sourceID,
		SnapshotID:   snapshotID,
		Members:      make([]SourceWikiSkeletonFactMember, 0, len(rows)),
		Files:        make([]types.SourceWikiSkeletonFile, 0, len(rows)),
	}
	seenPaths := make(map[string]struct{}, len(rows))
	seenFileIDs := make(map[string]struct{}, len(rows))
	seenVersionIDs := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		if row.Path == "" || row.SourceFileID == "" || row.FileVersionID == "" || row.JoinedFileID != row.SourceFileID ||
			row.JoinedFileTenantID != tenantID || row.JoinedFileKnowledgeBaseID != knowledgeBaseID || row.JoinedFileDataSourceID != sourceID ||
			row.JoinedSourceFileID != row.SourceFileID || row.JoinedVersionID != row.FileVersionID {
			return nil, fmt.Errorf("parsed source snapshot member has incomplete immutable identity")
		}
		if _, exists := seenPaths[row.Path]; exists {
			return nil, fmt.Errorf("parsed source snapshot contains a duplicate path")
		}
		if _, exists := seenFileIDs[row.SourceFileID]; exists {
			return nil, fmt.Errorf("parsed source snapshot contains a duplicate file identity")
		}
		if _, exists := seenVersionIDs[row.FileVersionID]; exists {
			return nil, fmt.Errorf("parsed source snapshot contains a duplicate file identity")
		}
		seenPaths[row.Path] = struct{}{}
		seenFileIDs[row.SourceFileID] = struct{}{}
		seenVersionIDs[row.FileVersionID] = struct{}{}
		var facts []types.ParsedSourceFact
		if len(row.Facts) > 0 {
			if err := json.Unmarshal(row.Facts, &facts); err != nil {
				return nil, fmt.Errorf("parser facts for a snapshot member are invalid")
			}
		}
		snapshot.Members = append(snapshot.Members, SourceWikiSkeletonFactMember{
			FileID: row.SourceFileID, VersionID: row.FileVersionID, Path: row.Path, Generated: row.Generated, Facts: facts,
		})
		snapshot.Files = append(snapshot.Files, types.SourceWikiSkeletonFile{Path: row.Path, Generated: row.Generated, Facts: facts})
	}
	var relations []types.SourceCodeRelation
	err = db.WithContext(ctx).Table("source_code_relations r").
		Joins("JOIN source_snapshots ss ON ss.id = r.snapshot_id AND ss.data_source_id = ? AND ss.tenant_id = ? AND ss.knowledge_base_id = ? AND ss.state = 'published'", sourceID, tenantID, knowledgeBaseID).
		Joins("JOIN source_publications sp ON sp.snapshot_id = ss.id AND sp.data_source_id = ss.data_source_id AND sp.tenant_id = ss.tenant_id AND sp.knowledge_base_id = ss.knowledge_base_id").
		Where("r.tenant_id = ? AND r.data_source_id = ? AND r.snapshot_id = ?", tenantID, sourceID, snapshotID).
		Order("r.from_path ASC, r.kind ASC, r.from_key ASC, r.to_path ASC, r.to_key ASC").
		Limit(types.SourceWikiSkeletonMaxRelations + 1).Find(&relations).Error
	if err != nil {
		return nil, err
	}
	if len(relations) > types.SourceWikiSkeletonMaxRelations {
		return nil, fmt.Errorf("source relations exceed the %d-edge skeleton scan bound", types.SourceWikiSkeletonMaxRelations)
	}
	if len(relations) != publishedSnapshot.RelationCount {
		return nil, fmt.Errorf("%w: source relation inventory does not match its published count", ErrSourceWikiDerivationUnavailable)
	}
	snapshot.Relations = relations
	snapshot.Complete = len(snapshot.Members) == len(rows) && len(snapshot.Files) == len(rows)
	return snapshot, nil
}

func (l *SourceWikiBatchLedger) ReserveCall(ctx context.Context, req types.SourceWikiBatchReserveCallRequest) (*types.SourceWikiBatchCallReservation, error) {
	if req.Phase == "card" {
		return nil, fmt.Errorf("%w: child call reservation must share the attempt transaction", ErrSourceWikiBatchInvalidState)
	}
	var reservation *types.SourceWikiBatchCallReservation
	deadlineReached := false
	qaDeadlineReached := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var err error
		reservation, err = l.ReserveCallInTx(tx, req)
		if errors.Is(err, ErrSourceWikiBatchDeadline) {
			var batch types.SourceWikiBatch
			if lockErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.BatchID).Take(&batch).Error; lockErr != nil {
				return lockErr
			}
			if expireErr := expireSourceWikiBatchInTx(tx, &batch, req.Now); expireErr != nil {
				return expireErr
			}
			deadlineReached = true
			return nil
		}
		if errors.Is(err, ErrSourceWikiBatchQATimeout) {
			var batch types.SourceWikiBatch
			if lockErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.BatchID).Take(&batch).Error; lockErr != nil {
				return lockErr
			}
			if failErr := failSourceWikiBatchInTx(tx, &batch, "batch QA phase deadline exhausted", req.Now); failErr != nil {
				return failErr
			}
			qaDeadlineReached = true
			return nil
		}
		return err
	})
	if err == nil && deadlineReached {
		err = ErrSourceWikiBatchDeadline
	}
	if err == nil && qaDeadlineReached {
		err = ErrSourceWikiBatchQATimeout
	}
	return reservation, err
}

// ReserveCallInTx reserves parent capacity before a caller locks the child
// attempt. The caller must roll back the same transaction if the child lease,
// epoch, or per-attempt reservation subsequently fails.
func (l *SourceWikiBatchLedger) ReserveCallInTx(tx *gorm.DB, req types.SourceWikiBatchReserveCallRequest) (*types.SourceWikiBatchCallReservation, error) {
	if tx == nil || req.Now.IsZero() || req.ReservedTokens <= 0 ||
		(req.Phase != "skeleton" && req.Phase != "card" && req.Phase != "batch_qa") ||
		(req.Phase == "card" && (req.AttemptID == "" || req.AttemptCallID == "")) ||
		(req.Phase != "card" && (req.BatchID == "" || req.AttemptID != "" || req.AttemptCallID != "")) {
		return nil, fmt.Errorf("%w: invalid batch call reservation", ErrSourceWikiBatchInvalidState)
	}
	if (req.Phase == "batch_qa") != (req.ExpectedQACursor != nil) {
		return nil, fmt.Errorf("%w: QA reservations require their expected cursor", ErrSourceWikiBatchInvalidState)
	}
	batchID := req.BatchID
	var child struct {
		ID         string
		BatchID    string `gorm:"column:batch_id"`
		SnapshotID string `gorm:"column:snapshot_id"`
		Status     string
		DeadlineAt time.Time `gorm:"column:deadline_at"`
		Calls      int
		Tokens     int
		MaxCalls   int `gorm:"column:max_calls"`
		MaxTokens  int `gorm:"column:max_tokens"`
	}
	if req.Phase == "card" {
		if err := tx.Table("source_wiki_attempts").Select("id, batch_id, snapshot_id, status, deadline_at, calls, tokens, max_calls, max_tokens").Where("id = ?", req.AttemptID).Take(&child).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, ErrSourceWikiBatchInvalidState
			}
			return nil, err
		}
		batchID = child.BatchID
		if batchID == "" || req.BatchID != "" && req.BatchID != batchID {
			return nil, ErrSourceWikiBatchInvalidState
		}
	}
	var batch types.SourceWikiBatch
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSourceWikiBatchNotFound
		}
		return nil, err
	}
	if batch.Status != "running" {
		return nil, ErrSourceWikiBatchInvalidState
	}
	if req.Phase == "skeleton" && batch.Phase != "skeleton" || req.Phase == "card" && batch.Phase != "cards" || req.Phase == "batch_qa" && batch.Phase != "batch_qa" {
		return nil, ErrSourceWikiBatchInvalidState
	}
	if req.Phase == "batch_qa" && *req.ExpectedQACursor != batch.QACursor {
		return nil, ErrSourceWikiBatchQACursorChanged
	}
	if !req.Now.Before(batch.DeadlineAt) {
		return nil, ErrSourceWikiBatchDeadline
	}
	if req.Phase == "card" && batch.RevalidationAttemptID == req.AttemptID &&
		(batch.QADueAt == nil || !req.Now.Before(*batch.QADueAt)) {
		return nil, ErrSourceWikiBatchQATimeout
	}
	if req.Phase == "batch_qa" && (batch.QADueAt == nil || !req.Now.Before(*batch.QADueAt)) {
		if batch.QADueAt == nil {
			return nil, ErrSourceWikiBatchInvalidState
		}
		return nil, ErrSourceWikiBatchQATimeout
	}
	if batch.CallsReserved >= batch.MaxCalls || batch.TokensReserved+req.ReservedTokens > batch.MaxTokens {
		return nil, ErrSourceWikiBatchBudgetExhausted
	}
	if req.Phase == "skeleton" && (batch.SkeletonCallsReserved >= batch.SkeletonMaxCalls || batch.SkeletonTokensReserved+req.ReservedTokens > batch.SkeletonMaxTokens) {
		return nil, ErrSourceWikiBatchBudgetExhausted
	}
	if req.Phase == "batch_qa" && (batch.QACallsReserved >= batch.QAMaxCalls || batch.QATokensReserved+req.ReservedTokens > batch.QAMaxTokens) {
		return nil, ErrSourceWikiBatchBudgetExhausted
	}
	if req.Phase == "card" {
		// Lock order is parent then child. Re-check the immutable association and
		// child deadline after the parent lock before the attempt ledger proceeds.
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_wiki_attempts").Select("id, batch_id, snapshot_id, status, deadline_at, calls, tokens, max_calls, max_tokens").Where("id = ?", req.AttemptID).Take(&child).Error; err != nil {
			return nil, err
		}
		if child.BatchID != batch.ID || child.SnapshotID != batch.SnapshotID || child.Status != "running" ||
			child.DeadlineAt.After(batch.DeadlineAt) || !req.Now.Before(child.DeadlineAt) ||
			child.Calls >= child.MaxCalls || child.Tokens+req.ReservedTokens > child.MaxTokens ||
			child.MaxCalls > types.SourceWikiBatchChildMaxCalls || child.MaxTokens > types.SourceWikiBatchChildMaxTokens {
			return nil, ErrSourceWikiBatchBudgetExhausted
		}
	}
	reservation := &types.SourceWikiBatchCallReservation{
		ID: uuid.NewString(), BatchID: batch.ID,
		Phase: req.Phase, ProviderPhase: req.ProviderPhase, ReservedTokens: req.ReservedTokens,
		Outcome: "reserved", CreatedAt: req.Now,
	}
	if req.ExpectedQACursor != nil {
		cursor := *req.ExpectedQACursor
		reservation.ExpectedQACursor = &cursor
	}
	if req.AttemptID != "" {
		reservation.AttemptID = &req.AttemptID
	}
	if req.AttemptCallID != "" {
		reservation.AttemptCallID = &req.AttemptCallID
	}
	if err := tx.Create(reservation).Error; err != nil {
		return nil, err
	}
	updates := map[string]any{
		"calls_reserved":  batch.CallsReserved + 1,
		"tokens_reserved": batch.TokensReserved + req.ReservedTokens,
		"updated_at":      req.Now,
	}
	if req.Phase == "skeleton" {
		updates["skeleton_calls_reserved"] = batch.SkeletonCallsReserved + 1
		updates["skeleton_tokens_reserved"] = batch.SkeletonTokensReserved + req.ReservedTokens
	}
	if req.Phase == "batch_qa" {
		updates["qa_calls_reserved"] = batch.QACallsReserved + 1
		updates["qa_tokens_reserved"] = batch.QATokensReserved + req.ReservedTokens
	}
	if err := tx.Model(&batch).Updates(updates).Error; err != nil {
		return nil, err
	}
	return reservation, nil
}

// RecordCallInTx settles a known provider result while the parent is locked.
// Failed/unknown reservations are never refunded; known successful usage is
// reconciled to the actual charge, matching the T17 attempt ledger.
func (l *SourceWikiBatchLedger) RecordCallInTx(tx *gorm.DB, reservationID, outcome string, actualTokens *int, now time.Time) error {
	if tx == nil || reservationID == "" || now.IsZero() || (outcome != "succeeded" && outcome != "provider_error" && outcome != "unknown" && outcome != "over_budget") || (actualTokens != nil && *actualTokens < 0) {
		return fmt.Errorf("%w: invalid batch call settlement", ErrSourceWikiBatchInvalidState)
	}
	var reservation types.SourceWikiBatchCallReservation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reservationID).Take(&reservation).Error; err != nil {
		return err
	}
	if reservation.Outcome != "reserved" {
		return ErrSourceWikiBatchInvalidState
	}
	var batch types.SourceWikiBatch
	if err := tx.Where("id = ?", reservation.BatchID).Take(&batch).Error; err != nil {
		return err
	}
	delta := 0
	if actualTokens != nil && (outcome == "succeeded" || outcome == "over_budget") {
		delta = *actualTokens - reservation.ReservedTokens
	} else if actualTokens != nil && outcome == "provider_error" && *actualTokens > reservation.ReservedTokens {
		delta = *actualTokens - reservation.ReservedTokens
	}
	nextTokens := batch.TokensReserved + delta
	if nextTokens < 0 || nextTokens > batch.MaxTokens {
		return ErrSourceWikiBatchBudgetExhausted
	}
	completed := now
	if err := tx.Model(&reservation).Updates(map[string]any{"outcome": outcome, "actual_tokens": actualTokens, "completed_at": &completed}).Error; err != nil {
		return err
	}
	updates := map[string]any{"tokens_reserved": nextTokens, "updated_at": now}
	if reservation.Phase == "skeleton" {
		updates["skeleton_tokens_reserved"] = batch.SkeletonTokensReserved + delta
	}
	if reservation.Phase == "batch_qa" {
		updates["qa_tokens_reserved"] = batch.QATokensReserved + delta
	}
	return tx.Model(&batch).Updates(updates).Error
}

// RecordCall settles direct skeleton or whole-batch QA reservations. Child
// settlements are part of the T17 child transaction and use RecordCallInTx.
func (l *SourceWikiBatchLedger) RecordCall(ctx context.Context, reservationID, outcome string, actualTokens *int, now time.Time) error {
	if l == nil || l.db == nil || reservationID == "" || now.IsZero() {
		return fmt.Errorf("%w: invalid batch call settlement", ErrSourceWikiBatchInvalidState)
	}
	var reservation types.SourceWikiBatchCallReservation
	if err := l.db.WithContext(ctx).Where("id = ?", reservationID).Take(&reservation).Error; err != nil {
		return err
	}
	deadlineReached := false
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", reservation.BatchID).Take(&batch).Error; err != nil {
			return err
		}
		if err := l.RecordCallInTx(tx, reservationID, outcome, actualTokens, now); err != nil {
			return err
		}
		if batch.Status == "running" && !now.Before(batch.DeadlineAt) {
			if err := expireSourceWikiBatchInTx(tx, &batch, now); err != nil {
				return err
			}
			deadlineReached = true
		}
		return nil
	})
	if err != nil {
		return err
	}
	if deadlineReached {
		return ErrSourceWikiBatchDeadline
	}
	return nil
}
