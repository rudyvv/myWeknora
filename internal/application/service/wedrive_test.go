package service

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestValidateInventoryHierarchy(t *testing.T) {
	tests := []struct {
		name  string
		root  string
		items []types.WeDriveInventoryItem
		ok    bool
	}{
		{
			name: "valid subtree", root: "root", ok: true,
			items: []types.WeDriveInventoryItem{
				{ExternalID: "root"},
				{ExternalID: "folder", ParentExternalID: "root"},
				{ExternalID: "file", ParentExternalID: "folder"},
			},
		},
		{name: "missing root", root: "root", items: []types.WeDriveInventoryItem{{ExternalID: "file"}}},
		{name: "root has parent", root: "root", items: []types.WeDriveInventoryItem{{ExternalID: "root", ParentExternalID: "outside"}}},
		{
			name: "orphan", root: "root",
			items: []types.WeDriveInventoryItem{{ExternalID: "root"}, {ExternalID: "file", ParentExternalID: "outside"}},
		},
		{
			name: "cycle", root: "root",
			items: []types.WeDriveInventoryItem{{ExternalID: "root"}, {ExternalID: "a", ParentExternalID: "b"}, {ExternalID: "b", ParentExternalID: "a"}},
		},
		{
			name: "duplicate id", root: "root",
			items: []types.WeDriveInventoryItem{{ExternalID: "root"}, {ExternalID: "root"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateInventoryHierarchy(tt.root, tt.items)
			if tt.ok {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrWeDriveInvalidSnapshot)
		})
	}
}

func TestInventoryDigestIsOrderIndependentAndDetectsMetadataChange(t *testing.T) {
	modified := time.Date(2026, 9, 17, 1, 2, 3, 0, time.UTC)
	a := types.WeDriveInventoryItem{ExternalID: "a", ParentExternalID: "root", Path: "/a", ItemType: "file", Size: 10, ModifiedAt: &modified}
	b := types.WeDriveInventoryItem{ExternalID: "b", ParentExternalID: "root", Path: "/b", ItemType: "file", ShareURL: "https://drive.weixin.qq.com/s?x"}
	require.Equal(t, InventoryDigest([]types.WeDriveInventoryItem{a, b}), InventoryDigest([]types.WeDriveInventoryItem{b, a}))
	b.Size++
	require.NotEqual(t, InventoryDigest([]types.WeDriveInventoryItem{a, b}), InventoryDigest([]types.WeDriveInventoryItem{a, {ExternalID: "b", ParentExternalID: "root", Path: "/b", ItemType: "file", ShareURL: "https://drive.weixin.qq.com/s?x"}}))
}

func TestHasParentTraversalChecksSegmentsOnly(t *testing.T) {
	require.True(t, hasParentTraversal("root/../secret"))
	require.True(t, hasParentTraversal(`root\..\secret`))
	require.False(t, hasParentTraversal("root/meeting..notes.docx"))
}

func TestInvalidSnapshotErrorIsStable(t *testing.T) {
	err := validateInventoryHierarchy("root", nil)
	require.True(t, errors.Is(err, ErrWeDriveInvalidSnapshot))
}

func TestValidWeDriveRootURL(t *testing.T) {
	require.True(t, validWeDriveRootURL("https://drive.weixin.qq.com/#/webdisk/folder?id=abc"))
	require.True(t, validWeDriveRootURL("https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/s.1970325030004462.772095854VR0/all?folderid=s.1970325030004462.772095854VR0_d.7724"))
	require.False(t, validWeDriveRootURL("https://drive.weixin.qq.com/#/webdisk"))
	require.False(t, validWeDriveRootURL("https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/i.1970325030004462.1688854293012816/all"))
	require.False(t, validWeDriveRootURL("http://drive.weixin.qq.com/#/webdisk/folder?id=abc"))
	require.False(t, validWeDriveRootURL("https://drive.weixin.qq.com.evil.example/folder"))
	require.False(t, validWeDriveRootURL("https://drive.weixin.qq.com/s?k=file-share"))
}

func TestBeginSnapshotRepairsUncommittedRootFromApprovedURL(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-root-recovery.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeDriveSnapshot{}))
	source := &types.WeDriveSource{
		ID: "source-root-recovery", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "user-1",
		Name: "root", RootURL: "https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/s.root/all?folderid=approved-root",
		RootExternalID: "stale-folder", Status: types.WeDriveSourceAwaitingInventory,
	}
	require.NoError(t, db.Create(source).Error)
	svc := NewWeDriveService(db, nil, nil)
	device := &types.WeDriveDevice{ID: "device-1", TenantID: 7}

	_, err = svc.BeginSnapshot(context.Background(), device, BeginSnapshotInput{
		SourceID: source.ID, Sequence: 1, RootExternalID: "another-folder", ExpectedItemCount: 1,
	})
	require.ErrorIs(t, err, ErrWeDriveForbidden)

	_, err = svc.BeginSnapshot(context.Background(), device, BeginSnapshotInput{
		SourceID: source.ID, Sequence: 1, RootExternalID: "approved-root", ExpectedItemCount: 1,
	})
	require.NoError(t, err)

	var saved types.WeDriveSource
	require.NoError(t, db.First(&saved, "id = ?", source.ID).Error)
	require.Equal(t, "approved-root", saved.RootExternalID)
}

