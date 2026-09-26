package service

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrWeDriveNotFound        = errors.New("wedrive resource not found")
	ErrWeDriveForbidden       = errors.New("wedrive access denied")
	ErrWeDriveInvalidState    = errors.New("wedrive resource is in an invalid state")
	ErrWeDriveInvalidSnapshot = errors.New("invalid wedrive inventory snapshot")
	ErrWeDriveScanNotDue      = errors.New("wedrive scan is not due")
	ErrWeDriveScanBusy        = errors.New("wedrive scan is already running")
)

type WeDriveService struct {
	db          *gorm.DB
	datasources interfaces.DataSourceService
	audit       interfaces.AuditLogService
	now         func() time.Time
	startedAt   time.Time
}

type weDriveProvisioningKey struct{}

func withWeDriveProvisioning(ctx context.Context) context.Context {
	return context.WithValue(ctx, weDriveProvisioningKey{}, true)
}

func NewWeDriveService(db *gorm.DB, datasources interfaces.DataSourceService, audit interfaces.AuditLogService) *WeDriveService {
	startedAt := time.Now().UTC()
	return &WeDriveService{
		db: db, datasources: datasources, audit: audit,
		now: func() time.Time { return time.Now().UTC() }, startedAt: startedAt,
	}
}

type WeComConnectionInput struct {
	Name      string `json:"name"`
	BotID     string `json:"bot_id"`
	Secret    string `json:"secret"`
	IsDefault bool   `json:"is_default"`
}

func (s *WeDriveService) CreateConnection(ctx context.Context, tenantID uint64, in WeComConnectionInput) (*types.WeComCLIConnection, error) {
	in.Name, in.BotID, in.Secret = strings.TrimSpace(in.Name), strings.TrimSpace(in.BotID), strings.TrimSpace(in.Secret)
	if tenantID == 0 || in.Name == "" || in.BotID == "" || in.Secret == "" {
		return nil, fmt.Errorf("name, bot_id and secret are required")
	}
	conn := &types.WeComCLIConnection{
		TenantID: tenantID, Name: in.Name, IsDefault: in.IsDefault,
		Credentials: types.WeComCLICredentials{BotID: in.BotID, Secret: in.Secret}, Status: "unknown",
	}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if in.IsDefault {
			if err := tx.Model(&types.WeComCLIConnection{}).Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Create(conn).Error
	})
	if err != nil {
		return nil, err
	}
	conn.Configured = true
	return conn, nil
}

func (s *WeDriveService) ListConnections(ctx context.Context, tenantID uint64) ([]*types.WeComCLIConnection, error) {
	var rows []*types.WeComCLIConnection
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND deleted_at IS NULL", tenantID).
		Order("is_default DESC, created_at ASC").Find(&rows).Error
	return rows, err
}

func (s *WeDriveService) UpdateConnection(ctx context.Context, tenantID uint64, id string, in WeComConnectionInput) (*types.WeComCLIConnection, error) {
	var conn types.WeComCLIConnection
	if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&conn).Error; err != nil {
		return nil, ErrWeDriveNotFound
	}
	if name := strings.TrimSpace(in.Name); name != "" {
		conn.Name = name
	}
	if botID := strings.TrimSpace(in.BotID); botID != "" {
		conn.Credentials.BotID = botID
	}
	if secret := strings.TrimSpace(in.Secret); secret != "" {
		conn.Credentials.Secret = secret
	}
	conn.IsDefault = in.IsDefault
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if conn.IsDefault {
			if err := tx.Model(&types.WeComCLIConnection{}).
				Where("tenant_id = ? AND id <> ? AND deleted_at IS NULL", tenantID, id).
				Update("is_default", false).Error; err != nil {
				return err
			}
		}
		return tx.Save(&conn).Error
	})
	if err != nil {
		return nil, err
	}
	conn.Configured = conn.Credentials.BotID != "" && conn.Credentials.Secret != ""
	return &conn, nil
}

func (s *WeDriveService) DeleteConnection(ctx context.Context, tenantID uint64, id string) error {
	var count int64
	if err := s.db.WithContext(ctx).Model(&types.WeDriveSource{}).
		Where("tenant_id = ? AND connection_id = ? AND deleted_at IS NULL", tenantID, id).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return fmt.Errorf("connection is used by %d WeDrive source(s)", count)
	}
	result := s.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", tenantID, id).Delete(&types.WeComCLIConnection{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrWeDriveNotFound
	}
	return nil
}

type RegistrationCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *WeDriveService) CreateRegistration(ctx context.Context, tenantID uint64, userID string) (*RegistrationCode, error) {
	if tenantID == 0 || userID == "" {
		return nil, ErrWeDriveForbidden
	}
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	code := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(code))
	expires := s.now().Add(10 * time.Minute)
	row := &types.WeDriveRegistration{TenantID: tenantID, UserID: userID, CodeHash: hex.EncodeToString(sum[:]), ExpiresAt: expires}
	if err := s.db.WithContext(ctx).Create(row).Error; err != nil {
		return nil, err
	}
	return &RegistrationCode{Code: code, ExpiresAt: expires}, nil
}

