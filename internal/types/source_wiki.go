package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

// SourceWikiEvidence is registered by the server from an immutable source read.
// Models cite only ID and cannot choose provenance or byte/line coordinates.
type SourceWikiEvidence struct {
	ID          string `json:"id"`
	KnowledgeID string `json:"knowledge_id"`
	SourceEvidence
	SHA256     string `json:"sha256"`
	TextSHA256 string `json:"text_sha256"`
}

type SourceWikiProvenance struct {
	SourceID             string               `json:"source_id"`
	ModulePath           string               `json:"module_path"`
	State                string               `json:"state"`
	ApplicableSnapshotID string               `json:"applicable_snapshot_id"`
	Evidence             []SourceWikiEvidence `json:"evidence"`
}

func (p SourceWikiProvenance) Value() (driver.Value, error) { return json.Marshal(p) }
func (p *SourceWikiProvenance) Scan(value any) error {
	if value == nil {
		*p = SourceWikiProvenance{}
		return nil
	}
	var data []byte
	switch v := value.(type) {
	case []byte:
		data = v
	case string:
		data = []byte(v)
	default:
		return fmt.Errorf("invalid source wiki provenance")
	}
	return json.Unmarshal(data, p)
}

type SourceWikiGenerateRequest struct {
	KnowledgeBaseID string `json:"knowledge_base_id"`
	SourceID        string `json:"source_id" binding:"required"`
	ModulePath      string `json:"module_path" binding:"required"`
	Title           string `json:"title" binding:"required"`
}
type SourceWikiAttempt struct {
	ID                   string      `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID             uint64      `json:"tenant_id"`
	KnowledgeBaseID      string      `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	SourceID             string      `json:"source_id" gorm:"type:varchar(36)"`
	SnapshotID           string      `json:"snapshot_id" gorm:"type:varchar(36)"`
	ModulePath           string      `json:"module_path"`
	Title                string      `json:"title"`
	Slug                 string      `json:"slug"`
	Status               string      `json:"status"`
	Reason               string      `json:"reason"`
	EvidenceKnowledgeIDs StringArray `json:"-" gorm:"type:jsonb"`
	Draft                JSON        `json:"draft" gorm:"type:jsonb"`
	Calls                int         `json:"calls"`
	Tokens               int         `json:"tokens"`
	Repairs              int         `json:"repairs"`
	CreatedAt            time.Time   `json:"created_at"`
	UpdatedAt            time.Time   `json:"updated_at"`
}
type SourceWikiEvidenceRef struct {
	ID            string  `gorm:"type:varchar(36);primaryKey"`
	PageID        string  `gorm:"type:varchar(36)"`
	RevisionID    *string `gorm:"type:varchar(36)"`
	Version       int
	EvidenceID    string
	SourceFileID  string `gorm:"type:varchar(36)"`
	FileVersionID string `gorm:"type:varchar(36)"`
	SnapshotID    string `gorm:"type:varchar(36)"`
}
