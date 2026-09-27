package service

import (
	"context"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWeDriveProgressRenewsLongScanAcrossServerRestart(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	createWeDriveScanTestSource(t, svc, "source", *now)
	assertWeDriveProgressAcrossRestart(t, svc, device, now, "source")
}

func assertWeDriveProgressAcrossRestart(t *testing.T, svc *WeDriveService, device *types.WeDriveDevice, now *time.Time, sourceID string) {
	t.Helper()
	ctx := context.Background()
	claimed, err := svc.ClaimScan(ctx, device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	for seq := int64(1); seq <= 8; seq++ {
		*now = now.Add(5 * time.Minute)
		if seq == 4 {
			svc = NewWeDriveService(svc.db, nil, nil)
			svc.now = func() time.Time { return *now }
			require.NoError(t, svc.RecoverInterruptedScans(ctx, device))
		}
		renewed, err := svc.RenewScanLease(ctx, device, sourceID, RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: seq})
		require.NoError(t, err)
		require.Equal(t, claimed.ScanAttemptID, renewed.ScanAttemptID)
		require.True(t, renewed.ScanLeaseExpiresAt.Equal(now.Add(30*time.Minute)))
		require.Equal(t, seq, renewed.ScanProgressSeq)
		require.Empty(t, renewed.LastSnapshotID)
		require.Nil(t, renewed.LastCompleteScanAt)
	}
	snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: sourceID, ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
	require.NoError(t, err)
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
	_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
	require.NoError(t, err)
	saved, err := svc.GetSource(ctx, device.TenantID, sourceID)
	require.NoError(t, err)
	require.Empty(t, saved.ScanAttemptID)
	require.Zero(t, saved.ScanProgressSeq)
	require.Nil(t, saved.ScanLastProgressAt)
	require.True(t, saved.LastCompleteScanAt.Equal(*now))
}

func TestWeDriveRepeatedHeartbeatsCannotKeepStalledScanAlive(t *testing.T) {
	for _, interval := range []int{0, 30} {
		svc, device, now := newWeDriveScanTestService(t)
		ctx := context.Background()
		createWeDriveScanTestSource(t, svc, "source", *now)
		_, err := svc.UpdateSourceScanSettings(ctx, 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: interval})
		require.NoError(t, err)
		claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err)
		for n := 0; n < 3; n++ {
			*now = now.Add(5 * time.Minute)
			renewed, err := svc.RenewScanLease(ctx, device, "source", RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 0})
			require.NoError(t, err)
			require.True(t, renewed.ScanLeaseExpiresAt.Equal(*claimed.ScanLeaseExpiresAt))
			require.True(t, renewed.ScanLastProgressAt.Equal(*claimed.ScanLastProgressAt))
		}
		*now = now.Add(5 * time.Minute)
		_, err = svc.RenewScanLease(ctx, device, "source", RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 1})
		require.ErrorIs(t, err, ErrWeDriveScanAttemptExpired, "late progress must not revive a stalled attempt")
		saved, err := svc.GetSource(ctx, 7, "source")
		require.NoError(t, err)
		require.Equal(t, "scan_no_progress", saved.LastScanErrorCode)
		require.Empty(t, saved.ScanAttemptID)
		require.Nil(t, saved.ScanLastProgressAt)
		if interval == 0 {
			require.Equal(t, types.WeDriveScanStateFailed, saved.ScanState)
			require.Nil(t, saved.NextScanAt)
		} else {
			require.Equal(t, types.WeDriveScanStateRetryWait, saved.ScanState)
			require.True(t, saved.NextScanAt.Equal(now.Add(10*time.Minute)))
		}
		current, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err, "the stale attempt must release device capacity")
		_, err = svc.RenewScanLease(ctx, device, "source", RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 99})
		require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
		unchanged, err := svc.GetSource(ctx, 7, "source")
		require.NoError(t, err)
		require.Equal(t, current.ScanAttemptID, unchanged.ScanAttemptID)
		require.True(t, unchanged.ScanLeaseExpiresAt.Equal(*current.ScanLeaseExpiresAt))
	}
}

func assertWeDriveScanHardDeadline(t *testing.T, svc *WeDriveService, device *types.WeDriveDevice, now *time.Time, sourceID string) {
	t.Helper()
	ctx := context.Background()
	start := *now
	claimed, err := svc.ClaimScan(ctx, device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	for seq := int64(1); seq <= 47; seq++ {
		*now = now.Add(5 * time.Minute)
		renewed, err := svc.RenewScanLease(ctx, device, sourceID, RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: seq})
		require.NoError(t, err)
		require.False(t, renewed.ScanLeaseExpiresAt.After(start.Add(4*time.Hour)))
		if seq == 47 {
			require.True(t, renewed.ScanLeaseExpiresAt.Equal(start.Add(4*time.Hour)))
		}
	}
	_, err = svc.RenewScanLease(ctx, device, sourceID, RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 46})
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict, "progress cannot move backwards")
	*now = start.Add(4 * time.Hour)
	_, err = svc.RenewScanLease(ctx, device, sourceID, RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 48})
	require.ErrorIs(t, err, ErrWeDriveScanAttemptExpired)
	saved, err := svc.GetSource(ctx, device.TenantID, sourceID)
	require.NoError(t, err)
	require.Equal(t, "scan_max_duration", saved.LastScanErrorCode)
	require.Empty(t, saved.ScanAttemptID)
	require.Nil(t, saved.ScanLeaseExpiresAt)
}

func TestWeDriveProgressCannotExtendScanBeyondFourHours(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	createWeDriveScanTestSource(t, svc, "source", *now)
	assertWeDriveScanHardDeadline(t, svc, device, now, "source")
}

func TestWeDriveDiscoveryCleansStalledManualAttemptWithoutStartingScan(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	createWeDriveScanTestSource(t, svc, "source", *now)
	_, err := svc.UpdateSourceScanSettings(t.Context(), 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: 0})
	require.NoError(t, err)
	claimed, err := svc.ClaimScan(t.Context(), device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	*now = now.Add(20 * time.Minute)
	rows, err := svc.ListDeviceSources(t.Context(), device, true)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, types.WeDriveScanStateFailed, rows[0].ScanState)
	require.Equal(t, "scan_no_progress", rows[0].LastScanErrorCode)
	require.Empty(t, rows[0].ScanAttemptID)
	require.Nil(t, rows[0].NextScanAt)
	_, err = svc.RenewScanLease(t.Context(), device, "source", RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 1})
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
}
