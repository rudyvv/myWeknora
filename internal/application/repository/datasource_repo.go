package repository

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// DataSourceRepository provides data access for data sources
type DataSourceRepository struct {
	db *gorm.DB
}

func (r *DataSourceRepository) SaveSourceProjectGroup(ctx context.Context, expected, members []*types.DataSource) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		existing := make(map[string]bool, len(expected))
		currentRows := make(map[string]*types.DataSource, len(expected))
		for _, before := range expected {
			var current types.DataSource
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND deleted_at IS NULL", before.ID).Take(&current).Error; err != nil {
				return err
			}
			if current.TenantID != before.TenantID || current.KnowledgeBaseID != before.KnowledgeBaseID ||
				current.SourceBindingState != before.SourceBindingState || current.SourceQueryEnabled != before.SourceQueryEnabled ||
				current.Status != before.Status || !bytes.Equal(current.Config, before.Config) ||
				current.SyncSchedule != before.SyncSchedule || current.SyncMode != before.SyncMode ||
				current.SyncDeletions != before.SyncDeletions || current.Name != before.Name {
				return errSourceConfigurationNotCurrent
			}
			existing[before.ID] = true
			currentRows[before.ID] = &current
		}
		repo := &DataSourceRepository{db: tx}
		for _, member := range members {
			var err error
			if existing[member.ID] {
				if err = validateSourceLifecycleUpdate(currentRows[member.ID], member); err != nil {
					return err
				}
				// Configuration changes must not overwrite a run's cursor,
				// timestamps or results if it finished since the form was loaded.
				fields := map[string]interface{}{"name": member.Name, "type": member.Type, "config": member.Config,
					"sync_schedule": member.SyncSchedule, "sync_mode": member.SyncMode, "status": member.Status,
					"conflict_strategy": member.ConflictStrategy, "sync_deletions": member.SyncDeletions}
				if member.SyncLogRetentionDays > 0 {
					fields["sync_log_retention_days"] = member.SyncLogRetentionDays
				}
				err = tx.Model(&types.DataSource{}).Where("id = ?", member.ID).Updates(fields).Error
			} else {
				err = repo.Create(ctx, member)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

// NewDataSourceRepository creates a new data source repository
func NewDataSourceRepository(db *gorm.DB) interfaces.DataSourceRepository {
	return &DataSourceRepository{db: db}
}

// Create inserts a new data source record
func (r *DataSourceRepository) Create(ctx context.Context, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is nil")
	}
	// GORM treats false as the zero value of bool. For a field tagged
	// default:true it replaces both the INSERT value and the in-memory field
	// with true, so a caller-selected false would be lost. Capture it, force
	// the column write, then restore the struct so Create's return value (and
	// the HTTP 201 body) match the database.
	syncDeletions := ds.SyncDeletions
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(ds).Error; err != nil {
			return err
		}
		return tx.Model(&types.DataSource{}).
			Where("id = ?", ds.ID).
			UpdateColumn("sync_deletions", syncDeletions).Error
	})
	ds.SyncDeletions = syncDeletions
	return err
}

// FindByID retrieves a data source by ID
func (r *DataSourceRepository) FindByID(ctx context.Context, id string) (*types.DataSource, error) {
	if id == "" {
		return nil, errors.New("id is empty")
	}
	var ds types.DataSource
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Where("deleted_at IS NULL").
		First(&ds).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("data source not found")
		}
		return nil, err
	}
	return &ds, nil
}

// FindByKnowledgeBase lists all data sources for a knowledge base
func (r *DataSourceRepository) FindByKnowledgeBase(ctx context.Context, kbID string) ([]*types.DataSource, error) {
	if kbID == "" {
		return nil, errors.New("knowledge base id is empty")
	}
	var dataSources []*types.DataSource
	if err := r.db.WithContext(ctx).
		Where("knowledge_base_id = ?", kbID).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Find(&dataSources).Error; err != nil {
		return nil, err
	}
	return dataSources, nil
}

