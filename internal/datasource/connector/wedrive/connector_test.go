package wedrive

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

type deletionEvidenceHandler struct{ items []types.FetchedItem }

func (h *deletionEvidenceHandler) Emit(_ context.Context, item types.FetchedItem) error {
	h.items = append(h.items, item)
	return nil
}
func (h *deletionEvidenceHandler) Checkpoint(context.Context, *types.SyncCursor) error { return nil }

type rejectedDeletionHandler struct{ deletionEvidenceHandler }

func (h *rejectedDeletionHandler) EmitWithOutcome(ctx context.Context, item types.FetchedItem) (bool, error) {
	if err := h.Emit(ctx, item); err != nil {
		return false, err
	}
	return false, nil
}

func deletionEvidenceFixture(t *testing.T, snapshots []types.WeDriveSnapshot, inventory []types.WeDriveInventoryItem) (*Connector, *types.DataSourceConfig) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "wedrive-deletions.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveSource{}, &types.WeDriveSnapshot{}, &types.WeDriveInventoryItem{}, &types.WeComCLIConnection{}))
	latest := snapshots[len(snapshots)-1]
	require.NoError(t, db.Create(&types.WeDriveSource{ID: "source", TenantID: 1, ConnectionID: "connection", LastSnapshotID: latest.ID, Status: types.WeDriveSourceActive}).Error)
	for i := range snapshots {
		require.NoError(t, db.Create(&snapshots[i]).Error)
	}
	for i := range inventory {
		require.NoError(t, db.Create(&inventory[i]).Error)
	}
	require.NoError(t, db.Exec("INSERT INTO wecom_cli_connections (id, tenant_id, name, credentials) VALUES (?, ?, ?, ?)", "connection", 1, "test", `{"bot_id":"test","secret":"test"}`).Error)
	return NewConnector(db), &types.DataSourceConfig{Settings: map[string]interface{}{"source_id": "source", "connection_id": "connection"}}
}

func deletionSnapshot(id string, sequence int64, committedAt time.Time) types.WeDriveSnapshot {
	return types.WeDriveSnapshot{ID: id, TenantID: 1, SourceID: "source", Sequence: sequence, Status: types.WeDriveSnapshotComplete, CommittedAt: &committedAt}
}

func deletionRoot(snapshotID string) types.WeDriveInventoryItem {
	return types.WeDriveInventoryItem{TenantID: 1, SourceID: "source", SnapshotID: snapshotID, ExternalID: "root", ItemType: "folder", Name: "root", Path: "root"}
}

func TestDeletionRequiresTwoDistinctCompleteSnapshots(t *testing.T) {
	now := time.Now().UTC()
	firstMissingAt := now.Add(-25 * time.Hour)
	connector, config := deletionEvidenceFixture(t,
		[]types.WeDriveSnapshot{deletionSnapshot("present", 1, firstMissingAt.Add(-time.Hour)), deletionSnapshot("absent-1", 2, firstMissingAt), deletionSnapshot("absent-2", 3, now)},
		[]types.WeDriveInventoryItem{deletionRoot("absent-1"), deletionRoot("absent-2")},
	)
	prev := encodeCursor(cursorState{SnapshotID: "present", DeletionEvidenceVersion: deletionEvidenceVersion, Items: map[string]itemState{"file": {Fingerprint: "prior", Title: "file"}}})
	first := &deletionEvidenceHandler{}
	next, err := connector.FetchStream(context.Background(), config, prev, first)
	require.NoError(t, err)
	require.Len(t, first.items, 1)
	require.True(t, first.items[0].IsDeleted)
	require.Equal(t, "file", first.items[0].ExternalID)
	require.NotContains(t, decodeCursor(next).Items, "file")
	require.Equal(t, "absent-2", decodeCursor(next).SnapshotID)
}

