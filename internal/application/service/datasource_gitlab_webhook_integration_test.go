//go:build integration

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/handler/gitlabwebhook"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestGitLabPushHookHTTPDurableTriggerDedupAndScheduledReconciliation(t *testing.T) {
	f := newJavaSourceFixture(t)
	tasks := make(chan *asynq.Task, 4)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: tasks}

	gin.SetMode(gin.TestMode)
	api := gin.New()
	api.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Next()
	})
	admin := gitlabwebhook.NewAdmin(f.service, f.service.kbService, f.service)
	api.PUT("/api/v1/datasource/:id/gitlab-webhook", admin.UpdateConfig)
	api.GET("/api/v1/datasource/:id/gitlab-webhook", admin.GetConfig)
	api.POST("/api/v1/datasource/:id/gitlab-webhook/test", admin.TestConnection)
	api.POST(types.GitLabWebhookCallbackPath, gitlabwebhook.New(f.service).ReceivePush)
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	client := server.Client()

	secret := "fixture-gitlab-hook-secret-32-bytes"
	configureBody, err := json.Marshal(map[string]any{"enabled": true, "secret": secret})
	require.NoError(t, err)
	configureRequest, err := http.NewRequest(http.MethodPut, server.URL+"/api/v1/datasource/"+f.ds.ID+"/gitlab-webhook", bytes.NewReader(configureBody))
	require.NoError(t, err)
	configureRequest.Header.Set("Content-Type", "application/json")
	configureResponse, err := client.Do(configureRequest)
	require.NoError(t, err)
	var status types.GitLabWebhookStatus
	require.NoError(t, json.NewDecoder(configureResponse.Body).Decode(&status))
	configureResponse.Body.Close()
	require.Equal(t, http.StatusOK, configureResponse.StatusCode)
	require.True(t, status.Enabled)
	require.True(t, status.Configured)
	require.Equal(t, types.GitLabWebhookCallbackPath, status.CallbackPath)
	encodedStatus, err := json.Marshal(status)
	require.NoError(t, err)
	require.NotContains(t, string(encodedStatus), secret)

	testRequest, err := client.Post(server.URL+"/api/v1/datasource/"+f.ds.ID+"/gitlab-webhook/test", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	var testResult types.GitLabWebhookTestResult
	require.NoError(t, json.NewDecoder(testRequest.Body).Decode(&testResult))
	testRequest.Body.Close()
	require.Equal(t, http.StatusOK, testRequest.StatusCode)
	require.NoError(t, err)
	require.Equal(t, "connected", testResult.GitLabAccessStatus, "test must read the registered project and branch")
	require.Equal(t, "unverified", testResult.InboundStatus, "outbound GitLab access is not proof of inbound delivery")
	require.Equal(t, "123", testResult.ProjectID)
	require.Equal(t, "main", testResult.Branch)
	require.Equal(t, "0 0 * * * *", testResult.SyncSchedule, "an empty source schedule defaults to hourly reconciliation")

	webhookRepo := f.service.syncLogRepo.(interfaces.GitLabWebhookRepository)
	alternateSources := []*types.DataSource{}
	enabledAlternate := true
	for _, alternate := range []struct {
		name      string
		status    string
		projectID string
		ref       string
		cleared   bool
	}{
		{name: "paused", status: types.DataSourceStatusPaused, projectID: "123", ref: "main"},
		{name: "unbound", status: types.DataSourceStatusActive, projectID: "123", ref: "release"},
		{name: "cleared", status: types.DataSourceStatusActive, projectID: "123", ref: "main", cleared: true},
	} {
		credentials := map[string]any{"base_url": "http://gitlab.fixture.invalid", "access_token": "fixture-token"}
		if alternate.cleared {
			credentials = map[string]any{}
		}
		config, marshalErr := json.Marshal(map[string]any{
			"type": types.ConnectorTypeGitLab, "credentials": credentials,
			"settings": map[string]any{"content_mode": "source", "projects": []any{map[string]any{"project_id": alternate.projectID, "ref": alternate.ref, "paths": []string{"src"}}}},
		})
		require.NoError(t, marshalErr)
		ds := &types.DataSource{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "alternate " + alternate.name,
			Type: types.ConnectorTypeGitLab, Status: alternate.status, Config: types.JSON(config)}
		require.NoError(t, repository.NewDataSourceRepository(f.db).Create(f.ctx, ds))
		require.NoError(t, webhookRepo.SetGitLabWebhookConfig(f.ctx, ds.ID, ds.TenantID,
			types.GitLabWebhookUpdate{Enabled: &enabledAlternate, Secret: &secret}))
		alternateSources = append(alternateSources, ds)
	}

	pushBody := `{"object_kind":"push","project":{"id":123,"path_with_namespace":"group/repo"},"project_id":999,"ref":"refs/heads/main","before":"0000000000000000000000000000000000000000","after":"` + f.sha + `","tenant_id":999,"repository":{"url":"https://attacker.invalid/other.git"}}`
	postPush := func(token string) *http.Response {
		req, requestErr := http.NewRequest(http.MethodPost, server.URL+types.GitLabWebhookCallbackPath, strings.NewReader(pushBody))
		require.NoError(t, requestErr)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitlab-Event", "Push Hook")
		req.Header.Set("X-Gitlab-Token", token)
		req.Header.Set("webhook-id", "fixture-delivery-001")
		res, requestErr := client.Do(req)
		require.NoError(t, requestErr)
		return res
	}

	unauthorized := postPush("wrong-secret")
	unauthorized.Body.Close()
	require.Equal(t, http.StatusUnauthorized, unauthorized.StatusCode)
	var receiptCount, runCount int64
	require.NoError(t, f.db.Table("source_gitlab_webhook_deliveries").Where("data_source_id=?", f.ds.ID).Count(&receiptCount).Error)
	require.NoError(t, f.db.Table("source_sync_runs").Where("data_source_id=?", f.ds.ID).Count(&runCount).Error)
	require.Zero(t, receiptCount, "invalid authentication must not record a receipt")
	require.Zero(t, runCount, "invalid authentication must not register a trigger")

	accepted := postPush(secret)
	require.Equal(t, http.StatusAccepted, accepted.StatusCode)
	var acceptedBody map[string]any
	require.NoError(t, json.NewDecoder(accepted.Body).Decode(&acceptedBody))
	accepted.Body.Close()
	require.Equal(t, true, acceptedBody["accepted"])
	require.Equal(t, false, acceptedBody["duplicate"])
	for _, alternate := range alternateSources {
		var alternateReceipts, alternateRuns int64
		require.NoError(t, f.db.Table("source_gitlab_webhook_deliveries").Where("data_source_id=?", alternate.ID).Count(&alternateReceipts).Error)
		require.NoError(t, f.db.Table("source_sync_runs").Where("data_source_id=?", alternate.ID).Count(&alternateRuns).Error)
		require.Zero(t, alternateReceipts, "%s source must not receive the webhook", alternate.Name)
		require.Zero(t, alternateRuns, "%s source must not be revived by the webhook", alternate.Name)
	}

	var task *asynq.Task
	select {
	case task = <-tasks:
	case <-time.After(2 * time.Second):
		t.Fatal("authenticated Push Hook did not dispatch the durable source run")
	}
	var taskPayload types.DataSourceSyncPayload
	require.NoError(t, json.Unmarshal(task.Payload(), &taskPayload))
	require.Equal(t, f.ds.ID, taskPayload.DataSourceID, "source identity must come from registered config, not request JSON")
	require.Equal(t, uint64(1), taskPayload.TenantID, "tenant identity must come from the registered source")
	require.Equal(t, "gitlab_webhook", taskPayload.Trigger)
	require.NoError(t, f.service.ProcessSync(f.ctx, task), "the worker must fetch and publish its current GitLab HEAD")
	published, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, published.Snapshot)
	require.Equal(t, f.sha, published.Snapshot.CommitSHA, "the worker target must come from current GitLab HEAD, not the hook payload")

	duplicate := postPush(secret)
	require.Equal(t, http.StatusAccepted, duplicate.StatusCode)
	var duplicateBody map[string]any
	require.NoError(t, json.NewDecoder(duplicate.Body).Decode(&duplicateBody))
	duplicate.Body.Close()
	require.Equal(t, true, duplicateBody["duplicate"])
	require.NoError(t, f.db.Table("source_gitlab_webhook_deliveries").Where("data_source_id=?", f.ds.ID).Count(&receiptCount).Error)
	require.NoError(t, f.db.Table("source_sync_runs").Where("data_source_id=? AND trigger='gitlab_webhook'", f.ds.ID).Count(&runCount).Error)
	require.EqualValues(t, 1, receiptCount)
	require.EqualValues(t, 1, runCount, "delivery retries must not create another coordinator run")

	statusRequest, err := client.Get(server.URL + "/api/v1/datasource/" + f.ds.ID + "/gitlab-webhook")
	require.NoError(t, err)
	require.NoError(t, json.NewDecoder(statusRequest.Body).Decode(&status))
	statusRequest.Body.Close()
	require.Equal(t, http.StatusOK, statusRequest.StatusCode)
	require.NotNil(t, status.LastReceivedAt)
	require.Equal(t, "fixture-delivery-001", status.LastEventID)

	// A missed Push Hook is caught through the ordinary configurable schedule,
	// which enters the exact same source coordinator and queue path.
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id=?", f.ds.ID).Update("sync_schedule", "* * * * * *").Error)
	scheduledTasks := make(chan *asynq.Task, 1)
	scheduler := datasource.NewScheduler(
		repository.NewDataSourceRepository(f.db), repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{tasks: scheduledTasks}, f.service.sourceSnapshots,
	)
	require.NoError(t, scheduler.Start(context.Background()))
	defer scheduler.Stop()
	select {
	case scheduledTask := <-scheduledTasks:
		var scheduled types.DataSourceSyncPayload
		require.NoError(t, json.Unmarshal(scheduledTask.Payload(), &scheduled))
		require.Equal(t, "schedule", scheduled.Trigger)
		require.Equal(t, f.ds.ID, scheduled.DataSourceID)
		require.NoError(t, f.service.ProcessSync(f.ctx, scheduledTask), "periodic reconciliation must use the same source worker")
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled source reconciliation did not trigger without a webhook delivery")
	}
}

