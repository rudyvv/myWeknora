package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
)

func (r *knowledgeRepository) ReadPublishedSourceFile(ctx context.Context, tenant uint64, id string, versionID ...string) (*types.SourceFileView, error) {
	if r.db.Dialector.Name() != "postgres" {
		return nil, fmt.Errorf("source file reader requires PostgreSQL")
	}
	var file types.SourceFileView
	query := r.db.WithContext(ctx).Table("source_files sf").
		Select("sf.id AS knowledge_id, sf.path, ss.id AS snapshot_id, ss.project_id, ss.commit_sha, ss.repository_url, sv.id AS file_version_id, sv.sha256, sv.encoding, sv.quality, sv.parser_version, sv.content AS raw_content, sv.symbols").
		Joins("JOIN source_publications sp ON sp.data_source_id=sf.data_source_id AND sp.tenant_id=sf.tenant_id AND sp.knowledge_base_id=sf.knowledge_base_id").
		Joins("JOIN source_snapshots ss ON ss.id=sp.snapshot_id AND ss.state='published'").
		Joins("JOIN source_snapshot_members sm ON sm.snapshot_id=sp.snapshot_id AND sm.source_file_id=sf.id AND sm.status='parsed'").
		Joins("JOIN source_file_versions sv ON sv.id=sm.file_version_id AND sv.source_file_id=sf.id AND sv.snapshot_id=sp.snapshot_id").
		Joins("JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source'").
		Joins("JOIN knowledges k ON k.id=sf.id AND k.tenant_id=sf.tenant_id AND k.knowledge_base_id=sf.knowledge_base_id AND k.deleted_at IS NULL").
		Where("sf.id=? AND sf.tenant_id=?", id, tenant)
	if len(versionID) > 0 && versionID[0] != "" {
		query = query.Where("sv.id=?", versionID[0])
	}
	err := query.Take(&file).Error
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(file.RawContent)
	if hex.EncodeToString(hash[:]) != file.SHA256 {
		return nil, fmt.Errorf("stored source file checksum mismatch")
	}
	file.Content = string(file.RawContent)
	return &file, nil
}
