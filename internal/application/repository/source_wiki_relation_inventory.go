package repository

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

const sourceWikiRelationBoundsSelect = `
	COALESCE(SUM(OCTET_LENGTH(COALESCE(context::text, ''))), 0) AS context_bytes,
	COALESCE(SUM(
		OCTET_LENGTH(COALESCE(from_path, '')) + OCTET_LENGTH(COALESCE(from_key, '')) +
		OCTET_LENGTH(COALESCE(to_path, '')) + OCTET_LENGTH(COALESCE(to_key, '')) +
		OCTET_LENGTH(COALESCE(resolution_reason, '')) + OCTET_LENGTH(COALESCE(from_range::text, '')) +
		OCTET_LENGTH(COALESCE(to_range::text, '')) + OCTET_LENGTH(COALESCE(context::text, ''))
	), 0) AS relation_bytes`

// SourceWikiRelationInventory is a complete, bounded relation inventory for
// one authorized current publication. It deliberately contains no file facts.
type SourceWikiRelationInventory struct {
	TenantID          uint64
	KnowledgeBaseID   string
	DataSourceID      string
	SnapshotID        string
	ExpectedCount     int
	RelationsComplete bool
	Relations         []types.SourceCodeRelation
}

type sourceWikiRelationInventorySnapshotRow struct {
	ID                  string
	TenantID            uint64
	KnowledgeBaseID     string
	DataSourceID        string
	ManifestComplete    bool
	RelationsStaged     bool
	WikiDerivationState string
	RelationCount       int
}

type sourceWikiRelationInventoryBounds struct {
	RelationCount int64
	ContextBytes  int64
	RelationBytes int64
}