func failGitLabSourceAndExhaustRetryBudget(t *testing.T, f *javaSourceFixture, tasks <-chan *asynq.Task) {
	t.Helper()
	f.embedVector = []float32{0, 0, 0}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	var task *asynq.Task
	select {
	case task = <-tasks:
	case <-time.After(2 * time.Second):
		t.Fatal("manual source sync did not enqueue")
	}
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, task), "zero")
	ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusError, ds.Status)
	require.NoError(t, f.db.Table("source_sync_runs").Where("sync_log_id = ?", log.ID).Update("retry_count", 5).Error)
	dispatches, err := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository).RecoverSourceTriggers(f.ctx, ds)
	require.NoError(t, err)
	require.Empty(t, dispatches)
	f.embedVector = nil
}

func TestGitLabWebhookErrorSourceAcceptsPushAndStartupReconcilesLatestHead(t *testing.T) {
	f := newJavaSourceFixture(t)
	pushTasks := make(chan *asynq.Task, 8)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: pushTasks}
	enabled, secret := true, "fixture-error-source-hook-secret"
	_, err := f.service.ConfigureGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID,
		types.GitLabWebhookUpdate{Enabled: &enabled, Secret: &secret})
	require.NoError(t, err)
	failGitLabSourceAndExhaustRetryBudget(t, f, pushTasks)

	newHead := f.advanceJava("latest after the source recovered")
	gin.SetMode(gin.TestMode)
	api := gin.New()
	api.POST(types.GitLabWebhookCallbackPath, gitlabwebhook.New(f.service).ReceivePush)
	postPush := func(deliveryID string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"object_kind":"push","project":{"id":123},"ref":"refs/heads/main","before":"%s","after":"%s"}`,
			strings.Repeat("0", 40), newHead)
		request := httptest.NewRequest(http.MethodPost, types.GitLabWebhookCallbackPath, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gitlab-Event", "Push Hook")
		request.Header.Set("X-Gitlab-Token", secret)
		request.Header.Set("webhook-id", deliveryID)
		recorder := httptest.NewRecorder()
		api.ServeHTTP(recorder, request)
		return recorder
	}
	originalConfig := append(types.JSON(nil), f.ds.Config...)
	setConfig := func(mutate func(map[string]any)) {
		var config map[string]any
		require.NoError(t, json.Unmarshal(originalConfig, &config))
		mutate(config)
		encoded, marshalErr := json.Marshal(config)
		require.NoError(t, marshalErr)
		require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).
			Update("config", types.JSON(encoded)).Error)
	}
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusPaused).Error)
	require.Equal(t, http.StatusUnauthorized, postPush("error-source-paused").Code,
		"a paused source must not be reactivated by a delivery")
	pausedSource, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusPaused, pausedSource.Status)
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusError).Error)
	setConfig(func(config map[string]any) {
		settings := config["settings"].(map[string]any)
		projects := settings["projects"].([]any)
		projects[0].(map[string]any)["ref"] = "release"
	})
	require.Equal(t, http.StatusUnauthorized, postPush("error-source-unbound-branch").Code,
		"an unbound branch must not be selected by the push payload")
	setConfig(func(config map[string]any) {
		config["credentials"] = map[string]any{}
	})
	require.Equal(t, http.StatusUnauthorized, postPush("error-source-cleared-credentials").Code,
		"a source with cleared GitLab credentials must not be revived")
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).
		Update("config", originalConfig).Error)
	acceptedResponse := postPush("error-source-recovery-push")
	require.Equal(t, http.StatusAccepted, acceptedResponse.Code,
		"a valid delivery must remain eligible after the retry budget is exhausted")
	var acceptedBody map[string]any
	require.NoError(t, json.Unmarshal(acceptedResponse.Body.Bytes(), &acceptedBody))
	require.Equal(t, true, acceptedBody["accepted"])
	require.Equal(t, false, acceptedBody["duplicate"])

	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("sync_schedule", "* * * * * *").Error)
	scheduledTasks := make(chan *asynq.Task, 8)
	scheduler := datasource.NewScheduler(repository.NewDataSourceRepository(f.db), repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{tasks: scheduledTasks}, f.service.sourceSnapshots)
	require.NoError(t, scheduler.Start(context.Background()))
	defer scheduler.Stop()
	require.Equal(t, 1, scheduler.EntryCount(), "startup must register an eligible errored GitLab source")

	var scheduledTask *asynq.Task
	deadline := time.After(2200 * time.Millisecond)
	for scheduledTask == nil {
		select {
		case candidate := <-scheduledTasks:
			var payload types.DataSourceSyncPayload
			require.NoError(t, json.Unmarshal(candidate.Payload(), &payload))
			if payload.Trigger == "schedule" {
				scheduledTask = candidate
			}
		case <-deadline:
			t.Fatal("startup scheduler did not periodically reconcile an errored GitLab source")
		}
	}
	scheduler.Stop()
	var scheduled types.DataSourceSyncPayload
	require.NoError(t, json.Unmarshal(scheduledTask.Payload(), &scheduled))
	require.Equal(t, f.ds.ID, scheduled.DataSourceID)
	stillErrored, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusError, stillErrored.Status, "receiving and scheduling do not silently reactivate the source")
	require.NoError(t, f.service.ProcessSync(f.ctx, scheduledTask), "scheduled recovery must use the normal source worker")
	published, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, published.Snapshot)
	require.Equal(t, newHead, published.Snapshot.CommitSHA, "reconciliation must publish current HEAD, not trust the hook payload")
}

func TestGitLabWebhookRegisteredCronContinuesAfterSourceEntersError(t *testing.T) {
	f := newJavaSourceFixture(t)
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("sync_schedule", "* * * * * *").Error)
	scheduledTasks := make(chan *asynq.Task, 8)
	scheduler := datasource.NewScheduler(repository.NewDataSourceRepository(f.db), repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{tasks: scheduledTasks}, f.service.sourceSnapshots)
	require.NoError(t, scheduler.Start(context.Background()))
	defer scheduler.Stop()
	require.Equal(t, 1, scheduler.EntryCount())

	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id = ?", f.ds.ID).Update("status", types.DataSourceStatusError).Error)
	// Ignore a cron delivery that may have raced the status transition; the
	// following tick must still register a source run while status remains error.
	drainDeadline := time.After(1100 * time.Millisecond)
	for draining := true; draining; {
		select {
		case <-scheduledTasks:
		case <-drainDeadline:
			draining = false
		}
	}
	var scheduledTask *asynq.Task
	select {
	case scheduledTask = <-scheduledTasks:
	case <-time.After(2200 * time.Millisecond):
		t.Fatal("an already-registered source cron stopped firing after the source entered error")
	}
	var payload types.DataSourceSyncPayload
	require.NoError(t, json.Unmarshal(scheduledTask.Payload(), &payload))
	require.Equal(t, "schedule", payload.Trigger)
	stillErrored, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusError, stillErrored.Status)
	require.NoError(t, f.service.ProcessSync(f.ctx, scheduledTask))
	published, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, published.Snapshot)
	require.Equal(t, f.sha, published.Snapshot.CommitSHA)
}

func TestGitLabWebhookSecretRotationRequiresFreshInboundEvidence(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: make(chan *asynq.Task, 32)}
	enabled := true
	secretA, secretB := "fixture-rotation-secret-A", "fixture-rotation-secret-B"
	_, err := f.service.ConfigureGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID,
		types.GitLabWebhookUpdate{Enabled: &enabled, Secret: &secretA})
	require.NoError(t, err)
	event := types.GitLabPushEvent{
		ProjectID: "123", Ref: "refs/heads/main", Before: strings.Repeat("0", 40),
		After: f.sha, DeliveryID: "before-secret-rotation",
	}
	accepted, _, err := f.service.ReceiveGitLabPush(f.ctx, secretA, event)
	require.NoError(t, err)
	require.True(t, accepted)

	start := make(chan struct{})
	results := make(chan error, 8)
	var requests sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		requests.Add(1)
		go func(index int) {
			defer requests.Done()
			<-start
			concurrentEvent := event
			concurrentEvent.DeliveryID = fmt.Sprintf("old-secret-race-%d", index)
			accepted, _, receiveErr := f.service.ReceiveGitLabPush(f.ctx, secretA, concurrentEvent)
			if receiveErr != nil && !errors.Is(receiveErr, datasource.ErrGitLabWebhookUnauthorized) {
				results <- receiveErr
				return
			}
			if receiveErr == nil && !accepted {
				results <- errors.New("authenticated in-flight delivery was not accepted")
				return
			}
			results <- nil
		}(i)
	}
	rotation := make(chan error, 1)
	go func() {
		<-start
		_, rotateErr := f.service.ConfigureGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID, types.GitLabWebhookUpdate{Secret: &secretB})
		rotation <- rotateErr
	}()
	close(start)
	requests.Wait()
	close(results)
	for requestErr := range results {
		require.NoError(t, requestErr)
	}
	require.NoError(t, <-rotation)

	event.DeliveryID = "old-secret-after-rotation"
	accepted, _, err = f.service.ReceiveGitLabPush(f.ctx, secretA, event)
	require.ErrorIs(t, err, datasource.ErrGitLabWebhookUnauthorized)
	require.False(t, accepted)
	status, err := f.service.TestGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID)
	require.NoError(t, err)
	require.Equal(t, "unverified", status.InboundStatus, "an old receipt cannot verify the rotated secret")

	event.DeliveryID = "new-secret-before-clear"
	accepted, _, err = f.service.ReceiveGitLabPush(f.ctx, secretB, event)
	require.NoError(t, err)
	require.True(t, accepted)
	status, err = f.service.TestGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID)
	require.NoError(t, err)
	require.Equal(t, "verified", status.InboundStatus)

	_, err = f.service.ConfigureGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID, types.GitLabWebhookUpdate{ClearSecret: true})
	require.NoError(t, err)
	_, err = f.service.ConfigureGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID,
		types.GitLabWebhookUpdate{Enabled: &enabled, Secret: &secretB})
	require.NoError(t, err)
	status, err = f.service.TestGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID)
	require.NoError(t, err)
	require.Equal(t, "unverified", status.InboundStatus, "clearing and re-enabling cannot reuse old inbound evidence")
	event.DeliveryID = "new-secret-after-reenable"
	accepted, _, err = f.service.ReceiveGitLabPush(f.ctx, secretB, event)
	require.NoError(t, err)
	require.True(t, accepted)
	status, err = f.service.TestGitLabWebhook(f.ctx, f.ds.ID, f.ds.TenantID)
	require.NoError(t, err)
	require.Equal(t, "verified", status.InboundStatus, "only a receipt authenticated with the current secret verifies it")
}