func TestFailedDeletionRemainsInCursorForNextSync(t *testing.T) {
	now := time.Now().UTC()
	connector, config := deletionEvidenceFixture(t,
		[]types.WeDriveSnapshot{deletionSnapshot("present", 1, now.Add(-26*time.Hour)), deletionSnapshot("absent-1", 2, now.Add(-25*time.Hour)), deletionSnapshot("absent-2", 3, now)},
		[]types.WeDriveInventoryItem{deletionRoot("absent-1"), deletionRoot("absent-2")},
	)
	prev := encodeCursor(cursorState{SnapshotID: "present", DeletionEvidenceVersion: deletionEvidenceVersion, Items: map[string]itemState{"file": {Fingerprint: "prior", Title: "file"}}})
	rejected := &rejectedDeletionHandler{}
	next, err := connector.FetchStream(context.Background(), config, prev, rejected)
	require.NoError(t, err)
	require.Len(t, rejected.items, 1)
	require.Contains(t, decodeCursor(next).Items, "file")

	retry := &deletionEvidenceHandler{}
	afterRetry, err := connector.FetchStream(context.Background(), config, next, retry)
	require.NoError(t, err)
	require.Len(t, retry.items, 1)
	require.NotContains(t, decodeCursor(afterRetry).Items, "file")
}

func TestWeDriveDataSourceBindingCannotCrossTenantOrKnowledgeBase(t *testing.T) {
	now := time.Now().UTC()
	connector, config := deletionEvidenceFixture(t, []types.WeDriveSnapshot{deletionSnapshot("present", 1, now)}, []types.WeDriveInventoryItem{deletionRoot("present")})
	require.NoError(t, connector.db.Model(&types.WeDriveSource{}).Where("id = ?", "source").Updates(map[string]any{
		"knowledge_base_id": "kb-1", "data_source_id": "ds-1",
	}).Error)
	ds := &types.DataSource{ID: "ds-1", TenantID: 1, KnowledgeBaseID: "kb-1"}
	require.NoError(t, connector.ValidateDataSourceBinding(context.Background(), config, ds))
	ds.TenantID = 2
	require.Error(t, connector.ValidateDataSourceBinding(context.Background(), config, ds))
	ds.TenantID, ds.KnowledgeBaseID = 1, "kb-2"
	require.Error(t, connector.ValidateDataSourceBinding(context.Background(), config, ds))
	ds.KnowledgeBaseID, ds.ID = "kb-1", "ds-2"
	require.Error(t, connector.ValidateDataSourceBinding(context.Background(), config, ds))
}

func TestDeletionDoesNotAdvanceOnRepeatedContentSync(t *testing.T) {
	now := time.Now().UTC()
	connector, config := deletionEvidenceFixture(t,
		[]types.WeDriveSnapshot{deletionSnapshot("present", 1, now.Add(-26*time.Hour)), deletionSnapshot("absent", 2, now.Add(-25*time.Hour))},
		[]types.WeDriveInventoryItem{deletionRoot("absent")},
	)
	prev := encodeCursor(cursorState{SnapshotID: "present", DeletionEvidenceVersion: deletionEvidenceVersion, Items: map[string]itemState{"file": {Fingerprint: "prior", Title: "file"}}})
	first := &deletionEvidenceHandler{}
	next, err := connector.FetchStream(context.Background(), config, prev, first)
	require.NoError(t, err)
	require.Empty(t, first.items)
	require.Equal(t, 1, decodeCursor(next).Items["file"].MissingCount)
	second := &deletionEvidenceHandler{}
	afterRepeat, err := connector.FetchStream(context.Background(), config, next, second)
	require.NoError(t, err)
	require.Empty(t, second.items)
	require.Equal(t, 1, decodeCursor(afterRepeat).Items["file"].MissingCount)
	thirdSnapshot := deletionSnapshot("absent-again", 3, now)
	require.NoError(t, connector.db.Create(&thirdSnapshot).Error)
	thirdRoot := deletionRoot(thirdSnapshot.ID)
	require.NoError(t, connector.db.Create(&thirdRoot).Error)
	require.NoError(t, connector.db.Model(&types.WeDriveSource{}).Where("id = ?", "source").Update("last_snapshot_id", thirdSnapshot.ID).Error)
	third := &deletionEvidenceHandler{}
	_, err = connector.FetchStream(context.Background(), config, afterRepeat, third)
	require.NoError(t, err)
	require.Len(t, third.items, 1)
	require.True(t, third.items[0].IsDeleted)
}

