package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrSourceWikiAttemptNotFound         = errors.New("source Wiki attempt not found")
	ErrSourceWikiAttemptLeased           = errors.New("source Wiki attempt is leased")
	ErrSourceWikiAttemptFenced           = errors.New("source Wiki attempt worker is fenced")
	ErrSourceWikiAttemptDeadline         = errors.New("source Wiki attempt deadline exceeded")
	ErrSourceWikiAttemptBudgetExhausted  = errors.New("source Wiki attempt budget exhausted")
	ErrSourceWikiAttemptInvalidState     = errors.New("invalid source Wiki attempt state")
	ErrSourceWikiAttemptTargetIncomplete = errors.New("source Wiki attempt lacks a fixed target and model snapshot")
)

// SourceWikiAttemptLedger serializes durable attempt transitions in PostgreSQL.
// Calls and their conservative token reservations are committed before a model
// adapter is invoked; all later worker writes carry the claimed epoch and owner.
type SourceWikiAttemptLedger struct {
	db *gorm.DB
}

func NewSourceWikiAttemptLedger(db *gorm.DB) *SourceWikiAttemptLedger {
	return &SourceWikiAttemptLedger{db: db}
}

func (l *SourceWikiAttemptLedger) Create(ctx context.Context, attempt *types.SourceWikiAttempt) error {
	if l == nil || l.db == nil || attempt == nil || attempt.ID == "" || attempt.Status != "running" ||
		attempt.KnowledgeBaseID == "" || attempt.SourceID == "" || attempt.SnapshotID == "" || attempt.Title == "" || attempt.Slug == "" ||
		attempt.SourceConfigFingerprint == "" || attempt.SourceUpdatedAt.IsZero() || attempt.ModelID == "" || attempt.ModelSettingsFingerprint == "" ||
		attempt.ModelContextWindow <= 0 || attempt.MaxCompletionTokens <= 0 || attempt.MaxCompletionTokens > types.SourceWikiAttemptMaxCompletionTokens ||
		attempt.MaxCompletionTokens >= attempt.ModelContextWindow ||
		attempt.MaxCalls <= 0 || attempt.MaxCalls > types.SourceWikiAttemptMaxCalls || attempt.MaxTokens <= 0 || attempt.MaxTokens > types.SourceWikiAttemptMaxTokens ||
		attempt.MaxElapsedMS <= 0 || attempt.MaxElapsedMS > types.SourceWikiAttemptMaxElapsedMS || attempt.MaxRepairs < 0 || attempt.MaxRepairs > types.SourceWikiAttemptMaxRepairs ||
		attempt.BasePageVersion < 0 || attempt.Calls != 0 || attempt.Tokens != 0 || attempt.Repairs != 0 ||
		attempt.CreatedAt.IsZero() || attempt.DeadlineAt.IsZero() ||
		!attempt.DeadlineAt.Equal(attempt.CreatedAt.Add(time.Duration(attempt.MaxElapsedMS)*time.Millisecond)) {
		return fmt.Errorf("%w: incomplete immutable attempt limits or target", ErrSourceWikiAttemptInvalidState)
	}
	if attempt.UpdatedAt.IsZero() {
		attempt.UpdatedAt = attempt.CreatedAt
	}
	if attempt.BatchID == "" {
		if attempt.ModulePath == "" {
			return fmt.Errorf("%w: manual module attempt requires a module path", ErrSourceWikiAttemptInvalidState)
		}
		if err := l.db.WithContext(ctx).Create(attempt).Error; err != nil {
			return fmt.Errorf("create source Wiki attempt: %w", err)
		}
		return nil
	}
	if attempt.TopicKind == "" || attempt.TopicKey == "" || attempt.MaxCalls > types.SourceWikiBatchChildMaxCalls ||
		attempt.MaxTokens > types.SourceWikiBatchChildMaxTokens || attempt.MaxRepairs > types.SourceWikiAttemptMaxRepairs ||
		attempt.MaxElapsedMS > types.SourceWikiAttemptMaxElapsedMS {
		return fmt.Errorf("%w: invalid batch child topic or limits", ErrSourceWikiAttemptInvalidState)
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", attempt.BatchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		if batch.Status != "running" || batch.Phase != "cards" ||
			batch.TenantID != attempt.TenantID || batch.KnowledgeBaseID != attempt.KnowledgeBaseID ||
			batch.SourceID != attempt.SourceID || batch.SnapshotID != attempt.SnapshotID ||
			batch.SourceConfigFingerprint != attempt.SourceConfigFingerprint || !batch.SourceUpdatedAt.Equal(attempt.SourceUpdatedAt) ||
			batch.ModelID != attempt.ModelID || batch.ModelSettingsFingerprint != attempt.ModelSettingsFingerprint ||
			batch.ModelContextWindow != attempt.ModelContextWindow || attempt.MaxCompletionTokens > batch.MaxCompletionTokens ||
			attempt.DeadlineAt.After(batch.DeadlineAt) || !attempt.CreatedAt.Before(batch.DeadlineAt) {
			return fmt.Errorf("%w: batch child is not bound to the active parent snapshot", ErrSourceWikiBatchInvalidState)
		}
		var topic types.SourceWikiCoverageTopic
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where(
			"source_id = ? AND topic_key = ? AND batch_id = ? AND snapshot_id = ? AND kind = ? AND status = 'planned'",
			attempt.SourceID, attempt.TopicKey, batch.ID, batch.SnapshotID, attempt.TopicKind,
		).Take(&topic).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: batch topic is not available to a child attempt", ErrSourceWikiBatchInvalidState)
			}
			return err
		}
		if err := tx.Create(attempt).Error; err != nil {
			return fmt.Errorf("create source Wiki batch attempt: %w", err)
		}
		claimed := tx.Model(&types.SourceWikiCoverageTopic{}).
			Where("id = ? AND batch_id = ? AND status = 'planned'", topic.ID, batch.ID).
			Updates(map[string]any{"status": "draft", "attempt_id": attempt.ID, "updated_at": attempt.CreatedAt})
		if claimed.Error != nil {
			return claimed.Error
		}
		if claimed.RowsAffected != 1 {
			return fmt.Errorf("%w: topic was claimed by another batch worker", ErrSourceWikiBatchInvalidState)
		}
		return nil
	})
}

