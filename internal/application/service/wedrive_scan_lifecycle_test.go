package service

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newWeDriveScanTestService(t *testing.T) (*WeDriveService, *types.WeDriveDevice, *time.Time) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "scans.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeDriveSnapshot{}, &types.WeDriveInventoryItem{}, &types.WeComCLIConnection{}))
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	svc := NewWeDriveService(db, nil, nil)
	svc.now = func() time.Time { return now }
	return svc, &types.WeDriveDevice{ID: "device", TenantID: 7}, &now
}

func createWeDriveScanTestSource(t *testing.T, svc *WeDriveService, id string, now time.Time) {
	t.Helper()
	require.NoError(t, svc.db.Create(&types.WeDriveSource{
		ID: id, TenantID: 7, KnowledgeBaseID: "kb", DeviceID: "device", CreatedBy: "owner",
		Name: "root", RootURL: "https://drive.weixin.qq.com/#/webdisk/folder?id=root", RootExternalID: "root",
		Status: types.WeDriveSourceActive, ScanIntervalMinutes: 30, ScanState: types.WeDriveScanStateWaiting,
		NextScanAt: &now, SyncSchedule: "unchanged-content-schedule",
	}).Error)
}

func TestWeDriveRunningCadenceChangeAppliesAfterOutcome(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval int
		success  bool
		state    string
		wait     time.Duration
	}{
		{"automatic success", 60, true, types.WeDriveScanStateWaiting, time.Hour},
		{"manual success", 0, true, types.WeDriveScanStateIdle, 0},
		{"automatic failure", 60, false, types.WeDriveScanStateRetryWait, 10 * time.Minute},
		{"manual failure", 0, false, types.WeDriveScanStateFailed, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, device, now := newWeDriveScanTestService(t)
			ctx := context.Background()
			createWeDriveScanTestSource(t, svc, "source", *now)
			claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "scheduled"})
			require.NoError(t, err)
			*now = now.Add(time.Minute)
			changed, err := svc.UpdateSourceScanSettings(ctx, 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: tc.interval})
			require.NoError(t, err)
			require.Equal(t, types.WeDriveScanStateRunning, changed.ScanState)
			require.Equal(t, claimed.ScanLeaseExpiresAt, changed.ScanLeaseExpiresAt)
			require.Equal(t, claimed.LastScanStartedAt, changed.LastScanStartedAt)
			require.Equal(t, claimed.ScanRetryCount, changed.ScanRetryCount)
			if tc.success {
				snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: "source", Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
				require.NoError(t, err)
				require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
				_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
				require.NoError(t, err)
			} else {
				_, err = svc.ReportScanFailure(ctx, device, "source", ReportWeDriveScanFailureInput{Code: "listing_timeout"})
				require.NoError(t, err)
			}
			finished, err := svc.GetSource(ctx, 7, "source")
			require.NoError(t, err)
			require.Equal(t, tc.state, finished.ScanState)
			require.Nil(t, finished.ScanLeaseExpiresAt)
			require.Equal(t, "unchanged-content-schedule", finished.SyncSchedule)
			if tc.wait == 0 {
				require.Nil(t, finished.NextScanAt)
			} else {
				require.Equal(t, now.Add(tc.wait), *finished.NextScanAt)
			}
			if tc.success {
				require.Equal(t, *now, *finished.LastCompleteScanAt)
				require.Zero(t, finished.ScanRetryCount)
				require.Empty(t, finished.LastScanErrorCode)
			} else {
				require.Nil(t, finished.LastCompleteScanAt)
			}
		})
	}
}

func TestWeDriveManualClaimBypassesDueTimeButKeepsDeviceSerial(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	ctx := context.Background()
	createWeDriveScanTestSource(t, svc, "first", now.Add(time.Hour))
	createWeDriveScanTestSource(t, svc, "second", *now)
	_, err := svc.ClaimScan(ctx, device, "first", ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.ErrorIs(t, err, ErrWeDriveScanNotDue)
	_, err = svc.ClaimScan(ctx, device, "first", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	_, err = svc.ClaimScan(ctx, device, "second", ClaimWeDriveScanInput{Trigger: "manual"})
	require.ErrorIs(t, err, ErrWeDriveScanBusy)
	// At the exact expiry boundary the previous lease must stop blocking.
	*now = now.Add(30 * time.Minute)
	_, err = svc.ClaimScan(ctx, device, "second", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	first, err := svc.GetSource(ctx, 7, "first")
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRetryWait, first.ScanState)
	require.Equal(t, "scan_timeout", first.LastScanErrorCode)
}

func TestWeDriveCadenceChangeSchedulesFromLastSuccess(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	ctx := context.Background()
	createWeDriveScanTestSource(t, svc, "source", *now)
	_, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.NoError(t, err)
	snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: "source", Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
	require.NoError(t, err)
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
	_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
	require.NoError(t, err)
	*now = now.Add(20 * time.Minute)
	changed, err := svc.UpdateSourceScanSettings(ctx, 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: 60})
	require.NoError(t, err)
	require.Equal(t, time.Date(2026, 9, 26, 11, 0, 0, 0, time.UTC), *changed.NextScanAt)
	*now = now.Add(2 * time.Hour)
	changed, err = svc.UpdateSourceScanSettings(ctx, 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: 30})
	require.NoError(t, err)
	require.Equal(t, *now, *changed.NextScanAt)
	changed, err = svc.UpdateSourceScanSettings(ctx, 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: 0})
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateIdle, changed.ScanState)
	require.Nil(t, changed.NextScanAt)
	_, err = svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.ErrorIs(t, err, ErrWeDriveScanNotDue)
}

func TestWeDriveApprovalStartsOnlyAutomaticSources(t *testing.T) {
	for _, tc := range []struct {
		name     string
		interval int
	}{{"manual", 0}, {"automatic", 30}} {
		t.Run(tc.name, func(t *testing.T) {
			interval := tc.interval
			svc, device, now := newWeDriveScanTestService(t)
			ctx := context.Background()
			require.NoError(t, svc.db.Create(&types.WeComCLIConnection{ID: "cli", TenantID: 7, Name: "CLI"}).Error)
			require.NoError(t, svc.db.Create(&types.WeDriveSource{
				ID: "pending", TenantID: 7, KnowledgeBaseID: "kb", DeviceID: device.ID, CreatedBy: "owner",
				Name: "root", RootURL: "https://drive.weixin.qq.com/#/webdisk/folder?id=root",
				Status: types.WeDriveSourcePending, ScanIntervalMinutes: interval, ScanState: types.WeDriveScanStateIdle,
			}).Error)
			approved, err := svc.ApproveSource(ctx, 7, "owner", "pending", "cli")
			require.NoError(t, err)
			require.Equal(t, types.WeDriveSourceAwaitingInventory, approved.Status)
			_, err = svc.ClaimScan(ctx, device, "pending", ClaimWeDriveScanInput{Trigger: "scheduled"})
			if interval == 0 {
				require.Equal(t, types.WeDriveScanStateIdle, approved.ScanState)
				require.Nil(t, approved.NextScanAt)
				require.ErrorIs(t, err, ErrWeDriveScanNotDue)
			} else {
				require.Equal(t, types.WeDriveScanStateWaiting, approved.ScanState)
				require.Equal(t, *now, *approved.NextScanAt)
				require.NoError(t, err)
			}
		})
	}
}
