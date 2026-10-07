//go:build integration

package service_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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

// postSourceManualSync submits the public manual-sync API with an optional
// expected_commit_sha condition and returns the raw HTTP response.
func postSourceManualSync(t *testing.T, f *service.SourceIntegrationFixture, expected string) *httptest.ResponseRecorder {
	t.Helper()
	h := handler.NewDataSourceHandler(f.DataSources, f.KBs)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(types.TenantIDContextKey.String(), uint64(1)) })
	router.POST("/datasource/:id/sync", h.ManualSync)
	body := ""
	if expected != "" {
		body = `{"expected_commit_sha":"` + expected + `"}`
	}
	request := httptest.NewRequest(http.MethodPost, "/datasource/"+f.Source.ID+"/sync", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request.WithContext(f.Ctx))
	return response
}

func sourceSyncPayload(t *testing.T, f *service.SourceIntegrationFixture, logID string, trigger string, deliveryGeneration int64) []byte {
	t.Helper()
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: f.Source.ID, TenantID: 1, SyncLogID: logID, Trigger: trigger, DeliveryGeneration: deliveryGeneration,
	})
	require.NoError(t, err)
	return payload
}

func TestSourceManualSyncRejectsStaleExpectedCommitWithoutEnqueue(t *testing.T) {
	f := service.NewSourceIntegrationFixture(t)
	preview, err := f.DataSources.PreviewSource(f.Ctx, f.Source.ID, nil)
	require.NoError(t, err)
	expected := preview.CommitSHA
	advanced := f.AdvanceFiles(map[string][]byte{"src/Service.java": []byte("package demo;\npublic class Service { public String movedOn() { return \"moved\"; } }\n")})
	require.NotEqual(t, expected, advanced)

	response := postSourceManualSync(t, f, expected)
	require.Equal(t, http.StatusBadRequest, response.Code, "a preview condition that no longer matches the branch HEAD must be rejected before registration")
	require.Contains(t, response.Body.String(), "source branch changed since preview")

	logs, err := f.DataSources.GetSyncLogs(f.Ctx, f.Source.ID, 50, 0)
	require.NoError(t, err)
	require.Empty(t, logs, "a rejected expected-commit request must not create a sync log")
	var runs int64
	require.NoError(t, f.DB.Table("source_sync_runs").Where("data_source_id=?", f.Source.ID).Count(&runs).Error)
	require.Zero(t, runs, "a rejected expected-commit request must not register a durable source run")
}

func TestSourceManualSyncExpectedCommitFailureKeepsPreviousPublicationWhenTargetVanishes(t *testing.T) {
	f := service.NewSourceIntegrationFixture(t)
	f.Sync()
	var previous struct{ SnapshotID string }
	require.NoError(t, f.DB.Table("source_publications").Select("snapshot_id").
		Where("data_source_id=?", f.Source.ID).Take(&previous).Error)

	preview, err := f.DataSources.PreviewSource(f.Ctx, f.Source.ID, nil)
	require.NoError(t, err)
	expected := preview.CommitSHA
	response := postSourceManualSync(t, f, expected)
	require.Equal(t, http.StatusOK, response.Code, "manual API must accept the current preview")
	var log types.SyncLog
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &log))

	forced := f.ForcePush()
	require.NotEqual(t, expected, forced)

	require.ErrorContains(t, f.DataSources.ProcessSync(f.Ctx, asynq.NewTask(types.TypeDataSourceSync,
		sourceSyncPayload(t, f, log.ID, "manual", 0))),
		"unable to fetch the fixed GitLab commit",
		"an unreachable fixed target must fail explicitly instead of falling back to the new branch HEAD")

	finished, err := f.DataSources.GetSyncLog(f.Ctx, log.ID)
	require.NoError(t, err)
	require.NotEqual(t, types.SyncLogStatusSuccess, finished.Status, "the vanished fixed target must never be acknowledged as published")
	require.Contains(t, finished.ErrorMessage, "unable to fetch the fixed GitLab commit")

	var current struct{ SnapshotID string }
	require.NoError(t, f.DB.Table("source_publications").Select("snapshot_id").
		Where("data_source_id=?", f.Source.ID).Take(&current).Error)
	require.Equal(t, previous.SnapshotID, current.SnapshotID,
		"the previous publication must remain the readable version after the fixed-target run failed")
	var published int64
	require.NoError(t, f.DB.Table("source_publications").Where("data_source_id=?", f.Source.ID).Count(&published).Error)
	require.EqualValues(t, 1, published, "no additional publication may appear for the failed fixed-target run")
}

