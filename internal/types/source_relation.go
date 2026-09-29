package types

import "time"

// SourceCodeRelation is an immutable, snapshot-scoped static edge. Ranges are
// JSONB so the relation keeps the same byte/line contract as SourceEvidence;
// no relation is valid without the source snapshot and source version IDs.
type SourceCodeRelation struct {
	ID               string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID         uint64    `json:"tenant_id"`
	DataSourceID     string    `json:"data_source_id" gorm:"type:varchar(36);index"`
	SnapshotID       string    `json:"snapshot_id" gorm:"type:varchar(36);index"`
	Kind             string    `json:"kind" gorm:"type:varchar(64);index"`
	FromFileID       string    `json:"from_file_id" gorm:"type:varchar(36);index"`
	FromVersionID    string    `json:"from_version_id" gorm:"type:varchar(36);index"`
	FromPath         string    `json:"from_path"`
	FromKey          string    `json:"from_key"`
	FromRange        JSON      `json:"from_range" gorm:"type:jsonb"`
	ToFileID         string    `json:"to_file_id" gorm:"type:varchar(36);index"`
	ToVersionID      string    `json:"to_version_id" gorm:"type:varchar(36);index"`
	ToPath           string    `json:"to_path"`
	ToKey            string    `json:"to_key"`
	ToRange          JSON      `json:"to_range" gorm:"type:jsonb"`
	Determinacy      string    `json:"determinacy" gorm:"type:varchar(24);index"`
	Quality          string    `json:"quality" gorm:"type:varchar(24)"`
	ResolutionReason string    `json:"resolution_reason,omitempty" gorm:"type:text;not null;default:''"`
	Context          JSON      `json:"context" gorm:"type:jsonb"`
	CreatedAt        time.Time `json:"created_at"`
}
