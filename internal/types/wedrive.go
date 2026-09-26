package types

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	WeDriveSourcePending           = "pending"
	WeDriveSourceAwaitingInventory = "awaiting_inventory"
	WeDriveSourceActive            = "active"
	WeDriveSourcePaused            = "paused"
	WeDriveSourceRejected          = "rejected"
	WeDriveSourceError             = "error"

	WeDriveScanStateIdle      = "idle"
	WeDriveScanStateWaiting   = "waiting"
	WeDriveScanStateRunning   = "running"
	WeDriveScanStateRetryWait = "retry_wait"
	WeDriveScanStateFailed    = "failed"

	WeDriveSnapshotUploading = "uploading"
	WeDriveSnapshotComplete  = "complete"
	WeDriveSnapshotFailed    = "failed"
)

const DefaultWeDriveScanIntervalMinutes = 30

func IsValidWeDriveScanInterval(minutes int) bool {
	switch minutes {
	case 0, 30, 60, 360, 1440:
		return true
	default:
		return false
	}
}

// WeComCLICredentials is encrypted as one atomic value. It is intentionally
// separate from DataSourceConfig: a tenant connection can serve many sources.
type WeComCLICredentials struct {
	BotID  string `json:"bot_id"`
	Secret string `json:"secret"`
}

func (c WeComCLICredentials) Value() (driver.Value, error) {
	if c.Secret != "" {
		key := utils.GetAESKey()
		if key == nil {
			return nil, fmt.Errorf("SYSTEM_AES_KEY must be configured before saving WeCom CLI credentials")
		}
		secret, err := utils.EncryptAESGCM(c.Secret, key)
		if err != nil {
			return nil, err
		}
		c.Secret = secret
	}
	return json.Marshal(c)
}

func (c *WeComCLICredentials) Scan(value interface{}) error {
	if value == nil {
		return nil
	}
	var raw []byte
	switch v := value.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return fmt.Errorf("unsupported WeCom credential value %T", value)
	}
	if err := json.Unmarshal(raw, c); err != nil {
		return err
	}
	if plain, ok := utils.DecryptStoredSecretLenient(c.Secret); ok {
		c.Secret = plain
	} else {
		log.Printf("[crypto] wecom CLI secret cannot be decrypted; treating connection as unconfigured")
		c.Secret = ""
	}
	return nil
}

type WeComCLIConnection struct {
	ID          string              `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID    uint64              `json:"tenant_id" gorm:"not null;index:idx_wecom_cli_connections_tenant"`
	Name        string              `json:"name" gorm:"type:varchar(128);not null"`
	Credentials WeComCLICredentials `json:"-" gorm:"column:credentials;type:jsonb;not null"`
	Configured  bool                `json:"configured" gorm:"-"`
	IsDefault   bool                `json:"is_default" gorm:"not null;default:false"`
	Status      string              `json:"status" gorm:"type:varchar(32);not null;default:'unknown'"`
	LastError   string              `json:"last_error,omitempty" gorm:"type:varchar(255);not null;default:''"`
	LastCheckAt *time.Time          `json:"last_check_at,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
	DeletedAt   gorm.DeletedAt      `json:"-" gorm:"index"`
}

func (WeComCLIConnection) TableName() string { return "wecom_cli_connections" }
func (c *WeComCLIConnection) BeforeCreate(*gorm.DB) error {
	if c.ID == "" {
		c.ID = uuid.NewString()
	}
	return nil
}
func (c *WeComCLIConnection) AfterFind(*gorm.DB) error {
	c.Configured = c.Credentials.BotID != "" && c.Credentials.Secret != ""
	return nil
}