func TestListSourcesIncludesBoundConnectionNameForAdmin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-source-label.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeComCLIConnection{}))
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-1", TenantID: 7, Name: "资料管理员"}).Error)
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-other-tenant", TenantID: 8, Name: "其他租户"}).Error)
	for _, source := range []*types.WeDriveSource{
		{ID: "source-1", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "user-1", Name: "bound", RootURL: "https://drive.weixin.qq.com/folder/1", ConnectionID: "connection-1"},
		{ID: "source-2", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "user-1", Name: "wrong tenant", RootURL: "https://drive.weixin.qq.com/folder/2", ConnectionID: "connection-other-tenant"},
	} {
		require.NoError(t, db.Create(source).Error)
	}

	svc := NewWeDriveService(db, nil, nil)
	adminRows, err := svc.ListSources(context.Background(), 7, "user-1", true, "")
	require.NoError(t, err)
	require.Len(t, adminRows, 2)
	byID := map[string]*types.WeDriveSource{}
	for _, row := range adminRows {
		byID[row.ID] = row
	}
	require.Equal(t, "资料管理员", byID["source-1"].ConnectionName)
	require.Empty(t, byID["source-2"].ConnectionName)

	memberRows, err := svc.ListSources(context.Background(), 7, "user-1", false, "")
	require.NoError(t, err)
	for _, row := range memberRows {
		require.Empty(t, row.ConnectionName)
	}
}

func TestDeleteSourceSoftDeletesAndStopsAgentDiscovery(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}))
	source := &types.WeDriveSource{ID: "source-delete", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "user-1", Name: "wrong root", RootURL: "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.1", Status: types.WeDriveSourceActive}
	require.NoError(t, db.Create(source).Error)

	svc := NewWeDriveService(db, nil, nil)
	require.NoError(t, svc.DeleteSource(context.Background(), 7, source.ID))

	_, err = svc.GetSource(context.Background(), 7, source.ID)
	require.ErrorIs(t, err, ErrWeDriveNotFound)
	var active int64
	require.NoError(t, db.Model(&types.WeDriveSource{}).Where("tenant_id = ? AND device_id = ? AND status IN ?", 7, "device-1", []string{types.WeDriveSourceAwaitingInventory, types.WeDriveSourceActive}).Count(&active).Error)
	require.Zero(t, active)
}

func TestWeDriveScanCadenceValuesAndVersionGate(t *testing.T) {
	require.True(t, types.IsValidWeDriveScanInterval(0))
	require.True(t, types.IsValidWeDriveScanInterval(30))
	require.True(t, types.IsValidWeDriveScanInterval(1440))
	require.False(t, types.IsValidWeDriveScanInterval(15))
	require.True(t, SupportsWeDriveScanCadence("0.3.0"))
	require.True(t, SupportsWeDriveScanCadence("1.0.0"))
	require.False(t, SupportsWeDriveScanCadence("0.2.9"))
	require.False(t, SupportsWeDriveScanCadence("unknown"))
}

func TestWeDriveScheduledClaimRetriesOnceThenReturnsToCadence(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-scan.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}))
	now := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	source := &types.WeDriveSource{
		ID: "source-cadence", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "user-1",
		Name: "root", RootURL: "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.1",
		Status: types.WeDriveSourceActive, ScanIntervalMinutes: 30, ScanState: types.WeDriveScanStateWaiting, NextScanAt: &now,
	}
	require.NoError(t, db.Create(source).Error)
	svc := NewWeDriveService(db, nil, nil)
	svc.now = func() time.Time { return now }
	device := &types.WeDriveDevice{ID: "device-1", TenantID: 7}

	claimed, err := svc.ClaimScan(context.Background(), device, source.ID, ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRunning, claimed.ScanState)
	failed, err := svc.ReportScanFailure(context.Background(), device, source.ID, ReportWeDriveScanFailureInput{Code: "listing_timeout"})
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRetryWait, failed.ScanState)
	require.Equal(t, 1, failed.ScanRetryCount)
	require.Equal(t, now.Add(weDriveScanRetryDelay), *failed.NextScanAt)

	now = now.Add(weDriveScanRetryDelay)
	_, err = svc.ClaimScan(context.Background(), device, source.ID, ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.NoError(t, err)
	failed, err = svc.ReportScanFailure(context.Background(), device, source.ID, ReportWeDriveScanFailureInput{Code: "listing_timeout"})
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateFailed, failed.ScanState)
	require.Equal(t, 0, failed.ScanRetryCount)
	require.Equal(t, now.Add(30*time.Minute), *failed.NextScanAt)
}

