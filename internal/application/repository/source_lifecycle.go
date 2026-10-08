package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var errSourceCleanupNotRetryable = errors.New("source cleanup operation is not retryable")

func (r *SyncLogRepository) FindSourceCleanup(ctx context.Context, dataSourceID string) (*types.SourceCleanupOperation, error) {
	var operation types.SourceCleanupOperation
	err := r.db.WithContext(ctx).Where("data_source_id=?", dataSourceID).Take(&operation).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &operation, nil
}

func (r *SyncLogRepository) FindSourceCleanups(ctx context.Context, dataSourceIDs []string) (map[string]*types.SourceCleanupOperation, error) {
	out := make(map[string]*types.SourceCleanupOperation)
	if len(dataSourceIDs) == 0 {
		return out, nil
	}
	var operations []types.SourceCleanupOperation
	if err := r.db.WithContext(ctx).Where("data_source_id IN ?", dataSourceIDs).Find(&operations).Error; err != nil {
		return nil, err
	}
	for i := range operations {
		operation := operations[i]
		out[operation.DataSourceID] = &operation
	}
	return out, nil
}

// UnbindSourceSync fences every in-flight writer before making the source
// unbound. Query visibility is deliberately unchanged; clearing is a separate
// explicit operation.
func (r *SyncLogRepository) UnbindSourceSync(ctx context.Context, ds *types.DataSource) error {
	return r.disconnectSourceSync(ctx, ds, false)
}

// DeleteSourceConnection retires the connector without soft-deleting the read
// identity: source read/Wiki evidence joins must keep authorizing retained data.
func (r *SyncLogRepository) DeleteSourceConnection(ctx context.Context, ds *types.DataSource) error {
	return r.disconnectSourceSync(ctx, ds, true)
}

func (r *SyncLogRepository) disconnectSourceSync(ctx context.Context, ds *types.DataSource, remove bool) error {
	if ds == nil || ds.ID == "" {
		return errors.New("data source is required")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := r.ensureSourceSyncState(tx, ds)
		if err != nil {
			return err
		}
		current, err := lockSourceLifecycleDataSource(tx, ds)
		if err != nil {
			return err
		}
		if !sourceModeEnabled(current) {
			return errors.New("source lifecycle is only available for source-mode data sources")
		}
		if sourceConfigFingerprint(ds) != sourceConfigFingerprint(current) {
			return errSourceConfigurationNotCurrent
		}
		config, err := sourceConfigWithoutCredentials(current.Config)
		if err != nil {
			return err
		}
		updates := map[string]any{"source_binding_state": types.SourceBindingUnbound}
		if remove {
			updates["status"] = types.DataSourceStatusDeleted
			current.Status = types.DataSourceStatusDeleted
		}
		if string(config) != string(current.Config) {
			updates["config"] = config
			current.Config = config
		}
		if err := tx.Model(&types.DataSource{}).Where("id=? AND tenant_id=?", current.ID, current.TenantID).Updates(updates).Error; err != nil {
			return err
		}
		current.SourceBindingState = types.SourceBindingUnbound
		if err := invalidateSourceForLifecycleTx(tx, r, state, current); err != nil {
			return err
		}
		if err := disableAndClearSourceWebhookTx(tx, current.ID, current.TenantID); err != nil {
			return err
		}
		if err := lockCurrentSourcePublication(tx, current.ID); err != nil {
			return err
		}
		return supersedeSourceWikiWorkTx(tx, current, "source_unbound")
	})
}