type RegisterDeviceInput struct {
	Code         string `json:"code"`
	Name         string `json:"name"`
	PublicKey    string `json:"public_key"`
	AgentVersion string `json:"agent_version"`
	Platform     string `json:"platform"`
}

func (s *WeDriveService) RegisterDevice(ctx context.Context, in RegisterDeviceInput) (*types.WeDriveDevice, error) {
	key, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(in.PublicKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("public_key must be an Ed25519 public key")
	}
	if strings.TrimSpace(in.Name) == "" {
		return nil, fmt.Errorf("device name is required")
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(in.Code)))
	hash := hex.EncodeToString(sum[:])
	var device types.WeDriveDevice
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reg types.WeDriveRegistration
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("code_hash = ?", hash).First(&reg).Error; err != nil {
			return ErrWeDriveForbidden
		}
		if reg.UsedAt != nil || !reg.ExpiresAt.After(s.now()) {
			return ErrWeDriveForbidden
		}
		device = types.WeDriveDevice{TenantID: reg.TenantID, UserID: reg.UserID, Name: strings.TrimSpace(in.Name),
			PublicKey: in.PublicKey, AgentVersion: strings.TrimSpace(in.AgentVersion), Platform: strings.TrimSpace(in.Platform), Status: "offline"}
		if device.Platform == "" {
			device.Platform = "windows"
		}
		if err := tx.Create(&device).Error; err != nil {
			return err
		}
		now := s.now()
		return tx.Model(&reg).Update("used_at", &now).Error
	})
	if err != nil {
		return nil, err
	}
	return &device, nil
}

func (s *WeDriveService) ListDevices(ctx context.Context, tenantID uint64, userID string, admin bool) ([]*types.WeDriveDevice, error) {
	q := s.db.WithContext(ctx).Where("tenant_id = ? AND deleted_at IS NULL", tenantID)
	if !admin {
		q = q.Where("user_id = ?", userID)
	}
	var rows []*types.WeDriveDevice
	err := q.Order("created_at DESC").Find(&rows).Error
	return rows, err
}

func (s *WeDriveService) RecordAgentVersion(ctx context.Context, device *types.WeDriveDevice, version string) {
	version = strings.TrimSpace(version)
	if device == nil || version == "" || version == device.AgentVersion || len(version) > 32 {
		return
	}
	if s.db.WithContext(ctx).Model(&types.WeDriveDevice{}).Where("id = ?", device.ID).Update("agent_version", version).Error == nil {
		device.AgentVersion = version
	}
}

// SupportsWeDriveScanCadence is deliberately conservative: older Agents have
// their own fixed thirty-minute timer and must never receive sources whose
// cadence they cannot honour.  A malformed or missing version is treated as
// an old Agent.
func SupportsWeDriveScanCadence(version string) bool {

	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) < 2 {
		return false
	}
	major, majorErr := strconv.Atoi(parts[0])
	minor, minorErr := strconv.Atoi(parts[1])
	if majorErr != nil || minorErr != nil {
		return false
	}
	return major > 0 || (major == 0 && minor >= 3)
}

func (s *WeDriveService) GetAuthorizedDevice(ctx context.Context, tenantID uint64, userID, id string, admin bool) (*types.WeDriveDevice, error) {
	q := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND revoked_at IS NULL AND deleted_at IS NULL", id, tenantID)
	if !admin {
		q = q.Where("user_id = ?", userID)
	}
	var row types.WeDriveDevice
	if err := q.First(&row).Error; err != nil {
		return nil, ErrWeDriveNotFound
	}
	return &row, nil
}

func (s *WeDriveService) RevokeDevice(ctx context.Context, tenantID uint64, userID, id string, admin bool) error {
	q := s.db.WithContext(ctx).Model(&types.WeDriveDevice{}).Where("tenant_id = ? AND id = ? AND revoked_at IS NULL", tenantID, id)
	if !admin {
		q = q.Where("user_id = ?", userID)
	}
	now := s.now()
	result := q.Updates(map[string]interface{}{"revoked_at": &now, "status": "revoked"})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrWeDriveNotFound
	}
	return nil
}

type CreateWeDriveSourceInput struct {
	KnowledgeBaseID     string `json:"knowledge_base_id"`
	ConnectionID        string `json:"connection_id"`
	DeviceID            string `json:"device_id"`
	Name                string `json:"name"`
	RootURL             string `json:"root_url"`
	AutoShare           *bool  `json:"auto_share"`
	SyncDeletions       bool   `json:"sync_deletions"`
	SyncSchedule        string `json:"sync_schedule"`
	ScanIntervalMinutes *int   `json:"scan_interval_minutes"`
}