func (l *SourceWikiAttemptLedger) Get(ctx context.Context, attemptID string) (*types.SourceWikiAttempt, error) {
	var attempt types.SourceWikiAttempt
	if err := l.db.WithContext(ctx).Where("id = ?", attemptID).First(&attempt).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrSourceWikiAttemptNotFound
		}
		return nil, err
	}
	return &attempt, nil
}

func (l *SourceWikiAttemptLedger) Claim(ctx context.Context, req types.SourceWikiAttemptClaimRequest) (types.SourceWikiAttemptLease, error) {
	if req.AttemptID == "" || req.Owner == "" || len(req.Owner) > 36 || req.Now.IsZero() || req.LeaseFor <= 0 {
		return types.SourceWikiAttemptLease{}, fmt.Errorf("%w: claim requires attempt, owner, time and positive lease", ErrSourceWikiAttemptInvalidState)
	}
	var lease types.SourceWikiAttemptLease
	var transitionErr error
	batchID, err := l.batchIDForAttempt(ctx, req.AttemptID)
	if err != nil {
		return types.SourceWikiAttemptLease{}, err
	}
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch *types.SourceWikiBatch
		if batchID != "" {
			batch = &types.SourceWikiBatch{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(batch).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrSourceWikiBatchNotFound
				}
				return err
			}
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.AttemptID).First(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptNotFound
			}
			return err
		}
		if batch != nil {
			if err := guardBatchChildTx(tx, batch, &attempt, req.Now); err != nil {
				transitionErr = err
				return nil
			}
		}
		if attempt.Status != "running" {
			return ErrSourceWikiAttemptInvalidState
		}
		if !req.Now.Before(attempt.DeadlineAt) {
			transitionErr = ErrSourceWikiAttemptDeadline
			if err := tx.Model(&attempt).Updates(map[string]any{
				"status": "failed", "reason": "attempt absolute time budget exhausted",
				"lease_owner": "", "lease_expires_at": nil, "updated_at": req.Now,
			}).Error; err != nil {
				return err
			}
			if err := markSourceWikiAttemptCallsUnknown(tx, attempt.ID, req.Now); err != nil {
				return err
			}
			return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
		}
		if !sourceWikiAttemptTargetIsFixed(attempt) {
			transitionErr = ErrSourceWikiAttemptTargetIncomplete
			if err := tx.Model(&attempt).Updates(map[string]any{
				"status": "failed", "reason": "attempt is missing its immutable source/model target snapshot",
				"lease_owner": "", "lease_expires_at": nil, "updated_at": req.Now,
			}).Error; err != nil {
				return err
			}
			if err := markSourceWikiAttemptCallsUnknown(tx, attempt.ID, req.Now); err != nil {
				return err
			}
			return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
		}
		if attempt.LeaseOwner != "" && attempt.LeaseExpiresAt != nil && req.Now.Before(*attempt.LeaseExpiresAt) {
			return ErrSourceWikiAttemptLeased
		}
		attempt.Epoch++
		expires := attemptLeaseExpiry(req.Now, req.LeaseFor, attempt.DeadlineAt)
		if err := tx.Model(&attempt).Updates(map[string]any{
			"epoch": attempt.Epoch, "lease_owner": req.Owner,
			"lease_expires_at": expires, "updated_at": req.Now,
		}).Error; err != nil {
			return err
		}
		// An unanswered dispatch from a previous epoch has unknown usage. Its
		// reservation remains charged; only the audit state changes.
		if err := tx.Model(&types.SourceWikiAttemptCall{}).
			Where("attempt_id = ? AND epoch < ? AND outcome = 'reserved'", attempt.ID, attempt.Epoch).
			Updates(map[string]any{"outcome": "unknown", "completed_at": req.Now}).Error; err != nil {
			return err
		}
		lease = types.SourceWikiAttemptLease{
			AttemptID: attempt.ID, Owner: req.Owner, Epoch: attempt.Epoch,
			ModelID: attempt.ModelID, ModelSettingsFingerprint: attempt.ModelSettingsFingerprint,
			ModelContextWindow: attempt.ModelContextWindow, MaxCompletionTokens: attempt.MaxCompletionTokens,
		}
		return nil
	})
	if err != nil {
		return types.SourceWikiAttemptLease{}, err
	}
	if transitionErr != nil {
		return types.SourceWikiAttemptLease{}, transitionErr
	}
	return lease, nil
}