// AcceptSourceCleanup closes source reads and writes in the same transaction
// that creates (or finds) the durable operation. The operation lock is always
// last in the coordinator → datasource → publication → operation order.
func (r *SyncLogRepository) AcceptSourceCleanup(ctx context.Context, ds *types.DataSource, scope string) (*types.SourceCleanupOperation, error) {
	if ds == nil || ds.ID == "" || scope != types.SourceCleanupScopeCurrentAndHistory {
		return nil, errors.New("source cleanup request is invalid")
	}
	var operation types.SourceCleanupOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := r.ensureSourceSyncState(tx, ds)
		if err != nil {
			return err
		}
		current, err := lockSourceLifecycleDataSource(tx, ds)
		if err != nil {
			return err
		}
		if !sourceModeEnabled(current) {
			return errors.New("source lifecycle is only available for source-mode data sources")
		}
		if sourceConfigFingerprint(ds) != sourceConfigFingerprint(current) {
			return errSourceConfigurationNotCurrent
		}
		config, err := sourceConfigWithoutCredentials(current.Config)
		if err != nil {
			return err
		}
		updates := map[string]any{"source_binding_state": types.SourceBindingUnbound, "source_query_enabled": false}
		if string(config) != string(current.Config) {
			updates["config"] = config
			current.Config = config
		}
		if err := tx.Model(&types.DataSource{}).Where("id=? AND tenant_id=?", current.ID, current.TenantID).Updates(updates).Error; err != nil {
			return err
		}
		current.SourceBindingState = types.SourceBindingUnbound
		current.SourceQueryEnabled = false
		if err := invalidateSourceForLifecycleTx(tx, r, state, current); err != nil {
			return err
		}
		if err := disableAndClearSourceWebhookTx(tx, current.ID, current.TenantID); err != nil {
			return err
		}
		if err := lockCurrentSourcePublication(tx, current.ID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("data_source_id=?", current.ID).Take(&operation).Error; err == nil {
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		now := time.Now().UTC()
		operation = types.SourceCleanupOperation{
			ID: uuid.NewString(), DataSourceID: current.ID, TenantID: current.TenantID,
			KnowledgeBaseID: current.KnowledgeBaseID, Scope: scope, Status: types.SourceCleanupPending,
			Phase: "current_wiki", Cursor: types.JSON(`{}`), FencingToken: state.FencingToken,
			CreatedAt: now, UpdatedAt: now,
		}
		return tx.Create(&operation).Error
	})
	if err != nil {
		return nil, err
	}
	return &operation, nil
}

// sourceConfigWithoutCredentials removes persisted connector secrets without
// decrypting them or touching source-selection settings and stable identity.
func sourceConfigWithoutCredentials(config types.JSON) (types.JSON, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(config, &fields); err != nil {
		return nil, fmt.Errorf("decode source configuration for credential removal: %w", err)
	}
	delete(fields, "credentials")
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, fmt.Errorf("encode source configuration after credential removal: %w", err)
	}
	return types.JSON(encoded), nil
}

func disableAndClearSourceWebhookTx(tx *gorm.DB, dataSourceID string, tenantID uint64) error {
	if !tx.Migrator().HasTable("source_gitlab_webhook_configs") {
		return nil
	}
	return tx.Table("source_gitlab_webhook_configs").Where("data_source_id=? AND tenant_id=?", dataSourceID, tenantID).
		Updates(map[string]any{
			"enabled": false, "secret_ciphertext": "", "last_received_at": nil, "last_event_id": "", "updated_at": time.Now().UTC(),
		}).Error
}

func (r *SyncLogRepository) RetrySourceCleanup(ctx context.Context, ds *types.DataSource, operationID string) (*types.SourceCleanupOperation, error) {
	if ds == nil || ds.ID == "" || operationID == "" {
		return nil, errors.New("source cleanup operation id is required")
	}
	var operation types.SourceCleanupOperation
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		state, err := r.ensureSourceSyncState(tx, ds)
		if err != nil {
			return err
		}
		current, err := lockSourceLifecycleDataSource(tx, ds)
		if err != nil {
			return err
		}
		if !sourceModeEnabled(current) || sourceConfigFingerprint(ds) != sourceConfigFingerprint(current) {
			return errSourceConfigurationNotCurrent
		}
		if err := invalidateSourceForLifecycleTx(tx, r, state, current); err != nil {
			return err
		}
		if current.SourceBindingState != types.SourceBindingUnbound || current.SourceQueryEnabled {
			if err := tx.Model(&types.DataSource{}).Where("id=? AND tenant_id=?", current.ID, current.TenantID).
				Updates(map[string]any{"source_binding_state": types.SourceBindingUnbound, "source_query_enabled": false}).Error; err != nil {
				return err
			}
		}
		if err := lockCurrentSourcePublication(tx, current.ID); err != nil {
			return err
		}
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND data_source_id=? AND scope=?", operationID, current.ID, types.SourceCleanupScopeCurrentAndHistory).
			Take(&operation).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return errSourceCleanupNotRetryable
			}
			return err
		}
		if operation.Status != types.SourceCleanupFailed || !operation.Retryable {
			return errSourceCleanupNotRetryable
		}
		operation.Status = types.SourceCleanupPending
		operation.Retryable = false
		operation.ErrorCode = ""
		operation.LeaseOwner = nil
		operation.LeaseExpiresAt = nil
		operation.UpdatedAt = time.Now().UTC()
		return tx.Model(&operation).Updates(map[string]any{
			"status": operation.Status, "retryable": false, "error_code": "",
			"lease_owner": nil, "lease_expires_at": nil,
			"updated_at": operation.UpdatedAt,
		}).Error
	})
	if err != nil {
		return nil, err
	}
	return &operation, nil
}