func (s *WeDriveService) CreateSource(ctx context.Context, tenantID uint64, userID string, role types.TenantRole, in CreateWeDriveSourceInput) (*types.WeDriveSource, error) {
	in.Name, in.RootURL = strings.TrimSpace(in.Name), strings.TrimSpace(in.RootURL)
	if in.KnowledgeBaseID == "" || in.DeviceID == "" || in.Name == "" || in.RootURL == "" {
		return nil, fmt.Errorf("knowledge_base_id, device_id, name and root_url are required")
	}
	if !validWeDriveRootURL(in.RootURL) {
		return nil, fmt.Errorf("root_url must be an HTTPS drive.weixin.qq.com URL")
	}
	var device types.WeDriveDevice
	q := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND revoked_at IS NULL AND deleted_at IS NULL", in.DeviceID, tenantID)
	if !role.HasPermission(types.TenantRoleAdmin) {
		q = q.Where("user_id = ?", userID)
	}
	if err := q.First(&device).Error; err != nil {
		return nil, ErrWeDriveForbidden
	}
	admin := role.HasPermission(types.TenantRoleAdmin)
	if !admin {
		in.ConnectionID = ""
		// Contributors may submit a source, but the frequency is an
		// operational policy owned by Admin/Owner. Keep their submission at
		// the governed manual default instead of accepting a forged form value.
		in.ScanIntervalMinutes = nil
	}
	if admin && in.ConnectionID == "" {
		var conn types.WeComCLIConnection
		if err := s.db.WithContext(ctx).Where("tenant_id = ? AND is_default = ? AND deleted_at IS NULL", tenantID, true).First(&conn).Error; err == nil {
			in.ConnectionID = conn.ID
		}
	}
	if in.ConnectionID != "" {
		var n int64
		if err := s.db.WithContext(ctx).Model(&types.WeComCLIConnection{}).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", in.ConnectionID, tenantID).Count(&n).Error; err != nil || n == 0 {
			return nil, ErrWeDriveForbidden
		}
	}
	autoShare := true
	if in.AutoShare != nil {
		autoShare = *in.AutoShare
	}
	// Content ingestion is manual. Directory cadence is stored separately and
	// must not be interpreted as a DataSource cron schedule.
	in.SyncSchedule = ""
	// New sources start with manual scans. Existing sources keep their stored
	// cadence, including the 30-minute value assigned during migration.
	scanInterval := 0
	if in.ScanIntervalMinutes != nil {
		scanInterval = *in.ScanIntervalMinutes
	}
	if !types.IsValidWeDriveScanInterval(scanInterval) {
		return nil, fmt.Errorf("scan_interval_minutes must be one of 0, 30, 60, 360 or 1440")
	}
	status := types.WeDriveSourcePending
	var approvedAt *time.Time
	approvedBy := ""
	if admin {
		if in.ConnectionID == "" {
			return nil, fmt.Errorf("a CLI connection is required")
		}
		status = types.WeDriveSourceAwaitingInventory
		now := s.now()
		approvedAt = &now
		approvedBy = userID
	}
	source := &types.WeDriveSource{TenantID: tenantID, KnowledgeBaseID: in.KnowledgeBaseID, ConnectionID: in.ConnectionID,
		DeviceID: in.DeviceID, CreatedBy: userID, ApprovedBy: approvedBy, Name: in.Name, RootURL: in.RootURL,
		Status: status, AutoShare: autoShare, SyncDeletions: in.SyncDeletions, SyncSchedule: in.SyncSchedule,
		ScanIntervalMinutes: scanInterval, ApprovedAt: approvedAt}
	scanNow := time.Time{}
	if approvedAt != nil {
		scanNow = *approvedAt
	}
	(weDriveScanLifecycle{source}).initialize(admin, scanNow)
	// ScanIntervalMinutes intentionally accepts zero for “manual only”. Include
	// it explicitly so the schema default used for legacy rows cannot replace
	// a caller-selected manual cadence during Create.
	if err := s.db.WithContext(ctx).Omit(clause.Associations).Select("*").Create(source).Error; err != nil {
		return nil, err
	}
	recordKBActivity(ctx, s.audit, source.TenantID, source.KnowledgeBaseID, types.AuditActionWeDriveSourceSubmitted,
		"wedrive_source", source.ID, types.AuditOutcomeAccepted,
		map[string]any{"name": source.Name, "status": source.Status, "auto_share": source.AutoShare, "sync_deletions": source.SyncDeletions})
	return source, nil
}

type UpdateWeDriveScanSettingsInput struct {
	ScanIntervalMinutes int `json:"scan_interval_minutes"`
}

// UpdateSourceScanSettings changes only RPA directory discovery. It must never
// update the data-source SyncSchedule, which controls the separate CLI worker.
func (s *WeDriveService) UpdateSourceScanSettings(ctx context.Context, tenantID uint64, id string, in UpdateWeDriveScanSettingsInput) (*types.WeDriveSource, error) {
	if !types.IsValidWeDriveScanInterval(in.ScanIntervalMinutes) {
		return nil, fmt.Errorf("scan_interval_minutes must be one of 0, 30, 60, 360 or 1440")
	}
	var source types.WeDriveSource
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&source).Error; err != nil {
			return ErrWeDriveNotFound
		}
		ready := source.Status == types.WeDriveSourceAwaitingInventory || source.Status == types.WeDriveSourceActive
		(weDriveScanLifecycle{&source}).changeCadence(in.ScanIntervalMinutes, ready, s.now())
		return tx.Save(&source).Error
	})
	if err != nil {
		return nil, err
	}
	return &source, nil
}

func validWeDriveRootURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "drive.weixin.qq.com") {
		return false
	}
	return strings.HasPrefix(parsed.Fragment, "/webdisk/") ||
		(strings.TrimRight(parsed.Path, "/") == "/webdisk/index" &&
			strings.HasPrefix(parsed.Fragment, "/cgi/ssr/space/") &&
			strings.Contains(parsed.Fragment, "folderid="))
}

// weDriveRootFolderID extracts the folder selected in the approved URL. A
// failed first scan may have stored a stale listing ID, but only this URL ID
// may replace it before the source has any complete snapshot.
func weDriveRootFolderID(value string) string {
	parsed, err := url.Parse(value)
	if err != nil {
		return ""
	}
	_, fragmentQuery, _ := strings.Cut(parsed.Fragment, "?")
	for _, rawQuery := range []string{fragmentQuery, parsed.RawQuery} {
		values, err := url.ParseQuery(rawQuery)
		if err != nil {
			continue
		}
		for _, key := range []string{"folderid", "fileid", "id"} {
			if id := strings.TrimSpace(values.Get(key)); id != "" {
				return id
			}
		}
	}
	return ""
}

// DeleteSource first pauses the source so the Agent can no longer scan it,
// then removes its generated data source (if one exists) and soft-deletes the
// source record. Previously ingested knowledge is intentionally retained: it
// belongs to the knowledge base and should only be removed through its normal
// document lifecycle.
func (s *WeDriveService) DeleteSource(ctx context.Context, tenantID uint64, id string) error {
	var source types.WeDriveSource
	if err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&source).Error; err != nil {
			return ErrWeDriveNotFound
		}
		// Pausing is the safety boundary: even if datasource cleanup fails, this
		// source immediately disappears from the Agent's scan queue.
		return tx.Model(&source).Update("status", types.WeDriveSourcePaused).Error
	}); err != nil {
		return err
	}

	if source.DataSourceID != "" && s.datasources != nil {
		if err := s.datasources.DeleteDataSource(ctx, source.DataSourceID); err != nil {
			return fmt.Errorf("source disabled, but datasource cleanup failed: %w", err)
		}
	}
	result := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).Delete(&types.WeDriveSource{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrWeDriveNotFound
	}
	recordKBActivity(ctx, s.audit, source.TenantID, source.KnowledgeBaseID, types.AuditActionWeDriveSourceDeleted,
		"wedrive_source", source.ID, types.AuditOutcomeSuccess,
		map[string]any{"name": source.Name, "datasource_removed": source.DataSourceID != ""})
	return nil
}

func (s *WeDriveService) ApproveSource(ctx context.Context, tenantID uint64, userID, id, connectionID string) (*types.WeDriveSource, error) {
	var source types.WeDriveSource
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&source).Error; err != nil {
			return ErrWeDriveNotFound
		}
		var conn types.WeComCLIConnection
		if err := tx.Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", connectionID, tenantID).First(&conn).Error; err != nil {
			return ErrWeDriveNotFound
		}
		now := s.now()
		source.ConnectionID, source.ApprovedBy, source.ApprovedAt, source.Status = connectionID, userID, &now, types.WeDriveSourceAwaitingInventory
		(weDriveScanLifecycle{&source}).initialize(true, now)
		return tx.Save(&source).Error
	})
	if err == nil {
		recordKBActivity(ctx, s.audit, source.TenantID, source.KnowledgeBaseID, types.AuditActionWeDriveSourceApproved,
			"wedrive_source", source.ID, types.AuditOutcomeSuccess,
			map[string]any{"connection_id": source.ConnectionID, "auto_share": source.AutoShare})
	}
	return &source, err
}

// RebindSourceConnection changes the CLI credential used by an approved
// WeDrive source. Once the first inventory has created a DataSource, the
// connection ID lives in two places: the source controls RPA discovery while
// the DataSource config controls CLI content fetches. Keep them in one
// transaction so a future sync can never observe a mixed pair.
func (s *WeDriveService) RebindSourceConnection(ctx context.Context, tenantID uint64, userID, id, connectionID string) (*types.WeDriveSource, error) {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return nil, fmt.Errorf("connection_id is required")
	}

	var source types.WeDriveSource
	previousConnectionID := ""
	changed := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).
			First(&source).Error; err != nil {
			return ErrWeDriveNotFound
		}
		if source.Status != types.WeDriveSourceAwaitingInventory && source.Status != types.WeDriveSourceActive {
			return fmt.Errorf("%w: only approved sources can change CLI credentials", ErrWeDriveInvalidState)
		}

		var connection types.WeComCLIConnection
		if err := tx.Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", connectionID, tenantID).First(&connection).Error; err != nil {
			return ErrWeDriveNotFound
		}
		if source.ConnectionID == connectionID {
			return nil
		}
		previousConnectionID = source.ConnectionID

		if source.DataSourceID != "" {
			var running int64
			if err := tx.Model(&types.SyncLog{}).
				Where("data_source_id = ? AND status = ?", source.DataSourceID, types.SyncLogStatusRunning).
				Count(&running).Error; err != nil {
				return err
			}
			if running > 0 {
				return fmt.Errorf("%w: a content sync is currently running", ErrWeDriveInvalidState)
			}

			var dataSource types.DataSource
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", source.DataSourceID, tenantID).
				First(&dataSource).Error; err != nil {
				return fmt.Errorf("%w: linked data source is unavailable", ErrWeDriveInvalidState)
			}
			if dataSource.Type != types.ConnectorTypeWeComDrive {
				return fmt.Errorf("%w: linked data source is not an enterprise WeChat drive source", ErrWeDriveInvalidState)
			}
			config, err := dataSource.ParseConfig()
			if err != nil || config == nil || config.Type != types.ConnectorTypeWeComDrive {
				return fmt.Errorf("%w: linked data source configuration is invalid", ErrWeDriveInvalidState)
			}
			if config.Settings == nil {
				config.Settings = map[string]interface{}{}
			}
			if configuredSourceID, _ := config.Settings["source_id"].(string); configuredSourceID != source.ID {
				return fmt.Errorf("%w: linked data source belongs to another sync source", ErrWeDriveInvalidState)
			}
			config.Settings["connection_id"] = connectionID
			blob, err := config.ToJSON()
			if err != nil {
				return err
			}
			if err := tx.Model(&dataSource).Update("config", blob).Error; err != nil {
				return err
			}
		}

		source.ConnectionID = connectionID
		changed = true
		return tx.Save(&source).Error
	})
	if err != nil {
		return nil, err
	}
	if changed {
		recordKBActivity(ctx, s.audit, source.TenantID, source.KnowledgeBaseID, types.AuditActionWeDriveSourceRebound,
			"wedrive_source", source.ID, types.AuditOutcomeSuccess,
			map[string]any{"previous_connection_id": previousConnectionID, "connection_id": source.ConnectionID, "changed_by": userID})
	}
	return &source, nil
}

