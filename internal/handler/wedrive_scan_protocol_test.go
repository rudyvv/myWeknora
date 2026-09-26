package handler

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/wedrive"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newWeDriveProtocolTest(t *testing.T, recordedVersion string) (*gin.Engine, *gorm.DB, ed25519.PrivateKey) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "protocol.db")), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.WeDriveDevice{}, &types.WeDriveSource{}, &types.WeDriveSnapshot{}, &types.WeDriveInventoryItem{}))
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	require.NoError(t, db.Create(&types.WeDriveDevice{ID: "device", TenantID: 7, UserID: "owner", Name: "tool", AgentVersion: recordedVersion, PublicKey: base64.RawURLEncoding.EncodeToString(pub)}).Error)
	require.NoError(t, db.Create(&types.WeDriveSource{ID: "source", TenantID: 7, KnowledgeBaseID: "kb", DeviceID: "device", CreatedBy: "owner", Name: "root", RootURL: "https://drive.weixin.qq.com/root", Status: types.WeDriveSourceActive, ScanState: types.WeDriveScanStateIdle, ScanIntervalMinutes: 0}).Error)
	h := NewWeDriveHandler(service.NewWeDriveService(db, nil, nil), wedrive.NewHub())
	router := gin.New()
	router.GET("/sources", h.ListAgentSources)
	router.POST("/sources/:source_id/claim-scan", h.ClaimScan)
	router.POST("/sources/:source_id/scan-failure", h.ReportScanFailure)
	router.POST("/snapshots", h.BeginSnapshot)
	router.POST("/snapshots/:snapshot_id/items", h.UploadSnapshotItems)
	router.POST("/snapshots/:snapshot_id/commit", h.CommitSnapshot)
	return router, db, key
}

func signedWeDriveProtocolRequest(router *gin.Engine, key ed25519.PrivateKey, method, path, version string, body []byte) *httptest.ResponseRecorder {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	digest := sha256.Sum256(body)
	signature := ed25519.Sign(key, []byte(method+"\n"+path+"\n"+timestamp+"\n"+hex.EncodeToString(digest[:])))
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("X-WeDrive-Device-ID", "device")
	req.Header.Set("X-WeDrive-Timestamp", timestamp)
	req.Header.Set("X-WeDrive-Signature", base64.RawURLEncoding.EncodeToString(signature))
	req.Header.Set("X-WeDrive-Agent-Version", version)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestWeDriveOldToolCannotWriteUsingRecordedNewVersion(t *testing.T) {
	router, db, key := newWeDriveProtocolTest(t, "1.0.0")
	for _, version := range []string{"", "0.3.1", "0.3.99", "0.4.invalid", "0.4.0-beta"} {
		for _, path := range []string{"/sources/source/claim-scan", "/sources/source/scan-failure", "/snapshots", "/snapshots/old/items", "/snapshots/old/commit"} {
			w := signedWeDriveProtocolRequest(router, key, "POST", path, version, []byte(`{"trigger":"manual","code":"old_failure"}`))
			require.Equal(t, http.StatusUpgradeRequired, w.Code, "version=%q path=%s: %s", version, path, w.Body.String())
			var response map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
			require.Equal(t, "agent_upgrade_required", response["code"])
			require.Equal(t, "0.4.0", response["minimum_agent_version"])
			device, err := service.NewWeDriveService(db, nil, nil).GetAuthorizedDevice(t.Context(), 7, "owner", "device", false)
			require.NoError(t, err)
			require.Equal(t, version, device.AgentVersion, "the UI must see the actual requesting tool version, including a missing header")
		}
		w := signedWeDriveProtocolRequest(router, key, "GET", "/sources", version, nil)
		require.Equal(t, http.StatusOK, w.Code)
		require.JSONEq(t, `[]`, w.Body.String())
	}
	svc := service.NewWeDriveService(db, nil, nil)
	source, err := svc.GetSource(t.Context(), 7, "source")
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateIdle, source.ScanState)
	require.Nil(t, source.ScanLeaseExpiresAt)
}

