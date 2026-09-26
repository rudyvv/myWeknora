package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestWeDriveSnapshotBeginUsesAttemptInsteadOfSequence(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	ctx := context.Background()
	createWeDriveScanTestSource(t, svc, "source", *now)
	claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	var in BeginSnapshotInput
	body, err := json.Marshal(map[string]any{"source_id": "source", "scan_attempt_id": claimed.ScanAttemptID, "sequence": 1, "root_external_id": "root", "expected_item_count": 1})
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(body, &in))
	first, err := svc.BeginSnapshot(ctx, device, in)
	require.NoError(t, err)
	in.Sequence = 2 // A transport retry can have a new client timestamp.
	retry, err := svc.BeginSnapshot(ctx, device, in)
	require.NoError(t, err)
	require.Equal(t, first.ID, retry.ID, "one attempt must have only one inventory")
	in.RootExternalID = "other-root"
	_, err = svc.BeginSnapshot(ctx, device, in)
	require.ErrorIs(t, err, ErrWeDriveInvalidSnapshot)
	in.RootExternalID, in.ExpectedItemCount = "root", 2
	_, err = svc.BeginSnapshot(ctx, device, in)
	require.ErrorIs(t, err, ErrWeDriveInvalidSnapshot)
}

func assertWeDriveStaleSnapshotWrites(t *testing.T, svc *WeDriveService, device *types.WeDriveDevice, now *time.Time, sourceID string) {
	t.Helper()
	ctx := context.Background()
	first, err := svc.ClaimScan(ctx, device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	in := BeginSnapshotInput{SourceID: sourceID, ScanAttemptID: first.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1}
	snapshot, err := svc.BeginSnapshot(ctx, device, in)
	require.NoError(t, err)
	items := []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, items))
	*now = now.Add(30 * time.Minute)
	second, err := svc.ClaimScan(ctx, device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	_, err = svc.BeginSnapshot(ctx, device, in)
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
	err = svc.UploadSnapshotItems(ctx, device, snapshot.ID, items)
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
	_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
	require.ErrorIs(t, err, ErrWeDriveScanAttemptConflict)
	saved, err := svc.GetSource(ctx, device.TenantID, sourceID)
	require.NoError(t, err)
	require.Equal(t, second.ScanAttemptID, saved.ScanAttemptID)
	require.Equal(t, second.ScanState, saved.ScanState)
	require.Equal(t, second.LastSnapshotID, saved.LastSnapshotID)
	require.Equal(t, second.LastCompleteScanAt, saved.LastCompleteScanAt)
	require.NotNil(t, saved.ScanLeaseExpiresAt)
	require.True(t, second.ScanLeaseExpiresAt.Equal(*saved.ScanLeaseExpiresAt))
	require.Equal(t, second.NextScanAt, saved.NextScanAt)
}

func TestWeDriveStaleSnapshotCannotUploadOrCommitNewAttempt(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	createWeDriveScanTestSource(t, svc, "source", *now)
	assertWeDriveStaleSnapshotWrites(t, svc, device, now, "source")
}

