package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func testWeDrivePostgresSnapshotTransactions(t *testing.T, db *gorm.DB) {
	t.Helper()
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	svc := NewWeDriveService(db, nil, NewAuditLogService(repository.NewAuditLogRepository(db)))
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	newSource := func(id string) *types.WeDriveDevice {
		t.Helper()
		device := &types.WeDriveDevice{ID: id, TenantID: 7}
		require.NoError(t, db.Create(&types.WeDriveSource{ID: id, TenantID: 7, KnowledgeBaseID: "kb", DeviceID: id, CreatedBy: "owner", Name: "root", RootURL: "https://drive.weixin.qq.com/root", Status: types.WeDriveSourceActive, ScanState: types.WeDriveScanStateIdle}).Error)
		return device
	}
	device := newSource("pg-stale-snapshot")
	assertWeDriveStaleSnapshotWrites(t, svc, device, &now, device.ID)
	device = newSource("pg-completion-replay")
	assertWeDriveSnapshotCompletionReplay(t, svc, device, &now, device.ID)

	for trial := 0; trial < 10; trial++ {
		id := fmt.Sprintf("pg-inventory-race-%d", trial)
		device := newSource(id)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		claimed, err := svc.ClaimScan(ctx, device, id, ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err)
		type beginResult struct {
			snapshot *types.WeDriveSnapshot
			err      error
		}
		begins := make(chan beginResult, 2)
		start := make(chan struct{})
		for sequence := int64(1); sequence <= 2; sequence++ {
			go func(sequence int64) {
				<-start
				snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: id, ScanAttemptID: claimed.ScanAttemptID, Sequence: sequence, RootExternalID: "root", ExpectedItemCount: 1})
				begins <- beginResult{snapshot, err}
			}(sequence)
		}
		close(start)
		first, second := <-begins, <-begins
		require.NoError(t, first.err)
		require.NoError(t, second.err)
		require.Equal(t, first.snapshot.ID, second.snapshot.ID)
		var count int64
		require.NoError(t, db.Model(&types.WeDriveSnapshot{}).Where("source_id = ? AND scan_attempt_id = ?", id, claimed.ScanAttemptID).Count(&count).Error)
		require.Equal(t, int64(1), count)
		require.NoError(t, svc.UploadSnapshotItems(ctx, device, first.snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
		commits := make(chan beginResult, 2)
		start = make(chan struct{})
		for n := 0; n < 2; n++ {
			go func() {
				<-start
				snapshot, err := svc.CommitSnapshot(ctx, device, first.snapshot.ID, CommitSnapshotInput{ItemCount: 1})
				commits <- beginResult{snapshot, err}
			}()
		}
		close(start)
		left, right := <-commits, <-commits
		require.NoError(t, left.err)
		require.NoError(t, right.err)
		require.Equal(t, left.snapshot.ID, right.snapshot.ID)
		require.True(t, left.snapshot.CommittedAt.Equal(*right.snapshot.CommittedAt))
		entries, err := svc.audit.List(ctx, 7, &interfaces.AuditLogQuery{Action: types.AuditActionWeDriveSnapshotCommitted, Limit: 100})
		require.NoError(t, err)
		audits := 0
		for _, entry := range entries {
			if entry.TargetID == id {
				audits++
			}
		}
		require.Equal(t, 1, audits)
		cancel()
	}

	for trial := 0; trial < 10; trial++ {
		id := fmt.Sprintf("pg-expired-write-race-%d", trial)
		device := newSource(id)
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		claimed, err := svc.ClaimScan(ctx, device, id, ClaimWeDriveScanInput{Trigger: "manual"})
		require.NoError(t, err)
		snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: id, ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
		require.NoError(t, err)
		require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
		now = *claimed.ScanLeaseExpiresAt
		start := make(chan struct{})
		writes := make(chan error, 2)
		claims := make(chan *types.WeDriveSource, 1)
		claimErrors := make(chan error, 1)
		go func() {
			<-start
			writes <- svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "tampered", Path: ".", ItemType: "folder"}})
		}()
		go func() {
			<-start
			_, err := svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
			writes <- err
		}()
		go func() {
			<-start
			source, err := svc.ClaimScan(ctx, device, id, ClaimWeDriveScanInput{Trigger: "manual"})
			claims <- source
			claimErrors <- err
		}()
		close(start)
		for n := 0; n < 2; n++ {
			err := <-writes
			require.True(t, errors.Is(err, ErrWeDriveScanAttemptExpired) || errors.Is(err, ErrWeDriveScanAttemptConflict), "unexpected stale write result: %v", err)
		}
		current := <-claims
		require.NoError(t, <-claimErrors)
		saved, err := svc.GetSource(ctx, 7, id)
		require.NoError(t, err)
		require.Equal(t, current.ScanAttemptID, saved.ScanAttemptID)
		require.Equal(t, types.WeDriveScanStateRunning, saved.ScanState)
		require.Empty(t, saved.LastSnapshotID)
		require.Nil(t, saved.LastCompleteScanAt)
		var original types.WeDriveInventoryItem
		require.NoError(t, db.First(&original, "snapshot_id = ?", snapshot.ID).Error)
		require.Equal(t, "root", original.Name)
		var unfinished types.WeDriveSnapshot
		require.NoError(t, db.First(&unfinished, "id = ?", snapshot.ID).Error)
		require.Equal(t, types.WeDriveSnapshotUploading, unfinished.Status)
		cancel()
	}

	// Hold the source externally. A blocked completion must not already hold
	// the snapshot lock, or another source-first writer could deadlock with it.
	device = newSource("pg-lock-order")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	claimed, err := svc.ClaimScan(ctx, device, device.ID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: device.ID, ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
	require.NoError(t, err)
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
	holder := db.WithContext(ctx).Begin()
	require.NoError(t, holder.Error)
	defer holder.Rollback()
	var source types.WeDriveSource
	require.NoError(t, holder.Clauses(clause.Locking{Strength: "UPDATE"}).First(&source, "id = ?", device.ID).Error)
	var holderPID int
	require.NoError(t, holder.Raw("SELECT pg_backend_pid()").Scan(&holderPID).Error)
	done := make(chan error, 1)
	go func() {
		_, err := svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
		done <- err
	}()
	require.Eventually(t, func() bool {
		var blocked int64
		err := db.WithContext(ctx).Raw("SELECT count(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid))", holderPID).Scan(&blocked).Error
		return err == nil && blocked > 0
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL lock_timeout = '1s'").Error; err != nil {
			return err
		}
		var row types.WeDriveSnapshot
		return tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&row, "id = ?", snapshot.ID).Error
	}))
	require.NoError(t, holder.Commit().Error)
	require.NoError(t, <-done)
}
