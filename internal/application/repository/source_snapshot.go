package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sourceSnapshotRepository struct{ db *gorm.DB }

type sourcePublicationOutboxRow struct {
	ID               string `gorm:"column:id"`
	TenantID         uint64 `gorm:"column:tenant_id"`
	KnowledgeBaseID  string `gorm:"column:knowledge_base_id"`
	DataSourceID     string `gorm:"column:data_source_id"`
	SnapshotID       string `gorm:"column:snapshot_id"`
	EventType        string `gorm:"column:event_type"`
	ConfigGeneration int64  `gorm:"column:config_generation"`
	AttemptCount     int    `gorm:"column:attempt_count"`
}

func NewSourceSnapshotRepository(db *gorm.DB) interfaces.SourceSnapshotRepository {
	return &sourceSnapshotRepository{db: db}
}

func (r *sourceSnapshotRepository) CheckReady(ctx context.Context) error {
	if r.db == nil || r.db.Dialector.Name() != "postgres" {
		return fmt.Errorf("source publication requires the built-in PostgreSQL database")
	}
	var ready bool
	err := r.db.WithContext(ctx).Raw(`SELECT
		EXISTS(SELECT 1 FROM pg_extension WHERE extname='vector') AND
		EXISTS(SELECT 1 FROM pg_extension WHERE extname='pg_search') AND
		EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND tablename='embeddings' AND indexdef LIKE '%USING bm25%') AND
		to_regclass('source_publications') IS NOT NULL AND
  to_regclass('source_parsed_artifacts') IS NOT NULL AND
  to_regclass('source_embedding_artifacts') IS NOT NULL`).Scan(&ready).Error
	if err != nil {
		return err
	}
	if !ready {
		return fmt.Errorf("source publication schema or keyword/vector indexes are unavailable")
	}
	return nil
}

func (r *sourceSnapshotRepository) Create(ctx context.Context, snapshot *types.SourceSnapshot, members []types.SourceSnapshotMember) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		if !snapshot.ManifestComplete || snapshot.MemberCount != len(members) {
			return fmt.Errorf("source manifest is incomplete")
		}
		if err := tx.Create(snapshot).Error; err != nil {
			return err
		}
		if len(members) > 0 {
			return tx.CreateInBatches(members, 100).Error
		}
		return nil
	})
}

func (r *sourceSnapshotRepository) SetState(ctx context.Context, id, state, message string) error {
	return r.db.WithContext(ctx).Model(&types.SourceSnapshot{}).Where("id=?", id).Updates(map[string]any{"state": state, "error": message}).Error
}

func (r *sourceSnapshotRepository) StageFile(ctx context.Context, file *types.SourceFile, version *types.SourceFileVersion, chunks []*types.Chunk) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(file).Error; err != nil {
			return err
		}
		if err := tx.Create(version).Error; err != nil {
			return err
		}
		if len(chunks) > 0 {
			if err := tx.Select("*").CreateInBatches(chunks, 100).Error; err != nil {
				return err
			}
			refs := make([]types.SourceChunkReference, len(chunks))
			for i, c := range chunks {
				refs[i] = types.SourceChunkReference{ChunkID: c.ID, SnapshotID: version.SnapshotID, SourceFileID: file.ID, FileVersionID: version.ID}
			}
			if err := tx.CreateInBatches(refs, 100).Error; err != nil {
				return err
			}
		}
		result := tx.Model(&types.SourceSnapshotMember{}).Where("snapshot_id=? AND path=? AND status='included'", version.SnapshotID, file.Path).
			Updates(map[string]any{"source_file_id": file.ID, "file_version_id": version.ID, "status": "parsed", "encoding": version.Encoding})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("source file is absent from the complete manifest")
		}
		return nil
	})
}

func (r *sourceSnapshotRepository) StageIndexes(ctx context.Context, indexes []*types.IndexInfo, vectors map[string][]float32) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		if err := pgrepo.NewPostgresRetrieveEngineRepository(tx).BatchSave(ctx, indexes, map[string]any{"embedding": vectors}); err != nil {
			return err
		}
		ids := make([]string, len(indexes))
		for i, index := range indexes {
			ids[i] = index.ChunkID
		}
		return tx.Table("embeddings").Where("chunk_id IN ?", ids).Update("is_enabled", false).Error
	})
}