func (s *WeDriveService) ListSources(ctx context.Context, tenantID uint64, userID string, admin bool, kbID string) ([]*types.WeDriveSource, error) {
	q := s.db.WithContext(ctx).Where("tenant_id = ? AND deleted_at IS NULL", tenantID)
	if !admin {
		q = q.Where("created_by = ?", userID)
	}
	if kbID != "" {
		q = q.Where("knowledge_base_id = ?", kbID)
	}
	var rows []*types.WeDriveSource
	if err := q.Order("created_at DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	if !admin || len(rows) == 0 {
		return rows, nil
	}
	connectionIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.ConnectionID != "" {
			connectionIDs = append(connectionIDs, row.ConnectionID)
		}
	}
	if len(connectionIDs) == 0 {
		return rows, nil
	}
	var labels []struct {
		ID   string
		Name string
	}
	if err := s.db.WithContext(ctx).Model(&types.WeComCLIConnection{}).
		Select("id", "name").
		Where("tenant_id = ? AND id IN ? AND deleted_at IS NULL", tenantID, connectionIDs).
		Scan(&labels).Error; err != nil {
		return nil, err
	}
	connectionNames := make(map[string]string, len(labels))
	for _, label := range labels {
		connectionNames[label.ID] = label.Name
	}
	for _, row := range rows {
		row.ConnectionName = connectionNames[row.ConnectionID]
	}
	return rows, nil
}

func (s *WeDriveService) GetSource(ctx context.Context, tenantID uint64, id string) (*types.WeDriveSource, error) {
	var source types.WeDriveSource
	if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND deleted_at IS NULL", id, tenantID).First(&source).Error; err != nil {
		return nil, ErrWeDriveNotFound
	}
	return &source, nil
}

func (s *WeDriveService) ListDeviceSources(ctx context.Context, device *types.WeDriveDevice, agentSupportsCadence bool) ([]*types.WeDriveSource, error) {
	if device == nil {
		return nil, ErrWeDriveForbidden
	}
	var rows []*types.WeDriveSource
	q := s.db.WithContext(ctx).
		Where("tenant_id = ? AND device_id = ? AND status IN ? AND deleted_at IS NULL", device.TenantID, device.ID,
			[]string{types.WeDriveSourceAwaitingInventory, types.WeDriveSourceActive})
	// Pre-cadence Agents hard-code a 30 minute scan and cannot honour manual
	// or longer intervals. Do not hand those sources to an older executable.
	if !agentSupportsCadence {
		q = q.Where("scan_interval_minutes = ?", types.DefaultWeDriveScanIntervalMinutes)
	}
	err := q.Order("created_at ASC").Find(&rows).Error
	return rows, err
}

type ClaimWeDriveScanInput struct {
	Trigger string `json:"trigger"`
}

