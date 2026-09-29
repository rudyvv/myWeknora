package source

import "context"

// PublishedChunkSQL is applied before LIMIT in indexes and public chunk reads.
// column is a trusted repository expression, never a request parameter.
func PublishedChunkSQL(ctx context.Context, column, knowledgeColumn string) string {
	return `((NOT EXISTS (SELECT 1 FROM source_chunk_references sc0 WHERE sc0.chunk_id=` + column + `) AND ` + OrdinaryChunkSQL(ctx, column, knowledgeColumn) + `) OR EXISTS (
		SELECT 1 FROM source_chunk_references sc
		JOIN source_snapshots ss ON ss.id=sc.snapshot_id
		JOIN source_snapshot_members sm ON sm.snapshot_id=sc.snapshot_id AND sm.source_file_id=sc.source_file_id AND sm.file_version_id=sc.file_version_id AND sm.status='parsed'
		JOIN data_sources ds ON ds.id=ss.data_source_id AND ds.tenant_id=ss.tenant_id AND ds.knowledge_base_id=ss.knowledge_base_id
		WHERE sc.chunk_id=` + column + ` AND ss.state='published' AND ds.deleted_at IS NULL
		AND ds.config->'settings'->>'content_mode'='source' AND ` + SnapshotSQL(ctx, "ss.id", "ss.data_source_id", "sc.source_file_id") + `))`
}

func PublishedKnowledgeSQL(ctx context.Context, column string) string {
	return `((NOT EXISTS (SELECT 1 FROM source_files sf0 WHERE sf0.id=` + column + `) AND ` + OrdinaryKnowledgeSQL(ctx, column) + `) OR EXISTS (
		SELECT 1 FROM source_snapshot_members sm
		JOIN source_snapshots ss ON ss.id=sm.snapshot_id
		JOIN data_sources ds ON ds.id=ss.data_source_id AND ds.tenant_id=ss.tenant_id AND ds.knowledge_base_id=ss.knowledge_base_id
		WHERE sm.source_file_id=` + column + ` AND sm.status='parsed' AND ss.state='published'
		AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source' AND ` + SnapshotSQL(ctx, "ss.id", "ss.data_source_id", "sm.source_file_id") + `))`
}
