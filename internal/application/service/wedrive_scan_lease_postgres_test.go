package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func testWeDrivePostgresLeaseTransactions(t *testing.T, db *gorm.DB) {
	t.Helper()
	svc := NewWeDriveService(db, nil, nil)
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	newSource := func(id string) *types.WeDriveDevice {
		t.Helper()
		require.NoError(t, db.Create(&types.WeDriveSource{ID: id, TenantID: 7, KnowledgeBaseID: "kb", DeviceID: id, CreatedBy: "owner", Name: "root", RootURL: "https://drive.weixin.qq.com/root", Status: types.WeDriveSourceActive, ScanState: types.WeDriveScanStateIdle}).Error)
		return &types.WeDriveDevice{ID: id, TenantID: 7}
	}
	device := newSource("pg-renew-restart")
	assertWeDriveProgressAcrossRestart(t, svc, device, &now, device.ID)
	device = newSource("pg-hard-deadline")
	assertWeDriveScanHardDeadline(t, svc, device, &now, device.ID)
	for trial := 0; trial < 10; trial++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		device := newSource(fmt.Sprintf("pg-renew-claim-%d", trial))
		claimed, err := svc.ClaimScan(ctx, device, device.ID, ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err)
		now = now.Add(20 * time.Minute)
		start := make(chan struct{})
		renewals := make(chan error, 1)
		claims := make(chan *types.WeDriveSource, 1)
		claimErrors := make(chan error, 1)
		go func() {
			<-start
			_, err := svc.RenewScanLease(ctx, device, device.ID, RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 1})
			renewals <- err
		}()
		go func() {
			<-start
			source, err := svc.ClaimScan(ctx, device, device.ID, ClaimWeDriveScanInput{Trigger: "manual"})
			claims <- source
			claimErrors <- err
		}()
		close(start)
		err = <-renewals
		require.True(t, errors.Is(err, ErrWeDriveScanAttemptExpired) || errors.Is(err, ErrWeDriveScanAttemptConflict), "late progress cannot win: %v", err)
		current := <-claims
		require.NoError(t, <-claimErrors)
		saved, err := svc.GetSource(ctx, 7, device.ID)
		require.NoError(t, err)
		require.Equal(t, current.ScanAttemptID, saved.ScanAttemptID)
		require.Zero(t, saved.ScanProgressSeq)
		require.True(t, saved.ScanLeaseExpiresAt.Equal(*current.ScanLeaseExpiresAt))
		cancel()
	}
	for trial := 0; trial < 10; trial++ {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		device := newSource(fmt.Sprintf("pg-renew-commit-%d", trial))
		claimed, err := svc.ClaimScan(ctx, device, device.ID, ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err)
		snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: device.ID, ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
		require.NoError(t, err)
		require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
		now = now.Add(5 * time.Minute)
		start := make(chan struct{})
		renewals, commits := make(chan error, 1), make(chan error, 1)
		go func() {
			<-start
			_, err := svc.RenewScanLease(ctx, device, device.ID, RenewWeDriveScanLeaseInput{ScanAttemptID: claimed.ScanAttemptID, ProgressSeq: 1})
			renewals <- err
		}()
		go func() {
			<-start
			_, err := svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
			commits <- err
		}()
		close(start)
		err = <-renewals
		require.True(t, err == nil || errors.Is(err, ErrWeDriveScanAttemptConflict), "renewal must not resurrect completion: %v", err)
		require.NoError(t, <-commits)
		saved, err := svc.GetSource(ctx, 7, device.ID)
		require.NoError(t, err)
		require.Equal(t, types.WeDriveScanStateIdle, saved.ScanState)
		require.Empty(t, saved.ScanAttemptID)
		require.Nil(t, saved.ScanLeaseExpiresAt)
		require.Nil(t, saved.ScanLastProgressAt)
		require.Zero(t, saved.ScanProgressSeq)
		require.Equal(t, snapshot.ID, saved.LastSnapshotID)
		cancel()
	}
}