func TestManualWeDriveScanHasNoAutomaticFollowUp(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-manual.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}))
	source := &types.WeDriveSource{ID: "source-manual", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "user-1", Name: "root", RootURL: "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.1", Status: types.WeDriveSourceActive, ScanIntervalMinutes: 0, ScanState: types.WeDriveScanStateIdle}
	require.NoError(t, db.Create(source).Error)
	svc := NewWeDriveService(db, nil, nil)
	device := &types.WeDriveDevice{ID: "device-1", TenantID: 7}
	_, err = svc.ClaimScan(context.Background(), device, source.ID, ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.ErrorIs(t, err, ErrWeDriveScanNotDue)
	claimed, err := svc.ClaimScan(context.Background(), device, source.ID, ClaimWeDriveScanInput{Trigger: "manual"})
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateRunning, claimed.ScanState)
}

func TestExpiredScheduledScanUsesFailureRetryDelay(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-expired.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}))
	now := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Minute)
	due := now.Add(-time.Hour)
	source := &types.WeDriveSource{
		ID: "expired", TenantID: 7, KnowledgeBaseID: "kb-1", DeviceID: "device-1", CreatedBy: "owner",
		Name: "root", RootURL: "https://drive.weixin.qq.com/root", Status: types.WeDriveSourceActive,
		ScanIntervalMinutes: 30, ScanState: types.WeDriveScanStateRunning,
		ScanLeaseExpiresAt: &expired, NextScanAt: &due,
	}
	require.NoError(t, db.Create(source).Error)
	svc := NewWeDriveService(db, nil, nil)
	svc.now = func() time.Time { return now }
	device := &types.WeDriveDevice{ID: "device-1", TenantID: 7}
	_, err = svc.ClaimScan(context.Background(), device, source.ID, ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.ErrorIs(t, err, ErrWeDriveScanNotDue)
	var saved types.WeDriveSource
	require.NoError(t, db.First(&saved, "id = ?", source.ID).Error)
	require.Equal(t, types.WeDriveScanStateRetryWait, saved.ScanState)
	require.Equal(t, "scan_timeout", saved.LastScanErrorCode)
	require.Equal(t, 1, saved.ScanRetryCount)
	require.Equal(t, now.Add(weDriveScanRetryDelay), *saved.NextScanAt)
	require.Nil(t, saved.ScanLeaseExpiresAt)

	now = now.Add(weDriveScanRetryDelay)
	_, err = svc.ClaimScan(context.Background(), device, source.ID, ClaimWeDriveScanInput{Trigger: "scheduled"})
	require.NoError(t, err)
}