type WeDriveDevice struct {
	ID           string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID     uint64         `json:"tenant_id" gorm:"not null;index:idx_wedrive_devices_tenant_user"`
	UserID       string         `json:"user_id" gorm:"type:varchar(36);not null;index:idx_wedrive_devices_tenant_user"`
	Name         string         `json:"name" gorm:"type:varchar(128);not null"`
	PublicKey    string         `json:"-" gorm:"type:text;not null"`
	AgentVersion string         `json:"agent_version" gorm:"type:varchar(32);not null;default:''"`
	Platform     string         `json:"platform" gorm:"type:varchar(32);not null;default:'windows'"`
	Status       string         `json:"status" gorm:"type:varchar(32);not null;default:'offline'"`
	LastSeenAt   *time.Time     `json:"last_seen_at,omitempty"`
	RevokedAt    *time.Time     `json:"revoked_at,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    gorm.DeletedAt `json:"-" gorm:"index"`
}

func (WeDriveDevice) TableName() string { return "wedrive_devices" }
func (d *WeDriveDevice) BeforeCreate(*gorm.DB) error {
	if d.ID == "" {
		d.ID = uuid.NewString()
	}
	return nil
}

type WeDriveRegistration struct {
	ID        string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID  uint64     `json:"tenant_id" gorm:"not null;index"`
	UserID    string     `json:"user_id" gorm:"type:varchar(36);not null;index"`
	CodeHash  string     `json:"-" gorm:"type:varchar(64);not null;uniqueIndex"`
	ExpiresAt time.Time  `json:"expires_at" gorm:"not null;index"`
	UsedAt    *time.Time `json:"-"`
	CreatedAt time.Time  `json:"created_at"`
}

func (WeDriveRegistration) TableName() string { return "wedrive_device_registrations" }
func (r *WeDriveRegistration) BeforeCreate(*gorm.DB) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	return nil
}

type WeDriveSource struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"not null;index:idx_wedrive_sources_tenant_status"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);not null;index"`
	DataSourceID    string `json:"data_source_id,omitempty" gorm:"type:varchar(36);index"`
	ConnectionID    string `json:"connection_id,omitempty" gorm:"type:varchar(36);index"`
	ConnectionName  string `json:"connection_name,omitempty" gorm:"-"`
	DeviceID        string `json:"device_id" gorm:"type:varchar(36);not null;index"`
	CreatedBy       string `json:"created_by" gorm:"type:varchar(36);not null;index"`
	ApprovedBy      string `json:"approved_by,omitempty" gorm:"type:varchar(36);not null;default:''"`
	Name            string `json:"name" gorm:"type:varchar(128);not null"`
	RootURL         string `json:"root_url" gorm:"type:text;not null"`
	RootExternalID  string `json:"root_external_id,omitempty" gorm:"type:varchar(255);not null;default:''"`
	Status          string `json:"status" gorm:"type:varchar(32);not null;index:idx_wedrive_sources_tenant_status"`
	AutoShare       bool   `json:"auto_share" gorm:"not null;default:true"`
	SyncDeletions   bool   `json:"sync_deletions" gorm:"not null;default:false"`
	SyncSchedule    string `json:"sync_schedule" gorm:"type:varchar(64);not null;default:''"`
	// No ORM default tag: zero is the persisted value for “manual only”. The
	// database migration retains its default for legacy rows, but GORM must not
	// replace an explicitly selected zero with 30 during Create.
	ScanIntervalMinutes int            `json:"scan_interval_minutes" gorm:"not null"`
	NextScanAt          *time.Time     `json:"next_scan_at,omitempty"`
	ScanRetryCount      int            `json:"scan_retry_count" gorm:"not null;default:0"`
	ScanState           string         `json:"scan_state" gorm:"type:varchar(32);not null;default:'idle'"`
	ScanLeaseExpiresAt  *time.Time     `json:"scan_lease_expires_at,omitempty"`
	LastScanStartedAt   *time.Time     `json:"last_scan_started_at,omitempty"`
	LastScanErrorCode   string         `json:"last_scan_error_code,omitempty" gorm:"type:varchar(64);not null;default:''"`
	LastSnapshotID      string         `json:"last_snapshot_id,omitempty" gorm:"type:varchar(36);not null;default:''"`
	LastCompleteScanAt  *time.Time     `json:"last_complete_scan_at,omitempty"`
	ApprovedAt          *time.Time     `json:"approved_at,omitempty"`
	CreatedAt           time.Time      `json:"created_at"`
	UpdatedAt           time.Time      `json:"updated_at"`
	DeletedAt           gorm.DeletedAt `json:"-" gorm:"index"`
}

func (WeDriveSource) TableName() string { return "wedrive_sources" }
func (s *WeDriveSource) BeforeCreate(*gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.Status == "" {
		s.Status = WeDriveSourcePending
	}
	return nil
}

