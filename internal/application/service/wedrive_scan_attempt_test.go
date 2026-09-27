package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestWeDriveOldFailureCannotEndNewScanAttempt(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	ctx := context.Background()
	createWeDriveScanTestSource(t, svc, "source", *now)
	first, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	// Read the wire response: the tool must receive its attempt identity.
	wire, err := json.Marshal(first)
	require.NoError(t, err)
	var response struct {
		ScanAttemptID string `json:"scan_attempt_id"`
	}
	require.NoError(t, json.Unmarshal(wire, &response))
	require.NotEmpty(t, response.ScanAttemptID)
	firstID := response.ScanAttemptID
	*now = now.Add(30 * time.Minute)
	second, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	wire, err = json.Marshal(second)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(wire, &response))
	require.NotEmpty(t, response.ScanAttemptID)
	require.NotEqual(t, firstID, response.ScanAttemptID)
	var failure ReportWeDriveScanFailureInput
	require.NoError(t, json.Unmarshal([]byte(`{"code":"old_failure","scan_attempt_id":"`+firstID+`"}`), &failure))
	_, err = svc.ReportScanFailure(ctx, device, "source", failure)
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
	saved, err := svc.GetSource(ctx, 7, "source")
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRunning, saved.ScanState)
	require.Equal(t, second.ScanLeaseExpiresAt, saved.ScanLeaseExpiresAt)
	require.Equal(t, second.NextScanAt, saved.NextScanAt)
	require.Equal(t, second.LastScanErrorCode, saved.LastScanErrorCode)
	require.Equal(t, second.ScanAttemptID, saved.ScanAttemptID)
	require.NoError(t, json.Unmarshal([]byte(`{"code":"current_failure","scan_attempt_id":"`+response.ScanAttemptID+`"}`), &failure))
	finished, err := svc.ReportScanFailure(ctx, device, "source", failure)
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRetryWait, finished.ScanState)
	require.Nil(t, finished.ScanLeaseExpiresAt)
	require.Empty(t, finished.ScanAttemptID)
}

func TestWeDriveFailureRequiresCurrentUnexpiredAttempt(t *testing.T) {
	for _, interval := range []int{0, 30} {
		svc, device, now := newWeDriveScanTestService(t)
		ctx := context.Background()
		createWeDriveScanTestSource(t, svc, "source", *now)
		_, err := svc.UpdateSourceScanSettings(ctx, 7, "source", UpdateWeDriveScanSettingsInput{ScanIntervalMinutes: interval})
		require.NoError(t, err)
		claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err)
		_, err = svc.ReportScanFailure(ctx, device, "source", ReportWeDriveScanFailureInput{Code: "missing_identity"})
		require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
		*now = now.Add(30*time.Minute - time.Nanosecond)
		_, err = svc.ReportScanFailure(ctx, &types.WeDriveDevice{ID: "other", TenantID: 7}, "source", ReportWeDriveScanFailureInput{ScanAttemptID: claimed.ScanAttemptID, Code: "wrong_device"})
		require.ErrorIs(t, err, ErrWeDriveForbidden)
		saved, err := svc.GetSource(ctx, 7, "source")
		require.NoError(t, err)
		require.Equal(t, claimed.ScanAttemptID, saved.ScanAttemptID)
		require.Equal(t, claimed.ScanLeaseExpiresAt, saved.ScanLeaseExpiresAt)
		*now = now.Add(time.Nanosecond)
		_, err = svc.ReportScanFailure(ctx, device, "source", ReportWeDriveScanFailureInput{ScanAttemptID: claimed.ScanAttemptID, Code: "late_failure"})
		require.ErrorIs(t, err, ErrWeDriveScanAttemptExpired)
		saved, err = svc.GetSource(ctx, 7, "source")
		require.NoError(t, err)
		require.Empty(t, saved.ScanAttemptID)
		require.Nil(t, saved.ScanLeaseExpiresAt)
		require.Equal(t, "scan_timeout", saved.LastScanErrorCode)
		if interval == 0 {
			require.Equal(t, types.WeDriveScanStateFailed, saved.ScanState)
			require.Nil(t, saved.NextScanAt)
		} else {
			require.Equal(t, types.WeDriveScanStateRetryWait, saved.ScanState)
			require.Equal(t, now.Add(10*time.Minute), *saved.NextScanAt)
		}
	}
}