func TestDeletionWaitsFor24HoursAfterFirstMissingSnapshot(t *testing.T) {
	now := time.Now().UTC()
	connector, config := deletionEvidenceFixture(t,
		[]types.WeDriveSnapshot{deletionSnapshot("present", 1, now.Add(-2*time.Hour)), deletionSnapshot("absent-1", 2, now.Add(-time.Hour)), deletionSnapshot("absent-2", 3, now)},
		[]types.WeDriveInventoryItem{deletionRoot("absent-1"), deletionRoot("absent-2")},
	)
	prev := encodeCursor(cursorState{SnapshotID: "present", DeletionEvidenceVersion: deletionEvidenceVersion, Items: map[string]itemState{"file": {Fingerprint: "prior", Title: "file"}}})
	h := &deletionEvidenceHandler{}
	next, err := connector.FetchStream(context.Background(), config, prev, h)
	require.NoError(t, err)
	require.Empty(t, h.items)
	require.Equal(t, 2, decodeCursor(next).Items["file"].MissingCount)
}

func TestDeletionStreakResetsWhenInterveningSnapshotContainsFile(t *testing.T) {
	now := time.Now().UTC()
	connector, config := deletionEvidenceFixture(t,
		[]types.WeDriveSnapshot{deletionSnapshot("present-0", 1, now.Add(-27*time.Hour)), deletionSnapshot("absent-1", 2, now.Add(-26*time.Hour)), deletionSnapshot("present-1", 3, now.Add(-25*time.Hour)), deletionSnapshot("absent-2", 4, now)},
		[]types.WeDriveInventoryItem{deletionRoot("absent-1"), deletionRoot("present-1"), {TenantID: 1, SourceID: "source", SnapshotID: "present-1", ExternalID: "file", ItemType: "file", Name: "file", Path: "file"}, deletionRoot("absent-2")},
	)
	prev := encodeCursor(cursorState{SnapshotID: "present-0", DeletionEvidenceVersion: deletionEvidenceVersion, Items: map[string]itemState{"file": {Fingerprint: "prior", Title: "file"}}})
	h := &deletionEvidenceHandler{}
	next, err := connector.FetchStream(context.Background(), config, prev, h)
	require.NoError(t, err)
	require.Empty(t, h.items)
	require.Equal(t, 1, decodeCursor(next).Items["file"].MissingCount)
}

func TestLegacyMissingCountCannotAuthorizeDeletion(t *testing.T) {
	now := time.Now().UTC()
	connector, config := deletionEvidenceFixture(t,
		[]types.WeDriveSnapshot{deletionSnapshot("absent", 1, now.Add(-26*time.Hour))},
		[]types.WeDriveInventoryItem{deletionRoot("absent")},
	)
	legacy := encodeCursor(cursorState{SnapshotID: "absent", Items: map[string]itemState{"file": {Fingerprint: "prior", Title: "file", MissingCount: 99, MissingSince: now.Add(-26 * time.Hour).Format(time.RFC3339Nano)}}})
	h := &deletionEvidenceHandler{}
	next, err := connector.FetchStream(context.Background(), config, legacy, h)
	require.NoError(t, err)
	require.Empty(t, h.items)
	require.Equal(t, 1, decodeCursor(next).Items["file"].MissingCount)
	require.Equal(t, deletionEvidenceVersion, decodeCursor(next).DeletionEvidenceVersion)
}

