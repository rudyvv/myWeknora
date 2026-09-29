package repository

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type sourceCodeRelationRepository struct{ db *gorm.DB }

func NewSourceCodeRelationRepository(db *gorm.DB) interfaces.SourceCodeRelationRepository {
	return &sourceCodeRelationRepository{db: db}
}

func (r *sourceCodeRelationRepository) ReplaceSnapshotRelations(ctx context.Context, tenant uint64, sourceID, snapshotID string, relations []types.SourceCodeRelation) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenant, sourceID, snapshotID).Delete(&types.SourceCodeRelation{}).Error; err != nil {
			return err
		}
		for i := range relations {
			if relations[i].ID == "" {
				relations[i].ID = uuid.NewString()
			}
			relations[i].TenantID, relations[i].DataSourceID, relations[i].SnapshotID = tenant, sourceID, snapshotID
		}
		if len(relations) == 0 {
			return nil
		}
		return tx.CreateInBatches(&relations, 100).Error
	})
}

func (r *sourceCodeRelationRepository) ListSnapshotRelations(ctx context.Context, tenant uint64, sourceID, snapshotID string, query interfaces.SourceRelationQuery) ([]types.SourceCodeRelation, error) {
	if query.Limit <= 0 || query.Limit > 500 {
		query.Limit = 500
	}
	db := r.db.WithContext(ctx).Where("tenant_id=? AND data_source_id=? AND snapshot_id=?", tenant, sourceID, snapshotID)
	if query.Kind != "" {
		db = db.Where("kind=?", query.Kind)
	}
	if query.Key != "" {
		db = db.Where("from_key=? OR to_key=?", query.Key, query.Key)
	}
	var relations []types.SourceCodeRelation
	err := db.Order("from_path, from_range->>'start_byte', kind, to_key").Limit(query.Limit).Find(&relations).Error
	return relations, err
}