// FailRecoveryTarget terminalizes a due or irrecoverably unbound attempt
// without granting execution access to its KB. It locks the exact persisted
// identity and requires the scanned epoch/lease snapshot to remain current.
func (l *SourceWikiAttemptLedger) FailRecoveryTarget(ctx context.Context, expected types.SourceWikiAttempt, reason string, now time.Time) error {
	if l == nil || l.db == nil || expected.ID == "" || expected.Status != "running" || expected.DeadlineAt.IsZero() || reason == "" || now.IsZero() {
		return fmt.Errorf("%w: recovery failure requires an attempt snapshot, reason and time", ErrSourceWikiAttemptInvalidState)
	}
	batchID, err := l.batchIDForAttempt(ctx, expected.ID)
	if err != nil {
		return err
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch *types.SourceWikiBatch
		if batchID != "" {
			batch = &types.SourceWikiBatch{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(batch).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrSourceWikiBatchNotFound
				}
				return err
			}
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND epoch = ? AND status = ?",
				expected.ID, expected.TenantID, expected.KnowledgeBaseID, expected.SourceID, expected.Epoch, "running").
			First(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptFenced
			}
			return err
		}
		if batch != nil {
			if attempt.BatchID != batch.ID || attempt.TenantID != batch.TenantID || attempt.KnowledgeBaseID != batch.KnowledgeBaseID ||
				attempt.SourceID != batch.SourceID || attempt.SnapshotID != batch.SnapshotID {
				return ErrSourceWikiAttemptFenced
			}
			if batch.Status == "running" && !now.Before(batch.DeadlineAt) {
				return expireSourceWikiBatchInTx(tx, batch, now)
			}
			if batch.Status != "running" || batch.Phase != "cards" {
				return failBatchChildInTx(tx, &attempt, "parent batch is no longer dispatchable", now)
			}
		}
		if attempt.LeaseOwner != expected.LeaseOwner || !sameAttemptLeaseExpiry(attempt.LeaseExpiresAt, expected.LeaseExpiresAt) ||
			!attempt.DeadlineAt.Equal(expected.DeadlineAt) {
			return ErrSourceWikiAttemptFenced
		}
		if now.Before(attempt.DeadlineAt) && attempt.LeaseOwner != "" && attempt.LeaseExpiresAt != nil && now.Before(*attempt.LeaseExpiresAt) {
			return ErrSourceWikiAttemptLeased
		}
		if !now.Before(attempt.DeadlineAt) {
			reason = "attempt absolute time budget exhausted"
		}
		if err := tx.Model(&attempt).Updates(map[string]any{
			"status": "failed", "reason": reason,
			"lease_owner": "", "lease_expires_at": nil, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := markSourceWikiAttemptCallsUnknown(tx, attempt.ID, now); err != nil {
			return err
		}
		return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
	})
}

func sameAttemptLeaseExpiry(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func (l *SourceWikiAttemptLedger) Renew(ctx context.Context, lease types.SourceWikiAttemptLease, now time.Time, leaseFor time.Duration) error {
	if leaseFor <= 0 {
		return fmt.Errorf("%w: lease duration must be positive", ErrSourceWikiAttemptInvalidState)
	}
	return l.withClaim(ctx, lease, now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
		expires := attemptLeaseExpiry(now, leaseFor, attempt.DeadlineAt)
		return tx.Model(attempt).Updates(map[string]any{"lease_expires_at": expires, "updated_at": now}).Error
	})
}