type WeDriveSnapshot struct {
	ID                string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID          uint64     `json:"tenant_id" gorm:"not null;index"`
	SourceID          string     `json:"source_id" gorm:"type:varchar(36);not null;index:idx_wedrive_snapshots_source_created"`
	DeviceID          string     `json:"device_id" gorm:"type:varchar(36);not null;index"`
	Sequence          int64      `json:"sequence" gorm:"not null"`
	Status            string     `json:"status" gorm:"type:varchar(32);not null;default:'uploading'"`
	ExpectedItemCount int        `json:"expected_item_count" gorm:"not null;default:0"`
	ReceivedItemCount int        `json:"received_item_count" gorm:"not null;default:0"`
	Digest            string     `json:"digest,omitempty" gorm:"type:varchar(64);not null;default:''"`
	AutoShareExisting int        `json:"auto_share_existing" gorm:"not null;default:0"`
	AutoShareCreated  int        `json:"auto_share_created" gorm:"not null;default:0"`
	AutoShareFailed   int        `json:"auto_share_failed" gorm:"not null;default:0"`
	CommittedAt       *time.Time `json:"committed_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at" gorm:"index:idx_wedrive_snapshots_source_created,priority:2"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

func (WeDriveSnapshot) TableName() string { return "wedrive_snapshots" }
func (s *WeDriveSnapshot) BeforeCreate(*gorm.DB) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.Status == "" {
		s.Status = WeDriveSnapshotUploading
	}
	return nil
}

type WeDriveInventoryItem struct {
	ID                 string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID           uint64     `json:"tenant_id" gorm:"not null;index"`
	SourceID           string     `json:"source_id" gorm:"type:varchar(36);not null;index:idx_wedrive_items_source_snapshot"`
	SnapshotID         string     `json:"snapshot_id" gorm:"type:varchar(36);not null;index:idx_wedrive_items_source_snapshot;uniqueIndex:idx_wedrive_items_snapshot_external"`
	ExternalID         string     `json:"external_id" gorm:"type:varchar(512);not null;uniqueIndex:idx_wedrive_items_snapshot_external"`
	ParentExternalID   string     `json:"parent_external_id,omitempty" gorm:"type:varchar(512);not null;default:''"`
	Name               string     `json:"name" gorm:"type:varchar(512);not null"`
	Path               string     `json:"path" gorm:"type:text;not null"`
	ItemType           string     `json:"item_type" gorm:"type:varchar(64);not null"`
	DocID              string     `json:"doc_id,omitempty" gorm:"type:varchar(255);not null;default:''"`
	ShareURL           string     `json:"-" gorm:"type:text;not null;default:''"`
	ShareURLConfigured bool       `json:"share_url_configured" gorm:"-"`
	MimeType           string     `json:"mime_type,omitempty" gorm:"type:varchar(128);not null;default:''"`
	Size               int64      `json:"size" gorm:"not null;default:0"`
	ModifiedAt         *time.Time `json:"modified_at,omitempty"`
	ContentFingerprint string     `json:"content_fingerprint,omitempty" gorm:"type:varchar(128);not null;default:''"`
	Consumability      string     `json:"consumability" gorm:"type:varchar(64);not null;default:'unknown'"`
	FailureCode        string     `json:"failure_code,omitempty" gorm:"type:varchar(64);not null;default:''"`
	MissingCount       int        `json:"missing_count" gorm:"not null;default:0"`
	MissingSince       *time.Time `json:"missing_since,omitempty"`
	CreatedAt          time.Time  `json:"created_at"`
}

func (WeDriveInventoryItem) TableName() string { return "wedrive_inventory_items" }
func (i *WeDriveInventoryItem) BeforeCreate(*gorm.DB) error {
	if i.ID == "" {
		i.ID = uuid.NewString()
	}
	return nil
}

// UnmarshalJSON accepts a share URL only from the signed Agent inventory
// upload. ShareURL deliberately remains json:"-" for all normal API output,
// because it is a download credential rather than user-visible metadata.
func (i *WeDriveInventoryItem) UnmarshalJSON(data []byte) error {
	type plain WeDriveInventoryItem
	wire := struct {
		*plain
		ShareURL string `json:"share_url"`
	}{plain: (*plain)(i)}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	i.ShareURL = strings.TrimSpace(wire.ShareURL)
	return nil
}

func (i *WeDriveInventoryItem) BeforeSave(*gorm.DB) error {
	if i.ShareURL == "" {
		return nil
	}
	key := utils.GetAESKey()
	if key == nil {
		return fmt.Errorf("SYSTEM_AES_KEY must be configured before saving share links")
	}
	value, err := utils.EncryptAESGCM(i.ShareURL, key)
	if err != nil {
		return err
	}
	i.ShareURL = value
	return nil
}
func (i *WeDriveInventoryItem) AfterFind(*gorm.DB) error {
	i.ShareURLConfigured = i.ShareURL != ""
	if i.ShareURL == "" {
		return nil
	}
	plain, ok := utils.DecryptStoredSecretLenient(i.ShareURL)
	if !ok {
		log.Printf("[crypto] wedrive share URL cannot be decrypted; treating item as unconsumable")
		i.ShareURL = ""
		return nil
	}
	i.ShareURL = plain
	return nil
}