// Update updates an existing data source
func (r *DataSourceRepository) Update(ctx context.Context, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is nil")
	}
	if ds.ID == "" {
		return errors.New("data source id is empty")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var current types.DataSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", ds.ID).Take(&current).Error; err != nil {
			return err
		}
		if sourceLifecycleUpdateManaged(&current, ds) {
			if err := validateSourceLifecycleUpdate(&current, ds); err != nil {
				return err
			}
			result := tx.Model(&types.DataSource{}).
				Where("id=? AND tenant_id=? AND source_binding_state=? AND source_query_enabled=?",
					current.ID, current.TenantID, current.SourceBindingState, current.SourceQueryEnabled).
				Updates(ds)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return errSourceConfigurationNotCurrent
			}
		} else if err := tx.Model(ds).Updates(ds).Error; err != nil {
			return err
		}
		// GORM Updates(struct) deliberately skips zero values, which would make
		// a user-selected sync_deletions=false impossible to persist.
		return tx.Model(&types.DataSource{}).
			Where("id = ?", ds.ID).
			UpdateColumn("sync_deletions", ds.SyncDeletions).Error
	})
}

func sourceLifecycleUpdateManaged(current, incoming *types.DataSource) bool {
	if current == nil || incoming == nil {
		return false
	}
	return sourceModeEnabled(current) || sourceModeEnabled(incoming) ||
		current.SourceBindingState == types.SourceBindingUnbound ||
		incoming.SourceBindingState == types.SourceBindingUnbound
}

func validateSourceLifecycleUpdate(current, incoming *types.DataSource) error {
	if current == nil || incoming == nil || current.ID != incoming.ID ||
		current.TenantID != incoming.TenantID || current.KnowledgeBaseID != incoming.KnowledgeBaseID ||
		current.SourceBindingState != incoming.SourceBindingState ||
		current.SourceQueryEnabled != incoming.SourceQueryEnabled {
		return errSourceConfigurationNotCurrent
	}

	terminal := current.SourceBindingState == types.SourceBindingUnbound ||
		(sourceModeEnabled(current) && !current.SourceQueryEnabled)
	if !terminal {
		return nil
	}
	if current.Type != incoming.Type {
		return errSourceConfigurationNotCurrent
	}
	incomingSourceMode := sourceModeEnabled(incoming)
	if len(incoming.Config) == 0 {
		incomingSourceMode = sourceModeEnabled(current)
	}
	if incomingSourceMode != sourceModeEnabled(current) {
		return errSourceConfigurationNotCurrent
	}
	if len(incoming.Config) > 0 {
		config, err := incoming.ParseConfig()
		if err != nil || config != nil && config.HasConfiguredCredentials(incoming.Type) {
			return errSourceConfigurationNotCurrent
		}
	}
	return nil
}

// UpdateSyncState updates only fields managed by sync execution. GORM's
// Updates(struct) skips zero values, so use a map here to persist cleared error
// messages without broadening the generic Update method.
func (r *DataSourceRepository) UpdateSyncState(ctx context.Context, ds *types.DataSource) error {
	if ds == nil {
		return errors.New("data source is nil")
	}
	if ds.ID == "" {
		return errors.New("data source id is empty")
	}
	if err := r.db.WithContext(ctx).
		Model(&types.DataSource{}).
		Where("id = ?", ds.ID).
		Updates(map[string]interface{}{
			"status":           ds.Status,
			"last_sync_at":     ds.LastSyncAt,
			"last_sync_cursor": ds.LastSyncCursor,
			"last_sync_result": ds.LastSyncResult,
			"error_message":    ds.ErrorMessage,
			"updated_at":       time.Now().UTC(),
		}).Error; err != nil {
		return err
	}
	return nil
}

// Delete performs a soft delete
func (r *DataSourceRepository) Delete(ctx context.Context, id string) error {
	if id == "" {
		return errors.New("id is empty")
	}
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		Delete(&types.DataSource{}).Error; err != nil {
		return err
	}
	return nil
}

// FindActive retrieves all active data sources (used for scheduling)
func (r *DataSourceRepository) FindActive(ctx context.Context) ([]*types.DataSource, error) {
	var dataSources []*types.DataSource
	if err := r.db.WithContext(ctx).
		Where("status = ?", types.DataSourceStatusActive).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Find(&dataSources).Error; err != nil {
		return nil, err
	}
	return dataSources, nil
}

// FindErrorGitLabSources retrieves GitLab sources whose last sync failed.
// Callers must still validate source mode and credentials before scheduling.
func (r *DataSourceRepository) FindErrorGitLabSources(ctx context.Context) ([]*types.DataSource, error) {
	var dataSources []*types.DataSource
	if err := r.db.WithContext(ctx).
		Where("status = ? AND type = ?", types.DataSourceStatusError, types.ConnectorTypeGitLab).
		Where("deleted_at IS NULL").
		Order("created_at DESC").
		Find(&dataSources).Error; err != nil {
		return nil, err
	}
	return dataSources, nil
}

// SyncLogRepository provides data access for sync logs
type SyncLogRepository struct {
	db *gorm.DB
}