func TestReadCLIDownloadPayloadUsesResponseSidecarWhenAttachmentIsAbsent(t *testing.T) {
	dir := t.TempDir()
	response := map[string]interface{}{
		"file_content": map[string]interface{}{"content": "# discount\n", "file_name": "discount.md"},
		"size":         len("# discount\n"),
	}
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	sidecar := filepath.Join(dir, "response.json")
	require.NoError(t, os.WriteFile(sidecar, encoded, 0o600))

	content, name, err := readCLIDownloadPayload(dir, "discount.md", map[string]interface{}{
		// The CLI response can point at a process-local sidecar path even
		// though it also wrote a safe copy into --output-dir.
		"file_path": filepath.Join(t.TempDir(), "response.json"),
		"size":      len("# discount\n"),
	})
	require.NoError(t, err)
	require.Equal(t, "discount.md", name)
	require.Equal(t, "# discount\n", string(content))
}

func TestReadCLIDownloadPayloadDecodesBase64Sidecar(t *testing.T) {
	dir := t.TempDir()
	want := []byte{0, 1, 2, 255}
	response := map[string]interface{}{
		"file_content": map[string]interface{}{"content": base64.StdEncoding.EncodeToString(want)},
		"size":         len(want),
	}
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "response.json"), encoded, 0o600))

	content, name, err := readCLIDownloadPayload(dir, "attachment.bin", map[string]interface{}{})
	require.NoError(t, err)
	require.Equal(t, "attachment.bin", name)
	require.Equal(t, want, content)
}

func TestReadCLIDownloadPayloadRejectsOutsideFilePathWithoutSidecar(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "response.json")
	require.NoError(t, os.WriteFile(outside, []byte(`{"file_content":"private"}`), 0o600))
	_, _, err := readCLIDownloadPayload(t.TempDir(), "attachment.md", map[string]interface{}{"file_path": outside})
	require.ErrorContains(t, err, "outside its temporary directory")
}

func TestCSVMarkdown(t *testing.T) {
	require.Equal(t, "| name | note |\n| --- | --- |\n| Alice | a\\|b |", csvMarkdown("name,note\nAlice,a|b\n"))
	require.Empty(t, csvMarkdown("\"unterminated"))
}

func TestRedactShareURL(t *testing.T) {
	value := "download https://drive.weixin.qq.com/s?k=secret failed"
	redacted := redact(value)
	require.NotContains(t, redacted, "secret")
	require.Contains(t, redacted, "<redacted-drive-share>")
}

func TestShareURLValidation(t *testing.T) {
	require.True(t, shareURLPattern.MatchString("https://drive.weixin.qq.com/s?k=abc123"))
	require.False(t, shareURLPattern.MatchString("https://drive.weixin.qq.com/s?abc=123"))
	require.False(t, shareURLPattern.MatchString("http://drive.weixin.qq.com/s?abc=123"))
	require.False(t, shareURLPattern.MatchString("https://example.com/s?abc=123"))
}

func TestCredentialFingerprintAndProfileBoundary(t *testing.T) {
	credentials := types.WeComCLICredentials{BotID: "bot", Secret: "secret"}
	require.Equal(t, credentialFingerprint(credentials), credentialFingerprint(credentials))
	require.NotEqual(t, credentialFingerprint(credentials), credentialFingerprint(types.WeComCLICredentials{BotID: "bot", Secret: "rotated"}))

	runner := &cliRunner{command: "must-not-run", profileRoot: t.TempDir()}
	_, err := runner.ensureAuthenticated(context.Background(), "../other-tenant", credentials)
	require.ErrorContains(t, err, "invalid WeCom CLI profile id")
}

func TestCLIRunnerUsesServiceStorageWhenNoExplicitStateDir(t *testing.T) {
	storage := t.TempDir()
	t.Setenv("WECOM_CLI_STATE_DIR", "")
	t.Setenv("LOCAL_STORAGE_BASE_DIR", filepath.Join(storage, "files"))

	runner := newCLIRunner()

	require.Equal(t, filepath.Join(storage, "wecom-cli"), runner.profileRoot)
}

