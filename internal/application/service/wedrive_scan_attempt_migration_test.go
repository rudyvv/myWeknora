package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func seedLegacyWeDriveScanAttempts(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, id := range []string{"legacy-auto-first", "legacy-auto-second", "legacy-manual", "legacy-waiting"} {
		interval, retry, state := 30, 0, "running"
		if id == "legacy-auto-second" {
			retry = 1
		}
		if id == "legacy-manual" {
			interval = 0
		}
		if id == "legacy-waiting" {
			state = "waiting"
		}
		require.NoError(t, db.Exec("INSERT INTO wedrive_sources (id, tenant_id, knowledge_base_id, device_id, created_by, name, root_url, status, scan_interval_minutes, scan_retry_count, scan_state, scan_lease_expires_at, last_snapshot_id, last_complete_scan_at) VALUES (?, 7, 'kb', ?, 'owner', ?, 'https://drive.weixin.qq.com/root', 'active', ?, ?, ?, ?, 'legacy-complete', ?)", id, id, id, interval, retry, state, time.Now().Add(time.Hour), time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO wedrive_snapshots (id, tenant_id, source_id, device_id, sequence, status, expected_item_count, received_item_count, digest) VALUES ('legacy-complete', 7, 'legacy-manual', 'legacy-manual', 1, 'complete', 1, 1, 'historical-digest')").Error)
}

func assertMigratedWeDriveScanAttempts(t *testing.T, db *gorm.DB) {
	t.Helper()
	svc := NewWeDriveService(db, nil, nil)
	ctx := context.Background()
	for _, id := range []string{"legacy-auto-first", "legacy-auto-second", "legacy-manual"} {
		source, err := svc.GetSource(ctx, 7, id)
		require.NoError(t, err)
		require.Empty(t, source.ScanAttemptID)
		require.Nil(t, source.ScanLeaseExpiresAt)
		require.Equal(t, "scan_protocol_upgrade", source.LastScanErrorCode)
		require.Equal(t, "legacy-complete", source.LastSnapshotID)
		require.True(t, source.LastCompleteScanAt.Equal(time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)))
		if id == "legacy-manual" {
			require.Equal(t, types.WeDriveScanStateFailed, source.ScanState)
			require.Nil(t, source.NextScanAt)
			_, err = svc.ClaimScan(ctx, &types.WeDriveDevice{ID: id, TenantID: 7}, id, ClaimWeDriveScanInput{Trigger: "manual"})
			require.NoError(t, err)
		} else {
			require.NotNil(t, source.NextScanAt)
			require.WithinDuration(t, time.Now().Add(10*time.Minute), *source.NextScanAt, 30*time.Second)
			require.Equal(t, types.WeDriveScanStateRetryWait, source.ScanState)
			require.Equal(t, 1, source.ScanRetryCount)
		}
	}
	waiting, err := svc.GetSource(ctx, 7, "legacy-waiting")
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateWaiting, waiting.ScanState)
	require.Empty(t, waiting.LastScanErrorCode)
	var snapshot types.WeDriveSnapshot
	require.NoError(t, db.First(&snapshot, "id = ?", "legacy-complete").Error)
	require.Equal(t, types.WeDriveSnapshotComplete, snapshot.Status)
	require.Equal(t, "historical-digest", snapshot.Digest)
	require.Equal(t, 1, snapshot.ReceivedItemCount)
}

func TestWeDriveSQLiteScanAttemptMigrationPreservesInventory(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "migration.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.Exec("CREATE TABLE data_sources (id TEXT PRIMARY KEY, type TEXT, sync_schedule TEXT)").Error)
	for _, migration := range []string{"000017_wecom_wedrive_sync.up.sql", "000018_wedrive_scan_cadence.up.sql", "000019_wedrive_manual_content_sync.up.sql", "000020_wedrive_scan_attempt.up.sql"} {
		if migration == "000020_wedrive_scan_attempt.up.sql" {
			seedLegacyWeDriveScanAttempts(t, db)
		}
		contents, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "sqlite", migration))
		require.NoError(t, err)
		require.NoError(t, db.Exec(string(contents)).Error)
	}
	assertMigratedWeDriveScanAttempts(t, db)
}