// NewSyncLogRepository creates a new sync log repository
func NewSyncLogRepository(db *gorm.DB) interfaces.SyncLogRepository {
	return &SyncLogRepository{db: db}
}

// Create inserts a new sync log entry
func (r *SyncLogRepository) Create(ctx context.Context, log *types.SyncLog) error {
	if log == nil {
		return errors.New("sync log is nil")
	}
	if err := r.db.WithContext(ctx).Create(log).Error; err != nil {
		return err
	}
	return nil
}

// FindByID retrieves a sync log by ID
func (r *SyncLogRepository) FindByID(ctx context.Context, id string) (*types.SyncLog, error) {
	if id == "" {
		return nil, errors.New("id is empty")
	}
	var log types.SyncLog
	if err := r.db.WithContext(ctx).
		Where("id = ?", id).
		First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, errors.New("sync log not found")
		}
		return nil, err
	}
	if err := r.attachSourceRunPhases(ctx, []*types.SyncLog{&log}); err != nil {
		return nil, err
	}
	return &log, nil
}

// FindByDataSource lists sync logs for a data source with pagination
func (r *SyncLogRepository) FindByDataSource(ctx context.Context, dsID string, limit int, offset int) ([]*types.SyncLog, error) {
	if dsID == "" {
		return nil, errors.New("data source id is empty")
	}
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	var logs []*types.SyncLog
	if err := r.db.WithContext(ctx).
		Where("data_source_id = ?", dsID).
		Order("started_at DESC").
		Limit(limit).
		Offset(offset).
		Find(&logs).Error; err != nil {
		return nil, err
	}
	if err := r.attachSourceRunPhases(ctx, logs); err != nil {
		return nil, err
	}
	return logs, nil
}

// FindLatest retrieves the most recent sync log for a data source
func (r *SyncLogRepository) FindLatest(ctx context.Context, dsID string) (*types.SyncLog, error) {
	if dsID == "" {
		return nil, errors.New("data source id is empty")
	}
	var log types.SyncLog
	if err := r.db.WithContext(ctx).
		Where("data_source_id = ?", dsID).
		Order("started_at DESC").
		Limit(1).
		First(&log).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if err := r.attachSourceRunPhases(ctx, []*types.SyncLog{&log}); err != nil {
		return nil, err
	}
	return &log, nil
}

