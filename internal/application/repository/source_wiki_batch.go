package repository

import (
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
	ErrSourceWikiBatchNotFound        = errors.New("source Wiki batch not found")
	ErrSourceWikiBatchDeadline        = errors.New("source Wiki batch deadline exceeded")
	ErrSourceWikiBatchBudgetExhausted = errors.New("source Wiki batch budget exhausted")
	ErrSourceWikiBatchInvalidState    = errors.New("invalid source Wiki batch state")
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
// parent snapshot. Existing ready links are retained; a ready card only stays
// current when it was generated against the same snapshot.
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
		if len(topics) == 0 || len(topics) > 100000 {
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
				BatchID: &batch.ID, WikiSlug: sourceWikiTopicSlug(topic), UpdatedAt: now,
			}
			updates := map[string]any{
				"knowledge_base_id": row.KnowledgeBaseID, "snapshot_id": row.SnapshotID, "kind": row.Kind,
				"module_path": row.ModulePath, "title": row.Title, "priority": row.Priority,
				"status":    gorm.Expr("CASE WHEN source_wiki_topics.status = 'ready' AND source_wiki_topics.last_ready_snapshot_id = EXCLUDED.snapshot_id THEN 'ready' ELSE EXCLUDED.status END"),
				"uncertain": row.Uncertain, "uncertainty_reasons": row.UncertaintyReasons, "relations": row.Relations,
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
		(phase != "skeleton" && phase != "cards" && phase != "batch_qa" && phase != "finished") ||
		(status != "running" && status != "completed" && status != "failed" && status != "expired") {
		return fmt.Errorf("%w: invalid batch progress", ErrSourceWikiBatchInvalidState)
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
		if status == "completed" && (phase != "finished" || cursor != batch.InitialCount || reason != "") {
			return ErrSourceWikiBatchInvalidState
		}
		if (status == "failed" || status == "expired") && reason == "" {
			return ErrSourceWikiBatchInvalidState
		}
		updates := map[string]any{
			"phase": phase, "status": status, "cursor": cursor,
			"current_topic_key": currentTopicKey, "reason": reason, "updated_at": now,
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
	case "finished":
		return 3
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
	if err := tx.Model(&types.SourceWikiBatchCallReservation{}).
		Where("batch_id = ? AND outcome = 'reserved'", batch.ID).
		Updates(map[string]any{"outcome": "unknown", "completed_at": now}).Error; err != nil {
		return err
	}
	return tx.Model(&types.SourceWikiCoverageTopic{}).
		Where("batch_id = ? AND status = 'planned'", batch.ID).
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

// LoadSourceWikiSkeletonEvidence reads parser-authored facts and static
// relations only from the requested published snapshot. Hard row bounds make
// the local scan finite; exceeding them fails the skeleton rather than
// silently omitting components or business flows.
func LoadSourceWikiSkeletonEvidence(ctx context.Context, db *gorm.DB, tenantID uint64, knowledgeBaseID, sourceID, snapshotID string) ([]types.SourceWikiSkeletonFile, []types.SourceCodeRelation, error) {
	if db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return nil, nil, fmt.Errorf("source Wiki skeleton requires a fixed source snapshot")
	}
	type factRow struct {
		Path      string
		Generated bool
		Facts     types.JSON
	}
	var rows []factRow
	err := db.WithContext(ctx).Table("source_snapshot_members sm").
		Select("sm.path, sm.generated, sv.facts").
		Joins("JOIN source_file_versions sv ON sv.id = sm.file_version_id AND sv.snapshot_id = sm.snapshot_id").
		Joins("JOIN source_snapshots ss ON ss.id = sm.snapshot_id AND ss.data_source_id = ? AND ss.tenant_id = ? AND ss.knowledge_base_id = ? AND ss.state = 'published'", sourceID, tenantID, knowledgeBaseID).
		Joins("JOIN source_publications sp ON sp.snapshot_id = ss.id AND sp.data_source_id = ss.data_source_id AND sp.tenant_id = ss.tenant_id AND sp.knowledge_base_id = ss.knowledge_base_id").
		Where("sm.snapshot_id = ? AND sm.status = 'parsed' AND sm.file_version_id <> ''", snapshotID).
		Order("sm.path ASC").Limit(types.SourceWikiSkeletonMaxFiles + 1).Find(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	if len(rows) > types.SourceWikiSkeletonMaxFiles {
		return nil, nil, fmt.Errorf("source inventory exceeds the %d-file skeleton scan bound", types.SourceWikiSkeletonMaxFiles)
	}
	files := make([]types.SourceWikiSkeletonFile, 0, len(rows))
	for _, row := range rows {
		var facts []types.ParsedSourceFact
		if len(row.Facts) > 0 {
			if err := json.Unmarshal(row.Facts, &facts); err != nil {
				return nil, nil, fmt.Errorf("parser facts for a snapshot member are invalid")
			}
		}
		files = append(files, types.SourceWikiSkeletonFile{Path: row.Path, Generated: row.Generated, Facts: facts})
	}
	var relations []types.SourceCodeRelation
	err = db.WithContext(ctx).Table("source_code_relations r").
		Joins("JOIN source_snapshots ss ON ss.id = r.snapshot_id AND ss.data_source_id = ? AND ss.tenant_id = ? AND ss.knowledge_base_id = ? AND ss.state = 'published'", sourceID, tenantID, knowledgeBaseID).
		Joins("JOIN source_publications sp ON sp.snapshot_id = ss.id AND sp.data_source_id = ss.data_source_id AND sp.tenant_id = ss.tenant_id AND sp.knowledge_base_id = ss.knowledge_base_id").
		Where("r.tenant_id = ? AND r.data_source_id = ? AND r.snapshot_id = ?", tenantID, sourceID, snapshotID).
		Order("r.from_path ASC, r.kind ASC, r.from_key ASC, r.to_path ASC, r.to_key ASC").
		Limit(types.SourceWikiSkeletonMaxRelations + 1).Find(&relations).Error
	if err != nil {
		return nil, nil, err
	}
	if len(relations) > types.SourceWikiSkeletonMaxRelations {
		return nil, nil, fmt.Errorf("source relations exceed the %d-edge skeleton scan bound", types.SourceWikiSkeletonMaxRelations)
	}
	return files, relations, nil
}

func (l *SourceWikiBatchLedger) ReserveCall(ctx context.Context, req types.SourceWikiBatchReserveCallRequest) (*types.SourceWikiBatchCallReservation, error) {
	if req.Phase == "card" {
		return nil, fmt.Errorf("%w: child call reservation must share the attempt transaction", ErrSourceWikiBatchInvalidState)
	}
	var reservation *types.SourceWikiBatchCallReservation
	deadlineReached := false
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
		return err
	})
	if err == nil && deadlineReached {
		err = ErrSourceWikiBatchDeadline
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
	if !req.Now.Before(batch.DeadlineAt) {
		return nil, ErrSourceWikiBatchDeadline
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
	if actualTokens != nil && outcome == "succeeded" {
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
