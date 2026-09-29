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
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sourceRunMaxRetries = 5
	sourceRunLeaseTTL   = 2 * time.Minute
)

var errSourceConfigurationNotCurrent = errors.New("source configuration is not current")

type sourceSyncStateRow struct {
	DataSourceID              string     `gorm:"column:data_source_id;primaryKey"`
	TenantID                  uint64     `gorm:"column:tenant_id"`
	ConfigGeneration          int64      `gorm:"column:config_generation"`
	ConfigFingerprint         string     `gorm:"column:config_fingerprint"`
	FencingToken              int64      `gorm:"column:fencing_token"`
	LeaseOwner                *string    `gorm:"column:lease_owner"`
	LeaseExpiresAt            *time.Time `gorm:"column:lease_expires_at"`
	ActiveSyncLogID           *string    `gorm:"column:active_sync_log_id"`
	PendingSyncLogID          *string    `gorm:"column:pending_sync_log_id"`
	PendingDeliveryGeneration int64      `gorm:"column:pending_delivery_generation"`
	PendingTrigger            string     `gorm:"column:pending_trigger"`
}

type sourceSyncRunRow struct {
	SyncLogID          string `gorm:"column:sync_log_id"`
	DataSourceID       string `gorm:"column:data_source_id"`
	TenantID           uint64 `gorm:"column:tenant_id"`
	ConfigGeneration   int64  `gorm:"column:config_generation"`
	FencingToken       int64  `gorm:"column:fencing_token"`
	DeliveryGeneration int64  `gorm:"column:delivery_generation"`
	Trigger            string `gorm:"column:trigger"`
	Phase              string `gorm:"column:phase"`
	TargetCommitSHA    string `gorm:"column:target_commit_sha"`
	RetryCount         int    `gorm:"column:retry_count"`
}

func sourceConfigFingerprint(ds *types.DataSource) string {
	canonical := []byte(ds.Config)
	var parsed any
	if json.Unmarshal(ds.Config, &parsed) == nil {
		if encoded, err := json.Marshal(parsed); err == nil {
			canonical = encoded
		}
	}
	identity := struct {
		DataSourceID    string          `json:"data_source_id"`
		TenantID        uint64          `json:"tenant_id"`
		KnowledgeBaseID string          `json:"knowledge_base_id"`
		Type            string          `json:"type"`
		Config          json.RawMessage `json:"config"`
	}{DataSourceID: ds.ID, TenantID: ds.TenantID, KnowledgeBaseID: ds.KnowledgeBaseID, Type: ds.Type, Config: canonical}
	encoded, err := json.Marshal(identity)
	if err != nil {
		encoded = canonical
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}

func (r *SyncLogRepository) ensureSourceSyncState(tx *gorm.DB, ds *types.DataSource) (*sourceSyncStateRow, error) {
	fingerprint := sourceConfigFingerprint(ds)
	if err := tx.Exec(`INSERT INTO source_sync_states(data_source_id,tenant_id,config_generation,config_fingerprint,updated_at)
		VALUES(?,?,1,?,?) ON CONFLICT(data_source_id) DO NOTHING`, ds.ID, ds.TenantID, fingerprint, time.Now().UTC()).Error; err != nil {
		return nil, err
	}
	var state sourceSyncStateRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").
		Where("data_source_id=? AND tenant_id=?", ds.ID, ds.TenantID).Take(&state).Error; err != nil {
		return nil, err
	}
	return &state, nil
}

// lockPersistedSourceDataSource reads source identity/config after coordinator
// state has been locked. Keep this state→datasource order consistent with
// result commits so a stale caller snapshot cannot rewrite coordinator state.
func lockPersistedSourceDataSource(tx *gorm.DB, dataSourceID string, tenantID uint64) (*types.DataSource, error) {
	var current types.DataSource
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).
		Where("id=? AND tenant_id=?", dataSourceID, tenantID).Take(&current).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errSourceConfigurationNotCurrent
		}
		return nil, err
	}
	return &current, nil
}

func sourceStateMatchesPersistedDataSource(state *sourceSyncStateRow, current *types.DataSource) bool {
	return state != nil && current != nil && state.ConfigFingerprint == sourceConfigFingerprint(current)
}

