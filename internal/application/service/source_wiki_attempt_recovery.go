package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	sourceWikiRecoveryInterval = 15 * time.Second
	sourceWikiRecoveryBatch    = 8
	sourceWikiRecoveryStopWait = 5 * time.Second
)

// SourceWikiAttemptRecovery scans only running attempts that have no live
// owner. It is a process-lifecycle module; the durable ledger remains the
// cross-instance concurrency authority.
type SourceWikiAttemptRecovery struct {
	db     *gorm.DB
	kb     interfaces.KnowledgeBaseService
	models interfaces.ModelService
	wiki   interfaces.SourceWikiService

	interval  time.Duration
	batchSize int

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	done    chan struct{}
}

func NewSourceWikiAttemptRecovery(db *gorm.DB, kb interfaces.KnowledgeBaseService, models interfaces.ModelService, wiki interfaces.SourceWikiService) *SourceWikiAttemptRecovery {
	return &SourceWikiAttemptRecovery{
		db: db, kb: kb, models: models, wiki: wiki,
		interval: sourceWikiRecoveryInterval, batchSize: sourceWikiRecoveryBatch,
	}
}

// Start launches an immediate bounded scan, then repeats at a fixed cadence.
// Claim's row lock, epoch, and owner fence coordinate every process instance.
func (r *SourceWikiAttemptRecovery) Start(parent context.Context) error {
	if r == nil || r.db == nil || r.kb == nil || r.models == nil || r.wiki == nil || r.interval <= 0 || r.batchSize <= 0 {
		return fmt.Errorf("source Wiki attempt recovery is not configured")
	}
	if r.db.Dialector.Name() != "postgres" {
		return fmt.Errorf("source Wiki attempt recovery requires PostgreSQL")
	}
	if parent == nil {
		parent = context.Background()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.started {
		return fmt.Errorf("source Wiki attempt recovery is already started")
	}
	ctx, cancel := context.WithCancel(parent)
	r.started, r.cancel, r.done = true, cancel, make(chan struct{})
	go r.run(ctx, r.done)
	return nil
}

// Stop cancels in-flight recovery work and waits for bounded shutdown. A model
// adapter that ignores cancellation cannot hold application cleanup forever.
func (r *SourceWikiAttemptRecovery) Stop() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	cancel, done := r.cancel, r.done
	r.mu.Unlock()
	if batchWorker, ok := r.wiki.(interfaces.SourceWikiBatchExecutionService); ok && r.db.Migrator().HasTable(&types.SourceWikiBatch{}) {
		batchWorker.StopSourceWikiBatches()
	}
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-time.After(sourceWikiRecoveryStopWait):
		return fmt.Errorf("source Wiki attempt recovery did not stop within %s", sourceWikiRecoveryStopWait)
	}
}