func TestRecoverInterruptedScansOnlyResetsThisDevicesPreRestartClaims(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-recovery.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}))

	startedAt := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	now := startedAt.Add(time.Minute)
	before := startedAt.Add(-time.Minute)
	after := startedAt.Add(time.Second)
	lease := now.Add(weDriveScanLease)
	for _, item := range []struct {
		id, deviceID string
		tenantID     uint64
		startedAt    time.Time
		interval     int
	}{
		{"interrupted-manual", "device-1", 7, before, 0},
		{"interrupted-scheduled", "device-1", 7, before, 30},
		{"current-scan", "device-1", 7, after, 0},
		{"another-device", "device-2", 7, before, 0},
		{"another-tenant", "device-1", 8, before, 0},
	} {
		source := &types.WeDriveSource{
			ID: item.id, TenantID: item.tenantID, KnowledgeBaseID: "kb-1", DeviceID: item.deviceID,
			CreatedBy: "owner", Name: item.id, RootURL: "https://drive.weixin.qq.com/root", Status: types.WeDriveSourceActive,
			ScanIntervalMinutes: item.interval, ScanState: types.WeDriveScanStateRunning,
			ScanLeaseExpiresAt: &lease, LastScanStartedAt: &item.startedAt,
		}
		require.NoError(t, db.Create(source).Error)
	}

	svc := NewWeDriveService(db, nil, nil)
	svc.startedAt = startedAt
	svc.now = func() time.Time { return now }
	require.NoError(t, svc.RecoverInterruptedScans(context.Background(), &types.WeDriveDevice{ID: "device-1", TenantID: 7}))

	rows := map[string]types.WeDriveSource{}
	for _, id := range []string{"interrupted-manual", "interrupted-scheduled", "current-scan", "another-device", "another-tenant"} {
		var saved types.WeDriveSource
		require.NoError(t, db.First(&saved, "id = ?", id).Error)
		rows[id] = saved
	}
	require.Equal(t, types.WeDriveScanStateFailed, rows["interrupted-manual"].ScanState)
	require.Nil(t, rows["interrupted-manual"].ScanLeaseExpiresAt)
	require.Nil(t, rows["interrupted-manual"].NextScanAt)
	require.Equal(t, "scan_interrupted", rows["interrupted-manual"].LastScanErrorCode)
	require.Equal(t, types.WeDriveScanStateRetryWait, rows["interrupted-scheduled"].ScanState)
	require.Equal(t, now.Add(weDriveScanRetryDelay), *rows["interrupted-scheduled"].NextScanAt)
	require.Equal(t, 1, rows["interrupted-scheduled"].ScanRetryCount)
	for _, id := range []string{"current-scan", "another-device", "another-tenant"} {
		require.Equal(t, types.WeDriveScanStateRunning, rows[id].ScanState, id)
		require.Equal(t, lease, *rows[id].ScanLeaseExpiresAt, id)
	}

	// Reconnecting again in the same process must not consume the retry.
	require.NoError(t, svc.RecoverInterruptedScans(context.Background(), &types.WeDriveDevice{ID: "device-1", TenantID: 7}))
	var scheduled types.WeDriveSource
	require.NoError(t, db.First(&scheduled, "id = ?", "interrupted-scheduled").Error)
	require.Equal(t, 1, scheduled.ScanRetryCount)
}

func TestCreateSourceKeepsManualScanCadenceForAdmin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-create-manual.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeDriveDevice{}, &types.WeComCLIConnection{}))
	require.NoError(t, db.Create(&types.WeDriveDevice{ID: "device-1", TenantID: 7, UserID: "owner-1", Status: "online"}).Error)
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-1", TenantID: 7, Name: "CLI"}).Error)

	manual := 0
	svc := NewWeDriveService(db, nil, nil)
	source, err := svc.CreateSource(context.Background(), 7, "owner-1", types.TenantRoleOwner, CreateWeDriveSourceInput{
		KnowledgeBaseID: "kb-1", ConnectionID: "connection-1", DeviceID: "device-1", Name: "root",
		RootURL:             "https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.1",
		ScanIntervalMinutes: &manual,
	})
	require.NoError(t, err)
	require.Equal(t, 0, source.ScanIntervalMinutes)
	require.Empty(t, source.SyncSchedule)
	require.Equal(t, types.WeDriveScanStateIdle, source.ScanState)
	require.Nil(t, source.NextScanAt)
	var persisted types.WeDriveSource
	require.NoError(t, db.First(&persisted, "id = ?", source.ID).Error)
	require.Equal(t, 0, persisted.ScanIntervalMinutes)
	require.Equal(t, types.WeDriveScanStateIdle, persisted.ScanState)
	require.Nil(t, persisted.NextScanAt)

	defaultSource, err := svc.CreateSource(context.Background(), 7, "owner-1", types.TenantRoleOwner, CreateWeDriveSourceInput{
		KnowledgeBaseID: "kb-1", ConnectionID: "connection-1", DeviceID: "device-1", Name: "default root",
		RootURL: "https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.2",
	})
	require.NoError(t, err)
	require.Zero(t, defaultSource.ScanIntervalMinutes)
	require.Nil(t, defaultSource.NextScanAt)

	require.NoError(t, db.Create(&types.WeDriveDevice{ID: "device-2", TenantID: 7, UserID: "contributor-1", Status: "online"}).Error)
	requestedCadence := 30
	contributorSource, err := svc.CreateSource(context.Background(), 7, "contributor-1", types.TenantRoleContributor, CreateWeDriveSourceInput{
		KnowledgeBaseID: "kb-1", DeviceID: "device-2", Name: "contributor root",
		RootURL:             "https://drive.weixin.qq.com/webdisk/index?t=home#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.3",
		ScanIntervalMinutes: &requestedCadence,
	})
	require.NoError(t, err)
	require.Zero(t, contributorSource.ScanIntervalMinutes)
	require.Equal(t, types.WeDriveSourcePending, contributorSource.Status)
}

