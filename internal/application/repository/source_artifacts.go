package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

func (r *sourceSnapshotRepository) GetPublished(ctx context.Context, tenant uint64, sourceID string) (*types.SourceRunResult, error) {
	var snapshot types.SourceSnapshot
	err := r.db.WithContext(ctx).Table("source_snapshots").Select("source_snapshots.*").
		Joins("JOIN source_publications p ON p.snapshot_id=source_snapshots.id").
		Where("p.tenant_id=? AND p.data_source_id=?", tenant, sourceID).First(&snapshot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var members []types.SourceSnapshotMember
	err = r.db.WithContext(ctx).Where("snapshot_id=?", snapshot.ID).Order("path").Find(&members).Error
	return &types.SourceRunResult{Snapshot: &snapshot, Members: members}, err
}

func (r *sourceSnapshotRepository) GetParsedArtifact(ctx context.Context, tenant uint64, sourceID, key string) (*types.ParsedSourceFile, error) {
	var artifact types.SourceParsedArtifact
	err := r.db.WithContext(ctx).Where("tenant_id=? AND data_source_id=? AND artifact_key=?", tenant, sourceID, key).First(&artifact).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var parsed types.ParsedSourceFile
	if err = json.Unmarshal(artifact.Parsed, &parsed); err != nil {
		return nil, err
	}
	return &parsed, nil
}
func (r *sourceSnapshotRepository) SaveParsedArtifact(ctx context.Context, tenant uint64, sourceID, key string, parsed *types.ParsedSourceFile) error {
	data, err := json.Marshal(parsed)
	if err != nil {
		return err
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		if err := tx.Exec(`WITH payload AS MATERIALIZED (
			SELECT ?::jsonb AS parsed, ?::bigint AS tenant_id, ?::varchar(36) AS data_source_id, ?::text AS artifact_key
		)
			INSERT INTO source_parsed_artifacts (tenant_id,data_source_id,artifact_key,parsed,logical_payload_bytes)
			SELECT payload.tenant_id,payload.data_source_id,payload.artifact_key,payload.parsed,octet_length(payload.parsed::text) FROM payload
			ON CONFLICT (tenant_id,data_source_id,artifact_key) DO NOTHING`, string(data), tenant, sourceID, key).Error; err != nil {
			return err
		}
		return r.assertSourceResourceQuotaTx(tx, tenant, sourceID)
	})
}
func (r *sourceSnapshotRepository) GetEmbeddingArtifacts(ctx context.Context, tenant uint64, sourceID string, keys []string) (map[string][]float32, error) {
	result := map[string][]float32{}
	if len(keys) == 0 {
		return result, nil
	}
	var artifacts []types.SourceEmbeddingArtifact
	if err := r.db.WithContext(ctx).Where("tenant_id=? AND data_source_id=? AND artifact_key IN ?", tenant, sourceID, keys).Find(&artifacts).Error; err != nil {
		return nil, err
	}
	for _, artifact := range artifacts {
		var vector []float32
		if err := json.Unmarshal(artifact.Vector, &vector); err != nil {
			return nil, err
		}
		result[artifact.ArtifactKey] = vector
	}
	return result, nil
}
func (r *sourceSnapshotRepository) SaveEmbeddingArtifacts(ctx context.Context, tenant uint64, sourceID string, vectors map[string][]float32) error {
	artifacts := make([]types.SourceEmbeddingArtifact, 0, len(vectors))
	for key, vector := range vectors {
		data, err := json.Marshal(vector)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, types.SourceEmbeddingArtifact{TenantID: tenant, DataSourceID: sourceID, ArtifactKey: key, Vector: types.JSON(data)})
	}
	if len(artifacts) == 0 {
		return nil
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		for start := 0; start < len(artifacts); start += 100 {
			end := min(start+100, len(artifacts))
			batch := artifacts[start:end]
			var values strings.Builder
			args := make([]any, 0, len(batch)*4)
			for i, artifact := range batch {
				if i > 0 {
					values.WriteByte(',')
				}
				values.WriteString("(?::bigint,?::varchar(36),?::text,?::jsonb)")
				args = append(args, artifact.TenantID, artifact.DataSourceID, artifact.ArtifactKey, string(artifact.Vector))
			}
			query := `WITH payload (tenant_id,data_source_id,artifact_key,vector) AS MATERIALIZED (VALUES ` + values.String() + `)
				INSERT INTO source_embedding_artifacts (tenant_id,data_source_id,artifact_key,vector,logical_payload_bytes)
				SELECT tenant_id,data_source_id,artifact_key,vector,jsonb_array_length(vector)::bigint*4 FROM payload
				ON CONFLICT (tenant_id,data_source_id,artifact_key) DO NOTHING`
			if err := tx.Exec(query, args...).Error; err != nil {
				return err
			}
		}
		return r.assertSourceResourceQuotaTx(tx, tenant, sourceID)
	})
}

func (r *sourceSnapshotRepository) UpdateProgress(ctx context.Context, snapshot *types.SourceSnapshot, members []types.SourceSnapshotMember) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := assertSourceLeaseTx(tx, ctx); err != nil {
			return err
		}
		updates := map[string]any{"state": snapshot.State, "error": snapshot.Error, "chunk_count": snapshot.ChunkCount,
			"processing_version": snapshot.ProcessingVersion, "embedding_version": snapshot.EmbeddingVersion,
			"parsed_count": snapshot.ParsedCount, "reused_file_count": snapshot.ReusedFileCount, "reused_chunk_count": snapshot.ReusedChunkCount,
			"embedded_chunk_count": snapshot.EmbeddedChunkCount, "reused_vector_count": snapshot.ReusedVectorCount}
		if err := tx.Model(&types.SourceSnapshot{}).Where("id=? AND state!='published'", snapshot.ID).Updates(updates).Error; err != nil {
			return err
		}
		for _, member := range members {
			if member.Status == "parsed" {
				if err := tx.Model(&types.SourceSnapshotMember{}).Where("snapshot_id=? AND path=? AND file_version_id=?", snapshot.ID, member.Path, member.FileVersionID).Update("parse_reused", member.ParseReused).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}