func (r *sourceSnapshotRepository) Publish(ctx context.Context, snapshot *types.SourceSnapshot, expected *types.DataSource, kb *types.KnowledgeBase, dimension int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		var ds types.DataSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=?", expected.ID, expected.TenantID).First(&ds).Error; err != nil {
			return err
		}
		if string(ds.Config) != string(expected.Config) || ds.KnowledgeBaseID != kb.ID {
			return fmt.Errorf("source configuration changed before publication")
		}
		var currentKB types.KnowledgeBase
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND tenant_id=?", kb.ID, kb.TenantID).First(&currentKB).Error; err != nil {
			return err
		}
		if currentKB.EmbeddingModelID != kb.EmbeddingModelID || !currentKB.IsKeywordEnabled() || !currentKB.IsVectorEnabled() || (currentKB.VectorStoreID != nil && *currentKB.VectorStoreID != "") {
			return fmt.Errorf("source knowledge base indexing configuration changed")
		}
		var model types.Model
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=?", kb.EmbeddingModelID).First(&model).Error; err != nil {
			return err
		}
		if model.Status != types.ModelStatusActive || model.Type != types.ModelTypeEmbedding || source.EmbeddingVersion(&model, dimension) != snapshot.EmbeddingVersion {
			return fmt.Errorf("source embedding configuration changed before publication")
		}
		var previous types.SourcePublication
		err := tx.Where("data_source_id=?", ds.ID).First(&previous).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			return err
		}
		if previous.SnapshotID != snapshot.PreviousSnapshotID {
			return fmt.Errorf("source publication changed during preparation")
		}
		var counts struct{ Members, Files, Chunks, Embeddings int64 }
		if err := tx.Raw(`SELECT
			(SELECT count(*) FROM source_snapshot_members WHERE snapshot_id=?) AS members,
			(SELECT count(*) FROM source_snapshot_members WHERE snapshot_id=? AND status='parsed') AS files,
			(SELECT count(*) FROM source_chunk_references WHERE snapshot_id=?) AS chunks,
			(SELECT count(*) FROM embeddings e JOIN source_chunk_references c ON c.chunk_id=e.chunk_id WHERE c.snapshot_id=? AND e.dimension=?) AS embeddings`,
			snapshot.ID, snapshot.ID, snapshot.ID, snapshot.ID, dimension).Scan(&counts).Error; err != nil {
			return err
		}
		if counts.Members != int64(snapshot.MemberCount) || counts.Files != int64(snapshot.FileCount) || counts.Chunks != int64(snapshot.ChunkCount) || counts.Embeddings != counts.Chunks {
			return fmt.Errorf("source membership or dual index preparation is incomplete")
		}
		// BM25 readiness is checked on the actual transaction's index database.
		var keywordReady bool
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND tablename='embeddings' AND indexdef LIKE '%USING bm25%')`).Scan(&keywordReady).Error; err != nil {
			return err
		}
		if !keywordReady {
			return fmt.Errorf("source keyword index is unavailable")
		}
		var members []types.SourceSnapshotMember
		if err := tx.Where("snapshot_id=? AND status='parsed'", snapshot.ID).Find(&members).Error; err != nil {
			return err
		}
		for _, member := range members {
			if err := tx.Model(&types.SourceFile{}).Where("id=? AND data_source_id=?", member.SourceFileID, ds.ID).Update("path", member.Path).Error; err != nil {
				return err
			}
			var version types.SourceFileVersion
			if err := tx.Where("id=? AND source_file_id=? AND snapshot_id=?", member.FileVersionID, member.SourceFileID, snapshot.ID).First(&version).Error; err != nil {
				return err
			}
			meta, _ := json.Marshal(map[string]string{"datasource_id": ds.ID, "project_id": snapshot.ProjectID, "commit_sha": snapshot.CommitSHA, "repository_url": snapshot.RepositoryURL, "source_path": member.Path, "source_snapshot_id": snapshot.ID, "source_file_version_id": version.ID, "source_quality": version.Quality})
			folder := path.Dir(member.Path)
			if folder == "." {
				folder = ""
			}
			knowledge := &types.Knowledge{ID: member.SourceFileID, TenantID: kb.TenantID, KnowledgeBaseID: kb.ID, Type: types.KnowledgeTypeSource, Title: member.Path, FileName: path.Base(member.Path), FolderPath: folder, FileType: strings.TrimPrefix(path.Ext(member.Path), "."), FileSize: member.Size, FileHash: version.SHA256, Source: snapshot.RepositoryURL, Channel: "gitlab", ParseStatus: types.ParseStatusCompleted, SummaryStatus: types.SummaryStatusNone, EnableStatus: "enabled", EmbeddingModelID: kb.EmbeddingModelID, Metadata: types.JSON(meta), CustomMetadata: types.JSON(`{}`)}
			// Preserve user-authored descriptions and tags across source versions.
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"title", "file_name", "folder_path", "file_type", "file_size", "file_hash", "source", "parse_status", "enable_status", "embedding_model_id", "metadata", "updated_at"})}).Create(knowledge).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&types.Chunk{}).Where("id IN (SELECT chunk_id FROM source_chunk_references WHERE snapshot_id=?)", snapshot.ID).Updates(map[string]any{"is_enabled": true, "index_status": "ready", "status": types.ChunkStatusIndexed}).Error; err != nil {
			return err
		}
		if err := tx.Table("embeddings").Where("chunk_id IN (SELECT chunk_id FROM source_chunk_references WHERE snapshot_id=?)", snapshot.ID).Update("is_enabled", true).Error; err != nil {
			return err
		}
		publication := types.SourcePublication{DataSourceID: ds.ID, SnapshotID: snapshot.ID, TenantID: kb.TenantID, KnowledgeBaseID: kb.ID}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "data_source_id"}}, DoUpdates: clause.AssignmentColumns([]string{"snapshot_id"})}).Create(&publication).Error; err != nil {
			return err
		}
		now := time.Now().UTC()
		if err := tx.Model(&types.SourceSnapshot{}).Where("id=? AND manifest_complete=true", snapshot.ID).Updates(map[string]any{"state": "published", "file_count": snapshot.FileCount, "chunk_count": snapshot.ChunkCount, "published_at": now, "processing_version": snapshot.ProcessingVersion, "embedding_version": snapshot.EmbeddingVersion, "parsed_count": snapshot.ParsedCount, "reused_file_count": snapshot.ReusedFileCount, "reused_chunk_count": snapshot.ReusedChunkCount, "embedded_chunk_count": snapshot.EmbeddedChunkCount, "reused_vector_count": snapshot.ReusedVectorCount}).Error; err != nil {
			return err
		}
		snapshot.State, snapshot.PublishedAt = "published", &now
		if lease, ok := types.SourceSyncLeaseFromContext(ctx); ok {
			eventID := uuid.NewString()
			payload, err := json.Marshal(types.SourceWikiUpdatePayload{
				SchemaVersion: 1, EventID: eventID, TenantID: kb.TenantID,
				KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, SnapshotID: snapshot.ID,
				CommitSHA: snapshot.CommitSHA, ConfigGeneration: lease.ConfigGeneration,
			})
			if err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO source_publication_outbox
				(id,tenant_id,knowledge_base_id,data_source_id,snapshot_id,event_type,payload,status,created_at,config_generation)
				VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(data_source_id,snapshot_id,event_type) DO NOTHING`,
				eventID, kb.TenantID, kb.ID, ds.ID, snapshot.ID, "source.wiki.update", string(payload), "pending", now, lease.ConfigGeneration).Error; err != nil {
				return err
			}
			if err := tx.Table("source_sync_states").Where(`data_source_id=? AND config_generation=? AND fencing_token=? AND active_sync_log_id=?`,
				lease.DataSourceID, lease.ConfigGeneration, lease.FencingToken, lease.SyncLogID).
				Updates(map[string]any{"last_successful_snapshot_id": snapshot.ID, "last_successful_published_at": now, "updated_at": now}).Error; err != nil {
				return err
			}
			if err := tx.Table("source_sync_runs").Where("sync_log_id=? AND fencing_token=?", lease.SyncLogID, lease.FencingToken).
				Updates(map[string]any{"phase": "published", "updated_at": now}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// RelaySourcePublicationOutbox accepts published snapshots into the durable,
// source-specific Wiki update lane. The acceptance row and outbox ack commit
// atomically; this records pending Wiki work, not generated Wiki content.
func (r *sourceSnapshotRepository) RelaySourcePublicationOutbox(ctx context.Context, limit int) (int, error) {
	if r.db == nil || r.db.Dialector.Name() != "postgres" {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}
	accepted, processed := 0, 0
	for processed < limit {
		var eventID string
		var eventAttemptCount int
		found, eventAccepted := false, false
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var event sourcePublicationOutboxRow
			err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
				Table("source_publication_outbox").Where("status='pending' AND next_attempt_at <= ?", time.Now().UTC()).
				Order("created_at ASC, id ASC").Take(&event).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			found, eventID, eventAttemptCount = true, event.ID, event.AttemptCount
			now := time.Now().UTC()
			stale := func(reason string) error {
				return tx.Table("source_publication_outbox").Where("id=? AND status='pending'", event.ID).
					Updates(map[string]any{"status": "superseded", "attempt_count": gorm.Expr("attempt_count + 1"), "last_error": reason, "delivered_at": now}).Error
			}
			if event.EventType != "source.wiki.update" || event.ID == "" || event.ConfigGeneration <= 0 {
				return stale("source publication outbox event is invalid or lacks a configuration generation")
			}
			var state sourceSyncStateRow
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Table("source_sync_states").
				Where("data_source_id=? AND tenant_id=?", event.DataSourceID, event.TenantID).Take(&state).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return stale("source configuration state no longer exists")
				}
				return err
			}
			var ds types.DataSource
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND tenant_id=? AND knowledge_base_id=?", event.DataSourceID, event.TenantID, event.KnowledgeBaseID).Take(&ds).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return stale("source was deleted or moved before Wiki update acceptance")
				}
				return err
			}
			var kb types.KnowledgeBase
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND tenant_id=?", event.KnowledgeBaseID, event.TenantID).Take(&kb).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return stale("knowledge base was deleted before Wiki update acceptance")
				}
				return err
			}
			if state.ConfigGeneration != event.ConfigGeneration || state.ConfigFingerprint != sourceConfigFingerprint(&ds) {
				return stale("source configuration generation changed before Wiki update acceptance")
			}
			var current types.SourcePublication
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("data_source_id=? AND tenant_id=? AND knowledge_base_id=?", event.DataSourceID, event.TenantID, event.KnowledgeBaseID).Take(&current).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return stale("source has no current published snapshot")
				}
				return err
			}
			if current.SnapshotID != event.SnapshotID {
				return stale("a newer source snapshot was published before Wiki update acceptance")
			}
			var snapshot types.SourceSnapshot
			if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("id=? AND tenant_id=? AND data_source_id=? AND knowledge_base_id=? AND state='published'",
				event.SnapshotID, event.TenantID, event.DataSourceID, event.KnowledgeBaseID).Take(&snapshot).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return stale("published source snapshot is no longer available")
				}
				return err
			}
			payload := types.SourceWikiUpdatePayload{
				SchemaVersion: 1, EventID: event.ID, TenantID: event.TenantID,
				KnowledgeBaseID: event.KnowledgeBaseID, DataSourceID: event.DataSourceID,
				SnapshotID: snapshot.ID, CommitSHA: snapshot.CommitSHA, ConfigGeneration: event.ConfigGeneration,
			}
			encoded, err := json.Marshal(payload)
			if err != nil {
				return err
			}
			if err := tx.Exec(`INSERT INTO task_pending_ops
				(tenant_id,task_type,scope,scope_id,op,dedup_key,payload,enqueued_at)
				VALUES(?,?,?,?,?,?,?::jsonb,?) ON CONFLICT DO NOTHING`,
				event.TenantID, types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase,
				event.KnowledgeBaseID, "published_snapshot", event.ID, string(encoded), now).Error; err != nil {
				return err
			}
			result := tx.Table("source_publication_outbox").Where("id=? AND status='pending'", event.ID).
				Updates(map[string]any{"status": "delivered", "attempt_count": gorm.Expr("attempt_count + 1"), "last_error": "", "delivered_at": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("source publication outbox event was concurrently acknowledged")
			}
			eventAccepted = true
			return nil
		})
		if err != nil {
			if eventID != "" {
				writeErr := r.db.WithContext(context.WithoutCancel(ctx)).Table("source_publication_outbox").
					Where("id=? AND status='pending'", eventID).
					Updates(map[string]any{
						"attempt_count": gorm.Expr("attempt_count + 1"), "last_error": err.Error(),
						"next_attempt_at": time.Now().UTC().Add(sourceOutboxRetryDelay(eventAttemptCount + 1)),
					}).Error
				if writeErr != nil {
					return accepted, errors.Join(err, writeErr)
				}
			}
			return accepted, err
		}
		if !found {
			break
		}
		processed++
		if eventAccepted {
			accepted++
		}
	}
	return accepted, nil
}

func sourceOutboxRetryDelay(attempt int) time.Duration {
	delay := time.Second
	for i := 1; i < attempt && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func (r *sourceSnapshotRepository) GetRun(ctx context.Context, tenant uint64, sourceID, logID string) (*types.SourceRunResult, error) {
	var snapshot types.SourceSnapshot
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND data_source_id=? AND sync_log_id=?", tenant, sourceID, logID).First(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	var members []types.SourceSnapshotMember
	if err := r.db.WithContext(ctx).Where("snapshot_id=?", snapshot.ID).Order("path").Find(&members).Error; err != nil {
		return nil, err
	}
	return &types.SourceRunResult{Snapshot: &snapshot, Members: members}, nil
}

func (r *sourceSnapshotRepository) GetStagedChunkIDs(ctx context.Context, snapshotID, fileVersionID string) ([]string, error) {
	var ids []string
	err := r.db.WithContext(ctx).Table("source_chunk_references cr").
		Joins("JOIN chunks c ON c.id=cr.chunk_id").
		Where("cr.snapshot_id=? AND cr.file_version_id=?", snapshotID, fileVersionID).
		Order("c.chunk_index ASC, c.id ASC").Pluck("cr.chunk_id", &ids).Error
	return ids, err
}
