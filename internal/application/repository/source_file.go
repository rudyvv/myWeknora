package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/Tencent/WeKnora/internal/source"

	"github.com/Tencent/WeKnora/internal/types"
)

func (r *knowledgeRepository) ReadPublishedSourceFile(ctx context.Context, tenant uint64, id string, versionID ...string) (*types.SourceFileView, error) {
	return r.readPublishedSourceFile(ctx, tenant, id, true, versionID...)
}

func (r *knowledgeRepository) ReadPublishedSourceFileInfo(ctx context.Context, tenant uint64, id string) (*types.SourceFileView, error) {
	return r.readPublishedSourceFile(ctx, tenant, id, false)
}

func (r *knowledgeRepository) readPublishedSourceFile(ctx context.Context, tenant uint64, id string, content bool, versionID ...string) (*types.SourceFileView, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	if r.db.Dialector.Name() != "postgres" {
		return nil, fmt.Errorf("source file reader requires PostgreSQL")
	}
	var file types.SourceFileView
	columns := "sf.id AS knowledge_id, sf.data_source_id, sm.path, ss.id AS snapshot_id, ss.project_id, ss.commit_sha, ss.repository_url, sv.id AS file_version_id, sv.sha256, sv.encoding, sv.quality, sv.parser_version, octet_length(sv.content) AS file_size"
	if content {
		columns += ", sv.content AS raw_content, sv.symbols"
	}
	query := r.db.WithContext(ctx).Table("source_files sf").
		Select(columns).
		Joins("JOIN source_snapshot_members sm ON sm.source_file_id=sf.id AND sm.status='parsed'").
		Joins("JOIN source_snapshots ss ON ss.id=sm.snapshot_id AND ss.data_source_id=sf.data_source_id AND ss.tenant_id=sf.tenant_id AND ss.knowledge_base_id=sf.knowledge_base_id AND ss.state='published'").
		Joins("JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.source_file_id=sf.id AND sv.snapshot_id=ss.id").
		Joins("JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source'").
		Joins("JOIN knowledges k ON k.id=sf.id AND k.tenant_id=sf.tenant_id AND k.knowledge_base_id=sf.knowledge_base_id AND k.deleted_at IS NULL").
		Where("sf.id=? AND sf.tenant_id=?", id, tenant).
		Where(source.SnapshotSQL(ctx, "ss.id", "sf.data_source_id", "sf.id"))
	if len(versionID) > 0 && versionID[0] != "" {
		query = query.Where("sv.id=?", versionID[0])
	}
	err := query.Take(&file).Error
	if err != nil {
		return nil, err
	}
	if !content {
		return &file, nil
	}
	hash := sha256.Sum256(file.RawContent)
	if hex.EncodeToString(hash[:]) != file.SHA256 {
		return nil, fmt.Errorf("stored source file checksum mismatch")
	}
	if err := enrichSFCReferences(ctx, r.db, &file); err != nil {
		return nil, err
	}
	file.Content = string(file.RawContent)
	return &file, nil
}