// attachSourceRunPhases adds coordinator state to public sync-log responses.
// Older test databases and installations without the source-run migration
// retain their historical response shape.
func (r *SyncLogRepository) attachSourceRunPhases(ctx context.Context, logs []*types.SyncLog) error {
	if len(logs) == 0 || !r.db.Migrator().HasTable("source_sync_runs") {
		return nil
	}
	ids := make([]string, 0, len(logs))
	for _, log := range logs {
		if log != nil && log.ID != "" {
			ids = append(ids, log.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	var phases []struct {
		SyncLogID string `gorm:"column:sync_log_id"`
		Phase     string `gorm:"column:phase"`
	}
	if err := r.db.WithContext(ctx).Table("source_sync_runs").Select("sync_log_id, phase").Where("sync_log_id IN ?", ids).Scan(&phases).Error; err != nil {
		return err
	}
	byID := make(map[string]string, len(phases))
	for _, phase := range phases {
		byID[phase.SyncLogID] = phase.Phase
	}
	for _, log := range logs {
		if log != nil {
			log.SourceRunPhase = byID[log.ID]
		}
	}
	return nil
}

// HasRunningSync checks if a data source has any sync currently in "running" status.
func (r *SyncLogRepository) HasRunningSync(ctx context.Context, dsID string) (bool, error) {
	if dsID == "" {
		return false, errors.New("data source id is empty")
	}
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&types.SyncLog{}).
		Where("data_source_id = ?", dsID).
		Where("status = ?", types.SyncLogStatusRunning).
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Update updates an existing sync log entry
func (r *SyncLogRepository) Update(ctx context.Context, log *types.SyncLog) error {
	if log == nil {
		return errors.New("sync log is nil")
	}
	if log.ID == "" {
		return errors.New("sync log id is empty")
	}
	if err := r.db.WithContext(ctx).
		Model(log).
		Updates(log).Error; err != nil {
		return err
	}
	return nil
}

// UpdateResult updates only fields produced by sync execution. Use an explicit
// map so empty error messages are written when a later sync succeeds.
func (r *SyncLogRepository) UpdateResult(ctx context.Context, log *types.SyncLog) error {
	if log == nil {
		return errors.New("sync log is nil")
	}
	if log.ID == "" {
		return errors.New("sync log id is empty")
	}
	lease, hasLease := types.SourceSyncLeaseFromContext(ctx)
	if hasLease && (lease.SyncLogID != log.ID || lease.DataSourceID != log.DataSourceID || lease.TenantID != log.TenantID) {
		return types.ErrSourceSyncLeaseLost
	}
	guarded := hasLease || log.SourceFencingToken > 0
	updates := map[string]interface{}{
		"status":        log.Status,
		"finished_at":   log.FinishedAt,
		"items_total":   log.ItemsTotal,
		"items_created": log.ItemsCreated,
		"items_updated": log.ItemsUpdated,
		"items_deleted": log.ItemsDeleted,
		"items_skipped": log.ItemsSkipped,
		"items_failed":  log.ItemsFailed,
		"error_message": log.ErrorMessage,
		"result":        log.Result,
		"updated_at":    time.Now().UTC(),
	}
	if guarded {
		generation, fencingToken := log.SourceConfigGeneration, log.SourceFencingToken
		if hasLease {
			generation, fencingToken = lease.ConfigGeneration, lease.FencingToken
		}
		return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var state sourceSyncStateRow
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_sync_states").
				Where("data_source_id=? AND tenant_id=?", log.DataSourceID, log.TenantID).Take(&state).Error; err != nil {
				return types.ErrSourceSyncLeaseLost
			}
			now := time.Now().UTC()
			if hasLease {
				if !sourceLeaseMatches(&state, lease, now) {
					return types.ErrSourceSyncLeaseLost
				}
			} else if state.ConfigGeneration != generation || state.FencingToken != fencingToken ||
				derefSourceID(state.ActiveSyncLogID) != log.ID || state.LeaseExpiresAt == nil ||
				!state.LeaseExpiresAt.After(now) || derefSourceID(state.LeaseOwner) == "" {
				return types.ErrSourceSyncLeaseLost
			}

			var current types.SyncLog
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id=? AND data_source_id=? AND tenant_id=?", log.ID, log.DataSourceID, log.TenantID).
				Take(&current).Error; err != nil {
				return types.ErrSourceSyncLeaseLost
			}
			if current.SourceConfigGeneration != generation || current.SourceFencingToken != fencingToken {
				return types.ErrSourceSyncLeaseLost
			}
			// The sync-log lock may have waited behind another transaction; the
			// state row is still locked, so check expiry again immediately before
			// applying the result.
			now = time.Now().UTC()
			if hasLease {
				if !sourceLeaseMatches(&state, lease, now) {
					return types.ErrSourceSyncLeaseLost
				}
			} else if state.ConfigGeneration != generation || state.FencingToken != fencingToken ||
				derefSourceID(state.ActiveSyncLogID) != log.ID || state.LeaseExpiresAt == nil ||
				!state.LeaseExpiresAt.After(now) || derefSourceID(state.LeaseOwner) == "" {
				return types.ErrSourceSyncLeaseLost
			}
			result := tx.Model(&types.SyncLog{}).
				Where("id=? AND data_source_id=? AND tenant_id=?", log.ID, log.DataSourceID, log.TenantID).
				Updates(updates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return types.ErrSourceSyncLeaseLost
			}
			return nil
		})
	}
	return r.db.WithContext(ctx).Model(&types.SyncLog{}).Where("id = ?", log.ID).Updates(updates).Error
}

// CancelPendingByDataSource marks all non-terminal sync logs for a data source as canceled.
func (r *SyncLogRepository) CancelPendingByDataSource(ctx context.Context, dsID string) error {
	if dsID == "" {
		return errors.New("data source id is empty")
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).
		Model(&types.SyncLog{}).
		Where("data_source_id = ?", dsID).
		Where("status IN ?", []string{types.SyncLogStatusRunning, "pending"}).
		Updates(map[string]interface{}{
			"status":        types.SyncLogStatusCanceled,
			"finished_at":   &now,
			"error_message": "data source deleted",
		}).Error
}

// CleanupOldLogs deletes sync logs older than the retention period
func (r *SyncLogRepository) CleanupOldLogs(ctx context.Context, retentionDays int) error {
	if retentionDays <= 0 {
		retentionDays = 30
	}
	// Delete logs older than the retention period
	if err := r.db.WithContext(ctx).
		Where("started_at < NOW() - INTERVAL ? DAY", retentionDays).
		Delete(&types.SyncLog{}).Error; err != nil {
		return err
	}
	return nil
}