func TestRebindSourceConnectionUpdatesLinkedDataSourceAtomically(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-rebind.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeComCLIConnection{}, &types.DataSource{}, &types.SyncLog{}))

	config, err := (&types.DataSourceConfig{
		Type:        types.ConnectorTypeWeComDrive,
		ResourceIDs: []string{"source-rebind"},
		Settings:    map[string]interface{}{"source_id": "source-rebind", "connection_id": "connection-old"},
	}).ToJSON()
	require.NoError(t, err)
	dataSource := &types.DataSource{ID: "datasource-rebind", TenantID: 7, KnowledgeBaseID: "kb-1", Name: "root", Type: types.ConnectorTypeWeComDrive, Config: config}
	source := &types.WeDriveSource{ID: "source-rebind", TenantID: 7, KnowledgeBaseID: "kb-1", DataSourceID: dataSource.ID, ConnectionID: "connection-old", DeviceID: "device-1", CreatedBy: "user-1", Name: "root", RootURL: "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.1", Status: types.WeDriveSourceActive}
	require.NoError(t, db.Create(dataSource).Error)
	require.NoError(t, db.Create(source).Error)
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-old", TenantID: 7, Name: "old"}).Error)
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-new", TenantID: 7, Name: "new"}).Error)

	svc := NewWeDriveService(db, nil, nil)
	updated, err := svc.RebindSourceConnection(context.Background(), 7, "admin-1", source.ID, "connection-new")
	require.NoError(t, err)
	require.Equal(t, "connection-new", updated.ConnectionID)

	var persistedSource types.WeDriveSource
	require.NoError(t, db.First(&persistedSource, "id = ?", source.ID).Error)
	require.Equal(t, "connection-new", persistedSource.ConnectionID)
	var persistedDataSource types.DataSource
	require.NoError(t, db.First(&persistedDataSource, "id = ?", dataSource.ID).Error)
	persistedConfig, err := persistedDataSource.ParseConfig()
	require.NoError(t, err)
	require.Equal(t, "connection-new", persistedConfig.Settings["connection_id"])
	require.Equal(t, source.ID, persistedConfig.Settings["source_id"])
}

func TestRebindSourceConnectionRejectsRunningContentSync(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-rebind-running.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeComCLIConnection{}, &types.DataSource{}, &types.SyncLog{}))

	config, err := (&types.DataSourceConfig{Type: types.ConnectorTypeWeComDrive, Settings: map[string]interface{}{"source_id": "source-running", "connection_id": "connection-old"}}).ToJSON()
	require.NoError(t, err)
	dataSource := &types.DataSource{ID: "datasource-running", TenantID: 7, KnowledgeBaseID: "kb-1", Name: "root", Type: types.ConnectorTypeWeComDrive, Config: config}
	source := &types.WeDriveSource{ID: "source-running", TenantID: 7, KnowledgeBaseID: "kb-1", DataSourceID: dataSource.ID, ConnectionID: "connection-old", DeviceID: "device-1", CreatedBy: "user-1", Name: "root", RootURL: "https://drive.weixin.qq.com/webdisk/index#/cgi/ssr/space/1/s.root/all?folderid=s.root_d.1", Status: types.WeDriveSourceActive}
	require.NoError(t, db.Create(dataSource).Error)
	require.NoError(t, db.Create(source).Error)
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-old", TenantID: 7, Name: "old"}).Error)
	require.NoError(t, db.Create(&types.WeComCLIConnection{ID: "connection-new", TenantID: 7, Name: "new"}).Error)
	require.NoError(t, db.Create(&types.SyncLog{ID: "sync-running", DataSourceID: dataSource.ID, TenantID: 7, Status: types.SyncLogStatusRunning}).Error)

	svc := NewWeDriveService(db, nil, nil)
	_, err = svc.RebindSourceConnection(context.Background(), 7, "admin-1", source.ID, "connection-new")
	require.ErrorIs(t, err, ErrWeDriveInvalidState)

	var persistedSource types.WeDriveSource
	require.NoError(t, db.First(&persistedSource, "id = ?", source.ID).Error)
	require.Equal(t, "connection-old", persistedSource.ConnectionID)
	var persistedDataSource types.DataSource
	require.NoError(t, db.First(&persistedDataSource, "id = ?", dataSource.ID).Error)
	persistedConfig, err := persistedDataSource.ParseConfig()
	require.NoError(t, err)
	require.Equal(t, "connection-old", persistedConfig.Settings["connection_id"])
}
