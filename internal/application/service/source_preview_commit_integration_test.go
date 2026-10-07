//go:build integration

package service_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceManualSyncPreviewCommitStaysFixedAfterBranchAdvances(t *testing.T) {
	t.Setenv("SOURCE_TEST_FIXED_ACCEPTANCE_CLONE", "source_t22_live_rehearsal_20261006")
	f := service.NewSourceIntegrationFixture(t)
	preview, err := f.DataSources.PreviewSource(f.Ctx, f.Source.ID, nil)
	require.NoError(t, err)
	expected := preview.CommitSHA
	h := handler.NewDataSourceHandler(f.DataSources, f.KBs)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(types.TenantIDContextKey.String(), uint64(1)) })
	router.POST("/datasource/:id/sync", h.ManualSync)
	request := httptest.NewRequest(http.MethodPost, "/datasource/"+f.Source.ID+"/sync", strings.NewReader(`{"expected_commit_sha":"`+expected+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request.WithContext(f.Ctx))
	require.Equal(t, http.StatusOK, response.Code, "manual API must accept the current preview")
	var log types.SyncLog
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &log))
	advanced := f.AdvanceFiles(map[string][]byte{"src/Service.java": []byte("package demo;\npublic class Service { public String newerHeadOnly() { return \"newer\"; } }\n")})
	require.NotEqual(t, expected, advanced)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.Source.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.DataSources.ProcessSync(f.Ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	finished, err := f.DataSources.GetSyncLog(f.Ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)
	result, err := finished.ParseResult()
	require.NoError(t, err)
	require.Equal(t, expected, result.Source.Snapshot.CommitSHA, "worker must publish the preview target, not the new branch HEAD")
	require.True(t, result.Source.Snapshot.ManifestComplete)
	// Redelivery must retain the same acknowledged publication.
	require.NoError(t, f.DataSources.ProcessSync(f.Ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	finished, err = f.DataSources.GetSyncLog(f.Ctx, log.ID)
	require.NoError(t, err)
	replay, err := finished.ParseResult()
	require.NoError(t, err)
	require.Equal(t, result.Source.Snapshot.ID, replay.Source.Snapshot.ID)
	require.Equal(t, expected, replay.Source.Snapshot.CommitSHA)
}