func sourceStateUpdate(tx *gorm.DB, state *sourceSyncStateRow, values map[string]any) error {
	values["updated_at"] = time.Now().UTC()
	return tx.Table("source_sync_states").Where("data_source_id=?", state.DataSourceID).Updates(values).Error
}

func cancelSourceRunTx(tx *gorm.DB, id, reason string) error {
	if id == "" {
		return nil
	}
	now := time.Now().UTC()
	return tx.Model(&types.SyncLog{}).
		Where("id=? AND status IN ?", id, []string{types.SyncLogStatusQueued, types.SyncLogStatusRunning}).
		Updates(map[string]any{"status": types.SyncLogStatusCanceled, "finished_at": &now, "error_message": reason, "updated_at": now}).Error
}

func (r *SyncLogRepository) invalidateSourceGeneration(tx *gorm.DB, state *sourceSyncStateRow, ds *types.DataSource) error {
	if state.ConfigFingerprint == sourceConfigFingerprint(ds) {
		return nil
	}
	if err := cancelSourceRunTx(tx, derefSourceID(state.ActiveSyncLogID), "source configuration changed; this run was fenced off"); err != nil {
		return err
	}
	if pending := derefSourceID(state.PendingSyncLogID); pending != derefSourceID(state.ActiveSyncLogID) {
		if err := cancelSourceRunTx(tx, pending, "source configuration changed; this trigger was superseded"); err != nil {
			return err
		}
	}
	state.ConfigGeneration++
	state.FencingToken++
	state.ConfigFingerprint = sourceConfigFingerprint(ds)
	state.LeaseOwner, state.LeaseExpiresAt = nil, nil
	state.ActiveSyncLogID, state.PendingSyncLogID = nil, nil
	state.PendingTrigger = ""
	return sourceStateUpdate(tx, state, map[string]any{
		"config_generation": state.ConfigGeneration, "config_fingerprint": state.ConfigFingerprint,
		"fencing_token": state.FencingToken, "lease_owner": nil, "lease_expires_at": nil,
		"active_sync_log_id": nil, "pending_sync_log_id": nil, "pending_trigger": "",
	})
}

