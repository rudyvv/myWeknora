package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"time"
)

const (
	SourceWikiAttemptMaxCalls                  = 18
	SourceWikiAttemptMaxTokens                 = 360000
	SourceWikiAttemptMaxElapsedMS        int64 = 180000
	SourceWikiAttemptMaxRepairs                = 2
	SourceWikiAttemptMaxCompletionTokens       = 4096
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
	TopicKind            string               `json:"topic_kind,omitempty"`
	TopicKey             string               `json:"topic_key,omitempty"`
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
	// AttemptID is supplied only to resume a previously returned running
	// attempt. Omitting it creates a fresh attempt with fresh limits.
	AttemptID  string `json:"attempt_id,omitempty"`
	SourceID   string `json:"source_id" binding:"required"`
	ModulePath string `json:"module_path" binding:"required"`
	Title      string `json:"title" binding:"required"`
	// These fields are populated only by the server-side durable batch runner.
	// They are excluded from JSON so callers cannot select a batch or topic.
	BatchID   string               `json:"-"`
	TopicKind string               `json:"-"`
	TopicKey  string               `json:"-"`
	Relations []SourceCodeRelation `json:"-"`
}

type SourceWikiAttemptResultKind string

const (
	SourceWikiAttemptResultKindInsufficientEvidence SourceWikiAttemptResultKind = "insufficient_evidence"
)

type SourceWikiAttempt struct {
	ID                       string                      `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID                 uint64                      `json:"tenant_id"`
	KnowledgeBaseID          string                      `json:"knowledge_base_id" gorm:"type:varchar(36)"`
	SourceID                 string                      `json:"source_id" gorm:"type:varchar(36)"`
	SnapshotID               string                      `json:"snapshot_id" gorm:"type:varchar(36)"`
	BatchID                  string                      `json:"batch_id,omitempty" gorm:"type:varchar(36);index"`
	TopicKind                string                      `json:"topic_kind,omitempty" gorm:"type:text;not null;default:''"`
	TopicKey                 string                      `json:"topic_key,omitempty" gorm:"type:text;not null;default:''"`
	ModulePath               string                      `json:"module_path"`
	Title                    string                      `json:"title"`
	Slug                     string                      `json:"slug"`
	Status                   string                      `json:"status"`
	ResultKind               SourceWikiAttemptResultKind `json:"result_kind,omitempty" gorm:"type:text;not null;default:''"`
	Reason                   string                      `json:"reason"`
	EvidenceKnowledgeIDs     StringArray                 `json:"-" gorm:"type:jsonb"`
	Draft                    JSON                        `json:"draft" gorm:"type:jsonb"`
	Calls                    int                         `json:"calls"`
	Tokens                   int                         `json:"tokens"`
	Repairs                  int                         `json:"repairs"`
	SourceConfigFingerprint  string                      `json:"-" gorm:"type:varchar(64);not null;default:''"`
	SourceUpdatedAt          time.Time                   `json:"-" gorm:"not null"`
	ModelID                  string                      `json:"-" gorm:"type:varchar(36);not null;default:''"`
	ModelSettingsFingerprint string                      `json:"-" gorm:"type:varchar(64);not null;default:''"`
	ModelContextWindow       int                         `json:"-" gorm:"not null;default:0"`
	MaxCompletionTokens      int                         `json:"-" gorm:"not null;default:4096"`
	BasePageVersion          int                         `json:"-" gorm:"not null;default:0"`
	Epoch                    int64                       `json:"-" gorm:"not null;default:0"`
	LeaseOwner               string                      `json:"-" gorm:"type:varchar(36);not null;default:''"`
	LeaseExpiresAt           *time.Time                  `json:"-"`
	DeadlineAt               time.Time                   `json:"deadline_at" gorm:"not null"`
	MaxCalls                 int                         `json:"max_calls" gorm:"not null"`
	MaxTokens                int                         `json:"max_tokens" gorm:"not null"`
	MaxElapsedMS             int64                       `json:"max_elapsed_ms" gorm:"not null"`
	MaxRepairs               int                         `json:"max_repairs" gorm:"not null"`
	Phase                    string                      `json:"phase,omitempty" gorm:"type:text;not null;default:''"`
	Checkpoint               JSON                        `json:"-" gorm:"type:jsonb"`
	StagedAt                 *time.Time                  `json:"staged_at,omitempty"`
	StagedPageVersion        int                         `json:"-" gorm:"not null;default:0"`
	CreatedAt                time.Time                   `json:"created_at"`
	UpdatedAt                time.Time                   `json:"updated_at"`
}

// SourceWikiAttemptLease is an epoch-fenced claim on one durable attempt.
// The epoch and owner must accompany every heartbeat, reservation, checkpoint,
// and terminal transition.
type SourceWikiAttemptLease struct {
	AttemptID                string
	Owner                    string
	Epoch                    int64
	ModelID                  string
	ModelSettingsFingerprint string
	ModelContextWindow       int
	MaxCompletionTokens      int
}

type SourceWikiAttemptClaimRequest struct {
	AttemptID string
	Owner     string
	Now       time.Time
	LeaseFor  time.Duration
}

type SourceWikiAttemptCallReservation struct {
	ID             string
	AttemptID      string
	Epoch          int64
	CallNumber     int
	Phase          string
	ReservedTokens int
}

type SourceWikiAttemptCallReservationRequest struct {
	Lease          SourceWikiAttemptLease
	Phase          string
	ReservedTokens int
	Now            time.Time
	LeaseFor       time.Duration
}

type SourceWikiAttemptCallCompletion struct {
	Lease         SourceWikiAttemptLease
	ReservationID string
	Outcome       string
	ActualTokens  *int
	Checkpoint    JSON
	Draft         JSON
	NextPhase     string
	Repairs       *int
	Now           time.Time
}

type SourceWikiAttemptProgress struct {
	Phase      string
	Checkpoint JSON
	Draft      JSON
	Repairs    int
	Now        time.Time
}

type SourceWikiAttemptCall struct {
	ID             string `gorm:"type:varchar(36);primaryKey"`
	AttemptID      string `gorm:"type:varchar(36);not null;index"`
	Epoch          int64  `gorm:"not null"`
	CallNumber     int    `gorm:"not null"`
	Phase          string `gorm:"type:text;not null"`
	ReservedTokens int    `gorm:"not null"`
	ActualTokens   *int
	Outcome        string    `gorm:"type:text;not null"`
	CreatedAt      time.Time `gorm:"not null"`
	CompletedAt    *time.Time
}

func (SourceWikiAttemptCall) TableName() string { return "source_wiki_attempt_calls" }

type SourceWikiEvidenceRef struct {
	ID            string  `gorm:"type:varchar(36);primaryKey"`
	PageID        string  `gorm:"type:varchar(36)"`
	RevisionID    *string `gorm:"type:varchar(36)"`
	Version       int
	EvidenceID    string
	SourceFileID  string `gorm:"type:varchar(36)"`
	FileVersionID string `gorm:"type:varchar(36)"`
	SnapshotID    string `gorm:"type:varchar(36)"`
	Path          string `gorm:"type:text"`
	CommitSHA     string `gorm:"type:text"`
}
