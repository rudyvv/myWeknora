package types

import "time"

// SourceRelationFactRef identifies an immutable parsed fact that causally
// contributed to a source relation. It deliberately carries identity and
// coordinates, not source text; the enclosing relation binds it to a source
// snapshot and the repeated IDs allow readers to reject copied cross-snapshot
// context.
type SourceRelationFactRef struct {
	DataSourceID  string      `json:"data_source_id"`
	SnapshotID    string      `json:"snapshot_id"`
	FileID        string      `json:"file_id"`
	FileVersionID string      `json:"file_version_id"`
	Path          string      `json:"path"`
	Kind          string      `json:"kind"`
	Role          string      `json:"role"`
	Quality       string      `json:"quality"`
	Range         SourceRange `json:"range"`
}

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