// LoadSourceWikiRelationInventory reads only relation rows for the fixed
// current published snapshot. Both count and serialized context bytes are
// checked before any rows are materialized.
func LoadSourceWikiRelationInventory(
	ctx context.Context,
	db *gorm.DB,
	tenantID uint64,
	knowledgeBaseID, sourceID, snapshotID string,
) (*SourceWikiRelationInventory, error) {
	if db == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" {
		return nil, fmt.Errorf("source Wiki relation inventory requires a fixed tenant, KB, source, and snapshot")
	}
	if db.Statement != nil {
		if committer, ok := db.Statement.ConnPool.(gorm.TxCommitter); ok && committer != nil {
			return nil, fmt.Errorf("source Wiki relation inventory does not accept an existing transaction")
		}
	}
	if db.Dialector.Name() != "postgres" {
		return nil, fmt.Errorf("source Wiki relation inventory requires PostgreSQL")
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, fmt.Errorf("validate source Wiki relation inventory read scope: %w", err)
	}

	var inventory *SourceWikiRelationInventory
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshot sourceWikiRelationInventorySnapshotRow
		permission := source.SourcePermissionSQL(ctx, "ss.data_source_id", "NULL")
		query := tx.Table("source_snapshots ss").
			Select(`ss.id, ss.tenant_id, ss.knowledge_base_id, ss.data_source_id,
				ss.manifest_complete, ss.relations_staged, ss.wiki_derivation_state, ss.relation_count`).
			Joins(`JOIN source_publications sp ON sp.snapshot_id=ss.id AND sp.data_source_id=ss.data_source_id
				AND sp.tenant_id=ss.tenant_id AND sp.knowledge_base_id=ss.knowledge_base_id`).
			Where(`ss.id=? AND ss.tenant_id=? AND ss.knowledge_base_id=? AND ss.data_source_id=?
				AND ss.state='published'`, snapshotID, tenantID, knowledgeBaseID, sourceID).
			Where(source.SnapshotSQL(ctx, "ss.id", "ss.data_source_id", "NULL")).
			Where(permission)
		if err := query.Take(&snapshot).Error; err != nil {
			return fmt.Errorf("source Wiki relation inventory publication is unavailable or unauthorized: %w", err)
		}
		if snapshot.ID != snapshotID || snapshot.TenantID != tenantID || snapshot.KnowledgeBaseID != knowledgeBaseID || snapshot.DataSourceID != sourceID {
			return fmt.Errorf("source Wiki relation inventory publication identity changed")
		}
		if snapshot.WikiDerivationState == "deferred_capacity" {
			return fmt.Errorf("%w: this source snapshot remains searchable, but its Wiki relation inventory was deferred by capacity", ErrSourceWikiDerivationDeferred)
		}
		if !snapshot.ManifestComplete || !snapshot.RelationsStaged || snapshot.WikiDerivationState != "complete" || snapshot.RelationCount < 0 {
			return fmt.Errorf("%w: the published source has no complete Wiki relation inventory", ErrSourceWikiDerivationUnavailable)
		}
		if snapshot.RelationCount > types.SourceWikiSkeletonMaxRelations {
			return fmt.Errorf("%w: source Wiki relation inventory exceeds the %d-edge bound", ErrSourceWikiDerivationDeferred, types.SourceWikiSkeletonMaxRelations)
		}

		var bounds sourceWikiRelationInventoryBounds
		if err := tx.Table("source_code_relations").
			Select("COUNT(*) AS relation_count, "+sourceWikiRelationBoundsSelect).
			Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).
			Scan(&bounds).Error; err != nil {
			return fmt.Errorf("read source Wiki relation inventory bounds: %w", err)
		}
		if bounds.RelationCount > types.SourceWikiSkeletonMaxRelations {
			return fmt.Errorf("%w: source Wiki relation inventory exceeds the %d-edge bound", ErrSourceWikiDerivationDeferred, types.SourceWikiSkeletonMaxRelations)
		}
		if bounds.ContextBytes > types.SourceWikiImpactMaxContextBytes {
			return fmt.Errorf("%w: source Wiki relation contexts exceed the %d-byte bound", ErrSourceWikiDerivationDeferred, types.SourceWikiImpactMaxContextBytes)
		}
		if bounds.RelationBytes > types.SourceWikiImpactMaxRelationBytes {
			return fmt.Errorf("%w: source Wiki relation fields exceed the %d-byte bound", ErrSourceWikiDerivationDeferred, types.SourceWikiImpactMaxRelationBytes)
		}
		if bounds.RelationCount != int64(snapshot.RelationCount) {
			return fmt.Errorf("%w: source Wiki relation inventory does not match its published count", ErrSourceWikiDerivationUnavailable)
		}

		var relations []types.SourceCodeRelation
		if err := tx.Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenantID, sourceID, snapshotID).
			Order("from_path ASC, kind ASC, from_key ASC, to_path ASC, to_key ASC, id ASC").
			Limit(types.SourceWikiSkeletonMaxRelations + 1).Find(&relations).Error; err != nil {
			return fmt.Errorf("load source Wiki relation inventory: %w", err)
		}
		if len(relations) > types.SourceWikiSkeletonMaxRelations {
			return fmt.Errorf("%w: source Wiki relation inventory exceeds the %d-edge bound", ErrSourceWikiDerivationDeferred, types.SourceWikiSkeletonMaxRelations)
		}
		if len(relations) != snapshot.RelationCount {
			return fmt.Errorf("%w: source Wiki relation inventory changed while being read", ErrSourceWikiDerivationUnavailable)
		}
		seen := make(map[string]struct{}, len(relations))
		var contextBytes, relationBytes int64
		for _, relation := range relations {
			if relation.ID == "" || relation.TenantID != tenantID || relation.DataSourceID != sourceID || relation.SnapshotID != snapshotID {
				return fmt.Errorf("%w: source Wiki relation identity is outside the fixed publication", ErrSourceWikiDerivationUnavailable)
			}
			if _, exists := seen[relation.ID]; exists {
				return fmt.Errorf("%w: source Wiki relation inventory contains duplicate identities", ErrSourceWikiDerivationUnavailable)
			}
			seen[relation.ID] = struct{}{}
			contextBytes += int64(len(relation.Context))
			if contextBytes > types.SourceWikiImpactMaxContextBytes {
				return fmt.Errorf("%w: source Wiki relation contexts exceed the %d-byte bound", ErrSourceWikiDerivationDeferred, types.SourceWikiImpactMaxContextBytes)
			}
			relationBytes += sourceWikiRelationVariableBytes(relation)
			if relationBytes > types.SourceWikiImpactMaxRelationBytes {
				return fmt.Errorf("%w: source Wiki relation fields exceed the %d-byte bound", ErrSourceWikiDerivationDeferred, types.SourceWikiImpactMaxRelationBytes)
			}
		}
		if int64(len(relations)) != bounds.RelationCount || contextBytes > bounds.ContextBytes || relationBytes > bounds.RelationBytes {
			return fmt.Errorf("%w: source Wiki relation inventory is incomplete", ErrSourceWikiDerivationUnavailable)
		}
		inventory = &SourceWikiRelationInventory{
			TenantID: tenantID, KnowledgeBaseID: knowledgeBaseID, DataSourceID: sourceID, SnapshotID: snapshotID,
			ExpectedCount: snapshot.RelationCount, RelationsComplete: true, Relations: relations,
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, fmt.Errorf("source Wiki relation inventory read scope changed: %w", err)
	}
	return inventory, nil
}

func sourceWikiRelationVariableBytes(relation types.SourceCodeRelation) int64 {
	return int64(len(relation.FromPath)) + int64(len(relation.FromKey)) + int64(len(relation.ToPath)) +
		int64(len(relation.ToKey)) + int64(len(relation.ResolutionReason)) + int64(len(relation.FromRange)) +
		int64(len(relation.ToRange)) + int64(len(relation.Context))
}
