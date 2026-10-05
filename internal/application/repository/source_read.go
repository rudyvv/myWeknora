package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// AcquireSourceRead takes publication row locks while recording the question's
// protected snapshots. Publishing and future collection use the same lock.
func (r *knowledgeRepository) AcquireSourceRead(ctx context.Context, targets types.SearchTargets) (types.SourceReadLease, func(), error) {
	if !r.sourceVisibility {
		for _, target := range targets {
			if target != nil && len(target.SourceIDs) > 0 {
				return types.SourceReadLease{}, nil, fmt.Errorf("source question reading requires the built-in PostgreSQL database")
			}
		}
		return types.SourceReadLease{}, func() {}, nil
	}
	id := uuid.NewString()
	lease := types.SourceReadLease{ID: id}
	expires := time.Now().Add(30 * time.Minute)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(expires) {
		expires = deadline
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("DELETE FROM source_read_leases WHERE expires_at<=now()").Error; err != nil {
			return err
		}
		if err := tx.Exec("INSERT INTO source_read_leases(id, expires_at) VALUES (?,?)", id, expires).Error; err != nil {
			return err
		}
		for _, target := range targets {
			if target == nil || target.KnowledgeBaseID == "" {
				continue
			}
			if target.Type == types.SearchTargetTypeKnowledge && len(target.KnowledgeIDs) == 0 {
				continue
			}
			var snapshots []types.SourcePublication
			q := tx.Table("source_publications sp").Select("sp.*").
				Joins("JOIN data_sources ds ON ds.id=sp.data_source_id AND ds.tenant_id=sp.tenant_id AND ds.knowledge_base_id=sp.knowledge_base_id AND ds.deleted_at IS NULL AND ds.source_query_enabled IS TRUE AND ds.config->'settings'->>'content_mode'='source'").
				Where("sp.knowledge_base_id=? AND sp.tenant_id=?", target.KnowledgeBaseID, target.TenantID)
			if len(target.SourceIDs) > 0 {
				q = q.Where("sp.data_source_id IN ?", target.SourceIDs)
			}
			tags := append(append([]string{}, target.TagIDs...), target.ScopeTagIDs...)
			if len(target.KnowledgeIDs) > 0 || len(tags) > 0 {
				member := tx.Table("source_snapshot_members pm").Select("1").
					Where("pm.snapshot_id=sp.snapshot_id AND pm.status='parsed'")
				if len(target.KnowledgeIDs) > 0 {
					member = member.Where("pm.source_file_id IN ?", target.KnowledgeIDs)
				}
				if len(tags) > 0 {
					member = member.Where("EXISTS (SELECT 1 FROM knowledge_tag_relations tr WHERE tr.knowledge_id=pm.source_file_id AND tr.tag_id IN ?)", tags)
				}
				q = q.Where("EXISTS (?)", member)
			}
			if err := q.Clauses(clause.Locking{Strength: "SHARE", Table: clause.Table{Name: "sp"}}).Find(&snapshots).Error; err != nil {
				return err
			}
			files := target.KnowledgeIDs
			if files == nil {
				files = []string{}
			}
			filesJSON, err := json.Marshal(files)
			if err != nil {
				return err
			}
			tagsJSON, err := json.Marshal(tags)
			if err != nil {
				return err
			}
			if tx.Migrator().HasTable("source_read_wiki_scopes") {
				sourceIDs := target.SourceIDs
				if sourceIDs == nil {
					sourceIDs = []string{}
				}
				sourcesJSON, err := json.Marshal(sourceIDs)
				if err != nil {
					return err
				}
				if err := tx.Exec(`INSERT INTO source_read_wiki_scopes(lease_id,knowledge_base_id,tenant_id,source_ids,knowledge_ids,tag_ids) VALUES (?,?,?,?::jsonb,?::jsonb,?::jsonb)`, id, target.KnowledgeBaseID, target.TenantID, string(sourcesJSON), string(filesJSON), string(tagsJSON)).Error; err != nil {
					return err
				}
			}
			if len(target.SourceIDs) == 0 {
				if err := tx.Exec(`INSERT INTO source_read_document_scopes(lease_id,knowledge_base_id,tenant_id,knowledge_ids,tag_ids) VALUES (?,?,?,?::jsonb,?::jsonb)`, id, target.KnowledgeBaseID, target.TenantID, string(filesJSON), string(tagsJSON)).Error; err != nil {
					return err
				}
			}
			for _, publication := range snapshots {
				lease.HasSources = true
				if err := tx.Exec(`INSERT INTO source_read_scopes(lease_id,snapshot_id,data_source_id,knowledge_base_id,tenant_id,knowledge_ids,tag_ids) VALUES (?,?,?,?,?,?::jsonb,?::jsonb)`,
					id, publication.SnapshotID, publication.DataSourceID, publication.KnowledgeBaseID, publication.TenantID, string(filesJSON), string(tagsJSON)).Error; err != nil {
					return err
				}
			}
		}
		if tx.Migrator().HasTable("source_wiki_evidence_refs") {
			if err := captureSourceWikiReadProjection(tx, ctx, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return types.SourceReadLease{}, nil, err
	}
	var once sync.Once
	cleanupLease := func() {
		once.Do(func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = r.db.WithContext(cleanup).Exec("DELETE FROM source_read_leases WHERE id=?", id).Error
		})
	}
	stop := context.AfterFunc(ctx, cleanupLease)
	release := func() { stop(); cleanupLease() }
	return lease, release, nil
}

func (r *knowledgeRepository) CheckSourceRead(ctx context.Context, id string) error {
	var active bool
	err := r.db.WithContext(ctx).Raw(`SELECT EXISTS(SELECT 1 FROM source_read_leases rl WHERE rl.id=? AND rl.expires_at>now()
		AND NOT EXISTS(SELECT 1 FROM source_read_scopes rs LEFT JOIN data_sources ds ON ds.id=rs.data_source_id
		WHERE rs.lease_id=rl.id AND (ds.id IS NULL OR ds.deleted_at IS NOT NULL OR ds.tenant_id<>rs.tenant_id OR ds.knowledge_base_id<>rs.knowledge_base_id OR ds.config->'settings'->>'content_mode' IS DISTINCT FROM 'source')))`, id).Scan(&active).Error
	if err != nil {
		return err
	}
	if !active {
		return fmt.Errorf("source question expired, completed or explicitly cleared")
	}
	return nil
}