func TestSourceManualSyncExpectedCommitSurvivesDurableRestartRecovery(t *testing.T) {
	f := service.NewSourceIntegrationFixture(t)
	preview, err := f.DataSources.PreviewSource(f.Ctx, f.Source.ID, nil)
	require.NoError(t, err)
	expected := preview.CommitSHA
	response := postSourceManualSync(t, f, expected)
	require.Equal(t, http.StatusOK, response.Code)
	var log types.SyncLog
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &log))

	advanced := f.AdvanceFiles(map[string][]byte{"src/Service.java": []byte("package demo;\npublic class Service { public String afterCrash() { return \"crashed\"; } }\n")})
	require.NotEqual(t, expected, advanced)

	// A crash between registration and worker delivery leaves only the durable
	// run; restart reconciliation must redispatch it with its fixed target.
	ds, err := f.DataSources.GetDataSource(f.Ctx, f.Source.ID)
	require.NoError(t, err)
	control := repository.NewSyncLogRepository(f.DB).(interfaces.SourceSyncControlRepository)
	dispatches, err := control.RecoverSourceTriggers(f.Ctx, ds)
	require.NoError(t, err)
	require.Len(t, dispatches, 1, "restart recovery must redispatch the registered expected-commit run")
	require.Equal(t, log.ID, dispatches[0].SyncLog.ID)
	require.Equal(t, "manual", dispatches[0].Trigger)

	require.NoError(t, f.DataSources.ProcessSync(f.Ctx, asynq.NewTask(types.TypeDataSourceSync,
		sourceSyncPayload(t, f, log.ID, dispatches[0].Trigger, dispatches[0].DeliveryGeneration))))
	finished, err := f.DataSources.GetSyncLog(f.Ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)
	result, err := finished.ParseResult()
	require.NoError(t, err)
	require.Equal(t, expected, result.Source.Snapshot.CommitSHA,
		"recovered delivery must publish the registered fixed target, not the post-crash branch HEAD")
	require.True(t, result.Source.Snapshot.ManifestComplete)
}

func TestSourceManualSyncExpectedCommitIsFencedByLaterConfigChange(t *testing.T) {
	f := service.NewSourceIntegrationFixture(t)
	preview, err := f.DataSources.PreviewSource(f.Ctx, f.Source.ID, nil)
	require.NoError(t, err)
	expected := preview.CommitSHA
	response := postSourceManualSync(t, f, expected)
	require.Equal(t, http.StatusOK, response.Code)
	var queuedLog types.SyncLog
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &queuedLog))

	advanced := f.AdvanceFiles(map[string][]byte{"src/Service.java": []byte("package demo;\npublic class Service { public String afterConfig() { return \"configured\"; } }\n")})
	require.NotEqual(t, expected, advanced)

	// A configuration change while the expected-commit run is still queued must
	// fence that run through the real update seam: the durable trigger is
	// canceled and a later plain trigger registers under the new config.
	ds, err := f.DataSources.GetDataSource(f.Ctx, f.Source.ID)
	require.NoError(t, err)
	var config struct {
		Type        string          `json:"type"`
		Credentials json.RawMessage `json:"credentials"`
		Settings    json.RawMessage `json:"settings"`
	}
	require.NoError(t, json.Unmarshal(ds.Config, &config))
	var settings map[string]any
	require.NoError(t, json.Unmarshal(config.Settings, &settings))
	settings["exclude_paths"] = []string{"fence-only-no-files"}
	updated, err := json.Marshal(map[string]any{"type": config.Type, "credentials": config.Credentials, "settings": settings})
	require.NoError(t, err)
	ds.Config = updated
	_, err = f.DataSources.UpdateDataSource(f.Ctx, ds)
	require.NoError(t, err, "the public update seam must accept a configuration change while a trigger is queued")

	replacementResponse := postSourceManualSync(t, f, "")
	require.Equal(t, http.StatusOK, replacementResponse.Code, "a plain manual trigger must still be accepted under the changed configuration")
	var replacementLog types.SyncLog
	require.NoError(t, json.Unmarshal(replacementResponse.Body.Bytes(), &replacementLog))

	// The superseded expected-commit delivery arrives late: it must not publish.
	require.NoError(t, f.DataSources.ProcessSync(f.Ctx, asynq.NewTask(types.TypeDataSourceSync,
		sourceSyncPayload(t, f, queuedLog.ID, "manual", 0))))
	fenced, err := f.DataSources.GetSyncLog(f.Ctx, queuedLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, fenced.Status,
		"a queued expected-commit run must be fenced off by a configuration change")
	require.Contains(t, fenced.ErrorMessage, "source configuration changed")
	var phase string
	require.NoError(t, f.DB.Table("source_sync_runs").Select("phase").
		Where("sync_log_id=?", queuedLog.ID).Scan(&phase).Error)
	require.Equal(t, "canceled", phase)

	require.NoError(t, f.DataSources.ProcessSync(f.Ctx, asynq.NewTask(types.TypeDataSourceSync,
		sourceSyncPayload(t, f, replacementLog.ID, "manual", 0))))
	published, err := f.DataSources.GetSyncLog(f.Ctx, replacementLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, published.Status)
	result, err := published.ParseResult()
	require.NoError(t, err)
	require.Equal(t, advanced, result.Source.Snapshot.CommitSHA,
		"only the newer trigger under the new configuration may publish the branch HEAD")
	var publishedSHA string
	require.NoError(t, f.DB.Table("source_publications sp").
		Select("snapshots.commit_sha").
		Joins("JOIN source_snapshots snapshots ON snapshots.id=sp.snapshot_id").
		Where("sp.data_source_id=?", f.Source.ID).Scan(&publishedSHA).Error)
	require.Equal(t, advanced, publishedSHA)
}
