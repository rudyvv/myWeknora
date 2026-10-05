package repository

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

// sourceResourceUsageTx is the single scoped aggregate for logical source
// payload. It measures original bytes as retained BYTEA length, parsed cache
// as the canonical JSON text length, and vector payload as float32 dimension
// times four. A cached vector artifact and its persisted embedding row are
// distinct retained records and each is counted once; embedding identity
// references are unique by chunk_id. Unpublished stages are included.
func sourceResourceUsageTx(tx *gorm.DB, tenant uint64, sourceID string) (types.SourceResourceUsage, error) {
	var usage types.SourceResourceUsage
	err := tx.Raw(`SELECT
		COALESCE((SELECT SUM(octet_length(v.content))
			FROM source_file_versions v JOIN source_files f ON f.id=v.source_file_id
			WHERE f.tenant_id=? AND f.data_source_id=?),0)::bigint AS original_bytes,
		COALESCE((SELECT SUM(octet_length(a.parsed::text))
			FROM source_parsed_artifacts a WHERE a.tenant_id=? AND a.data_source_id=?),0)::bigint AS parsed_cache_bytes,
		(COALESCE((SELECT SUM(jsonb_array_length(a.vector)::bigint*4)
			FROM source_embedding_artifacts a WHERE a.tenant_id=? AND a.data_source_id=?),0) +
		 COALESCE((SELECT SUM(e.dimension::bigint*4)
			FROM embeddings e JOIN source_chunk_references cr ON cr.chunk_id=e.chunk_id
			JOIN source_files f ON f.id=cr.source_file_id
			WHERE f.tenant_id=? AND f.data_source_id=?),0))::bigint AS vector_bytes`,
		tenant, sourceID, tenant, sourceID, tenant, sourceID, tenant, sourceID).Scan(&usage).Error
	return usage, err
}

func (r *sourceSnapshotRepository) GetSourceResourceUsage(ctx context.Context, tenant uint64, sourceID string) (types.SourceResourceUsage, error) {
	if r.db == nil || tenant == 0 || sourceID == "" {
		return types.SourceResourceUsage{}, fmt.Errorf("source resource scope is invalid")
	}
	return sourceResourceUsageTx(r.db.WithContext(ctx), tenant, sourceID)
}

func (r *sourceSnapshotRepository) assertSourceResourceQuotaTx(tx *gorm.DB, tenant uint64, sourceID string) error {
	usage, err := sourceResourceUsageTx(tx, tenant, sourceID)
	if err != nil {
		return err
	}
	policy := r.resourcePolicy
	if usage.OriginalBytes > policy.OriginalBytesPerSource ||
		usage.ParsedCacheBytes > policy.ParsedCacheBytesPerSource ||
		usage.VectorBytes > policy.VectorBytesPerSource {
		return source.ErrSourceQuotaExceeded
	}
	return nil
}
