package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sourceSnapshotRepository struct{ db *gorm.DB }

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
		to_regclass('source_embedding_artifacts') IS NOT NULL AND
		to_regclass('source_code_relations') IS NOT NULL AND
		EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='source_file_versions' AND column_name='facts') AND
		EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='source_snapshots' AND column_name='relations_staged')`).Scan(&ready).Error
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

func (r *sourceSnapshotRepository) StageRelations(ctx context.Context, tenant uint64, sourceID, snapshotID string, relations []types.SourceCodeRelation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshot types.SourceSnapshot
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND data_source_id=? AND state!='published' AND manifest_complete=true", snapshotID, tenant, sourceID).First(&snapshot).Error; err != nil {
			return fmt.Errorf("source relation snapshot is unavailable")
		}
		if err := tx.Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenant, sourceID, snapshotID).Delete(&types.SourceCodeRelation{}).Error; err != nil {
			return err
		}
		type endpointKey struct {
			fileID    string
			versionID string
			path      string
		}
		validEndpoints := make(map[endpointKey]struct{})
		if len(relations) > 0 {
			fileIDs := make([]string, 0, len(relations)*2)
			versionIDs := make([]string, 0, len(relations)*2)
			seenFileIDs := make(map[string]struct{}, len(relations)*2)
			seenVersionIDs := make(map[string]struct{}, len(relations)*2)
			addEndpointIDs := func(fileID, versionID string) {
				if fileID != "" {
					if _, ok := seenFileIDs[fileID]; !ok {
						seenFileIDs[fileID] = struct{}{}
						fileIDs = append(fileIDs, fileID)
					}
				}
				if versionID != "" {
					if _, ok := seenVersionIDs[versionID]; !ok {
						seenVersionIDs[versionID] = struct{}{}
						versionIDs = append(versionIDs, versionID)
					}
				}
			}
			for _, relation := range relations {
				addEndpointIDs(relation.FromFileID, relation.FromVersionID)
				addEndpointIDs(relation.ToFileID, relation.ToVersionID)
			}
			var endpoints []struct {
				SourceFileID  string
				FileVersionID string
				Path          string
			}
			if len(fileIDs) > 0 && len(versionIDs) > 0 {
				if err := tx.Table("source_snapshot_members sm").
					Select("sm.source_file_id, sm.file_version_id, sm.path").
					Joins("JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.source_file_id=sm.source_file_id").
					Joins("JOIN source_files sf ON sf.id=sm.source_file_id").
					Where("sm.snapshot_id=? AND sm.status='parsed' AND sm.source_file_id IN ? AND sm.file_version_id IN ? AND sv.snapshot_id=sm.snapshot_id AND sf.tenant_id=? AND sf.data_source_id=? AND sf.knowledge_base_id=?", snapshotID, fileIDs, versionIDs, tenant, sourceID, snapshot.KnowledgeBaseID).
					Find(&endpoints).Error; err != nil {
					return err
				}
			}
			for _, endpoint := range endpoints {
				validEndpoints[endpointKey{fileID: endpoint.SourceFileID, versionID: endpoint.FileVersionID, path: endpoint.Path}] = struct{}{}
			}
		}
		for i := range relations {
			relation := &relations[i]
			relation.TenantID, relation.DataSourceID, relation.SnapshotID = tenant, sourceID, snapshotID
			if relation.FromFileID == "" || relation.FromVersionID == "" || relation.FromPath == "" {
				return fmt.Errorf("source relation has no verified source endpoint")
			}
			if _, ok := validEndpoints[endpointKey{fileID: relation.FromFileID, versionID: relation.FromVersionID, path: relation.FromPath}]; !ok {
				return fmt.Errorf("source relation origin is outside its immutable snapshot")
			}
			if relation.ToFileID != "" || relation.ToVersionID != "" || relation.ToPath != "" {
				if relation.ToFileID == "" || relation.ToVersionID == "" || relation.ToPath == "" {
					return fmt.Errorf("source relation target endpoint is incomplete")
				}
				if _, ok := validEndpoints[endpointKey{fileID: relation.ToFileID, versionID: relation.ToVersionID, path: relation.ToPath}]; !ok {
					return fmt.Errorf("source relation target is outside its immutable snapshot")
				}
			} else if relation.Determinacy == "uncertain" && relation.ResolutionReason == "" {
				return fmt.Errorf("unresolved source relation has no diagnostic reason")
			}
		}
		if len(relations) > 0 {
			if err := tx.CreateInBatches(&relations, 100).Error; err != nil {
				return err
			}
		}
		return tx.Model(&types.SourceSnapshot{}).Where("id=? AND state!='published'", snapshotID).
			Updates(map[string]any{"relation_count": len(relations), "relations_staged": true}).Error
	})
}

func (r *sourceSnapshotRepository) StageIndexes(ctx context.Context, indexes []*types.IndexInfo, vectors map[string][]float32) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
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
		var counts struct{ Members, Files, Chunks, Embeddings, Relations int64 }
		if err := tx.Raw(`SELECT
			(SELECT count(*) FROM source_snapshot_members WHERE snapshot_id=?) AS members,
			(SELECT count(*) FROM source_snapshot_members WHERE snapshot_id=? AND status='parsed') AS files,
			(SELECT count(*) FROM source_chunk_references WHERE snapshot_id=?) AS chunks,
			(SELECT count(*) FROM embeddings e JOIN source_chunk_references c ON c.chunk_id=e.chunk_id WHERE c.snapshot_id=? AND e.dimension=?) AS embeddings,
			(SELECT count(*) FROM source_code_relations WHERE tenant_id=? AND data_source_id=? AND snapshot_id=?) AS relations`,
			snapshot.ID, snapshot.ID, snapshot.ID, snapshot.ID, dimension, snapshot.TenantID, snapshot.DataSourceID, snapshot.ID).Scan(&counts).Error; err != nil {
			return err
		}
		var relationStaged bool
		if err := tx.Model(&types.SourceSnapshot{}).Select("relations_staged").Where("id=?", snapshot.ID).Scan(&relationStaged).Error; err != nil {
			return err
		}
		if !relationStaged || counts.Relations != int64(snapshot.RelationCount) || counts.Members != int64(snapshot.MemberCount) || counts.Files != int64(snapshot.FileCount) || counts.Chunks != int64(snapshot.ChunkCount) || counts.Embeddings != counts.Chunks {
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
		return nil
	})
}

func (r *sourceSnapshotRepository) GetRun(ctx context.Context, tenant uint64, sourceID, logID string) (*types.SourceRunResult, error) {
	var snapshot types.SourceSnapshot
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND data_source_id=? AND sync_log_id=?", tenant, sourceID, logID).First(&snapshot).Error; err != nil {
		return nil, err
	}
	var members []types.SourceSnapshotMember
	if err := r.db.WithContext(ctx).Where("snapshot_id=?", snapshot.ID).Order("path").Find(&members).Error; err != nil {
		return nil, err
	}
	return &types.SourceRunResult{Snapshot: &snapshot, Members: members}, nil
}