func (s *WeDriveService) ClaimScan(ctx context.Context, device *types.WeDriveDevice, sourceID string, in ClaimWeDriveScanInput) (*types.WeDriveSource, error) {
	if device == nil || sourceID == "" || (in.Trigger != "manual" && in.Trigger != "scheduled") {
		return nil, ErrWeDriveInvalidState
	}
	var source types.WeDriveSource
	var claimErr error
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := s.now()
		// Lock the same ordered set for every claim on this device. Locking only
		// the target source leaves two different sources free to claim at once.
		var deviceSources []types.WeDriveSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND device_id = ? AND deleted_at IS NULL", device.TenantID, device.ID).
			Order("id ASC").Find(&deviceSources).Error; err != nil {
			return err
		}
		for i := range deviceSources {
			row := &deviceSources[i]
			if (weDriveScanLifecycle{row}).hasActiveLease(now) {
				claimErr = ErrWeDriveScanBusy
				return nil
			}
		}
		for i := range deviceSources {
			row := &deviceSources[i]
			if row.ScanState != types.WeDriveScanStateRunning {
				continue
			}
			if err := (weDriveScanLifecycle{row}).fail("scan_timeout", now); err != nil {
				return err
			}
			if err := tx.Save(row).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("id = ? AND tenant_id = ? AND device_id = ? AND deleted_at IS NULL", sourceID, device.TenantID, device.ID).First(&source).Error; err != nil {
			return ErrWeDriveForbidden
		}
		if source.Status != types.WeDriveSourceAwaitingInventory && source.Status != types.WeDriveSourceActive {
			return ErrWeDriveInvalidState
		}
		if err := (weDriveScanLifecycle{&source}).claim(in.Trigger, now); err != nil {
			claimErr = err
			return nil
		}
		return tx.Save(&source).Error
	})
	if err != nil {
		return nil, err
	}
	if claimErr != nil {
		return nil, claimErr
	}
	return &source, nil
}

type ReportWeDriveScanFailureInput struct {
	Code string `json:"code"`
}

// RecoverInterruptedScans runs when an Agent reconnects after a server restart.
// Claims made by this server process remain untouched during ordinary socket
// reconnects, including reconnects while the Agent is actively scanning.
func (s *WeDriveService) RecoverInterruptedScans(ctx context.Context, device *types.WeDriveDevice) error {
	if device == nil {
		return ErrWeDriveForbidden
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var sources []types.WeDriveSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND device_id = ? AND scan_state = ? AND (last_scan_started_at IS NULL OR last_scan_started_at < ?) AND deleted_at IS NULL",
				device.TenantID, device.ID, types.WeDriveScanStateRunning, s.startedAt).
			Find(&sources).Error; err != nil {
			return err
		}
		now := s.now()
		for i := range sources {
			if err := (weDriveScanLifecycle{&sources[i]}).fail("scan_interrupted", now); err != nil {
				return err
			}
			if err := tx.Save(&sources[i]).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *WeDriveService) ReportScanFailure(ctx context.Context, device *types.WeDriveDevice, sourceID string, in ReportWeDriveScanFailureInput) (*types.WeDriveSource, error) {
	if device == nil || sourceID == "" {
		return nil, ErrWeDriveInvalidState
	}
	code := strings.TrimSpace(in.Code)
	if code == "" || len(code) > 64 {
		return nil, ErrWeDriveInvalidState
	}
	var source types.WeDriveSource
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND device_id = ? AND deleted_at IS NULL", sourceID, device.TenantID, device.ID).First(&source).Error; err != nil {
			return ErrWeDriveForbidden
		}
		if err := (weDriveScanLifecycle{&source}).fail(code, s.now()); err != nil {
			return err
		}
		return tx.Save(&source).Error
	})
	if err != nil {
		return nil, err
	}
	return &source, nil
}

func (s *WeDriveService) VerifyAgentRequest(ctx context.Context, deviceID, timestamp, signature, method, path string, body []byte) (*types.WeDriveDevice, error) {
	var device types.WeDriveDevice
	if err := s.db.WithContext(ctx).Where("id = ? AND revoked_at IS NULL AND deleted_at IS NULL", deviceID).First(&device).Error; err != nil {
		return nil, ErrWeDriveForbidden
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || s.now().Sub(time.Unix(ts, 0)).Abs() > 5*time.Minute {
		return nil, ErrWeDriveForbidden
	}
	pub, err := base64.RawURLEncoding.DecodeString(device.PublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return nil, ErrWeDriveForbidden
	}
	sig, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return nil, ErrWeDriveForbidden
	}
	digest := sha256.Sum256(body)
	message := method + "\n" + path + "\n" + timestamp + "\n" + hex.EncodeToString(digest[:])
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(message), sig) {
		return nil, ErrWeDriveForbidden
	}
	now := s.now()
	_ = s.db.WithContext(ctx).Model(&types.WeDriveDevice{}).Where("id = ?", device.ID).
		Updates(map[string]interface{}{"last_seen_at": &now, "status": "online"}).Error
	device.LastSeenAt, device.Status = &now, "online"
	return &device, nil
}

type BeginSnapshotInput struct {
	SourceID          string `json:"source_id"`
	Sequence          int64  `json:"sequence"`
	RootExternalID    string `json:"root_external_id"`
	ExpectedItemCount int    `json:"expected_item_count"`
}

