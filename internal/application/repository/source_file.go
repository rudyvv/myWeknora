package repository

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
	columns := "sf.id AS knowledge_id, sf.knowledge_base_id, sf.data_source_id, sm.path, ss.id AS snapshot_id, ss.project_id, ss.commit_sha, ss.repository_url, sv.id AS file_version_id, sv.sha256, sv.encoding, sv.quality, sv.parser_version, octet_length(sv.content) AS file_size"
	if content {
		columns += ", sv.content AS raw_content, sv.symbols, sv.facts, sv.diagnostics"
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
	file.Content = string(file.RawContent)
	if source.HasReadScope(ctx) {
		file.Relations, file.RelationsTruncated, file.RelationsNextCursor, err = r.readVisibleSourceRelations(ctx, tenant, &file)
		if err != nil {
			return nil, err
		}
	}
	return &file, nil
}

const sourceRelationPageSize = 100

type sourceRelationCursor struct {
	SnapshotID    string `json:"s"`
	FileID        string `json:"f"`
	VersionID     string `json:"v"`
	FromPath      string `json:"p"`
	FromStartByte int64  `json:"b"`
	Kind          string `json:"k"`
	ToKey         string `json:"t"`
	ID            string `json:"i"`
}

func (r *knowledgeRepository) readVisibleSourceRelations(ctx context.Context, tenant uint64, file *types.SourceFileView) ([]types.SourceCodeRelation, bool, string, error) {
	var relations []types.SourceCodeRelation
	query := r.db.WithContext(ctx).Table("source_code_relations r").Select("r.*").
		Joins(`JOIN source_snapshots rs ON rs.id=r.snapshot_id AND rs.tenant_id=r.tenant_id AND rs.data_source_id=r.data_source_id AND rs.state='published'`).
		Joins(`JOIN source_snapshot_members fm ON fm.snapshot_id=r.snapshot_id AND fm.source_file_id=r.from_file_id AND fm.file_version_id=r.from_version_id AND fm.path=r.from_path AND fm.status='parsed'`).
		Joins(`JOIN source_file_versions fv ON fv.id=r.from_version_id AND fv.source_file_id=r.from_file_id AND fv.snapshot_id=r.snapshot_id`).
		Joins(`JOIN source_files ff ON ff.id=r.from_file_id AND ff.tenant_id=r.tenant_id AND ff.data_source_id=r.data_source_id`).
		Joins(`JOIN knowledges fk ON fk.id=ff.id AND fk.tenant_id=ff.tenant_id AND fk.knowledge_base_id=ff.knowledge_base_id AND fk.deleted_at IS NULL`).
		Joins(`LEFT JOIN source_snapshot_members tm ON tm.snapshot_id=r.snapshot_id AND tm.source_file_id=r.to_file_id AND tm.file_version_id=r.to_version_id AND tm.path=r.to_path AND tm.status='parsed'`).
		Joins(`LEFT JOIN source_file_versions tv ON tv.id=r.to_version_id AND tv.source_file_id=r.to_file_id AND tv.snapshot_id=r.snapshot_id`).
		Joins(`LEFT JOIN source_files tf ON tf.id=r.to_file_id AND tf.tenant_id=r.tenant_id AND tf.data_source_id=r.data_source_id`).
		Joins(`LEFT JOIN knowledges tk ON tk.id=tf.id AND tk.tenant_id=tf.tenant_id AND tk.knowledge_base_id=tf.knowledge_base_id AND tk.deleted_at IS NULL`).
		Where("r.tenant_id=? AND r.data_source_id=? AND r.snapshot_id=?", tenant, file.DataSourceID, file.SnapshotID).
		Where("rs.knowledge_base_id=? AND ff.knowledge_base_id=? AND (r.to_file_id='' OR tf.knowledge_base_id=?)", file.KnowledgeBaseID, file.KnowledgeBaseID, file.KnowledgeBaseID).
		Where("((r.from_file_id=? AND r.from_version_id=?) OR (r.to_file_id=? AND r.to_version_id=?))", file.KnowledgeID, file.FileVersionID, file.KnowledgeID, file.FileVersionID).
		Where("(r.to_file_id='' OR (tm.source_file_id IS NOT NULL AND tv.id IS NOT NULL AND tf.id IS NOT NULL AND tk.id IS NOT NULL))").
		Where(source.SnapshotSQL(ctx, "r.snapshot_id", "r.data_source_id", "r.from_file_id")).
		Where("(r.to_file_id='' OR " + source.SnapshotSQL(ctx, "r.snapshot_id", "r.data_source_id", "r.to_file_id") + ")")
	cursorToken := source.RelationCursorFromContext(ctx)
	if cursorToken != "" {
		var cursor sourceRelationCursor
		if len(cursorToken) > 4096 {
			return nil, false, "", fmt.Errorf("invalid source relation cursor")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(cursorToken)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.ID == "" ||
			cursor.SnapshotID != file.SnapshotID || cursor.FileID != file.KnowledgeID || cursor.VersionID != file.FileVersionID {
			return nil, false, "", fmt.Errorf("invalid source relation cursor")
		}
		query = query.Where(`(r.from_path, COALESCE(NULLIF(r.from_range->>'start_byte','')::bigint,0), r.kind, r.to_key, r.id) > (?, ?, ?, ?, ?)`,
			cursor.FromPath, cursor.FromStartByte, cursor.Kind, cursor.ToKey, cursor.ID)
	}
	pageSize := source.RelationPageSizeFromContext(ctx, sourceRelationPageSize)
	err := query.Order("r.from_path, COALESCE(NULLIF(r.from_range->>'start_byte','')::bigint,0), r.kind, r.to_key, r.id").Limit(pageSize + 1).Find(&relations).Error
	if err != nil {
		return nil, false, "", err
	}
	truncated := len(relations) > pageSize
	if truncated {
		relations = relations[:pageSize]
		last := relations[len(relations)-1]
		var location struct {
			StartByte int64 `json:"start_byte"`
		}
		if err := json.Unmarshal(last.FromRange, &location); err != nil {
			return nil, false, "", fmt.Errorf("invalid source relation range: %w", err)
		}
		next := sourceRelationCursor{SnapshotID: file.SnapshotID, FileID: file.KnowledgeID, VersionID: file.FileVersionID,
			FromPath: last.FromPath, FromStartByte: location.StartByte, Kind: last.Kind, ToKey: last.ToKey, ID: last.ID}
		encoded, err := json.Marshal(next)
		if err != nil {
			return nil, false, "", err
		}
		return relations, true, base64.RawURLEncoding.EncodeToString(encoded), nil
	}
	return relations, false, "", nil
}