// RegisterSourceTrigger atomically records the visible queued run and makes it
// either the next dispatch or the one latest pending catch-up trigger.
func (r *SyncLogRepository) RegisterSourceTrigger(ctx context.Context, ds *types.DataSource, log *types.SyncLog, trigger string) (bool, int64, error) {
	if ds == nil || log == nil || ds.ID == "" || log.DataSourceID != ds.ID || log.TenantID != ds.TenantID {
		return false, 0, errors.New("source trigger identity is invalid")
	}
	if trigger == "" {
		trigger = "manual"
	}
	var shouldDispatch bool
	var deliveryGeneration int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := r.ensureSourceSyncState(tx, ds)
		if err != nil {
			return err
		}
		current, err := lockPersistedSourceDataSource(tx, ds.ID, ds.TenantID)
		if err != nil {
			return err
		}
		if sourceConfigFingerprint(ds) != sourceConfigFingerprint(current) || !sourceStateMatchesPersistedDataSource(state, current) {
			return errSourceConfigurationNotCurrent
		}
		if err := r.invalidateSourceGeneration(tx, state, current); err != nil {
			return err
		}
		log.Status = types.SyncLogStatusQueued
		if err := tx.Create(log).Error; err != nil {
			return err
		}
		if previous := derefSourceID(state.PendingSyncLogID); previous != "" {
			if err := cancelSourceRunTx(tx, previous, "a newer source trigger is waiting"); err != nil {
				return err
			}
		}
		deliveryGeneration = state.PendingDeliveryGeneration + 1
		state.PendingSyncLogID = stringPointer(log.ID)
		state.PendingDeliveryGeneration = deliveryGeneration
		state.PendingTrigger = trigger
		if err := tx.Exec(`INSERT INTO source_sync_runs(sync_log_id,data_source_id,tenant_id,config_generation,delivery_generation,trigger,phase,updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, log.ID, ds.ID, ds.TenantID, state.ConfigGeneration, deliveryGeneration, trigger, "queued", time.Now().UTC()).Error; err != nil {
			return err
		}
		shouldDispatch = state.ActiveSyncLogID == nil
		return sourceStateUpdate(tx, state, map[string]any{
			"pending_sync_log_id": log.ID, "pending_delivery_generation": deliveryGeneration, "pending_trigger": trigger,
		})
	})
	return shouldDispatch, deliveryGeneration, err
}

// AdvanceSourceConfig increments the generation and fences running workers as
// soon as persisted connector settings change, before another task is needed.
func (r *SyncLogRepository) AdvanceSourceConfig(ctx context.Context, ds *types.DataSource, enabled bool) error {
	if ds == nil || ds.ID == "" {
		return errors.New("data source is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM source_sync_states WHERE data_source_id=?)", ds.ID).Scan(&exists).Error; err != nil {
			return err
		}
		if !exists && !enabled {
			return nil
		}
		state, err := r.ensureSourceSyncState(tx, ds)
		if err != nil {
			return err
		}
		if !enabled {
			fingerprint := "disabled:" + sourceConfigFingerprint(ds)
			if state.ConfigFingerprint == fingerprint {
				return nil
			}
			if err := cancelSourceRunTx(tx, derefSourceID(state.ActiveSyncLogID), "source mode was disabled; this run was fenced off"); err != nil {
				return err
			}
			if pending := derefSourceID(state.PendingSyncLogID); pending != derefSourceID(state.ActiveSyncLogID) {
				if err := cancelSourceRunTx(tx, pending, "source mode was disabled; this trigger was canceled"); err != nil {
					return err
				}
			}
			state.ConfigGeneration++
			state.FencingToken++
			state.ConfigFingerprint = fingerprint
			state.LeaseOwner, state.LeaseExpiresAt = nil, nil
			state.ActiveSyncLogID, state.PendingSyncLogID, state.PendingTrigger = nil, nil, ""
			return sourceStateUpdate(tx, state, map[string]any{
				"config_generation": state.ConfigGeneration, "config_fingerprint": fingerprint,
				"fencing_token": state.FencingToken, "lease_owner": nil, "lease_expires_at": nil,
				"active_sync_log_id": nil, "pending_sync_log_id": nil, "pending_trigger": "",
			})
		}
		return r.invalidateSourceGeneration(tx, state, ds)
	})
}

func (r *SyncLogRepository) ClaimSourceRun(ctx context.Context, ds *types.DataSource, logID string, deliveryGeneration int64, owner string, leaseTTL time.Duration) (types.SourceSyncLease, bool, error) {
	if ds == nil || ds.ID == "" || logID == "" {
		return types.SourceSyncLease{}, false, errors.New("source run identity is required")
	}
	if leaseTTL <= 0 {
		leaseTTL = sourceRunLeaseTTL
	}
	if owner == "" {
		return types.SourceSyncLease{}, false, errors.New("source lease owner is required")
	}
	var lease types.SourceSyncLease
	var claimed bool
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := r.ensureSourceSyncState(tx, ds)
		if err != nil {
			return err
		}
		current, err := lockPersistedSourceDataSource(tx, ds.ID, ds.TenantID)
		if err != nil {
			return err
		}
		if sourceConfigFingerprint(ds) != sourceConfigFingerprint(current) || !sourceStateMatchesPersistedDataSource(state, current) {
			return errSourceConfigurationNotCurrent
		}
		if err := r.invalidateSourceGeneration(tx, state, current); err != nil {
			return err
		}
		var log types.SyncLog
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND data_source_id=? AND tenant_id=?", logID, ds.ID, ds.TenantID).Take(&log).Error; err != nil {
			return err
		}
		if log.Status == types.SyncLogStatusSuccess || log.Status == types.SyncLogStatusCanceled {
			return nil
		}
		var run sourceSyncRunRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_runs").Where("sync_log_id=?", logID).Take(&run).Error; err != nil {
			if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			// Compatibility for pre-coordinator tasks already persisted in
			// sync_logs: accept one orphan only when there is no newer trigger.
			run = sourceSyncRunRow{SyncLogID: logID, DataSourceID: ds.ID, TenantID: ds.TenantID, ConfigGeneration: state.ConfigGeneration, DeliveryGeneration: deliveryGeneration, Trigger: "recovery", Phase: "queued"}
			if err := tx.Exec(`INSERT INTO source_sync_runs(sync_log_id,data_source_id,tenant_id,config_generation,delivery_generation,trigger,phase,updated_at)
				VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(sync_log_id) DO NOTHING`, logID, ds.ID, ds.TenantID, run.ConfigGeneration, run.DeliveryGeneration, run.Trigger, run.Phase, time.Now().UTC()).Error; err != nil {
				return err
			}
		}
		if run.ConfigGeneration != state.ConfigGeneration {
			return cancelSourceRunTx(tx, logID, "stale source trigger generation")
		}
		// A delivery is only a wake-up hint. Coalescing can advance the current
		// generation while older queue messages are still in flight; those
		// messages must not cancel the durable run that the newer generation
		// represents.
		if deliveryGeneration > 0 && deliveryGeneration != run.DeliveryGeneration {
			return nil
		}
		now := time.Now().UTC()
		activeID := derefSourceID(state.ActiveSyncLogID)
		leaseExpired := state.LeaseExpiresAt == nil || !state.LeaseExpiresAt.After(now)
		resumingActive := activeID == logID && leaseExpired
		if activeID != "" && !resumingActive {
			if !leaseExpired {
				if activeID == logID {
					// Duplicate queue deliveries for the already-running task are
					// not new triggers and must not advance its delivery generation.
					return nil
				}
				return r.coalesceSourceTriggerTx(tx, state, &run, &log)
			}
			// An expired older run is recovered before its pending catch-up run.
			if activeID != logID {
				return nil
			}
		}
		pendingID := derefSourceID(state.PendingSyncLogID)
		if !resumingActive && pendingID != "" && pendingID != logID {
			var pending types.SyncLog
			if err := tx.Where("id=?", pendingID).Take(&pending).Error; err == nil && pending.CreatedAt.After(log.CreatedAt) {
				return cancelSourceRunTx(tx, logID, "a newer source trigger is waiting")
			}
			if err := cancelSourceRunTx(tx, pendingID, "a newer source trigger is waiting"); err != nil {
				return err
			}
			state.PendingSyncLogID = stringPointer(logID)
			state.PendingDeliveryGeneration++
			run.DeliveryGeneration = state.PendingDeliveryGeneration
			if err := tx.Table("source_sync_runs").Where("sync_log_id=?", logID).Update("delivery_generation", run.DeliveryGeneration).Error; err != nil {
				return err
			}
		}
		if activeID == "" && pendingID == "" {
			state.PendingSyncLogID = stringPointer(logID)
			if state.PendingDeliveryGeneration == 0 {
				state.PendingDeliveryGeneration = 1
			}
		}
		state.FencingToken++
		expires := now.Add(leaseTTL)
		state.LeaseOwner = stringPointer(owner)
		state.LeaseExpiresAt = &expires
		state.ActiveSyncLogID = stringPointer(logID)
		if derefSourceID(state.PendingSyncLogID) == logID {
			state.PendingSyncLogID = nil
			state.PendingTrigger = ""
		}
		if err := sourceStateUpdate(tx, state, map[string]any{
			"fencing_token": state.FencingToken, "lease_owner": owner, "lease_expires_at": expires,
			"active_sync_log_id": logID, "pending_sync_log_id": state.PendingSyncLogID,
			"pending_trigger": state.PendingTrigger,
		}); err != nil {
			return err
		}
		if err := tx.Table("source_sync_runs").Where("sync_log_id=?", logID).Updates(map[string]any{
			"fencing_token": state.FencingToken, "phase": gorm.Expr("CASE WHEN phase IN ('queued','retry_wait') THEN 'running' ELSE phase END"), "updated_at": now,
		}).Error; err != nil {
			return err
		}
		if err := tx.Model(&types.SyncLog{}).Where("id=?", logID).Updates(map[string]any{
			"status": types.SyncLogStatusRunning, "finished_at": nil, "source_config_generation": state.ConfigGeneration,
			"source_fencing_token": state.FencingToken, "updated_at": now,
		}).Error; err != nil {
			return err
		}
		lease = types.SourceSyncLease{DataSourceID: ds.ID, TenantID: ds.TenantID, SyncLogID: logID, Owner: owner,
			ConfigGeneration: state.ConfigGeneration, FencingToken: state.FencingToken, ExpiresAt: expires, TargetCommitSHA: run.TargetCommitSHA}
		claimed = true
		return nil
	})
	if errors.Is(err, errSourceConfigurationNotCurrent) {
		return types.SourceSyncLease{}, false, nil
	}
	return lease, claimed, err
}

func (r *SyncLogRepository) coalesceSourceTriggerTx(tx *gorm.DB, state *sourceSyncStateRow, run *sourceSyncRunRow, log *types.SyncLog) error {
	pendingID := derefSourceID(state.PendingSyncLogID)
	if pendingID == log.ID {
		return nil
	}
	if pendingID != "" {
		var pending types.SyncLog
		if err := tx.Where("id=?", pendingID).Take(&pending).Error; err == nil && !log.CreatedAt.After(pending.CreatedAt) {
			return cancelSourceRunTx(tx, log.ID, "a newer source trigger is already waiting")
		}
		if err := cancelSourceRunTx(tx, pendingID, "a newer source trigger is waiting"); err != nil {
			return err
		}
	}
	state.PendingDeliveryGeneration++
	state.PendingSyncLogID = stringPointer(log.ID)
	state.PendingTrigger = run.Trigger
	if err := tx.Table("source_sync_runs").Where("sync_log_id=?", log.ID).Updates(map[string]any{
		"delivery_generation": state.PendingDeliveryGeneration, "updated_at": time.Now().UTC(),
	}).Error; err != nil {
		return err
	}
	return sourceStateUpdate(tx, state, map[string]any{
		"pending_sync_log_id": log.ID, "pending_delivery_generation": state.PendingDeliveryGeneration, "pending_trigger": state.PendingTrigger,
	})
}

func (r *SyncLogRepository) RenewSourceRun(ctx context.Context, lease types.SourceSyncLease, leaseTTL time.Duration) (types.SourceSyncLease, error) {
	if leaseTTL <= 0 {
		leaseTTL = sourceRunLeaseTTL
	}
	now := time.Now().UTC()
	expires := now.Add(leaseTTL)
	result := r.db.WithContext(ctx).Table("source_sync_states").Where(`data_source_id=? AND tenant_id=? AND active_sync_log_id=?
		AND lease_owner=? AND config_generation=? AND fencing_token=? AND lease_expires_at>?`,
		lease.DataSourceID, lease.TenantID, lease.SyncLogID, lease.Owner, lease.ConfigGeneration, lease.FencingToken, now).
		Updates(map[string]any{"lease_expires_at": expires, "updated_at": now})
	if result.Error != nil {
		return lease, result.Error
	}
	if result.RowsAffected != 1 {
		return lease, types.ErrSourceSyncLeaseLost
	}
	lease.ExpiresAt = expires
	return lease, nil
}

func (r *SyncLogRepository) ReleaseSourceRun(ctx context.Context, lease types.SourceSyncLease, retry bool) (*types.SourceSyncDispatch, error) {
	var dispatch *types.SourceSyncDispatch
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state sourceSyncStateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").Where("data_source_id=?", lease.DataSourceID).Take(&state).Error; err != nil {
			return err
		}
		if !sourceLeaseMatches(&state, lease, time.Now().UTC()) {
			return types.ErrSourceSyncLeaseLost
		}
		var run sourceSyncRunRow
		if err := tx.Table("source_sync_runs").Where("sync_log_id=?", lease.SyncLogID).Take(&run).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if retry && run.RetryCount < sourceRunMaxRetries {
			run.RetryCount++
			run.DeliveryGeneration++
			if err := tx.Table("source_sync_runs").Where("sync_log_id=?", lease.SyncLogID).Updates(map[string]any{
				"retry_count": run.RetryCount, "delivery_generation": run.DeliveryGeneration, "phase": "retry_wait", "updated_at": now,
			}).Error; err != nil {
				return err
			}
			state.LeaseOwner, state.LeaseExpiresAt = nil, nil
			if err := sourceStateUpdate(tx, &state, map[string]any{"lease_owner": nil, "lease_expires_at": nil}); err != nil {
				return err
			}
			if err := tx.Model(&types.SyncLog{}).Where("id=? AND source_fencing_token=?", lease.SyncLogID, lease.FencingToken).Updates(map[string]any{
				"status": types.SyncLogStatusQueued, "finished_at": nil, "source_fencing_token": 0, "updated_at": now,
			}).Error; err != nil {
				return err
			}
			dispatch = &types.SourceSyncDispatch{Trigger: run.Trigger, DeliveryGeneration: run.DeliveryGeneration}
			if err := tx.Where("id=?", lease.SyncLogID).Take(&dispatch.SyncLog).Error; err != nil {
				return err
			}
			return nil
		}
		if retry {
			if err := tx.Model(&types.SyncLog{}).Where("id=?", lease.SyncLogID).Updates(map[string]any{
				"status": types.SyncLogStatusFailed, "finished_at": now, "error_message": "source retry budget exhausted; retry manually", "updated_at": now,
			}).Error; err != nil {
				return err
			}
			if err := tx.Table("source_sync_runs").Where("sync_log_id=?", lease.SyncLogID).Updates(map[string]any{"phase": "failed", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		if err := tx.Table("source_sync_runs").Where("sync_log_id=? AND phase!='published'", lease.SyncLogID).
			Updates(map[string]any{"phase": "failed", "updated_at": now}).Error; err != nil {
			return err
		}
		state.ActiveSyncLogID, state.LeaseOwner, state.LeaseExpiresAt = nil, nil, nil
		if err := sourceStateUpdate(tx, &state, map[string]any{"active_sync_log_id": nil, "lease_owner": nil, "lease_expires_at": nil}); err != nil {
			return err
		}
		pending, err := pendingDispatchTx(tx, &state)
		dispatch = pending
		return err
	})
	return dispatch, err
}

func pendingDispatchTx(tx *gorm.DB, state *sourceSyncStateRow) (*types.SourceSyncDispatch, error) {
	id := derefSourceID(state.PendingSyncLogID)
	if id == "" {
		return nil, nil
	}
	var log types.SyncLog
	if err := tx.Where("id=? AND status=?", id, types.SyncLogStatusQueued).Take(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &types.SourceSyncDispatch{SyncLog: &log, Trigger: state.PendingTrigger, DeliveryGeneration: state.PendingDeliveryGeneration}, nil
}

// RecoverAllSourceTriggers recovers durable source work independently of the
// datasource's cron status. Error and paused sources may still own queued
// retry work, while only active sources are returned by FindActive for cron.
func (r *SyncLogRepository) RecoverAllSourceTriggers(ctx context.Context) ([]types.SourceSyncDispatch, error) {
	var sources []types.DataSource
	if err := r.db.WithContext(ctx).Model(&types.DataSource{}).
		Joins(`JOIN source_sync_states AS source_state
			ON source_state.data_source_id = data_sources.id
			AND source_state.tenant_id = data_sources.tenant_id`).
		Where("(source_state.active_sync_log_id IS NOT NULL OR source_state.pending_sync_log_id IS NOT NULL)").
		Order("data_sources.id ASC").Find(&sources).Error; err != nil {
		return nil, err
	}
	dispatches := make([]types.SourceSyncDispatch, 0, len(sources))
	for i := range sources {
		recovered, err := r.RecoverSourceTriggers(ctx, &sources[i])
		if err != nil {
			return dispatches, err
		}
		dispatches = append(dispatches, recovered...)
	}
	return dispatches, nil
}

func (r *SyncLogRepository) RecoverSourceTriggers(ctx context.Context, ds *types.DataSource) ([]types.SourceSyncDispatch, error) {
	if ds == nil || ds.ID == "" {
		return nil, errors.New("source data source identity is required")
	}
	dispatches := []types.SourceSyncDispatch{}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var exists bool
		if err := tx.Raw("SELECT EXISTS(SELECT 1 FROM source_sync_states WHERE data_source_id=? AND tenant_id=?)", ds.ID, ds.TenantID).Scan(&exists).Error; err != nil {
			return err
		}
		if !exists {
			// Legacy queued source sync logs predate this coordinator.
			var current types.DataSource
			if err := tx.Where("id=? AND tenant_id=?", ds.ID, ds.TenantID).Take(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			var logs []types.SyncLog
			if err := tx.Where("data_source_id=? AND tenant_id=? AND status=?", ds.ID, ds.TenantID, types.SyncLogStatusQueued).Order("created_at DESC").Limit(1).Find(&logs).Error; err != nil {
				return err
			}
			if len(logs) == 0 {
				return nil
			}
			state, err := r.ensureSourceSyncState(tx, &current)
			if err != nil {
				return err
			}
			persisted, err := lockPersistedSourceDataSource(tx, ds.ID, ds.TenantID)
			if err != nil {
				return err
			}
			if !sourceStateMatchesPersistedDataSource(state, persisted) {
				return errSourceConfigurationNotCurrent
			}
			state.PendingSyncLogID = stringPointer(logs[0].ID)
			state.PendingTrigger = "recovery"
			state.PendingDeliveryGeneration = 1
			if err := tx.Exec(`INSERT INTO source_sync_runs(sync_log_id,data_source_id,tenant_id,config_generation,delivery_generation,trigger,phase,updated_at)
				VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(sync_log_id) DO NOTHING`, logs[0].ID, ds.ID, ds.TenantID, state.ConfigGeneration, 1, "recovery", "queued", time.Now().UTC()).Error; err != nil {
				return err
			}
			if err := sourceStateUpdate(tx, state, map[string]any{"pending_sync_log_id": logs[0].ID, "pending_trigger": "recovery", "pending_delivery_generation": 1}); err != nil {
				return err
			}
			dispatches = append(dispatches, types.SourceSyncDispatch{SyncLog: &logs[0], Trigger: "recovery", DeliveryGeneration: 1})
			return nil
		}
		var state sourceSyncStateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").Where("data_source_id=? AND tenant_id=?", ds.ID, ds.TenantID).Take(&state).Error; err != nil {
			return err
		}
		current, err := lockPersistedSourceDataSource(tx, ds.ID, ds.TenantID)
		if err != nil {
			return err
		}
		if !sourceStateMatchesPersistedDataSource(&state, current) {
			return errSourceConfigurationNotCurrent
		}
		if err := r.invalidateSourceGeneration(tx, &state, current); err != nil {
			return err
		}
		activeID := derefSourceID(state.ActiveSyncLogID)
		if activeID != "" && (state.LeaseExpiresAt == nil || !state.LeaseExpiresAt.After(time.Now().UTC())) {
			var log types.SyncLog
			if err := tx.Where("id=?", activeID).Take(&log).Error; err != nil {
				return err
			}
			var run sourceSyncRunRow
			if err := tx.Table("source_sync_runs").Where("sync_log_id=?", activeID).Take(&run).Error; err != nil {
				return err
			}
			if log.Status == types.SyncLogStatusSuccess || log.Status == types.SyncLogStatusCanceled || run.RetryCount >= sourceRunMaxRetries {
				state.ActiveSyncLogID, state.LeaseOwner, state.LeaseExpiresAt = nil, nil, nil
				if log.Status != types.SyncLogStatusSuccess && log.Status != types.SyncLogStatusCanceled {
					now := time.Now().UTC()
					_ = tx.Model(&types.SyncLog{}).Where("id=?", activeID).Updates(map[string]any{"status": types.SyncLogStatusFailed, "finished_at": now, "error_message": "source retry budget exhausted; retry manually"}).Error
				}
				if err := sourceStateUpdate(tx, &state, map[string]any{"active_sync_log_id": nil, "lease_owner": nil, "lease_expires_at": nil}); err != nil {
					return err
				}
				pending, err := pendingDispatchTx(tx, &state)
				if err != nil {
					return err
				}
				if pending != nil {
					dispatches = append(dispatches, *pending)
				}
				return nil
			}
			if state.LeaseExpiresAt == nil {
				dispatches = append(dispatches, types.SourceSyncDispatch{SyncLog: &log, Trigger: run.Trigger, DeliveryGeneration: run.DeliveryGeneration})
				return nil
			}
			now := time.Now().UTC()
			run.RetryCount++
			run.DeliveryGeneration++
			state.FencingToken++ // invalidate the previous owner before re-dispatch
			state.LeaseOwner, state.LeaseExpiresAt = nil, nil
			if err := sourceStateUpdate(tx, &state, map[string]any{"fencing_token": state.FencingToken, "lease_owner": nil, "lease_expires_at": nil}); err != nil {
				return err
			}
			if err := tx.Table("source_sync_runs").Where("sync_log_id=?", activeID).Updates(map[string]any{"retry_count": run.RetryCount, "delivery_generation": run.DeliveryGeneration, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Model(&types.SyncLog{}).Where("id=?", activeID).Updates(map[string]any{"status": types.SyncLogStatusQueued, "finished_at": nil, "source_fencing_token": 0, "updated_at": now}).Error; err != nil {
				return err
			}
			dispatches = append(dispatches, types.SourceSyncDispatch{SyncLog: &log, Trigger: run.Trigger, DeliveryGeneration: run.DeliveryGeneration})
			return nil
		}
		pending, err := pendingDispatchTx(tx, &state)
		if err != nil {
			return err
		}
		if pending != nil {
			dispatches = append(dispatches, *pending)
		}
		return nil
	})
	if errors.Is(err, errSourceConfigurationNotCurrent) {
		return nil, nil
	}
	return dispatches, err
}

func (r *SyncLogRepository) RecordSourceRunPhase(ctx context.Context, lease types.SourceSyncLease, phase, targetSHA string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state sourceSyncStateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").Where("data_source_id=?", lease.DataSourceID).Take(&state).Error; err != nil {
			return err
		}
		if !sourceLeaseMatches(&state, lease, time.Now().UTC()) {
			return types.ErrSourceSyncLeaseLost
		}
		values := map[string]any{"phase": phase, "updated_at": time.Now().UTC()}
		if targetSHA != "" {
			var existing string
			if err := tx.Table("source_sync_runs").Select("target_commit_sha").Where("sync_log_id=?", lease.SyncLogID).Scan(&existing).Error; err != nil {
				return err
			}
			if existing != "" && existing != targetSHA {
				return fmt.Errorf("source target commit changed during recovery")
			}
			values["target_commit_sha"] = targetSHA
		}
		values["completed_stages"] = gorm.Expr(`CASE WHEN phase = ? THEN completed_stages ELSE completed_stages || jsonb_build_array(phase) END`, phase)
		return tx.Table("source_sync_runs").Where("sync_log_id=? AND config_generation=? AND fencing_token=?", lease.SyncLogID, lease.ConfigGeneration, lease.FencingToken).Updates(values).Error
	})
}

func (r *SyncLogRepository) CommitSourceRunResult(ctx context.Context, lease types.SourceSyncLease, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var state sourceSyncStateRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").Where("data_source_id=?", lease.DataSourceID).Take(&state).Error; err != nil {
			return types.ErrSourceSyncLeaseLost
		}
		if !sourceLeaseMatches(&state, lease, time.Now().UTC()) {
			return types.ErrSourceSyncLeaseLost
		}
		var current types.DataSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=?", ds.ID, ds.TenantID).Take(&current).Error; err != nil {
			return err
		}
		status := ds.Status
		if current.Status == types.DataSourceStatusPaused {
			status = types.DataSourceStatusPaused
		}
		updates := map[string]any{"status": status, "error_message": ds.ErrorMessage, "last_sync_result": ds.LastSyncResult, "updated_at": time.Now().UTC()}
		if ds.LastSyncAt != nil {
			updates["last_sync_at"] = ds.LastSyncAt
		}
		return tx.Model(&types.DataSource{}).Where("id=? AND tenant_id=?", ds.ID, ds.TenantID).Updates(updates).Error
	})
}

func assertSourceLeaseTx(tx *gorm.DB, ctx context.Context) error {
	lease, ok := types.SourceSyncLeaseFromContext(ctx)
	if !ok {
		return nil // Preserve explicit legacy/internal test entry points.
	}
	var state sourceSyncStateRow
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").
		Where("data_source_id=? AND tenant_id=?", lease.DataSourceID, lease.TenantID).Take(&state).Error; err != nil {
		return types.ErrSourceSyncLeaseLost
	}
	if !sourceLeaseMatches(&state, lease, time.Now().UTC()) {
		return types.ErrSourceSyncLeaseLost
	}
	return nil
}

func sourceLeaseMatches(state *sourceSyncStateRow, lease types.SourceSyncLease, now time.Time) bool {
	return state != nil && state.ConfigGeneration == lease.ConfigGeneration && state.FencingToken == lease.FencingToken &&
		state.LeaseExpiresAt != nil && state.LeaseExpiresAt.After(now) && derefSourceID(state.LeaseOwner) == lease.Owner &&
		derefSourceID(state.ActiveSyncLogID) == lease.SyncLogID
}

func derefSourceID(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func stringPointer(value string) *string { return &value }