func (l *SourceWikiAttemptLedger) ReserveCall(ctx context.Context, req types.SourceWikiAttemptCallReservationRequest) (types.SourceWikiAttemptCallReservation, error) {
	if req.Phase == "" || req.ReservedTokens <= 0 || req.LeaseFor <= 0 {
		return types.SourceWikiAttemptCallReservation{}, fmt.Errorf("%w: provider reservation requires phase and positive limits", ErrSourceWikiAttemptInvalidState)
	}
	batchID, err := l.batchIDForAttempt(ctx, req.Lease.AttemptID)
	if err != nil {
		return types.SourceWikiAttemptCallReservation{}, err
	}
	if batchID != "" {
		return l.reserveBatchCall(ctx, batchID, req)
	}
	var reservation types.SourceWikiAttemptCallReservation
	err = l.withClaim(ctx, req.Lease, req.Now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
		if attempt.Calls >= attempt.MaxCalls || attempt.Tokens+req.ReservedTokens > attempt.MaxTokens {
			return ErrSourceWikiAttemptBudgetExhausted
		}
		callNumber := attempt.Calls + 1
		id := uuid.NewString()
		call := types.SourceWikiAttemptCall{
			ID: id, AttemptID: attempt.ID, Epoch: attempt.Epoch, CallNumber: callNumber,
			Phase: req.Phase, ReservedTokens: req.ReservedTokens, Outcome: "reserved", CreatedAt: req.Now,
		}
		if err := tx.Create(&call).Error; err != nil {
			return err
		}
		expires := attemptLeaseExpiry(req.Now, req.LeaseFor, attempt.DeadlineAt)
		if err := tx.Model(attempt).Updates(map[string]any{
			"calls": callNumber, "tokens": attempt.Tokens + req.ReservedTokens,
			"phase": req.Phase, "lease_expires_at": expires, "updated_at": req.Now,
		}).Error; err != nil {
			return err
		}
		reservation = types.SourceWikiAttemptCallReservation{
			ID: id, AttemptID: attempt.ID, Epoch: attempt.Epoch,
			CallNumber: callNumber, Phase: req.Phase, ReservedTokens: req.ReservedTokens,
		}
		return nil
	})
	if err != nil {
		return types.SourceWikiAttemptCallReservation{}, err
	}
	return reservation, nil
}

func (l *SourceWikiAttemptLedger) CompleteCall(ctx context.Context, req types.SourceWikiAttemptCallCompletion) error {
	if req.ReservationID == "" || req.Now.IsZero() || (req.ActualTokens != nil && *req.ActualTokens < 0) {
		return fmt.Errorf("%w: invalid provider completion", ErrSourceWikiAttemptInvalidState)
	}
	if req.Outcome != "succeeded" && req.Outcome != "provider_error" && req.Outcome != "unknown" {
		return fmt.Errorf("%w: unknown provider outcome", ErrSourceWikiAttemptInvalidState)
	}
	batchID, err := l.batchIDForAttempt(ctx, req.Lease.AttemptID)
	if err != nil {
		return err
	}
	if batchID != "" {
		return l.completeBatchCall(ctx, batchID, req)
	}
	var overBudget bool
	err = l.withClaim(ctx, req.Lease, req.Now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
		var call types.SourceWikiAttemptCall
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND attempt_id = ? AND epoch = ?", req.ReservationID, attempt.ID, attempt.Epoch).First(&call).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptInvalidState
			}
			return err
		}
		if call.Outcome != "reserved" {
			return ErrSourceWikiAttemptInvalidState
		}
		delta := 0
		if req.ActualTokens != nil {
			if req.Outcome == "succeeded" {
				delta = *req.ActualTokens - call.ReservedTokens
			} else if *req.ActualTokens > call.ReservedTokens {
				// Provider errors with known extra usage add to the conservative
				// reservation; failed/unknown calls never receive a refund.
				delta = *req.ActualTokens - call.ReservedTokens
			}
		}
		nextTokens := attempt.Tokens + delta
		if nextTokens < 0 {
			return fmt.Errorf("%w: token ledger would become negative", ErrSourceWikiAttemptInvalidState)
		}
		overBudget = nextTokens > attempt.MaxTokens
		callOutcome := req.Outcome
		if overBudget {
			callOutcome = "over_budget"
		}
		updates := map[string]any{"outcome": callOutcome, "actual_tokens": req.ActualTokens, "completed_at": req.Now}
		if err := tx.Model(&call).Updates(updates).Error; err != nil {
			return err
		}
		attemptUpdates := map[string]any{"tokens": nextTokens, "updated_at": req.Now}
		if !overBudget && req.Outcome == "succeeded" {
			if req.NextPhase != "" {
				attemptUpdates["phase"] = req.NextPhase
			}
			if len(req.Checkpoint) > 0 {
				attemptUpdates["checkpoint"] = req.Checkpoint
			}
			if len(req.Draft) > 0 {
				attemptUpdates["draft"] = req.Draft
			}
			if req.Repairs != nil {
				if *req.Repairs < attempt.Repairs || *req.Repairs > attempt.MaxRepairs {
					return fmt.Errorf("%w: repair count is not monotonic or exceeds its limit", ErrSourceWikiAttemptInvalidState)
				}
				attemptUpdates["repairs"] = *req.Repairs
			}
		}
		return tx.Model(attempt).Updates(attemptUpdates).Error
	})
	if err != nil {
		return err
	}
	if overBudget {
		return ErrSourceWikiAttemptBudgetExhausted
	}
	return nil
}