func TestWeDriveNewToolClaimsAndReportsItsAttempt(t *testing.T) {
	for _, version := range []string{"0.4.0", "v0.4.0", "0.4.1", "1.0.0"} {
		router, db, key := newWeDriveProtocolTest(t, "0.3.1")
		w := signedWeDriveProtocolRequest(router, key, "POST", "/sources/source/claim-scan", version, []byte(`{"trigger":"manual"}`))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var claimed types.WeDriveSource
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &claimed))
		require.NotEmpty(t, claimed.ScanAttemptID)
		require.NotNil(t, claimed.ScanLeaseExpiresAt)
		for _, attempt := range []string{"", "old-attempt"} {
			body, err := json.Marshal(service.ReportWeDriveScanFailureInput{ScanAttemptID: attempt, Code: "stale_failure"})
			require.NoError(t, err)
			w = signedWeDriveProtocolRequest(router, key, "POST", "/sources/source/scan-failure", version, body)
			require.Equal(t, http.StatusConflict, w.Code)
			require.Contains(t, w.Body.String(), "scan_attempt_conflict")
		}
		body, err := json.Marshal(service.ReportWeDriveScanFailureInput{ScanAttemptID: claimed.ScanAttemptID, Code: "listing_timeout"})
		require.NoError(t, err)
		w = signedWeDriveProtocolRequest(router, key, "POST", "/sources/source/scan-failure", version, body)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		source, err := service.NewWeDriveService(db, nil, nil).GetSource(t.Context(), 7, "source")
		require.NoError(t, err)
		require.Equal(t, types.WeDriveScanStateFailed, source.ScanState)
		require.Empty(t, source.ScanAttemptID)
		require.Nil(t, source.ScanLeaseExpiresAt)
	}
}

func TestWeDriveSignedSnapshotProtocolBindsInventoryToClaim(t *testing.T) {
	router, db, key := newWeDriveProtocolTest(t, "0.4.0")
	post := func(path string, input any, status int) *httptest.ResponseRecorder {
		t.Helper()
		body, err := json.Marshal(input)
		require.NoError(t, err)
		w := signedWeDriveProtocolRequest(router, key, "POST", path, "0.4.0", body)
		require.Equal(t, status, w.Code, "%s: %s", path, w.Body.String())
		return w
	}
	claim := func() types.WeDriveSource {
		t.Helper()
		w := post("/sources/source/claim-scan", service.ClaimWeDriveScanInput{Trigger: "manual"}, http.StatusOK)
		var source types.WeDriveSource
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &source))
		return source
	}
	first := claim()
	begin := service.BeginSnapshotInput{SourceID: "source", Sequence: 1, RootExternalID: "root", ExpectedItemCount: 1}
	w := post("/snapshots", begin, http.StatusConflict)
	require.Contains(t, w.Body.String(), "scan_attempt_conflict")
	begin.ScanAttemptID = first.ScanAttemptID
	w = post("/snapshots", begin, http.StatusCreated)
	var snapshot types.WeDriveSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &snapshot))
	require.Equal(t, first.ScanAttemptID, snapshot.ScanAttemptID)
	begin.Sequence = 2
	w = post("/snapshots", begin, http.StatusCreated)
	var retry types.WeDriveSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &retry))
	require.Equal(t, snapshot.ID, retry.ID)
	begin.ExpectedItemCount = 2
	post("/snapshots", begin, http.StatusConflict)
	items := map[string]any{"items": []types.WeDriveInventoryItem{{ExternalID: "root", Name: "root", Path: ".", ItemType: "folder"}}}
	post("/snapshots/"+snapshot.ID+"/items", items, http.StatusNoContent)
	commit := service.CommitSnapshotInput{ItemCount: 1}
	w = post("/snapshots/"+snapshot.ID+"/commit", commit, http.StatusOK)
	var complete types.WeDriveSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &complete))
	second := claim()
	begin.ScanAttemptID, begin.Sequence, begin.ExpectedItemCount = second.ScanAttemptID, 3, 1
	w = post("/snapshots", begin, http.StatusCreated)
	var interrupted types.WeDriveSnapshot
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &interrupted))
	post("/sources/source/scan-failure", service.ReportWeDriveScanFailureInput{ScanAttemptID: second.ScanAttemptID, Code: "tree_walk_failed"}, http.StatusOK)
	third := claim()
	for _, path := range []string{"/snapshots/" + interrupted.ID + "/items", "/snapshots/" + interrupted.ID + "/commit"} {
		input := any(commit)
		if path == "/snapshots/"+interrupted.ID+"/items" {
			input = items
		}
		w = post(path, input, http.StatusConflict)
		require.Contains(t, w.Body.String(), "scan_attempt_conflict")
	}
	w = post("/snapshots/"+snapshot.ID+"/commit", commit, http.StatusOK)
	require.JSONEq(t, string(mustWeDriveProtocolJSON(t, complete)), w.Body.String())
	post("/snapshots/"+snapshot.ID+"/commit", service.CommitSnapshotInput{ItemCount: 1, AutoShareCreated: 1}, http.StatusConflict)
	saved, err := service.NewWeDriveService(db, nil, nil).GetSource(t.Context(), 7, "source")
	require.NoError(t, err)
	require.Equal(t, third.ScanAttemptID, saved.ScanAttemptID)
	require.Equal(t, snapshot.ID, saved.LastSnapshotID)
	require.Equal(t, types.WeDriveScanStateRunning, saved.ScanState)
}

func mustWeDriveProtocolJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	require.NoError(t, err)
	return body
}