func (r *SourceWikiAttemptRecovery) run(ctx context.Context, done chan struct{}) {
	defer close(done)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.recoverBatch(ctx); err != nil && ctx.Err() == nil {
			logger.Warnf(ctx, "[SourceWikiRecovery] bounded recovery scan failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (r *SourceWikiAttemptRecovery) recoverBatch(ctx context.Context) error {
	now := time.Now()
	var candidates []types.SourceWikiAttempt
	if err := r.db.WithContext(ctx).
		Where("status=? AND (deadline_at<=? OR lease_expires_at IS NULL OR lease_expires_at<=?)", "running", now, now).
		Order("deadline_at ASC, created_at ASC, id ASC").Limit(r.batchSize).Find(&candidates).Error; err != nil {
		return err
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.recoverOne(ctx, candidate); err != nil && !errors.Is(err, repository.ErrSourceWikiAttemptLeased) &&
			!errors.Is(err, repository.ErrSourceWikiAttemptFenced) && !errors.Is(err, repository.ErrSourceWikiAttemptInvalidState) {
			logger.Warnf(ctx, "[SourceWikiRecovery] attempt %s recovery deferred: %v", candidate.ID, err)
		}
	}
	if batchWorker, ok := r.wiki.(interfaces.SourceWikiBatchExecutionService); ok {
		var batches []types.SourceWikiBatch
		if err := r.db.WithContext(ctx).Where("status = 'running'").Order("updated_at ASC, id ASC").Limit(r.batchSize).Find(&batches).Error; err != nil {
			return err
		}
		for _, batch := range batches {
			batchWorker.ResumeSourceWikiBatch(ctx, batch.ID)
		}
	}
	return nil
}

func (r *SourceWikiAttemptRecovery) recoverOne(parent context.Context, candidate types.SourceWikiAttempt) error {
	if candidate.ID == "" {
		return fmt.Errorf("candidate lacks an attempt identity")
	}
	ctx := types.WithExecutionTenant(parent, candidate.TenantID)
	var attempt types.SourceWikiAttempt
	if err := r.db.WithContext(ctx).
		Where("id=? AND tenant_id=? AND knowledge_base_id=? AND source_id=? AND status=?", candidate.ID, candidate.TenantID, candidate.KnowledgeBaseID, candidate.SourceID, "running").
		Take(&attempt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	ledger := repository.NewSourceWikiAttemptLedger(r.db)
	now := time.Now()
	if !now.Before(attempt.DeadlineAt) {
		return ledger.FailRecoveryTarget(ctx, attempt, "attempt absolute time budget exhausted", now)
	}
	if attempt.KnowledgeBaseID == "" || attempt.TenantID == 0 {
		return ledger.FailRecoveryTarget(ctx, attempt, "attempt KB/tenant binding is unavailable", now)
	}
	kb, err := r.kb.GetKnowledgeBaseByIDOnly(ctx, attempt.KnowledgeBaseID)
	if err != nil {
		if errors.Is(err, repository.ErrKnowledgeBaseNotFound) {
			return ledger.FailRecoveryTarget(ctx, attempt, "attempt KB/tenant binding is unavailable", now)
		}
		return err
	}
	if kb == nil || kb.ID != attempt.KnowledgeBaseID || kb.TenantID != attempt.TenantID {
		return ledger.FailRecoveryTarget(ctx, attempt, "attempt KB/tenant binding is unavailable", now)
	}
	if attempt.SourceID == "" {
		return ledger.FailRecoveryTarget(ctx, attempt, "attempt source/tenant binding is unavailable", now)
	}
	var sourceConfig types.DataSource
	if err := r.db.WithContext(ctx).
		Where("id=? AND tenant_id=? AND knowledge_base_id=?", attempt.SourceID, attempt.TenantID, attempt.KnowledgeBaseID).
		First(&sourceConfig).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ledger.FailRecoveryTarget(ctx, attempt, "attempt source/tenant binding is unavailable", now)
		}
		return err
	}
	// The grant is minted only after both target rows were loaded from storage
	// and their tenant/parent bindings matched the admitted attempt.
	taskCtx, err := access.WithKBTaskWrite(ctx, kb, attempt.TenantID)
	if err != nil {
		return fmt.Errorf("attempt task-write grant rejected: %w", err)
	}
	if !kb.IsWikiEnabled() {
		return r.terminalize(taskCtx, ledger, attempt.ID, "Wiki feature is disabled")
	}
	if sourceWikiSourceFingerprint(&sourceConfig) != attempt.SourceConfigFingerprint || !sourceConfig.UpdatedAt.Equal(attempt.SourceUpdatedAt) {
		return r.terminalize(taskCtx, ledger, attempt.ID, "source configuration changed during module generation")
	}
	var publication types.SourcePublication
	if err := r.db.WithContext(taskCtx).
		Where("data_source_id=? AND tenant_id=? AND knowledge_base_id=?", attempt.SourceID, attempt.TenantID, attempt.KnowledgeBaseID).
		Take(&publication).Error; err != nil || publication.SnapshotID != attempt.SnapshotID {
		return r.terminalize(taskCtx, ledger, attempt.ID, "source publication changed during module generation")
	}
	if sourceWikiEffectiveModelID(kb) != attempt.ModelID || attempt.ModelContextWindow <= types.SourceWikiAttemptMaxCompletionTokens {
		return r.terminalize(taskCtx, ledger, attempt.ID, "Wiki model settings or confirmed context window changed during module generation")
	}
	model, err := r.models.GetModelByID(taskCtx, attempt.ModelID)
	if err != nil || sourceWikiModelFingerprint(model) != attempt.ModelSettingsFingerprint || model.Parameters.ContextWindow != attempt.ModelContextWindow ||
		model.Parameters.ContextWindow <= attempt.MaxCompletionTokens {
		return r.terminalize(taskCtx, ledger, attempt.ID, "Wiki model settings or confirmed context window changed during module generation")
	}
	request := types.SourceWikiGenerateRequest{
		KnowledgeBaseID: attempt.KnowledgeBaseID, AttemptID: attempt.ID, SourceID: attempt.SourceID,
		ModulePath: attempt.ModulePath, Title: attempt.Title,
	}
	if attempt.BatchID != "" {
		request.BatchID, request.TopicKind, request.TopicKey = attempt.BatchID, attempt.TopicKind, attempt.TopicKey
		batchExecution, ok := r.wiki.(interfaces.SourceWikiBatchExecutionService)
		if !ok {
			return ledger.FailRecoveryTarget(taskCtx, attempt, "batch topic recovery service is unavailable", time.Now())
		}
		_, err = batchExecution.GenerateTopic(taskCtx, request)
	} else {
		_, err = r.wiki.GenerateModule(taskCtx, request)
	}
	if errors.Is(err, repository.ErrSourceWikiAttemptDeadline) {
		return nil
	}
	return err
}

func (r *SourceWikiAttemptRecovery) terminalize(ctx context.Context, ledger *repository.SourceWikiAttemptLedger, attemptID, reason string) error {
	now := time.Now()
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{
		AttemptID: attemptID, Owner: uuid.NewString(), Now: now, LeaseFor: sourceWikiAttemptLeaseFor,
	})
	if errors.Is(err, repository.ErrSourceWikiAttemptDeadline) || errors.Is(err, repository.ErrSourceWikiAttemptLeased) ||
		errors.Is(err, repository.ErrSourceWikiAttemptFenced) || errors.Is(err, repository.ErrSourceWikiAttemptInvalidState) {
		return nil
	}
	if err != nil {
		return err
	}
	return ledger.Finish(ctx, lease, "failed", reason, time.Now())
}