func (l *SourceWikiAttemptLedger) batchIDForAttempt(ctx context.Context, attemptID string) (string, error) {
	var row struct {
		BatchID string `gorm:"column:batch_id"`
	}
	if err := l.db.WithContext(ctx).Table("source_wiki_attempts").Select("batch_id").Where("id = ?", attemptID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrSourceWikiAttemptNotFound
		}
		return "", err
	}
	return row.BatchID, nil
}

// guardBatchChildTx runs only after the parent and child rows have been locked
// in that order. A parent deadline atomically expires every child and leaves
// unanswered provider reservations charged as unknown.
func guardBatchChildTx(tx *gorm.DB, batch *types.SourceWikiBatch, attempt *types.SourceWikiAttempt, now time.Time) error {
	if batch == nil || attempt == nil || attempt.BatchID != batch.ID || batch.TenantID != attempt.TenantID ||
		batch.KnowledgeBaseID != attempt.KnowledgeBaseID || batch.SourceID != attempt.SourceID || batch.SnapshotID != attempt.SnapshotID {
		return ErrSourceWikiAttemptFenced
	}
	if batch.Status == "running" && !now.Before(batch.DeadlineAt) {
		if err := expireSourceWikiBatchInTx(tx, batch, now); err != nil {
			return err
		}
		return ErrSourceWikiBatchDeadline
	}
	if batch.Status != "running" || batch.Phase != "cards" {
		if attempt.Status == "running" {
			if err := failBatchChildInTx(tx, attempt, "parent batch is no longer dispatchable", now); err != nil {
				return err
			}
		}
		return ErrSourceWikiBatchInvalidState
	}
	if batch.RevalidationAttemptID == attempt.ID && (batch.QADueAt == nil || !now.Before(*batch.QADueAt)) {
		return ErrSourceWikiBatchQATimeout
	}
	if attempt.DeadlineAt.After(batch.DeadlineAt) {
		if err := failBatchChildInTx(tx, attempt, "child deadline exceeds parent batch deadline", now); err != nil {
			return err
		}
		return ErrSourceWikiBatchInvalidState
	}
	return nil
}

func failBatchChildInTx(tx *gorm.DB, attempt *types.SourceWikiAttempt, reason string, now time.Time) error {
	if err := tx.Model(attempt).Updates(map[string]any{
		"status": "failed", "reason": reason, "lease_owner": "", "lease_expires_at": nil, "updated_at": now,
	}).Error; err != nil {
		return err
	}
	if err := markSourceWikiAttemptCallsUnknown(tx, attempt.ID, now); err != nil {
		return err
	}
	if err := tx.Model(&types.SourceWikiBatchCallReservation{}).
		Where("attempt_id = ? AND outcome = 'reserved'", attempt.ID).
		Updates(map[string]any{"outcome": "unknown", "completed_at": now}).Error; err != nil {
		return err
	}
	return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
}

