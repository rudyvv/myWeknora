package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Run against an isolated PostgreSQL database with
// WEKNORA_WEDRIVE_TEST_POSTGRES_DSN. Each run creates and removes its own
// schema, so migration and locking behavior are exercised without touching
// application tables.
func TestWeDrivePostgresMigrationsAndConcurrentClaims(t *testing.T) {
	dsn := os.Getenv("WEKNORA_WEDRIVE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_WEDRIVE_TEST_POSTGRES_DSN to run PostgreSQL integration test")
	}
	admin, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	defer adminSQL.Close()

	schema := fmt.Sprintf("wedrive_it_%d", time.Now().UnixNano())
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	defer func() { require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error) }()

	db, err := gorm.Open(postgres.New(postgres.Config{DSN: dsn + " search_path=" + schema + ",public", PreferSimpleProtocol: true}), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	defer sqlDB.Close()

	require.NoError(t, db.Exec("CREATE TABLE data_sources (id varchar(36) PRIMARY KEY, type varchar(50), sync_schedule varchar(100))").Error)
	for _, version := range []string{"000096_wecom_wedrive_sync.up.sql", "000097_wedrive_scan_cadence.up.sql", "000098_wedrive_manual_content_sync.up.sql", "000099_wedrive_scan_attempt.up.sql"} {
		if version == "000099_wedrive_scan_attempt.up.sql" {
			seedLegacyWeDriveScanAttempts(t, db)
		}
		if version == "000097_wedrive_scan_cadence.up.sql" {
			require.NoError(t, db.Exec("INSERT INTO data_sources (id, type, sync_schedule) VALUES ('legacy-ds', 'wecom_drive_rpa', '*/30 * * * *')").Error)
			require.NoError(t, db.Exec("INSERT INTO wedrive_sources (id, tenant_id, knowledge_base_id, device_id, created_by, name, root_url, status, sync_schedule) VALUES ('legacy-source', 7, 'kb', 'legacy-device', 'owner', 'legacy', 'https://drive.weixin.qq.com/root', 'active', '*/30 * * * *')").Error)
		}
		contents, readErr := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", version))
		require.NoError(t, readErr)
		require.NoErrorf(t, db.Exec(string(contents)).Error, "migration %s", version)
	}
	assertMigratedWeDriveScanAttempts(t, db)
	var legacy struct{ SyncSchedule string }
	require.NoError(t, db.Raw("SELECT sync_schedule FROM wedrive_sources WHERE id = 'legacy-source'").Scan(&legacy).Error)
	require.Empty(t, legacy.SyncSchedule)
	require.NoError(t, db.Raw("SELECT sync_schedule FROM data_sources WHERE id = 'legacy-ds'").Scan(&legacy).Error)
	require.Empty(t, legacy.SyncSchedule)

	for _, id := range []string{"race-a", "race-b"} {
		require.NoError(t, db.Exec("INSERT INTO wedrive_sources (id, tenant_id, knowledge_base_id, device_id, created_by, name, root_url, status, scan_interval_minutes, scan_state) VALUES (?, 7, 'kb', 'race-device', 'owner', ?, 'https://drive.weixin.qq.com/root', 'active', 0, 'idle')", id, id).Error)
	}
	svc := NewWeDriveService(db, nil, nil)
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	device := &types.WeDriveDevice{ID: "race-device", TenantID: 7}
	for trial := 0; trial < 30; trial++ {
		require.NoError(t, db.Exec("UPDATE wedrive_sources SET scan_state = 'idle', scan_lease_expires_at = NULL, scan_attempt_id = '' WHERE id IN ('race-a', 'race-b')").Error)
		start := make(chan struct{})
		results := make(chan error, 2)
		var wg sync.WaitGroup
		for _, id := range []string{"race-a", "race-b"} {
			wg.Add(1)
			go func(sourceID string) {
				defer wg.Done()
				<-start
				_, claimErr := svc.ClaimScan(context.Background(), device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
				results <- claimErr
			}(id)
		}
		close(start)
		wg.Wait()
		close(results)
		success, busy := 0, 0
		for claimErr := range results {
			switch {
			case claimErr == nil:
				success++
			case errors.Is(claimErr, ErrWeDriveScanBusy):
				busy++
			default:
				t.Fatalf("trial %d: unexpected claim error: %v", trial, claimErr)
			}
		}
		require.Equalf(t, 1, success, "trial %d: same-device claims must serialize", trial)
		require.Equal(t, 1, busy)
		var running int64
		require.NoError(t, db.Model(&types.WeDriveSource{}).Where("id IN ? AND scan_state = ?", []string{"race-a", "race-b"}, types.WeDriveScanStateRunning).Count(&running).Error)
		require.Equal(t, int64(1), running)
		var current types.WeDriveSource
		require.NoError(t, db.Where("device_id = ? AND scan_state = ?", device.ID, types.WeDriveScanStateRunning).First(&current).Error)
		require.NotEmpty(t, current.ScanAttemptID)
		require.NotNil(t, current.ScanLeaseExpiresAt)
	}

	expired := now.Add(-time.Minute)
	due := now.Add(-time.Hour)
	require.NoError(t, db.Create(&types.WeDriveSource{ID: "expired", TenantID: 7, KnowledgeBaseID: "kb", DeviceID: "expired-device", CreatedBy: "owner", Name: "expired", RootURL: "https://drive.weixin.qq.com/root", Status: types.WeDriveSourceActive, ScanIntervalMinutes: 30, ScanState: types.WeDriveScanStateRunning, ScanLeaseExpiresAt: &expired, NextScanAt: &due}).Error)
	expiredDevice := &types.WeDriveDevice{ID: "expired-device", TenantID: 7}
	_, err = svc.ClaimScan(context.Background(), expiredDevice, "expired", ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.ErrorIs(t, err, ErrWeDriveScanNotDue)
	var saved types.WeDriveSource
	require.NoError(t, db.First(&saved, "id = ?", "expired").Error)
	require.Equal(t, types.WeDriveScanStateRetryWait, saved.ScanState)
	require.True(t, saved.NextScanAt.Equal(now.Add(weDriveScanRetryDelay)), "retry must be delayed by 10 minutes")
	now = now.Add(weDriveScanRetryDelay)
	first, err := svc.ClaimScan(context.Background(), expiredDevice, "expired", ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.NoError(t, err)
	require.NotEmpty(t, first.ScanAttemptID)
	now = now.Add(30 * time.Minute)
	second, err := svc.ClaimScan(context.Background(), expiredDevice, "expired", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	require.NotEqual(t, first.ScanAttemptID, second.ScanAttemptID)
	_, err = svc.ReportScanFailure(context.Background(), expiredDevice, "expired", ReportWeDriveScanFailureInput{ScanAttemptID: first.ScanAttemptID, Code: "old_failure"})
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
	unchanged, err := svc.GetSource(context.Background(), 7, "expired")
	require.NoError(t, err)
	require.Equal(t, second.ScanAttemptID, unchanged.ScanAttemptID)
	require.Equal(t, types.WeDriveScanStateRunning, unchanged.ScanState)
	require.True(t, unchanged.ScanLeaseExpiresAt.Equal(*second.ScanLeaseExpiresAt))
	finished, err := svc.ReportScanFailure(context.Background(), expiredDevice, "expired", ReportWeDriveScanFailureInput{ScanAttemptID: second.ScanAttemptID, Code: "current_failure"})
	require.NoError(t, err)
	require.Empty(t, finished.ScanAttemptID)
	require.Nil(t, finished.ScanLeaseExpiresAt)
}