func TestControlledDownloadPathUsesOnlyExpectedRegularFile(t *testing.T) {
	root := t.TempDir()
	expected := filepath.Join(root, "downloaded.pdf")
	require.NoError(t, os.WriteFile(expected, []byte("content"), 0o600))

	path, err := controlledDownloadPath(root, "downloaded.pdf")
	require.NoError(t, err)
	require.Equal(t, expected, path)

	_, err = controlledDownloadPath(root, "missing.pdf")
	require.Error(t, err)
	_, err = controlledDownloadPath(root, "../outside.pdf")
	require.Error(t, err)
}

func TestAttachKnowledgeFolderPreservesRelativeSourceHierarchy(t *testing.T) {
	fetched := types.FetchedItem{FileName: "内容导出.md", Metadata: map[string]string{"channel": "wecom_drive"}}
	item := types.WeDriveInventoryItem{Path: "EVIP/2.项目管理/1.项目设置/原始文档.docx", Name: "原始文档.docx"}

	attachKnowledgeFolder(&fetched, item)

	require.Equal(t, "业务文档/EVIP/2.项目管理/1.项目设置/内容导出.md", fetched.FileName)
	require.Equal(t, "业务文档/EVIP/2.项目管理/1.项目设置", fetched.Metadata["folder_path"])
	require.Equal(t, item.Path, fetched.Metadata["source_path"])
}

func TestSourceFolderPathPutsRootFilesUnderBusinessDocuments(t *testing.T) {
	require.Equal(t, "业务文档", sourceFolderPath("根目录文件.pdf"))
	require.Equal(t, "业务文档", sourceFolderPath(""))
}

func TestCountDeferredConsumableFilesExcludesFoldersUnchangedAndUnsupported(t *testing.T) {
	items := []types.WeDriveInventoryItem{
		{ItemType: "folder"},
		{ItemType: "file", ExternalID: "unchanged", Consumability: "supported"},
		{ItemType: "file", ExternalID: "needs-fetch", Consumability: "supported"},
		{ItemType: "file", ExternalID: "unsupported", Consumability: "unsupported"},
	}
	prev := cursorState{Items: map[string]itemState{
		"unchanged": {Fingerprint: inventoryFingerprint(items[1])},
	}}

	require.Equal(t, 1, countDeferredConsumableFiles(items, prev))
}

func TestUnsupportedFormatCountsIncludesAllInventoryFiles(t *testing.T) {
	counts := unsupportedFormatCounts([]types.WeDriveInventoryItem{
		{ItemType: "folder", Name: "folder", Consumability: "unsupported"},
		{ItemType: "file", Name: "a.sql", Consumability: "unsupported"},
		{ItemType: "file", Name: "b.SQL", Consumability: "unsupported"},
		{ItemType: "file", Name: "c.pdf", Consumability: "supported"},
	})

	require.Equal(t, map[string]int{".sql": 2}, counts)
}