func (l *SourceWikiAttemptLedger) reserveBatchCall(ctx context.Context, batchID string, req types.SourceWikiAttemptCallReservationRequest) (types.SourceWikiAttemptCallReservation, error) {
	if req.Lease.AttemptID == "" || req.Lease.Owner == "" || req.Lease.Epoch <= 0 || req.Now.IsZero() {
		return types.SourceWikiAttemptCallReservation{}, fmt.Errorf("%w: incomplete worker lease", ErrSourceWikiAttemptInvalidState)
	}
	var reservation types.SourceWikiAttemptCallReservation
	var transitionErr error
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.Lease.AttemptID).Take(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptNotFound
			}
			return err
		}
		if err := guardBatchChildTx(tx, &batch, &attempt, req.Now); err != nil {
			transitionErr = err
			return nil
		}
		if attempt.Status != "running" {
			return ErrSourceWikiAttemptFenced
		}
		if !req.Now.Before(attempt.DeadlineAt) {
			transitionErr = ErrSourceWikiAttemptDeadline
			return failBatchChildInTx(tx, &attempt, "attempt absolute time budget exhausted", req.Now)
		}
		if attempt.Epoch != req.Lease.Epoch || attempt.LeaseOwner != req.Lease.Owner ||
			attempt.ModelID != req.Lease.ModelID || attempt.ModelSettingsFingerprint != req.Lease.ModelSettingsFingerprint ||
			attempt.ModelContextWindow != req.Lease.ModelContextWindow || attempt.MaxCompletionTokens != req.Lease.MaxCompletionTokens ||
			attempt.LeaseExpiresAt == nil || !req.Now.Before(*attempt.LeaseExpiresAt) {
			return ErrSourceWikiAttemptFenced
		}
		if attempt.Calls >= attempt.MaxCalls || attempt.Tokens+req.ReservedTokens > attempt.MaxTokens {
			return ErrSourceWikiAttemptBudgetExhausted
		}
		callNumber, id := attempt.Calls+1, uuid.NewString()
		batchReservation, err := NewSourceWikiBatchLedger(l.db).ReserveCallInTx(tx, types.SourceWikiBatchReserveCallRequest{
			BatchID: batch.ID, AttemptID: attempt.ID, AttemptCallID: id,
			Phase: "card", ProviderPhase: req.Phase, ReservedTokens: req.ReservedTokens, Now: req.Now,
		})
		if err != nil {
			return err
		}
		_ = batchReservation
		call := types.SourceWikiAttemptCall{
			ID: id, AttemptID: attempt.ID, Epoch: attempt.Epoch, CallNumber: callNumber,
			Phase: req.Phase, ReservedTokens: req.ReservedTokens, Outcome: "reserved", CreatedAt: req.Now,
		}
		if err := tx.Create(&call).Error; err != nil {
			return err
		}
		expires := attemptLeaseExpiry(req.Now, req.LeaseFor, attempt.DeadlineAt)
		if err := tx.Model(&attempt).Updates(map[string]any{
			"calls": callNumber, "tokens": attempt.Tokens + req.ReservedTokens,
			"phase": req.Phase, "lease_expires_at": expires, "updated_at": req.Now,
		}).Error; err != nil {
			return err
		}
		reservation = types.SourceWikiAttemptCallReservation{
			ID: id, AttemptID: attempt.ID, Epoch: attempt.Epoch,
			CallNumber: callNumber, Phase: req.Phase, ReservedTokens: req.ReservedTokens,
		}
		return nil
	})
	if err != nil {
		return types.SourceWikiAttemptCallReservation{}, err
	}
	if transitionErr != nil {
		return types.SourceWikiAttemptCallReservation{}, transitionErr
	}
	return reservation, nil
}

func (l *SourceWikiAttemptLedger) completeBatchCall(ctx context.Context, batchID string, req types.SourceWikiAttemptCallCompletion) error {
	var overBudget bool
	var transitionErr error
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiBatchNotFound
			}
			return err
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.Lease.AttemptID).Take(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptNotFound
			}
			return err
		}
		if err := guardBatchChildTx(tx, &batch, &attempt, req.Now); err != nil {
			transitionErr = err
			return nil
		}
		if attempt.Status != "running" || attempt.Epoch != req.Lease.Epoch || attempt.LeaseOwner != req.Lease.Owner ||
			attempt.ModelID != req.Lease.ModelID || attempt.ModelSettingsFingerprint != req.Lease.ModelSettingsFingerprint ||
			attempt.ModelContextWindow != req.Lease.ModelContextWindow || attempt.MaxCompletionTokens != req.Lease.MaxCompletionTokens ||
			attempt.LeaseExpiresAt == nil || !req.Now.Before(*attempt.LeaseExpiresAt) {
			return ErrSourceWikiAttemptFenced
		}
		if !req.Now.Before(attempt.DeadlineAt) {
			transitionErr = ErrSourceWikiAttemptDeadline
			return failBatchChildInTx(tx, &attempt, "attempt absolute time budget exhausted", req.Now)
		}
		var call types.SourceWikiAttemptCall
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND attempt_id = ? AND epoch = ?", req.ReservationID, attempt.ID, attempt.Epoch).Take(&call).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptInvalidState
			}
			return err
		}
		if call.Outcome != "reserved" {
			return ErrSourceWikiAttemptInvalidState
		}
		delta := 0
		if req.ActualTokens != nil {
			if req.Outcome == "succeeded" && *req.ActualTokens >= 0 {
				delta = *req.ActualTokens - call.ReservedTokens
			} else if req.Outcome == "provider_error" && *req.ActualTokens > call.ReservedTokens {
				delta = *req.ActualTokens - call.ReservedTokens
			}
		}
		nextTokens := attempt.Tokens + delta
		if nextTokens < 0 {
			return fmt.Errorf("%w: token ledger would become negative", ErrSourceWikiAttemptInvalidState)
		}
		overBudget = nextTokens > attempt.MaxTokens
		callOutcome := req.Outcome
		if overBudget {
			callOutcome = "over_budget"
		}
		var parentCall types.SourceWikiBatchCallReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("attempt_call_id = ? AND attempt_id = ? AND batch_id = ?", call.ID, attempt.ID, batch.ID).Take(&parentCall).Error; err != nil {
			return ErrSourceWikiBatchInvalidState
		}
		if err := NewSourceWikiBatchLedger(l.db).RecordCallInTx(tx, parentCall.ID, callOutcome, req.ActualTokens, req.Now); err != nil {
			return err
		}
		if err := tx.Model(&call).Updates(map[string]any{"outcome": callOutcome, "actual_tokens": req.ActualTokens, "completed_at": req.Now}).Error; err != nil {
			return err
		}
		attemptUpdates := map[string]any{"tokens": nextTokens, "updated_at": req.Now}
		if !overBudget && req.Outcome == "succeeded" {
			if req.NextPhase != "" {
				attemptUpdates["phase"] = req.NextPhase
			}
			if len(req.Checkpoint) > 0 {
				attemptUpdates["checkpoint"] = req.Checkpoint
			}
			if len(req.Draft) > 0 {
				attemptUpdates["draft"] = req.Draft
			}
			if req.Repairs != nil {
				if *req.Repairs < attempt.Repairs || *req.Repairs > attempt.MaxRepairs {
					return fmt.Errorf("%w: repair count is not monotonic or exceeds its limit", ErrSourceWikiAttemptInvalidState)
				}
				attemptUpdates["repairs"] = *req.Repairs
			}
		}
		return tx.Model(&attempt).Updates(attemptUpdates).Error
	})
	if err != nil {
		return err
	}
	if transitionErr != nil {
		return transitionErr
	}
	if overBudget {
		return ErrSourceWikiAttemptBudgetExhausted
	}
	return nil
}