func (s *WeDriveService) BeginSnapshot(ctx context.Context, device *types.WeDriveDevice, in BeginSnapshotInput) (*types.WeDriveSnapshot, error) {
	if device == nil || in.SourceID == "" || in.Sequence <= 0 || in.RootExternalID == "" || in.ExpectedItemCount < 1 {
		return nil, ErrWeDriveInvalidSnapshot
	}
	var result types.WeDriveSnapshot
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// The Agent may retry after losing the HTTP response. Return the same
		// upload session instead of manufacturing a conflicting snapshot.
		var existing types.WeDriveSnapshot
		lookup := tx.Where("source_id = ? AND sequence = ?", in.SourceID, in.Sequence).First(&existing)
		if lookup.Error == nil {
			if existing.DeviceID != device.ID || existing.ExpectedItemCount != in.ExpectedItemCount {
				return ErrWeDriveInvalidSnapshot
			}
			result = existing
			return nil
		}
		if !errors.Is(lookup.Error, gorm.ErrRecordNotFound) {
			return lookup.Error
		}
		var source types.WeDriveSource
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND device_id = ? AND deleted_at IS NULL", in.SourceID, device.TenantID, device.ID).First(&source).Error; err != nil {
			return ErrWeDriveForbidden
		}
		if source.Status != types.WeDriveSourceAwaitingInventory && source.Status != types.WeDriveSourceActive {
			return ErrWeDriveInvalidState
		}
		approvedRootID := weDriveRootFolderID(source.RootURL)
		if approvedRootID != "" && approvedRootID != in.RootExternalID {
			return ErrWeDriveForbidden
		}
		if source.RootExternalID != "" && source.RootExternalID != in.RootExternalID &&
			(source.LastSnapshotID != "" || approvedRootID == "") {
			return ErrWeDriveForbidden
		}
		if source.RootExternalID != in.RootExternalID {
			source.RootExternalID = in.RootExternalID
			if err := tx.Model(&source).Update("root_external_id", in.RootExternalID).Error; err != nil {
				return err
			}
		}
		result = types.WeDriveSnapshot{TenantID: device.TenantID, SourceID: source.ID, DeviceID: device.ID, Sequence: in.Sequence, ExpectedItemCount: in.ExpectedItemCount, Status: types.WeDriveSnapshotUploading}
		return tx.Create(&result).Error
	})
	return &result, err
}

func (s *WeDriveService) UploadSnapshotItems(ctx context.Context, device *types.WeDriveDevice, snapshotID string, items []types.WeDriveInventoryItem) error {
	if device == nil || snapshotID == "" || len(items) == 0 || len(items) > 500 {
		return ErrWeDriveInvalidSnapshot
	}
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var snapshot types.WeDriveSnapshot
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND device_id = ? AND status = ?", snapshotID, device.TenantID, device.ID, types.WeDriveSnapshotUploading).First(&snapshot).Error; err != nil {
			return ErrWeDriveInvalidState
		}
		for n := range items {
			item := &items[n]
			item.ID, item.TenantID, item.SourceID, item.SnapshotID = "", device.TenantID, snapshot.SourceID, snapshot.ID
			item.ExternalID, item.ParentExternalID, item.Name, item.Path = strings.TrimSpace(item.ExternalID), strings.TrimSpace(item.ParentExternalID), strings.TrimSpace(item.Name), strings.TrimSpace(item.Path)
			if item.ExternalID == "" || item.Name == "" || item.Path == "" || hasParentTraversal(item.Path) {
				return ErrWeDriveInvalidSnapshot
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "snapshot_id"}, {Name: "external_id"}}, DoUpdates: clause.AssignmentColumns([]string{
				"parent_external_id", "name", "path", "item_type", "doc_id", "share_url", "mime_type", "size", "modified_at", "content_fingerprint", "consumability", "failure_code",
			})}).Create(item).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := tx.Model(&types.WeDriveInventoryItem{}).Where("snapshot_id = ?", snapshot.ID).Count(&count).Error; err != nil {
			return err
		}
		if count > int64(snapshot.ExpectedItemCount) {
			return ErrWeDriveInvalidSnapshot
		}
		return tx.Model(&snapshot).Update("received_item_count", count).Error
	})
}

func hasParentTraversal(value string) bool {
	normalized := strings.ReplaceAll(value, "\\", "/")
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return true
		}
	}
	return false
}

type CommitSnapshotInput struct {
	Digest            string `json:"digest"`
	ItemCount         int    `json:"item_count"`
	AutoShareExisting int    `json:"auto_share_existing"`
	AutoShareCreated  int    `json:"auto_share_created"`
	AutoShareFailed   int    `json:"auto_share_failed"`
}