func lockSourceLifecycleDataSource(tx *gorm.DB, ds *types.DataSource) (*types.DataSource, error) {
	var current types.DataSource
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND tenant_id=? AND deleted_at IS NULL", ds.ID, ds.TenantID).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errSourceConfigurationNotCurrent
	}
	if err != nil {
		return nil, err
	}
	return &current, nil
}

func invalidateSourceForLifecycleTx(tx *gorm.DB, repo *SyncLogRepository, state *sourceSyncStateRow, current *types.DataSource) error {
	// The disabled fingerprint is the terminal generation for this lifecycle
	// transition. Fencing through the ordinary config fingerprint first would
	// advance the generation twice on the initial transition and once again on
	// every repeated clear.
	return repo.invalidateDisabledSourceGeneration(tx, state, current)
}

func lockCurrentSourcePublication(tx *gorm.DB, dataSourceID string) error {
	if !tx.Migrator().HasTable("source_publications") {
		return nil
	}
	var publications []types.SourcePublication
	return tx.Clauses(clause.Locking{Strength: "UPDATE"}).Table("source_publications").
		Where("data_source_id=?", dataSourceID).Find(&publications).Error
}

func supersedeSourceWikiWorkTx(tx *gorm.DB, ds *types.DataSource, reasonCode string) error {
	now := time.Now().UTC()
	if tx.Migrator().HasTable("source_wiki_update_plans") {
		if err := tx.Exec(`UPDATE source_wiki_update_plans SET status='superseded',source_wide_stale=TRUE,
			reason_code=?,reason='Source lifecycle fenced pending Wiki work.',completed_at=?,updated_at=?
			WHERE source_id=? AND status IN ('pending','running')`, reasonCode, now, now, ds.ID).Error; err != nil {
			return err
		}
		if tx.Migrator().HasTable("source_wiki_update_plan_items") {
			if err := tx.Exec(`UPDATE source_wiki_update_plan_items SET state='superseded',updated_at=?
				WHERE plan_id IN (SELECT id FROM source_wiki_update_plans WHERE source_id=? AND status='superseded')
				AND state IN ('pending','running')`, now, ds.ID).Error; err != nil {
				return err
			}
		}
	}
	if tx.Migrator().HasTable("source_publication_outbox") {
		if err := tx.Exec(`UPDATE source_publication_outbox SET status='superseded',last_error=?
			WHERE data_source_id=? AND status='pending'`, reasonCode, ds.ID).Error; err != nil {
			return err
		}
	}
	if tx.Migrator().HasTable("task_pending_ops") {
		if err := tx.Exec(`DELETE FROM task_pending_ops WHERE task_type=? AND scope=? AND scope_id=?
			AND payload->>'data_source_id'=?`, types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase,
			ds.KnowledgeBaseID, ds.ID).Error; err != nil {
			return fmt.Errorf("supersede source Wiki deliveries: %w", err)
		}
	}
	return nil
}

var _ interfaces.SourceLifecycleRepository = (*SyncLogRepository)(nil)