func (l *SourceWikiAttemptLedger) SaveProgress(ctx context.Context, lease types.SourceWikiAttemptLease, progress types.SourceWikiAttemptProgress) error {
	if progress.Phase == "" || progress.Now.IsZero() || progress.Repairs < 0 {
		return fmt.Errorf("%w: progress checkpoint requires a phase, time and valid repair count", ErrSourceWikiAttemptInvalidState)
	}
	return l.withClaim(ctx, lease, progress.Now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
		if progress.Repairs < attempt.Repairs || progress.Repairs > attempt.MaxRepairs {
			return fmt.Errorf("%w: repair count is not monotonic or exceeds its limit", ErrSourceWikiAttemptInvalidState)
		}
		updates := map[string]any{"phase": progress.Phase, "repairs": progress.Repairs, "updated_at": progress.Now}
		if len(progress.Checkpoint) > 0 {
			updates["checkpoint"] = progress.Checkpoint
		}
		if len(progress.Draft) > 0 {
			updates["draft"] = progress.Draft
		}
		return tx.Model(attempt).Updates(updates).Error
	})
}

func (l *SourceWikiAttemptLedger) Finish(ctx context.Context, lease types.SourceWikiAttemptLease, status, reason string, now time.Time) error {
	return l.FinishWithResultKind(ctx, lease, status, reason, "", now)
}

func (l *SourceWikiAttemptLedger) FinishWithResultKind(ctx context.Context, lease types.SourceWikiAttemptLease, status, reason string, resultKind types.SourceWikiAttemptResultKind, now time.Time) error {
	if (status != "ready" && status != "failed") || (status == "failed" && reason == "") || (status == "ready" && reason != "") {
		return fmt.Errorf("%w: terminal state requires a consistent status and reason", ErrSourceWikiAttemptInvalidState)
	}
	if resultKind != "" && (status != "failed" || resultKind != types.SourceWikiAttemptResultKindInsufficientEvidence) {
		return fmt.Errorf("%w: unsupported typed attempt result", ErrSourceWikiAttemptInvalidState)
	}
	return l.withClaim(ctx, lease, now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
		if err := tx.Model(attempt).Updates(map[string]any{
			"status": status, "reason": reason, "lease_owner": "",
			"lease_expires_at": nil, "result_kind": resultKind, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
	})
}

// StageBatchCandidate ends model work without publishing a page or releasing
// the exact evidence owner. Staged attempts are durable candidates and are
// deliberately not claimable as fresh generation work.
func (l *SourceWikiAttemptLedger) StageBatchCandidate(ctx context.Context, lease types.SourceWikiAttemptLease, pageVersion int, now time.Time) error {
	if l == nil || l.db == nil || lease.AttemptID == "" || pageVersion < 0 || now.IsZero() {
		return fmt.Errorf("%w: invalid staged candidate", ErrSourceWikiAttemptInvalidState)
	}
	batchID, err := l.batchIDForAttempt(ctx, lease.AttemptID)
	if err != nil {
		return err
	}
	if batchID == "" {
		return fmt.Errorf("%w: only batch attempts can be staged", ErrSourceWikiAttemptInvalidState)
	}
	return l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch types.SourceWikiBatch
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(&batch).Error; err != nil {
			return err
		}
		if batch.Status != "running" || batch.Phase != "cards" || !now.Before(batch.DeadlineAt) {
			return ErrSourceWikiBatchInvalidState
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND batch_id = ?", lease.AttemptID, batch.ID).Take(&attempt).Error; err != nil {
			return err
		}
		if attempt.Status != "running" || attempt.Epoch != lease.Epoch || attempt.LeaseOwner != lease.Owner ||
			attempt.LeaseExpiresAt == nil || !now.Before(*attempt.LeaseExpiresAt) || !now.Before(attempt.DeadlineAt) ||
			attempt.BatchID != batch.ID || attempt.SnapshotID != batch.SnapshotID || len(attempt.Checkpoint) == 0 || len(attempt.Draft) == 0 {
			return ErrSourceWikiAttemptFenced
		}
		var ownerCount int64
		if err := tx.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", attempt.ID).Count(&ownerCount).Error; err != nil {
			return err
		}
		if ownerCount == 0 {
			return fmt.Errorf("%w: staged candidate lost its evidence owner", ErrSourceWikiAttemptInvalidState)
		}
		result := tx.Model(&attempt).Updates(map[string]any{
			"status": "staged", "phase": "staged", "staged_at": now,
			"staged_page_version": pageVersion, "lease_owner": "", "lease_expires_at": nil, "updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrSourceWikiAttemptFenced
		}
		return nil
	})
}

