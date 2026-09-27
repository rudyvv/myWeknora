package handler

import (
	"encoding/base64"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/wedrive"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Optional cross-language acceptance uses the actual Python client, signed
// HTTP handlers and SQLite. Set WEKNORA_WEDRIVE_TEST_PYTHON to a Python with
// the agent dependencies installed; no external microdisk account is needed.
func TestWeDrivePythonClientEndToEnd(t *testing.T) {
	python := os.Getenv("WEKNORA_WEDRIVE_TEST_PYTHON")
	if python == "" {
		t.Skip("set WEKNORA_WEDRIVE_TEST_PYTHON for cross-language release acceptance")
	}
	_, db, key := newWeDriveProtocolTest(t, "0.4.0")
	h := NewWeDriveHandler(service.NewWeDriveService(db, nil, nil), wedrive.NewHub())
	router := gin.New()
	agent := router.Group("/api/v1/wedrive/agent")
	agent.GET("/sources", h.ListAgentSources)
	agent.POST("/sources/:source_id/claim-scan", h.ClaimScan)
	agent.POST("/sources/:source_id/scan-lease", h.RenewScanLease)
	agent.POST("/sources/:source_id/scan-failure", h.ReportScanFailure)
	agent.POST("/snapshots", h.BeginSnapshot)
	agent.POST("/snapshots/:snapshot_id/items", h.UploadSnapshotItems)
	agent.POST("/snapshots/:snapshot_id/commit", h.CommitSnapshot)
	server := httptest.NewServer(router)
	defer server.Close()
	agentRoot, err := filepath.Abs(filepath.Join("..", "..", "agents", "wedrive-sync"))
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), python, filepath.Join(agentRoot, "tests", "protocol_acceptance.py"), server.URL)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+filepath.Join(agentRoot, "src"), "WEDRIVE_TEST_KEY="+base64.StdEncoding.EncodeToString(key.Seed()))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	t.Log(string(output))
	source, err := service.NewWeDriveService(db, nil, nil).GetSource(t.Context(), 7, "source")
	require.NoError(t, err)
	require.Equal(t, types.WeDriveScanStateFailed, source.ScanState)
	require.Empty(t, source.ScanAttemptID)
	require.NotEmpty(t, source.LastSnapshotID)
	require.NotNil(t, source.LastCompleteScanAt)
}
