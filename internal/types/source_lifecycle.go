package types

import "time"

const (
	SourceBindingBound   = "bound"
	SourceBindingUnbound = "unbound"

	SourceCleanupPending   = "pending"
	SourceCleanupRunning   = "running"
	SourceCleanupFailed    = "failed"
	SourceCleanupCompleted = "completed"

	SourceCleanupScopeCurrentAndHistory = "current_and_history"
)

// SourceCleanupOperation is the durable, resumable record for withdrawing a
// source's current and historical published material. Internal cursor and
// lease fields never leave the service/repository boundary.
type SourceCleanupOperation struct {
	ID              string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	DataSourceID    string     `json:"-" gorm:"column:data_source_id;type:varchar(36);uniqueIndex"`
	TenantID        uint64     `json:"-" gorm:"column:tenant_id;index"`
	KnowledgeBaseID string     `json:"-" gorm:"column:knowledge_base_id;index"`
	Scope           string     `json:"-" gorm:"type:varchar(32)"`
	Status          string     `json:"status" gorm:"type:varchar(16);index"`
	Retryable       bool       `json:"retryable"`
	ErrorCode       string     `json:"error_code,omitempty" gorm:"type:varchar(64)"`
	Phase           string     `json:"-" gorm:"type:varchar(32)"`
	Cursor          JSON       `json:"-" gorm:"type:jsonb"`
	FencingToken    int64      `json:"-"`
	AttemptCount    int        `json:"-"`
	LeaseOwner      *string    `json:"-"`
	LeaseExpiresAt  *time.Time `json:"-"`
	CreatedAt       time.Time  `json:"-"`
	UpdatedAt       time.Time  `json:"-"`
	CompletedAt     *time.Time `json:"-"`
}

func (SourceCleanupOperation) TableName() string { return "source_cleanup_operations" }