func assertWeDriveSnapshotCompletionReplay(t *testing.T, svc *WeDriveService, device *types.WeDriveDevice, now *time.Time, sourceID string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, svc.db.AutoMigrate(&types.AuditLog{}))
	svc.audit = NewAuditLogService(repository.NewAuditLogRepository(svc.db))
	claimed, err := svc.ClaimScan(ctx, device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: sourceID, ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
	require.NoError(t, err)
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
	in := CommitSnapshotInput{ItemCount: 1}
	complete, err := svc.CommitSnapshot(ctx, device, snapshot.ID, in)
	require.NoError(t, err)
	// Compare against the persisted result: PostgreSQL timestamps have
	// microsecond precision, whereas GORM's in-memory UpdatedAt has nanoseconds.
	require.NoError(t, svc.db.First(complete, "id = ?", complete.ID).Error)
	*now = now.Add(time.Minute)
	newAttempt, err := svc.ClaimScan(ctx, device, sourceID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	for _, replay := range []CommitSnapshotInput{in, {ItemCount: 1, Digest: strings.ToUpper(complete.Digest)}} {
		result, err := svc.CommitSnapshot(ctx, device, snapshot.ID, replay)
		require.NoError(t, err)
		require.Equal(t, complete.ID, result.ID)
		require.Equal(t, complete.ScanAttemptID, result.ScanAttemptID)
		require.Equal(t, complete.Digest, result.Digest)
		require.Equal(t, complete.ReceivedItemCount, result.ReceivedItemCount)
		require.True(t, complete.CommittedAt.Equal(*result.CommittedAt))
		require.True(t, complete.UpdatedAt.Equal(result.UpdatedAt))
	}
	for _, conflicting := range []CommitSnapshotInput{
		{ItemCount: 2}, {ItemCount: 1, Digest: strings.Repeat("0", 64)},
		{ItemCount: 1, AutoShareCreated: 1}, {ItemCount: 1, AutoShareExisting: 1}, {ItemCount: 1, AutoShareFailed: 1},
	} {
		_, err := svc.CommitSnapshot(ctx, device, snapshot.ID, conflicting)
		require.ErrorIs(t, err, ErrWeDriveInvalidSnapshot)
	}
	saved, err := svc.GetSource(ctx, device.TenantID, sourceID)
	require.NoError(t, err)
	require.Equal(t, newAttempt.ScanAttemptID, saved.ScanAttemptID)
	require.Equal(t, newAttempt.ScanState, saved.ScanState)
	require.NotNil(t, saved.ScanLeaseExpiresAt)
	require.True(t, newAttempt.ScanLeaseExpiresAt.Equal(*saved.ScanLeaseExpiresAt))
	require.Equal(t, newAttempt.NextScanAt, saved.NextScanAt)
	require.Equal(t, complete.ID, saved.LastSnapshotID)
	require.Equal(t, newAttempt.LastCompleteScanAt, saved.LastCompleteScanAt)
	entries, err := svc.audit.List(ctx, device.TenantID, &interfaces.AuditLogQuery{Action: types.AuditActionWeDriveSnapshotCommitted, Limit: 100})
	require.NoError(t, err)
	count := 0
	for _, entry := range entries {
		if entry.TargetID == sourceID {
			count++
		}
	}
	require.Equal(t, 1, count, "retries must not duplicate success audit")
}

func TestWeDriveSnapshotCompletionOnlyAcceptsEquivalentReplay(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	createWeDriveScanTestSource(t, svc, "source", *now)
	assertWeDriveSnapshotCompletionReplay(t, svc, device, now, "source")
}

// The datasource port is an external side effect; lifecycle rules and audit
// persistence use the production implementations and real SQLite above.
type failingWeDriveActivation struct {
	interfaces.DataSourceService
	calls int
}

func (f *failingWeDriveActivation) CreateDataSource(context.Context, *types.DataSource) (*types.DataSource, error) {
	f.calls++
	return nil, errors.New("activation unavailable")
}

func TestWeDriveCommittedSnapshotReplayDoesNotRetryActivation(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	createWeDriveScanTestSource(t, svc, "source", *now)
	activation := &failingWeDriveActivation{}
	svc.datasources = activation
	ctx := context.Background()
	claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: "source", ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
	require.NoError(t, err)
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
	_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
	require.ErrorContains(t, err, "activate datasource")
	result, err := svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
	require.NoError(t, err)
	require.Equal(t, types.WeDriveSnapshotComplete, result.Status)
	require.Equal(t, 1, activation.calls)
}

func TestWeDriveExpiredSnapshotWritesPersistTimeoutCleanup(t *testing.T) {
	for _, action := range []string{"begin", "upload", "commit"} {
		t.Run(action, func(t *testing.T) {
			svc, device, now := newWeDriveScanTestService(t)
			ctx := context.Background()
			createWeDriveScanTestSource(t, svc, "source", *now)
			claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
			require.NoError(t, err)
			in := BeginSnapshotInput{SourceID: "source", ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1}
			snapshot, err := svc.BeginSnapshot(ctx, device, in)
			require.NoError(t, err)
			items := []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}
			require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, items))
			*now = *claimed.ScanLeaseExpiresAt
			switch action {
			case "begin":
				_, err = svc.BeginSnapshot(ctx, device, in)
			case "upload":
				err = svc.UploadSnapshotItems(ctx, device, snapshot.ID, items)
			case "commit":
				_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
			}
			require.ErrorIs(t, err, ErrWeDriveScanAttemptExpired)
			source, err := svc.GetSource(ctx, 7, "source")
			require.NoError(t, err)
			require.Equal(t, types.WeDriveScanStateRetryWait, source.ScanState)
			require.Equal(t, "scan_timeout", source.LastScanErrorCode)
			require.Equal(t, now.Add(10*time.Minute), *source.NextScanAt)
			require.Empty(t, source.ScanAttemptID)
			require.Nil(t, source.ScanLeaseExpiresAt)
			require.Empty(t, source.LastSnapshotID)
			require.Nil(t, source.LastCompleteScanAt)
		})
	}
}