func TestPublicFetchErrorDoesNotExposeInventoryIDOrLocalPath(t *testing.T) {
	err := errors.New(`create WeCom CLI profile: mkdir C:\Users\28211\AppData\Roaming\weknora\wecom-cli: Access is denied`)

	message := publicFetchError(err)

	require.Equal(t, "WeCom CLI credential storage is unavailable", message)
	require.NotContains(t, message, `C:\`)
	require.NotContains(t, message, "28211")
}

func TestPublicFetchErrorClassifiesSafeCLIProblems(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "online document membership",
			err:  errors.New("wecom-cli error code=640008"),
			want: "Some online documents are not accessible to the configured WeCom CLI identity",
		},
		{
			name: "offline share cannot download",
			err:  errors.New("wecom-cli error code=640027"),
			want: "Some offline file share links cannot be downloaded by the configured WeCom CLI identity",
		},
		{
			name: "rate limited",
			err:  errors.New("wecom-cli rate limit persisted after retries: wecom-cli error code=850005"),
			want: "WeCom CLI rate limit was reached; retry later",
		},
		{
			name: "daily content quota",
			err:  errors.New("wecom-cli error code=640459"),
			want: "The WeCom CLI bot daily file-content retrieval quota has been reached; retry after the quota resets",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, publicFetchError(tt.err))
		})
	}
}

func TestDailyContentQuotaStopsFurtherCLIAttempts(t *testing.T) {
	require.True(t, isDailyContentQuotaError(&cliAPIError{Code: "640459"}))
	require.False(t, isDailyContentQuotaError(&cliAPIError{Code: "640027"}))
}

func TestPublicItemFetchErrorClassifiesGenericFailuresWithoutResourceData(t *testing.T) {
	err := errors.New("wecom-cli failed: response omitted")
	online := publicItemFetchError(types.WeDriveInventoryItem{DocID: "w3_example"}, err)
	offline := publicItemFetchError(types.WeDriveInventoryItem{}, err)

	require.Equal(t, "Some online documents could not be exported by the configured WeCom CLI identity", online)
	require.Equal(t, "Some offline files with a share link could not be downloaded by the configured WeCom CLI identity", offline)
	for _, message := range []string{online, offline} {
		require.NotContains(t, message, "response omitted")
		require.NotContains(t, message, "C:\\")
	}
}

func TestPublicItemFetchErrorClassifiesAgentShareFailureReason(t *testing.T) {
	err := errors.New("offline file has no valid share link")
	message := publicItemFetchError(types.WeDriveInventoryItem{FailureCode: "auto_share_link_not_created"}, err)

	require.Equal(t, "Agent could not create some share links; check the source user's share permission and tenant sharing policy", message)
	require.NotContains(t, message, "offline file")
}

func TestCLIErrorFromOutputExtractsOnlyAPIErrorCode(t *testing.T) {
	err := cliErrorFromOutput(`request failed: errcode=640008; details omitted`)
	var apiErr *cliAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "640008", apiErr.Code)
	err = cliErrorFromOutput(`remote command failed with HTTP status 403`)
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "http_403", apiErr.Code)
	require.Nil(t, cliErrorFromOutput(`request failed without an API code`))
}

func TestCLIErrorFromOutputRecognizesDailyContentQuotaWithoutNumericCode(t *testing.T) {
	// Some wecom-cli releases return this localized message without an
	// errcode/code field. Treating it as an unclassified item failure causes
	// the caller to retry every remaining file instead of preserving its cursor.
	err := cliErrorFromOutput("当前用户通过机器人获取微盘文件内容已超过当日最大次数限制")

	var apiErr *cliAPIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, "640459", apiErr.Code)
	require.True(t, isDailyContentQuotaError(err))
}

func TestFetchErrorDetailsAggregatesCountsWithoutResourceData(t *testing.T) {
	details := fetchErrorDetails(map[string]int{
		"Some files could not be fetched from WeCom Drive":                              59,
		"Some offline files do not have a usable share link":                            19,
		"Some online documents are not accessible to the configured WeCom CLI identity": 13,
	})

	require.Equal(t, []string{
		"Some files could not be fetched from WeCom Drive (59 items)",
		"Some offline files do not have a usable share link (19 items)",
		"Some online documents are not accessible to the configured WeCom CLI identity (13 items)",
	}, details)
	for _, detail := range details {
		require.NotContains(t, detail, "drive.weixin.qq.com")
		require.NotContains(t, detail, "C:\\")
	}
}

func TestUnsupportedFormatDetailsAggregatesExtensionsWithoutResourceData(t *testing.T) {
	details := unsupportedFormatDetails(map[string]int{
		".sql": 28,
		".rp":  1,
	})

	require.Equal(t, "Unsupported file formats are not supported (.rp: 1 item; .sql: 28 items)", details)
	require.NotContains(t, details, "drive.weixin.qq.com")
	require.NotContains(t, details, "C:\\")
}