func (s *WeDriveService) CommitSnapshot(ctx context.Context, device *types.WeDriveDevice, snapshotID string, in CommitSnapshotInput) (*types.WeDriveSnapshot, error) {
	if device == nil || snapshotID == "" || in.ItemCount < 1 {
		return nil, ErrWeDriveInvalidSnapshot
	}
	var snapshot types.WeDriveSnapshot
	var source types.WeDriveSource
	justCommitted := false
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND tenant_id = ? AND device_id = ?", snapshotID, device.TenantID, device.ID).First(&snapshot).Error; err != nil {
			return ErrWeDriveNotFound
		}
		if snapshot.Status == types.WeDriveSnapshotComplete {
			if in.Digest != "" && subtle.ConstantTimeCompare([]byte(snapshot.Digest), []byte(strings.ToLower(in.Digest))) != 1 {
				return ErrWeDriveInvalidSnapshot
			}
			return tx.Where("id = ? AND tenant_id = ? AND device_id = ? AND deleted_at IS NULL", snapshot.SourceID, device.TenantID, device.ID).First(&source).Error
		}
		if snapshot.Status != types.WeDriveSnapshotUploading || snapshot.ReceivedItemCount != in.ItemCount || snapshot.ExpectedItemCount != in.ItemCount {
			return ErrWeDriveInvalidSnapshot
		}
		if err := tx.Where("id = ? AND tenant_id = ? AND device_id = ? AND deleted_at IS NULL", snapshot.SourceID, device.TenantID, device.ID).First(&source).Error; err != nil {
			return ErrWeDriveForbidden
		}
		var items []types.WeDriveInventoryItem
		if err := tx.Where("snapshot_id = ?", snapshot.ID).Find(&items).Error; err != nil {
			return err
		}
		if err := validateInventoryHierarchy(source.RootExternalID, items); err != nil {
			return err
		}
		digest := InventoryDigest(items)
		if in.Digest != "" && subtle.ConstantTimeCompare([]byte(digest), []byte(strings.ToLower(in.Digest))) != 1 {
			return fmt.Errorf("%w: digest mismatch", ErrWeDriveInvalidSnapshot)
		}
		now := s.now()
		snapshot.Status, snapshot.Digest, snapshot.CommittedAt = types.WeDriveSnapshotComplete, digest, &now
		snapshot.AutoShareExisting, snapshot.AutoShareCreated, snapshot.AutoShareFailed = in.AutoShareExisting, in.AutoShareCreated, in.AutoShareFailed
		justCommitted = true
		if err := tx.Save(&snapshot).Error; err != nil {
			return err
		}
		source.LastSnapshotID, source.Status = snapshot.ID, types.WeDriveSourceActive
		(weDriveScanLifecycle{&source}).complete(now)
		return tx.Save(&source).Error
	})
	if err != nil {
		return nil, err
	}
	if justCommitted {
		recordKBActivity(ctx, s.audit, source.TenantID, source.KnowledgeBaseID, types.AuditActionWeDriveSnapshotCommitted,
			"wedrive_source", source.ID, types.AuditOutcomeSuccess,
			map[string]any{"snapshot_id": snapshot.ID, "item_count": snapshot.ReceivedItemCount,
				"auto_share_existing": snapshot.AutoShareExisting, "auto_share_created": snapshot.AutoShareCreated, "auto_share_failed": snapshot.AutoShareFailed})
	}
	if source.DataSourceID == "" && s.datasources != nil {
		cfg := &types.DataSourceConfig{Type: types.ConnectorTypeWeComDrive, ResourceIDs: []string{source.ID}, Settings: map[string]interface{}{"source_id": source.ID, "connection_id": source.ConnectionID}}
		blob, _ := cfg.ToJSON()
		ds, createErr := s.datasources.CreateDataSource(withWeDriveProvisioning(ctx), &types.DataSource{TenantID: source.TenantID, KnowledgeBaseID: source.KnowledgeBaseID,
			Name: source.Name, Type: types.ConnectorTypeWeComDrive, Config: blob, SyncSchedule: "",
			SyncMode: types.SyncModeIncremental, Status: types.DataSourceStatusActive, SyncDeletions: source.SyncDeletions})
		if createErr != nil {
			_ = s.db.WithContext(ctx).Model(&types.WeDriveSource{}).Where("id = ?", source.ID).Updates(map[string]interface{}{"status": types.WeDriveSourceError}).Error
			return nil, fmt.Errorf("activate datasource: %w", createErr)
		}
		source.DataSourceID = ds.ID
		_ = s.db.WithContext(ctx).Model(&types.WeDriveSource{}).Where("id = ?", source.ID).Update("data_source_id", ds.ID).Error
	}
	return &snapshot, nil
}

func InventoryDigest(items []types.WeDriveInventoryItem) string {
	sort.Slice(items, func(i, j int) bool { return items[i].ExternalID < items[j].ExternalID })
	h := sha256.New()
	for _, item := range items {
		modified := ""
		if item.ModifiedAt != nil {
			modified = item.ModifiedAt.UTC().Format(time.RFC3339Nano)
		}
		share := "0"
		if item.ShareURL != "" {
			share = "1"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%s\n", item.ExternalID, item.ParentExternalID, item.Path, item.ItemType, item.Size, modified, item.DocID, share)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validateInventoryHierarchy(root string, items []types.WeDriveInventoryItem) error {
	parents := make(map[string]string, len(items))
	for _, item := range items {
		if _, exists := parents[item.ExternalID]; exists {
			return fmt.Errorf("%w: duplicate external id", ErrWeDriveInvalidSnapshot)
		}
		parents[item.ExternalID] = item.ParentExternalID
	}
	parent, ok := parents[root]
	if !ok || parent != "" {
		return fmt.Errorf("%w: root is missing or has a parent", ErrWeDriveInvalidSnapshot)
	}
	for id := range parents {
		seen := map[string]struct{}{}
		for current := id; current != root; {
			if _, cycle := seen[current]; cycle {
				return fmt.Errorf("%w: parent cycle", ErrWeDriveInvalidSnapshot)
			}
			seen[current] = struct{}{}
			p, exists := parents[current]
			if !exists || p == "" {
				return fmt.Errorf("%w: item is outside approved root", ErrWeDriveInvalidSnapshot)
			}
			current = p
		}
	}
	return nil
}