func TestWeDriveSnapshotCannotCompleteWhenValidationCrossesLeaseExpiry(t *testing.T) {
	svc, device, now := newWeDriveScanTestService(t)
	ctx := context.Background()
	createWeDriveScanTestSource(t, svc, "source", *now)
	claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: "source", ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1})
	require.NoError(t, err)
	require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}))
	reads := 0
	svc.now = func() time.Time {
		reads++
		if reads == 1 {
			return claimed.ScanLeaseExpiresAt.Add(-time.Second)
		}
		return *claimed.ScanLeaseExpiresAt
	}
	_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, CommitSnapshotInput{ItemCount: 1})
	require.ErrorIs(t, err, ErrWeDriveScanAttemptExpired)
	source, err := svc.GetSource(ctx, 7, "source")
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRetryWait, source.ScanState)
	require.Empty(t, source.ScanAttemptID)
	require.Empty(t, source.LastSnapshotID)
	require.Nil(t, source.LastCompleteScanAt)
	require.Equal(t, "scan_timeout", source.LastScanErrorCode)
	require.True(t, source.NextScanAt.Equal(claimed.ScanLeaseExpiresAt.Add(10*time.Minute)))
	var unchanged types.WeDriveSnapshot
	require.NoError(t, svc.db.First(&unchanged, "id = ?", snapshot.ID).Error)
	require.Equal(t, types.WeDriveSnapshotUploading, unchanged.Status)
	require.Nil(t, unchanged.CommittedAt)
}

func TestWeDriveSnapshotIntegrityChecksStillGuardCompletion(t *testing.T) {
	for _, scenario := range []string{"missing root", "orphan", "bad digest", "incomplete"} {
		t.Run(scenario, func(t *testing.T) {
			svc, device, now := newWeDriveScanTestService(t)
			ctx := context.Background()
			createWeDriveScanTestSource(t, svc, "source", *now)
			claimed, err := svc.ClaimScan(ctx, device, "source", ClaimWeDriveScanInput{Trigger: "manual"})
			require.NoError(t, err)
			snapshot, err := svc.BeginSnapshot(ctx, device, BeginSnapshotInput{SourceID: "source", ScanAttemptID: claimed.ScanAttemptID, Sequence: 1, RootExternalID: "root", ExpectedItemCount: 2})
			require.NoError(t, err)
			items := []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}, {ExternalID: "file", Name: "file", Path: "file", ParentExternalID: "root", ItemType: "file"}}
			in := CommitSnapshotInput{ItemCount: 2}
			switch scenario {
			case "missing root":
				items[0].ExternalID = "outside-root"
			case "orphan":
				items[1].ParentExternalID = "outside"
			case "bad digest":
				in.Digest = strings.Repeat("0", 64)
			case "incomplete":
				items = items[:1]
			}
			require.NoError(t, svc.UploadSnapshotItems(ctx, device, snapshot.ID, items))
			_, err = svc.CommitSnapshot(ctx, device, snapshot.ID, in)
			require.ErrorIs(t, err, ErrWeDriveInvalidSnapshot)
			source, err := svc.GetSource(ctx, 7, "source")
			require.NoError(t, err)
			require.Equal(t, claimed.ScanAttemptID, source.ScanAttemptID)
			require.Equal(t, types.WeDriveScanStateRunning, source.ScanState)
			require.Empty(t, source.LastSnapshotID)
		})
	}
}