func (l *SourceWikiAttemptLedger) withClaim(ctx context.Context, lease types.SourceWikiAttemptLease, now time.Time, apply func(*gorm.DB, *types.SourceWikiAttempt) error) error {
	if lease.AttemptID == "" || lease.Owner == "" || lease.Epoch <= 0 || now.IsZero() {
		return fmt.Errorf("%w: incomplete worker lease", ErrSourceWikiAttemptInvalidState)
	}
	var transitionErr error
	batchID, err := l.batchIDForAttempt(ctx, lease.AttemptID)
	if err != nil {
		return err
	}
	err = l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var batch *types.SourceWikiBatch
		if batchID != "" {
			batch = &types.SourceWikiBatch{}
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", batchID).Take(batch).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrSourceWikiBatchNotFound
				}
				return err
			}
		}
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", lease.AttemptID).First(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptNotFound
			}
			return err
		}
		if batch != nil {
			if err := guardBatchChildTx(tx, batch, &attempt, now); err != nil {
				transitionErr = err
				return nil
			}
		}
		if attempt.Status != "running" {
			return ErrSourceWikiAttemptFenced
		}
		if !now.Before(attempt.DeadlineAt) {
			transitionErr = ErrSourceWikiAttemptDeadline
			if err := tx.Model(&attempt).Updates(map[string]any{
				"status": "failed", "reason": "attempt absolute time budget exhausted",
				"lease_owner": "", "lease_expires_at": nil, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := markSourceWikiAttemptCallsUnknown(tx, attempt.ID, now); err != nil {
				return err
			}
			return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
		}
		if attempt.Epoch != lease.Epoch || attempt.LeaseOwner != lease.Owner ||
			attempt.ModelID != lease.ModelID || attempt.ModelSettingsFingerprint != lease.ModelSettingsFingerprint ||
			attempt.ModelContextWindow != lease.ModelContextWindow || attempt.MaxCompletionTokens != lease.MaxCompletionTokens ||
			attempt.LeaseExpiresAt == nil || !now.Before(*attempt.LeaseExpiresAt) {
			return ErrSourceWikiAttemptFenced
		}
		return apply(tx, &attempt)
	})
	if err != nil {
		return err
	}
	return transitionErr
}

func attemptLeaseExpiry(now time.Time, leaseFor time.Duration, deadline time.Time) time.Time {
	expires := now.Add(leaseFor)
	if expires.After(deadline) {
		return deadline
	}
	return expires
}

func sourceWikiAttemptTargetIsFixed(attempt types.SourceWikiAttempt) bool {
	return attempt.SnapshotID != "" && attempt.SourceConfigFingerprint != "" &&
		attempt.SourceUpdatedAt.After(time.Unix(0, 0)) && attempt.ModelID != "" &&
		attempt.ModelSettingsFingerprint != "" && attempt.ModelContextWindow > attempt.MaxCompletionTokens && attempt.MaxCompletionTokens > 0
}

func markSourceWikiAttemptCallsUnknown(tx *gorm.DB, attemptID string, now time.Time) error {
	return tx.Model(&types.SourceWikiAttemptCall{}).
		Where("attempt_id = ? AND outcome = 'reserved'", attemptID).
		Updates(map[string]any{"outcome": "unknown", "completed_at": now}).Error
}
