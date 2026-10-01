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
		attempt.KnowledgeBaseID == "" || attempt.SourceID == "" || attempt.SnapshotID == "" || attempt.ModulePath == "" || attempt.Title == "" || attempt.Slug == "" ||
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
	if err := l.db.WithContext(ctx).Create(attempt).Error; err != nil {
		return fmt.Errorf("create source Wiki attempt: %w", err)
	}
	return nil
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
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", req.AttemptID).First(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptNotFound
			}
			return err
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
	var reservation types.SourceWikiAttemptCallReservation
	err := l.withClaim(ctx, req.Lease, req.Now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
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
	var overBudget bool
	err := l.withClaim(ctx, req.Lease, req.Now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
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
	if (status != "ready" && status != "failed") || (status == "failed" && reason == "") || (status == "ready" && reason != "") {
		return fmt.Errorf("%w: terminal state requires a consistent status and reason", ErrSourceWikiAttemptInvalidState)
	}
	return l.withClaim(ctx, lease, now, func(tx *gorm.DB, attempt *types.SourceWikiAttempt) error {
		if err := tx.Model(attempt).Updates(map[string]any{
			"status": status, "reason": reason, "lease_owner": "",
			"lease_expires_at": nil, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		return ReleaseSourceWikiAttemptEvidence(tx, attempt.ID)
	})
}

func (l *SourceWikiAttemptLedger) withClaim(ctx context.Context, lease types.SourceWikiAttemptLease, now time.Time, apply func(*gorm.DB, *types.SourceWikiAttempt) error) error {
	if lease.AttemptID == "" || lease.Owner == "" || lease.Epoch <= 0 || now.IsZero() {
		return fmt.Errorf("%w: incomplete worker lease", ErrSourceWikiAttemptInvalidState)
	}
	var transitionErr error
	err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var attempt types.SourceWikiAttempt
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", lease.AttemptID).First(&attempt).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrSourceWikiAttemptNotFound
			}
			return err
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
