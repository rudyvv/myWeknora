//go:build integration

package service

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/gitlab"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSourceFirstJavaSnapshotIsPublishedAndSearchable(t *testing.T) {
	f := newJavaSourceFixture(t)
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, log)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)
	results, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
		QueryText: "getPushSchedule", MatchCount: 10, SkipContextEnrichment: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, results)
	require.Contains(t, results[0].Content, "getPushSchedule")
	require.Equal(t, f.sha, results[0].Metadata["commit_sha"])
	var evidence struct {
		Source types.SourceEvidence `json:"source"`
	}
	require.NoError(t, json.Unmarshal(results[0].ChunkMetadata, &evidence))
	require.Equal(t, "src/Service.java", evidence.Source.Path)
	require.Equal(t, f.sha, evidence.Source.CommitSHA)
	require.Equal(t, types.SourceRange{StartByte: 0, EndByte: 140, StartLine: 1, EndLine: 8}, evidence.Source.Range)
	require.Contains(t, evidence.Source.GitLabURL, "/-/blob/"+f.sha+"/src/Service.java#L1-8")
	require.Contains(t, evidence.Source.Symbols, "Service.getPushSchedule")
	for _, params := range []types.SearchParams{
		{QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: true},
		{QueryText: "getPushSchedule", MatchCount: 10, DisableKeywordsMatch: true},
	} {
		hits, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, searchErr)
		require.NotEmpty(t, hits, "both real index routes must find the known Java method")
	}
}

func TestManualSourceSyncRemainsPendingWhenQueueEnqueueFails(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}

	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err, "the durable source trigger was accepted before queue delivery")
	require.NotNil(t, log)
	require.Equal(t, "queued", log.Status)

	stored, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, "queued", stored.Status, "a transient enqueue failure must leave a recoverable pending run")
	require.Empty(t, stored.ErrorMessage)
	requireSourceRunPhase(t, f, log.ID, "queued")
}

func TestSourceSchedulerRecoversPendingManualTriggerAfterRestart(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, log)
	require.Equal(t, types.SyncLogStatusQueued, log.Status)
	require.NoError(t, f.service.ResumeDataSource(f.ctx, f.ds.ID))

	recoveredTasks := make(chan *asynq.Task, 1)
	scheduler := datasource.NewScheduler(
		repository.NewDataSourceRepository(f.db),
		repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{tasks: recoveredTasks},
		f.service.sourceSnapshots,
	)
	require.NoError(t, scheduler.Start(f.ctx))
	defer scheduler.Stop()

	select {
	case task := <-recoveredTasks:
		var payload types.DataSourceSyncPayload
		require.NoError(t, json.Unmarshal(task.Payload(), &payload))
		require.Equal(t, log.ID, payload.SyncLogID, "restart recovery must redeliver the registered run")
		require.Equal(t, "manual", payload.Trigger)
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not redeliver the durable source trigger")
	}
}

func TestSourceSchedulerRepairsConfigFenceAfterDatasourceWriteCrash(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
	oldLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, oldLog.Status)

	control := repository.NewSyncLogRepository(f.db).(interfaces.SourceSyncControlRepository)
	newConfig := *f.ds
	var config map[string]any
	require.NoError(t, json.Unmarshal(newConfig.Config, &config))
	settings := config["settings"].(map[string]any)
	settings["crash_window_probe"] = true
	newConfig.Config, err = json.Marshal(config)
	require.NoError(t, err)
	// This is the committed pre-write fence in UpdateDataSource. The process
	// dies before the separate datasource repository transaction persists B.
	require.NoError(t, control.AdvanceSourceConfig(f.ctx, &newConfig, true))
	canceled, err := f.service.GetSyncLog(f.ctx, oldLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, canceled.Status)

	var before struct {
		ConfigGeneration int64   `gorm:"column:config_generation"`
		ActiveSyncLogID  *string `gorm:"column:active_sync_log_id"`
		PendingSyncLogID *string `gorm:"column:pending_sync_log_id"`
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&before).Error)
	require.Nil(t, before.ActiveSyncLogID)
	require.Nil(t, before.PendingSyncLogID)

	recoveredTasks := make(chan *asynq.Task, 1)
	scheduler := datasource.NewScheduler(
		repository.NewDataSourceRepository(f.db),
		repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{tasks: recoveredTasks},
		f.service.sourceSnapshots,
	)
	require.NoError(t, scheduler.Start(f.ctx))
	defer scheduler.Stop()
	select {
	case task := <-recoveredTasks:
		t.Fatalf("startup must not resurrect the canceled trigger: %s", task.Type())
	default:
	}

	// Startup reconciliation must restore coordinator state from persisted A,
	// allowing the next manual trigger to register under the current config.
	newLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err, "restart must repair a committed pre-write fence with no trigger pointers")
	require.NotEqual(t, oldLog.ID, newLog.ID)
	require.Equal(t, types.SyncLogStatusQueued, newLog.Status)
	var after struct {
		ConfigGeneration int64 `gorm:"column:config_generation"`
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&after).Error)
	require.Greater(t, after.ConfigGeneration, before.ConfigGeneration, "reconciliation must advance, never roll back, the generation")
}

func TestSourceSchedulerReconciliationPreservesDisabledFence(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
	oldLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)

	disabled := *f.ds
	var config map[string]any
	require.NoError(t, json.Unmarshal(disabled.Config, &config))
	config["settings"].(map[string]any)["content_mode"] = "document"
	disabled.Config, err = json.Marshal(config)
	require.NoError(t, err)
	control := repository.NewSyncLogRepository(f.db).(interfaces.SourceSyncControlRepository)
	require.NoError(t, control.AdvanceSourceConfig(f.ctx, &disabled, false))
	require.NoError(t, repository.NewDataSourceRepository(f.db).Update(f.ctx, &disabled))
	canceled, err := f.service.GetSyncLog(f.ctx, oldLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, canceled.Status)

	var before struct {
		ConfigGeneration  int64   `gorm:"column:config_generation"`
		ConfigFingerprint string  `gorm:"column:config_fingerprint"`
		ActiveSyncLogID   *string `gorm:"column:active_sync_log_id"`
		PendingSyncLogID  *string `gorm:"column:pending_sync_log_id"`
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&before).Error)
	require.True(t, strings.HasPrefix(before.ConfigFingerprint, "disabled:"))
	require.Nil(t, before.ActiveSyncLogID)
	require.Nil(t, before.PendingSyncLogID)

	dispatches, err := control.RecoverAllSourceTriggers(f.ctx)
	require.NoError(t, err)
	require.Empty(t, dispatches, "reconciling every coordinator row must not re-enable a document-mode datasource")
	var after struct {
		ConfigGeneration  int64  `gorm:"column:config_generation"`
		ConfigFingerprint string `gorm:"column:config_fingerprint"`
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&after).Error)
	require.Equal(t, before.ConfigGeneration, after.ConfigGeneration)
	require.Equal(t, before.ConfigFingerprint, after.ConfigFingerprint)
}

func TestSourceSchedulerReconciliationDoesNotReviveDeletedOrCredentialClearedSources(t *testing.T) {
	for _, scenario := range []string{"deleted source", "cleared credentials"} {
		t.Run(scenario, func(t *testing.T) {
			f := newJavaSourceFixture(t)
			f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
			log, err := f.service.ManualSync(f.ctx, f.ds.ID)
			require.NoError(t, err)
			require.Equal(t, types.SyncLogStatusQueued, log.Status)

			switch scenario {
			case "deleted source":
				require.NoError(t, f.service.DeleteDataSource(f.ctx, f.ds.ID))
			case "cleared credentials":
				require.NoError(t, f.service.ClearDataSourceCredentials(f.ctx, f.ds.ID))
			}
			canceled, err := f.service.GetSyncLog(f.ctx, log.ID)
			require.NoError(t, err)
			require.Equal(t, types.SyncLogStatusCanceled, canceled.Status)

			dispatches, err := repository.NewSyncLogRepository(f.db).(interfaces.SourceSyncControlRepository).
				RecoverAllSourceTriggers(f.ctx)
			require.NoError(t, err)
			require.Empty(t, dispatches, "startup reconciliation must preserve deletion and credential-clear fences")
		})
	}
}

func TestSourceSchedulerReconciliationSerializesWithConfigUpdate(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
	oldLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)

	updated := *f.ds
	var config map[string]any
	require.NoError(t, json.Unmarshal(updated.Config, &config))
	config["settings"].(map[string]any)["concurrent_reconcile_probe"] = true
	updated.Config, err = json.Marshal(config)
	require.NoError(t, err)
	control := repository.NewSyncLogRepository(f.db).(interfaces.SourceSyncControlRepository)
	// Leave the exact durable mismatch produced by the pre-write fence.
	require.NoError(t, control.AdvanceSourceConfig(f.ctx, &updated, true))
	canceled, err := f.service.GetSyncLog(f.ctx, oldLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, canceled.Status)

	ctx, cancel := context.WithTimeout(f.ctx, 15*time.Second)
	defer cancel()
	updateDone := make(chan error, 1)
	go func() {
		if updateErr := control.AdvanceSourceConfig(ctx, &updated, true); updateErr != nil {
			updateDone <- updateErr
			return
		}
		dsRepo := repository.NewDataSourceRepository(f.db)
		if updateErr := dsRepo.Update(ctx, &updated); updateErr != nil {
			updateDone <- updateErr
			return
		}
		updateDone <- control.AdvanceSourceConfig(ctx, &updated, true)
	}()
	recoveryDone := make(chan error, 1)
	go func() {
		_, recoveryErr := control.RecoverAllSourceTriggers(ctx)
		recoveryDone <- recoveryErr
	}()
	for i := 0; i < 2; i++ {
		select {
		case err := <-updateDone:
			require.NoError(t, err, "the config update must complete without a lock cycle")
			updateDone = nil
		case err := <-recoveryDone:
			require.NoError(t, err, "reconciliation must serialize with the config update")
			recoveryDone = nil
		case <-ctx.Done():
			t.Fatal("config update and reconciliation deadlocked")
		}
	}

	stored, err := f.service.GetDataSource(ctx, f.ds.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(updated.Config), string(stored.Config))
	newLog, err := f.service.ManualSync(ctx, f.ds.ID)
	require.NoError(t, err, "the persisted latest config must be registerable after both transactions finish")
	require.NotEqual(t, oldLog.ID, newLog.ID)
	require.Equal(t, types.SyncLogStatusQueued, newLog.Status)
}

func TestSourceManualTriggersSerializeAndCatchUpToLatestCommit(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.parseStarted = make(chan struct{}, 1)
	f.parseRelease = make(chan struct{}, 1)
	delivered := make(chan *asynq.Task, 4)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: delivered}

	firstLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, firstLog.Status)
	var firstTask *asynq.Task
	select {
	case firstTask = <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("initial source trigger was not enqueued")
	}
	firstDone := make(chan error, 1)
	go func() { firstDone <- f.service.ProcessSync(f.ctx, firstTask) }()
	select {
	case <-f.parseStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("first source run did not reach the controlled parse stage")
	}

	commitC := f.advanceJava("package demo; public class Service { int versionC() { return 3; } }\n")
	logC, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, logC.Status)
	commitD := f.advanceJava("package demo; public class Service { int versionD() { return 4; } }\n")
	logD, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, logD.Status)
	requireSourceRunPhase(t, f, logD.ID, "waiting_for_catch_up")
	coalesced, err := f.service.GetSyncLog(f.ctx, logC.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, coalesced.Status, "C should be replaced by the latest pending trigger D")

	f.parseRelease <- struct{}{}
	require.NoError(t, <-firstDone)
	var catchupTask *asynq.Task
	select {
	case catchupTask = <-delivered:
	case <-time.After(2 * time.Second):
		t.Fatal("latest pending trigger was not dispatched after B published")
	}
	f.parseStarted, f.parseRelease = nil, nil
	require.NoError(t, f.service.ProcessSync(f.ctx, catchupTask))

	publication, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, publication)
	require.Equal(t, commitD, publication.Snapshot.CommitSHA, "the catch-up run must publish D, not intermediate C")
	var intermediateCount int64
	require.NoError(t, f.db.Table("source_snapshots").Where("data_source_id=? AND commit_sha=? AND state='published'", f.ds.ID, commitC).Count(&intermediateCount).Error)
	require.Zero(t, intermediateCount, "coalesced commit C should not publish")
	var outboxCount int64
	require.NoError(t, f.db.Table("source_publication_outbox").Where("data_source_id=? AND event_type='source.wiki.update' AND status='pending'", f.ds.ID).Count(&outboxCount).Error)
	require.EqualValues(t, 2, outboxCount, "each committed publication must atomically leave a recoverable Wiki update signal")
}

func TestSourceStaleRunCannotOverwriteCanceledStatus(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)

	ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	control := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository)
	lease, claimed, err := control.ClaimSourceRun(f.ctx, ds, log.ID, 1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	var config map[string]any
	require.NoError(t, json.Unmarshal(ds.Config, &config))
	settings := config["settings"].(map[string]any)
	settings["fencing_test_config_change"] = true
	ds.Config, err = json.Marshal(config)
	require.NoError(t, err)
	require.NoError(t, control.AdvanceSourceConfig(f.ctx, ds, true))

	fenced, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, fenced.Status)
	require.ErrorIs(t, control.RecordSourceRunPhase(f.ctx, lease, "ready", ""), types.ErrSourceSyncLeaseLost)

	fenced.SourceConfigGeneration = lease.ConfigGeneration
	fenced.SourceFencingToken = lease.FencingToken
	fenced.Status = types.SyncLogStatusSuccess
	err = f.service.syncLogRepo.UpdateResult(f.ctx, fenced)
	after, readErr := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, readErr)
	require.ErrorIs(t, err, types.ErrSourceSyncLeaseLost)
	require.Equal(t, types.SyncLogStatusCanceled, after.Status)
}

func TestSourceConcurrentConfigCancellationFencesResultWrite(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	control := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository)
	lease, claimed, err := control.ClaimSourceRun(f.ctx, ds, log.ID, 1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	stale, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	stale.Status = types.SyncLogStatusSuccess
	stale.SourceConfigGeneration, stale.SourceFencingToken = lease.ConfigGeneration, lease.FencingToken

	cancelTx := f.db.Begin()
	require.NoError(t, cancelTx.Error)
	t.Cleanup(func() { _ = cancelTx.Rollback().Error })
	var cancelPID int
	require.NoError(t, cancelTx.Raw("SELECT pg_backend_pid()").Scan(&cancelPID).Error)
	require.NoError(t, repository.NewSyncLogRepository(cancelTx).(interfaces.SourceSyncControlRepository).
		AdvanceSourceConfig(f.ctx, ds, false))

	writeCtx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	writeDone := make(chan error, 1)
	go func() { writeDone <- f.service.syncLogRepo.UpdateResult(writeCtx, stale) }()
	blocked := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var count int64
		require.NoError(t, f.db.Raw("SELECT count(*) FROM pg_stat_activity WHERE ? = ANY(pg_blocking_pids(pid)) AND wait_event_type='Lock'", cancelPID).Scan(&count).Error)
		if count > 0 {
			blocked = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !blocked {
		_ = cancelTx.Rollback().Error
		t.Fatal("result update never reached the canceled log lock")
	}
	require.NoError(t, cancelTx.Commit().Error)
	writeErr := <-writeDone
	stored, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.ErrorIs(t, writeErr, types.ErrSourceSyncLeaseLost)
	require.Equal(t, types.SyncLogStatusCanceled, stored.Status)
}

func TestSourceRetryableFailureKeepsDurableRecoveryAndVisibleResult(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedVector = []float32{0, 0, 0}
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
	attempt := &types.SourceWikiAttempt{
		ID: uuid.NewString(), TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID,
		SourceID: f.ds.ID, ModulePath: "src", Title: "Consumed T14 budget", Slug: "consumed-t14-budget",
		Status: "failed", Calls: 3, Tokens: 4821, Repairs: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	require.NoError(t, f.db.Create(attempt).Error)

	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log)), "zero")

	stored, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, stored.Status, "retryable failure remains a durable queued run")
	require.Contains(t, stored.ErrorMessage, "zero")
	requireSourceRunPhase(t, f, log.ID, "retry_wait")

	ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.DataSourceStatusPaused, ds.Status, "manual sync must not unpause a paused datasource")
	require.NotEmpty(t, ds.ErrorMessage)
	require.NotEmpty(t, ds.LastSyncResult)

	dispatches, err := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository).RecoverSourceTriggers(f.ctx, ds)
	require.NoError(t, err)
	require.Len(t, dispatches, 1, "lost queue retry must be recoverable from database state")
	require.Equal(t, log.ID, dispatches[0].SyncLog.ID)
	var retained types.SourceWikiAttempt
	require.NoError(t, f.db.Where("id=?", attempt.ID).Take(&retained).Error)
	require.Equal(t, 3, retained.Calls)
	require.Equal(t, 4821, retained.Tokens)
	require.Equal(t, 1, retained.Repairs, "source-run recovery must not reset a consumed T14 Wiki-attempt budget")
}

func requireSourceRunPhase(t *testing.T, f *javaSourceFixture, logID, want string) {
	t.Helper()
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 50, 0)
	require.NoError(t, err)
	for _, log := range logs {
		if log.ID == logID {
			encoded, err := json.Marshal(log)
			require.NoError(t, err)
			var response struct {
				SourceRunPhase string `json:"source_run_phase"`
			}
			require.NoError(t, json.Unmarshal(encoded, &response))
			require.Equal(t, want, response.SourceRunPhase, "public sync-log JSON should expose the durable phase")
			return
		}
	}
	require.FailNow(t, "sync log missing from public sync history", "log id %s", logID)
}

func TestSourceRetryRecoveryIncludesErrorAndPausedDataSources(t *testing.T) {
	for _, initialStatus := range []string{types.DataSourceStatusActive, types.DataSourceStatusPaused} {
		t.Run(initialStatus, func(t *testing.T) {
			f := newJavaSourceFixture(t)
			require.NoError(t, f.db.Model(&types.DataSource{}).Where("id=?", f.ds.ID).Update("status", initialStatus).Error)
			f.embedVector = []float32{0, 0, 0}
			f.service.taskEnqueuer = sourceTestTaskEnqueuer{err: errors.New("queue unavailable")}
			log, err := f.service.ManualSync(f.ctx, f.ds.ID)
			require.NoError(t, err)
			require.ErrorContains(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log)), "zero")

			deliveries := make(chan *asynq.Task, 10)
			scheduler := datasource.NewScheduler(repository.NewDataSourceRepository(f.db), repository.NewSyncLogRepository(f.db),
				sourceTestTaskEnqueuer{tasks: deliveries}, f.service.sourceSnapshots)
			require.NoError(t, scheduler.Start(f.ctx))
			scheduler.Stop()
			require.Len(t, deliveries, 1, "startup must redeliver durable retry work even when the source is not cron-eligible")
			require.Zero(t, scheduler.EntryCount(), "retry recovery must not create a cron entry for error/paused state")
			f.embedVector = []float32{1, 0, 0}
			require.NoError(t, f.service.ProcessSync(f.ctx, <-deliveries))
			stored, err := f.service.GetSyncLog(f.ctx, log.ID)
			require.NoError(t, err)
			require.Equal(t, types.SyncLogStatusSuccess, stored.Status, "the recovered retry must complete")
			ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
			require.NoError(t, err)
			if initialStatus == types.DataSourceStatusPaused {
				require.Equal(t, types.DataSourceStatusPaused, ds.Status, "recovering manual work must preserve paused scheduling state")
			} else {
				require.Equal(t, types.DataSourceStatusActive, ds.Status)
			}
		})
	}
}

func TestSourceStaleClaimCannotCancelCurrentConfig(t *testing.T) {
	f := newJavaSourceFixture(t)
	oldLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	oldDS, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)

	newDS := *oldDS
	var config map[string]any
	require.NoError(t, json.Unmarshal(newDS.Config, &config))
	config["settings"].(map[string]any)["new_config_probe"] = true
	newDS.Config, err = json.Marshal(config)
	require.NoError(t, err)
	_, err = f.service.UpdateDataSource(f.ctx, &newDS)
	require.NoError(t, err)
	newLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	currentDS, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	control := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository)
	var deliveryGeneration int64
	require.NoError(t, f.db.Table("source_sync_runs").Select("delivery_generation").Where("sync_log_id=?", newLog.ID).Scan(&deliveryGeneration).Error)
	_, claimed, err := control.ClaimSourceRun(f.ctx, currentDS, newLog.ID, deliveryGeneration, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	_, staleClaimed, err := control.ClaimSourceRun(f.ctx, oldDS, oldLog.ID, 1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.False(t, staleClaimed)
	stored, err := f.service.GetSyncLog(f.ctx, newLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusRunning, stored.Status, "a delayed claimant must not cancel the current-config run")
}

func TestSourceStaleRegisterAndRecoveryCannotRewriteCurrentConfig(t *testing.T) {
	f := newJavaSourceFixture(t)
	_, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	oldDS, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)

	newDS := *oldDS
	var config map[string]any
	require.NoError(t, json.Unmarshal(newDS.Config, &config))
	config["settings"].(map[string]any)["new_config_probe"] = true
	newDS.Config, err = json.Marshal(config)
	require.NoError(t, err)
	_, err = f.service.UpdateDataSource(f.ctx, &newDS)
	require.NoError(t, err)
	newLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	currentDS, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	control := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository)
	var deliveryGeneration int64
	require.NoError(t, f.db.Table("source_sync_runs").Select("delivery_generation").Where("sync_log_id=?", newLog.ID).Scan(&deliveryGeneration).Error)
	_, claimed, err := control.ClaimSourceRun(f.ctx, currentDS, newLog.ID, deliveryGeneration, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)

	staleLog := &types.SyncLog{DataSourceID: oldDS.ID, TenantID: oldDS.TenantID, Status: types.SyncLogStatusQueued, StartedAt: time.Now().UTC()}
	_, _, err = control.RegisterSourceTrigger(f.ctx, oldDS, staleLog, "manual")
	require.Error(t, err, "registration from a stale configuration must be rejected")
	stored, err := f.service.GetSyncLog(f.ctx, newLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusRunning, stored.Status)

	dispatches, err := control.RecoverSourceTriggers(f.ctx, oldDS)
	require.NoError(t, err)
	require.Empty(t, dispatches, "recovery must consult current persisted state and not recreate stale work")
	stored, err = f.service.GetSyncLog(f.ctx, newLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusRunning, stored.Status, "stale recovery input must not cancel the current-config run")
}

func TestSourceConfigGenerationRestoresAfterDatasourceWriteFails(t *testing.T) {
	f := newJavaSourceFixture(t)
	require.NoError(t, f.db.Exec(`CREATE FUNCTION reject_source_config_write() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.config IS DISTINCT FROM OLD.config THEN RAISE EXCEPTION 'injected source config write failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER reject_source_config_write BEFORE UPDATE ON data_sources
			FOR EACH ROW EXECUTE FUNCTION reject_source_config_write();`).Error)
	current, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	updated := *current
	var config map[string]any
	require.NoError(t, json.Unmarshal(updated.Config, &config))
	config["settings"].(map[string]any)["rejected_config_probe"] = true
	updated.Config, err = json.Marshal(config)
	require.NoError(t, err)
	_, err = f.service.UpdateDataSource(f.ctx, &updated)
	require.ErrorContains(t, err, "injected source config write failure")

	stored, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, current.Config, stored.Config, "the failed write must leave the stored configuration unchanged")
	_, err = f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err, "the coordinator must be restored to the stored config after a rejected write")
}

func TestSourceRecoveryRejectsIncompatiblePersistedArtifactVersions(t *testing.T) {
	for _, field := range []string{"processing_version", "embedding_version"} {
		t.Run(field, func(t *testing.T) {
			f := newJavaSourceFixture(t)
			syncSourceFixture(t, f)
			published, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
			require.NoError(t, err)
			f.advanceJava("package demo; public class Service { int recoveryTarget() { return 2; } }\n")

			f.embedStarted = make(chan struct{}, 1)
			f.embedRelease = make(chan struct{})
			delivered := make(chan *asynq.Task, 2)
			f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: delivered}
			log, err := f.service.ManualSync(f.ctx, f.ds.ID)
			require.NoError(t, err)
			task := <-delivered
			runCtx, cancel := context.WithCancel(f.ctx)
			done := make(chan error, 1)
			go func() { done <- f.service.ProcessSync(runCtx, task) }()
			select {
			case <-f.embedStarted:
			case <-time.After(20 * time.Second):
				cancel()
				t.Fatal("source run did not reach embedding after staging parsed chunks")
			}
			var staged struct {
				ID string `gorm:"column:id"`
			}
			require.NoError(t, f.db.Table("source_snapshots").Select("id").Where("sync_log_id=?", log.ID).Take(&staged).Error)
			cancel()
			require.Error(t, <-done)
			var retryTask *asynq.Task
			select {
			case retryTask = <-delivered:
			case <-time.After(5 * time.Second):
				t.Fatal("durable retry dispatch was not enqueued after the interrupted stage")
			}
			f.embedStarted, f.embedRelease = nil, nil

			require.NoError(t, f.db.Table("source_snapshots").Where("id=?", staged.ID).Update(field, "incompatible-fixture-version").Error)
			parseCalls := f.parseCount.Load()
			err = f.service.ProcessSync(f.ctx, retryTask)
			require.ErrorContains(t, err, "start a new source run")
			require.Equal(t, parseCalls, f.parseCount.Load(), "recovery must reject the stage before parsing can be rebound to old chunk ids")
			current, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
			require.NoError(t, err)
			require.Equal(t, published.Snapshot.ID, current.Snapshot.ID, "incompatible recovery must retain the previous publication")
		})
	}
}

func TestSourceUnchangedTargetDoesNotRepublish(t *testing.T) {
	f := newJavaSourceFixture(t)
	firstLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, firstLog)))
	before, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)

	secondLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, secondLog)))
	after, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)

	var publications, signals int64
	require.NoError(t, f.db.Table("source_snapshots").Where("data_source_id=? AND state='published'", f.ds.ID).Count(&publications).Error)
	require.NoError(t, f.db.Table("source_publication_outbox").Where("data_source_id=?", f.ds.ID).Count(&signals).Error)
	require.Equal(t, before.Snapshot.ID, after.Snapshot.ID, "same target and effective processing/indexing identity must retain the existing publication")
	require.EqualValues(t, 1, publications)
	require.EqualValues(t, 1, signals)
	completed, err := f.service.GetSyncLog(f.ctx, secondLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, completed.Status)
	require.Equal(t, "published", completed.SourceRunPhase,
		"a successful same-target no-op must not expose a failed source-run phase")
}

func TestSourceWikiNotificationSurvivesCredentialRotationAndSameTargetSync(t *testing.T) {
	f := newJavaSourceFixture(t)
	firstLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, firstLog)))
	before, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)

	var original struct {
		ID               string `gorm:"column:id"`
		SnapshotID       string `gorm:"column:snapshot_id"`
		ConfigGeneration int64  `gorm:"column:config_generation"`
		Status           string `gorm:"column:status"`
	}
	require.NoError(t, f.db.Table("source_publication_outbox").Where("data_source_id=?", f.ds.ID).Take(&original).Error)
	require.Equal(t, "pending", original.Status)

	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	baseURL, ok := config.Credentials["base_url"].(string)
	require.True(t, ok)
	_, err = f.service.UpdateDataSourceCredentials(f.ctx, f.ds.ID, map[string]interface{}{
		"base_url": baseURL, "access_token": "rotated-fixture-token",
	})
	require.NoError(t, err)
	accepted, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
	require.NoError(t, err)
	require.Zero(t, accepted, "the old-generation event is fenced after the credential edit")
	require.NoError(t, f.db.Table("source_publication_outbox").Where("id=?", original.ID).Select("status").Scan(&original.Status).Error)
	require.Equal(t, "superseded", original.Status)

	secondLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, secondLog)))
	after, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, before.Snapshot.ID, after.Snapshot.ID, "same-target sync must retain the published snapshot")

	var current struct {
		ConfigGeneration int64  `gorm:"column:config_generation"`
		Status           string `gorm:"column:status"`
	}
	require.NoError(t, f.db.Table("source_publication_outbox").Where("id=?", original.ID).Select("status, config_generation").Take(&current).Error)
	require.Equal(t, "pending", current.Status, "a validated same-target run must restore durable notification work")
	require.Greater(t, current.ConfigGeneration, original.ConfigGeneration)
	var stateGeneration int64
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Select("config_generation").Take(&stateGeneration).Error)
	require.Equal(t, stateGeneration, current.ConfigGeneration)

	accepted, err = f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, accepted)
	var payload types.SourceWikiUpdatePayload
	var op types.TaskPendingOp
	currentDeliveryID := types.SourceWikiDeliveryID(original.ID, current.ConfigGeneration)
	require.NoError(t, f.db.Where("task_type=? AND dedup_key=?", types.TypeSourceWikiUpdate, currentDeliveryID).Take(&op).Error)
	require.NoError(t, json.Unmarshal(op.Payload, &payload))
	require.Equal(t, original.SnapshotID, payload.SnapshotID)
	require.Equal(t, current.ConfigGeneration, payload.ConfigGeneration)
	require.Equal(t, original.ID, payload.EventID)
	require.Equal(t, currentDeliveryID, payload.DeliveryID)
}

func TestSourceWikiNotificationRefreshesDeliveredEventAfterCredentialRotation(t *testing.T) {
	f := newJavaSourceFixture(t)
	firstLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, firstLog)))

	accepted, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, accepted)
	pendingOps := repository.NewTaskPendingOpsRepository(f.db)
	peekOps := func() []*types.TaskPendingOp {
		ops, readErr := pendingOps.PeekBatch(f.ctx, types.TypeSourceWikiUpdate,
			types.TaskScopeKnowledgeBase, f.kb.ID, 10)
		require.NoError(t, readErr)
		return ops
	}
	staleBefore := time.Now().Add(-time.Minute)
	originalOps, err := pendingOps.ClaimBatch(f.ctx, types.TypeSourceWikiUpdate,
		types.TaskScopeKnowledgeBase, f.kb.ID, 10, staleBefore)
	require.NoError(t, err)
	require.Len(t, originalOps, 1)
	var originalPayload types.SourceWikiUpdatePayload
	require.NoError(t, json.Unmarshal(originalOps[0].Payload, &originalPayload))
	require.NotNil(t, originalOps[0].ClaimedAt, "the original notification is owned by the old consumer")

	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	baseURL, ok := config.Credentials["base_url"].(string)
	require.True(t, ok)
	_, err = f.service.UpdateDataSourceCredentials(f.ctx, f.ds.ID, map[string]interface{}{
		"base_url": baseURL, "access_token": "rotated-fixture-token",
	})
	require.NoError(t, err)

	secondLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, secondLog)))
	var currentGeneration int64
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).
		Select("config_generation").Take(&currentGeneration).Error)

	updatedOps := peekOps()
	require.Len(t, updatedOps, 2,
		"a generation refresh must create a separately identifiable op while the old claim is in flight")
	var currentQueuedOp *types.TaskPendingOp
	var staleQueuedOp *types.TaskPendingOp
	for _, op := range updatedOps {
		var payload types.SourceWikiUpdatePayload
		require.NoError(t, json.Unmarshal(op.Payload, &payload))
		if payload.ConfigGeneration == currentGeneration {
			currentQueuedOp = op
		} else if payload.ConfigGeneration == originalPayload.ConfigGeneration {
			staleQueuedOp = op
		}
	}
	require.NotNil(t, staleQueuedOp, "refreshing a notification must leave the old claimed row untouched")
	require.Equal(t, originalOps[0].ID, staleQueuedOp.ID)
	require.Equal(t, originalOps[0].ClaimedAt, staleQueuedOp.ClaimedAt,
		"refreshing a newer generation must not release the old consumer's claim")
	require.NotNil(t, currentQueuedOp, "the current generation must have a durable pending op")
	require.NotEqual(t, originalOps[0].ID, currentQueuedOp.ID,
		"the old consumer's ID-based acknowledgement must not target the refreshed op")
	currentClaim, err := pendingOps.ClaimBatch(f.ctx, types.TypeSourceWikiUpdate,
		types.TaskScopeKnowledgeBase, f.kb.ID, 10, staleBefore)
	require.NoError(t, err)
	require.Len(t, currentClaim, 1, "a second consumer must be able to claim the current generation while the old claim remains held")
	require.Equal(t, currentQueuedOp.ID, currentClaim[0].ID)
	currentQueuedOp = currentClaim[0]

	var updatedPayload types.SourceWikiUpdatePayload
	require.NoError(t, json.Unmarshal(currentQueuedOp.Payload, &updatedPayload))
	require.Equal(t, originalPayload.EventID, updatedPayload.EventID)
	require.Equal(t, originalPayload.SnapshotID, updatedPayload.SnapshotID,
		"refreshing a delivered event must continue to reference the same published snapshot")
	require.Greater(t, updatedPayload.ConfigGeneration, originalPayload.ConfigGeneration)
	require.Equal(t, currentGeneration, updatedPayload.ConfigGeneration,
		"the durable Wiki notification must carry the current source generation")
	require.NotEqual(t, originalOps[0].DedupKey, currentQueuedOp.DedupKey,
		"different source generations must have independent queue deduplication identities")
	require.Equal(t, originalPayload.EventID, updatedPayload.EventID,
		"a generation refresh retains the stable publication event identity")
	require.Equal(t, types.SourceWikiDeliveryID(updatedPayload.EventID, currentGeneration), updatedPayload.DeliveryID)
	require.Equal(t, updatedPayload.DeliveryID, currentQueuedOp.DedupKey)
	require.NotEqual(t, originalPayload.DeliveryID, updatedPayload.DeliveryID)

	require.NoError(t, pendingOps.DeleteByIDs(f.ctx, []int64{originalOps[0].ID}),
		"the original consumer must be able to acknowledge its old claimed row")
	remainingOps := peekOps()
	require.Len(t, remainingOps, 1, "acknowledging the old generation must leave the current-generation notification durable")
	require.Equal(t, currentQueuedOp.ID, remainingOps[0].ID)

	thirdLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, thirdLog)))
	sameGenerationOps := peekOps()
	require.Len(t, sameGenerationOps, 1, "same-generation no-op must not duplicate the durable notification")
	require.Equal(t, currentQueuedOp.ID, sameGenerationOps[0].ID)
	require.Equal(t, currentQueuedOp.ClaimedAt, sameGenerationOps[0].ClaimedAt,
		"same-generation no-op must preserve the active consumer claim")

	for i := 0; i < 2; i++ {
		accepted, err = f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
		require.NoError(t, err)
		require.Zero(t, accepted, "relaying an already delivered event must be idempotent")
		require.Len(t, peekOps(), 1, "duplicate relay must not duplicate the durable Wiki op")
	}
}

func TestSourcePublicationOutboxAcceptsTypedPendingUpdateAtomically(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log)))

	var event struct {
		ID               string `gorm:"column:id"`
		SnapshotID       string `gorm:"column:snapshot_id"`
		ConfigGeneration int64  `gorm:"column:config_generation"`
		Status           string `gorm:"column:status"`
		AttemptCount     int    `gorm:"column:attempt_count"`
	}
	require.NoError(t, f.db.Table("source_publication_outbox").Where("data_source_id=?", f.ds.ID).Take(&event).Error)
	require.Equal(t, "pending", event.Status)

	// Fail the ack after the receiver insert. The shared transaction must roll
	// back both writes, leave the event pending, and record one bounded attempt.
	require.NoError(t, f.db.Exec(`CREATE FUNCTION reject_source_outbox_ack() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.status = 'delivered' THEN RAISE EXCEPTION 'injected source outbox ack failure'; END IF;
			RETURN NEW;
		END $$;
		CREATE TRIGGER reject_source_outbox_ack BEFORE UPDATE ON source_publication_outbox
			FOR EACH ROW EXECUTE FUNCTION reject_source_outbox_ack();`).Error)
	_, err = f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
	require.ErrorContains(t, err, "injected source outbox ack failure")
	var pendingCount int64
	initialDeliveryID := types.SourceWikiDeliveryID(event.ID, event.ConfigGeneration)
	require.NoError(t, f.db.Table("task_pending_ops").Where("task_type=? AND dedup_key=?", types.TypeSourceWikiUpdate, initialDeliveryID).Count(&pendingCount).Error)
	require.Zero(t, pendingCount, "receiver acceptance must roll back with the failed acknowledgement")
	var retryWindow struct {
		NextAttemptAt time.Time `gorm:"column:next_attempt_at"`
		AttemptCount  int       `gorm:"column:attempt_count"`
	}
	require.NoError(t, f.db.Table("source_publication_outbox").Select("next_attempt_at, attempt_count").Where("id=?", event.ID).Take(&retryWindow).Error)
	require.Equal(t, 1, retryWindow.AttemptCount)
	require.True(t, retryWindow.NextAttemptAt.After(time.Now().UTC()), "transient receiver failures use bounded retry backoff")
	require.NoError(t, f.db.Exec("DROP TRIGGER reject_source_outbox_ack ON source_publication_outbox; DROP FUNCTION reject_source_outbox_ack()").Error)
	require.NoError(t, f.db.Table("source_publication_outbox").Where("id=?", event.ID).Update("next_attempt_at", time.Now().UTC()).Error)

	// A process restart runs the startup relay and accepts the still-pending row.
	scheduler := datasource.NewScheduler(
		repository.NewDataSourceRepository(f.db), repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{}, f.service.sourceSnapshots,
	)
	require.NoError(t, scheduler.Start(f.ctx))
	scheduler.Stop()

	var accepted types.TaskPendingOp
	require.NoError(t, f.db.Where("task_type=? AND scope=? AND scope_id=? AND op=? AND dedup_key=?",
		types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase, f.kb.ID, "published_snapshot", initialDeliveryID).Take(&accepted).Error)
	var payload types.SourceWikiUpdatePayload
	require.NoError(t, json.Unmarshal(accepted.Payload, &payload))
	require.Equal(t, 1, payload.SchemaVersion)
	require.Equal(t, event.ID, payload.EventID)
	require.Equal(t, initialDeliveryID, payload.DeliveryID)
	require.Equal(t, f.ds.TenantID, payload.TenantID)
	require.Equal(t, f.kb.ID, payload.KnowledgeBaseID)
	require.Equal(t, f.ds.ID, payload.DataSourceID)
	require.Equal(t, event.SnapshotID, payload.SnapshotID)
	require.Equal(t, event.ConfigGeneration, payload.ConfigGeneration)
	require.Equal(t, f.sha, payload.CommitSHA)

	acceptedCount, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
	require.NoError(t, err)
	require.Zero(t, acceptedCount)
	require.NoError(t, f.db.Table("task_pending_ops").Where("task_type=? AND dedup_key=?", types.TypeSourceWikiUpdate, payload.DeliveryID).Count(&pendingCount).Error)
	require.EqualValues(t, 1, pendingCount, "replaying an acknowledged event cannot create duplicate pending Wiki work")
	require.NoError(t, f.db.Table("source_publication_outbox").Where("id=? AND status='delivered' AND attempt_count=2", event.ID).Count(&pendingCount).Error)
	require.EqualValues(t, 1, pendingCount)
}

func TestSourcePublicationOutboxSupersedesStaleTargets(t *testing.T) {
	t.Run("latest published snapshot wins", func(t *testing.T) {
		f := newJavaSourceFixture(t)
		firstLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
		require.NoError(t, err)
		require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, firstLog)))
		f.advanceJava("package demo; public class Service { int latest() { return 2; } }\n")
		secondLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
		require.NoError(t, err)
		require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, secondLog)))
		latest, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
		require.NoError(t, err)

		accepted, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
		require.NoError(t, err)
		require.Equal(t, 1, accepted)
		var eventID string
		require.NoError(t, f.db.Table("task_pending_ops").Where("task_type=?", types.TypeSourceWikiUpdate).Pluck("dedup_key", &eventID).Error)
		var payload types.SourceWikiUpdatePayload
		var op types.TaskPendingOp
		require.NoError(t, f.db.Where("task_type=? AND dedup_key=?", types.TypeSourceWikiUpdate, eventID).Take(&op).Error)
		require.NoError(t, json.Unmarshal(op.Payload, &payload))
		require.Equal(t, latest.Snapshot.ID, payload.SnapshotID)
		var superseded int64
		require.NoError(t, f.db.Table("source_publication_outbox").Where("snapshot_id<>? AND status='superseded'", latest.Snapshot.ID).Count(&superseded).Error)
		require.EqualValues(t, 1, superseded)
	})

}

func TestSourcePublicationOutboxDoesNotRecreateWorkAfterInvalidation(t *testing.T) {
	for _, scenario := range []string{"configuration changed", "source deleted", "knowledge base deleted", "publication cleared"} {
		t.Run(scenario, func(t *testing.T) {
			f := newJavaSourceFixture(t)
			log, err := f.service.ManualSync(f.ctx, f.ds.ID)
			require.NoError(t, err)
			require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log)))

			switch scenario {
			case "configuration changed":
				updated := *f.ds
				config, parseErr := updated.ParseConfig()
				require.NoError(t, parseErr)
				config.Settings["exclude_paths"] = []string{"src/Service.java"}
				updated.Config, err = config.ToJSON()
				require.NoError(t, err)
				_, err = f.service.UpdateDataSource(f.ctx, &updated)
				require.NoError(t, err)
			case "source deleted":
				require.NoError(t, f.service.DeleteDataSource(f.ctx, f.ds.ID))
			case "knowledge base deleted":
				require.NoError(t, f.db.Model(&types.KnowledgeBase{}).Where("id=?", f.kb.ID).Update("deleted_at", time.Now().UTC()).Error)
			case "publication cleared":
				require.NoError(t, f.db.Exec("DELETE FROM source_publications WHERE data_source_id=?", f.ds.ID).Error)
			}

			_, err = f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
			require.NoError(t, err)
			var pendingCount, superseded int64
			require.NoError(t, f.db.Table("task_pending_ops").Where("task_type=?", types.TypeSourceWikiUpdate).Count(&pendingCount).Error)
			require.Zero(t, pendingCount, "invalidated publication must not recreate pending Wiki work")
			require.NoError(t, f.db.Table("source_publication_outbox").Where("data_source_id=? AND status='superseded'", f.ds.ID).Count(&superseded).Error)
			require.EqualValues(t, 1, superseded)
		})
	}
}

func TestSourceLeaseSerializesWorkersAndFencesExpiredOwner(t *testing.T) {
	f := newJavaSourceFixture(t)
	delivered := make(chan *asynq.Task, 1)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: delivered}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	task := <-delivered
	var payload types.DataSourceSyncPayload
	require.NoError(t, json.Unmarshal(task.Payload(), &payload))
	control := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository)
	claimDS, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)

	const workers = 12
	leases := make(chan types.SourceSyncLease, workers)
	var claimed atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			lease, ok, claimErr := control.ClaimSourceRun(f.ctx, claimDS, log.ID, payload.DeliveryGeneration, uuid.NewString(), time.Minute)
			require.NoError(t, claimErr)
			if ok {
				claimed.Add(1)
				leases <- lease
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 1, claimed.Load(), "a source run has one concurrent database lease owner")
	oldLease := <-leases
	_, staleDeliveryClaimed, err := control.ClaimSourceRun(f.ctx, claimDS, log.ID, payload.DeliveryGeneration+1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.False(t, staleDeliveryClaimed, "a mismatched queue generation is only a no-op")
	var statusAfterStaleDelivery string
	require.NoError(t, f.db.Raw("SELECT status FROM sync_logs WHERE id=?", log.ID).Scan(&statusAfterStaleDelivery).Error)
	require.Equal(t, types.SyncLogStatusRunning, statusAfterStaleDelivery, "stale delivery must not cancel the active run")
	// Advance the database lease clock deterministically to model a crashed
	// worker without sleeping for the production lease TTL.
	require.NoError(t, f.db.Exec("UPDATE source_sync_states SET lease_expires_at=now()-interval '1 second' WHERE data_source_id=?", f.ds.ID).Error)
	newLease, ok, err := control.ClaimSourceRun(f.ctx, claimDS, log.ID, payload.DeliveryGeneration, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, ok, "a worker can reclaim the run after lease expiry")
	require.Greater(t, newLease.FencingToken, oldLease.FencingToken)
	require.ErrorIs(t, control.RecordSourceRunPhase(f.ctx, oldLease, "ready", ""), types.ErrSourceSyncLeaseLost)
	var config map[string]any
	require.NoError(t, json.Unmarshal(claimDS.Config, &config))
	settings, ok := config["settings"].(map[string]any)
	require.True(t, ok)
	settings["coordination_test_generation"] = true
	updatedConfig, err := json.Marshal(config)
	require.NoError(t, err)
	updatedDS := *claimDS
	updatedDS.Config = updatedConfig
	require.NoError(t, control.AdvanceSourceConfig(f.ctx, &updatedDS, true))
	require.ErrorIs(t, control.RecordSourceRunPhase(f.ctx, newLease, "ready", ""), types.ErrSourceSyncLeaseLost,
		"a configuration generation change fences the current owner")
}

func TestSourceRetryReusesCompletedParseStage(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	delivered := make(chan *asynq.Task, 2)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: delivered}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	task := <-delivered
	runCtx, cancel := context.WithCancel(f.ctx)
	done := make(chan error, 1)
	go func() { done <- f.service.ProcessSync(runCtx, task) }()
	select {
	case <-f.embedStarted:
	case <-time.After(20 * time.Second):
		cancel()
		t.Fatal("source run did not reach embedding after parsing and file staging")
	}
	var staged struct {
		ID string `gorm:"column:id"`
	}
	require.NoError(t, f.db.Table("source_snapshots").Select("id").Where("sync_log_id=?", log.ID).Take(&staged).Error)
	var parsedMembers int64
	require.NoError(t, f.db.Table("source_snapshot_members").Where("snapshot_id=? AND status='parsed'", staged.ID).Count(&parsedMembers).Error)
	require.EqualValues(t, 1, parsedMembers, "the completed file stage must be checkpointed before embedding")
	cancel()
	require.Error(t, <-done, "the canceled worker should leave the run recoverable")
	f.embedStarted, f.embedRelease = nil, nil
	retryTask := <-delivered
	require.NoError(t, f.service.ProcessSync(f.ctx, retryTask))
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status)
	require.EqualValues(t, 1, f.parseCount.Load(), "retry should reuse the persisted parse artifact")
	var snapshotCount, versionCount int64
	require.NoError(t, f.db.Table("source_snapshots").Where("sync_log_id=?", log.ID).Count(&snapshotCount).Error)
	require.NoError(t, f.db.Table("source_file_versions").Where("snapshot_id=?", staged.ID).Count(&versionCount).Error)
	require.EqualValues(t, 1, snapshotCount, "retry should resume the same immutable run snapshot")
	require.EqualValues(t, 1, versionCount, "retry should reuse the already staged file version")
	var outboxCount int64
	require.NoError(t, f.db.Table("source_publication_outbox").Where("snapshot_id=? AND status='pending'", staged.ID).Count(&outboxCount).Error)
	require.EqualValues(t, 1, outboxCount, "retry publication should produce one durable Wiki signal")
}

type sourceTestTaskEnqueuer struct {
	err   error
	tasks chan<- *asynq.Task
}

func (e sourceTestTaskEnqueuer) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.tasks != nil {
		e.tasks <- task
	}
	return &asynq.TaskInfo{ID: "source-test-task"}, nil
}

func sourceTestSyncTask(t *testing.T, f *javaSourceFixture, log *types.SyncLog) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: f.ds.ID, TenantID: f.ds.TenantID, SyncLogID: log.ID, Trigger: "manual",
	})
	require.NoError(t, err)
	return asynq.NewTask(types.TypeDataSourceSync, payload)
}

func TestSourcePythonSnapshotPublishesIndexesAndReadView(t *testing.T) {
	cache := os.Getenv("SOURCE_PARSER_CACHE")
	lock, err := os.ReadFile(filepath.Join(cache, "grammar.lock.json"))
	require.NoError(t, err)
	if !strings.Contains(string(lock), `"python"`) {
		t.Skip("SOURCE_PARSER_CACHE does not include the locked Python grammar")
	}
	raw := []byte("from pkg.booking import helper as booking_helper\r\n@router.post(\"/预约\")\r\nclass ReservationService:\r\n    @trace\r\n    async def reserve_booking(self, 名称: str) -> str:\r\n        return f\"预约 {名称}\"\r\n")
	brokenRaw := []byte("async def broken(:\r\n    return \"degraded_python_marker 中文😀\"\r\n")
	f := newJavaSourceFixture(t, map[string][]byte{
		"src/reservation.py":  raw,
		"src/syntax_error.py": brokenRaw,
	})
	parserVersion, err := source.ParserVersion(f.ctx, os.Getenv("SOURCE_PARSER_URL"))
	require.NoError(t, err)
	require.Contains(t, parserVersion, "-rules-4-")
	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	_, rulesVersion, err := datasource.ParseSourceSettings(config)
	require.NoError(t, err)
	var parserLock struct {
		PackVersion string `json:"pack_version"`
		Grammars    map[string]struct {
			GrammarSHA256 string `json:"grammar_sha256"`
		} `json:"grammars"`
	}
	require.NoError(t, json.Unmarshal(lock, &parserLock))
	legacyLanguageVersions := make(map[string]string, len(parserLock.Grammars))
	for language, grammar := range parserLock.Grammars {
		legacyLanguageVersions[language] = fmt.Sprintf("%s-pack-%s-rules-3-%s", language, parserLock.PackVersion, grammar.GrammarSHA256)
	}
	legacyVersionsJSON, err := json.Marshal(legacyLanguageVersions)
	require.NoError(t, err)
	legacyFingerprint := sha256.Sum256(legacyVersionsJSON)
	legacyParserVersion := fmt.Sprintf("source-pack-%s-rules-3-%x", parserLock.PackVersion, legacyFingerprint[:16])
	require.NotEqual(t, parserVersion, legacyParserVersion)
	legacyArtifactKey := sourceParseArtifactKey("src/reservation.py", raw, legacyParserVersion, rulesVersion)
	currentArtifactKey := sourceParseArtifactKey("src/reservation.py", raw, parserVersion, rulesVersion)
	require.NotEqual(t, legacyArtifactKey, currentArtifactKey, "Python extraction-rule changes must invalidate same-SHA parse artifacts")
	stale, err := source.ParseFile(f.ctx, os.Getenv("SOURCE_PARSER_URL"), "src/reservation.py", raw)
	require.NoError(t, err)
	importNames := map[string]bool{}
	keptSymbols := stale.Symbols[:0]
	for _, symbol := range stale.Symbols {
		if symbol.Kind == "import" {
			importNames[symbol.QualifiedName] = true
			continue
		}
		keptSymbols = append(keptSymbols, symbol)
	}
	stale.Symbols = keptSymbols
	for i := range stale.Chunks {
		kept := stale.Chunks[i].Symbols[:0]
		for _, name := range stale.Chunks[i].Symbols {
			if !importNames[name] {
				kept = append(kept, name)
			}
		}
		stale.Chunks[i].Symbols = kept
	}
	stale.ParserVersion = legacyParserVersion
	require.NoError(t, f.service.sourceSnapshots.SaveParsedArtifact(f.ctx, f.ds.TenantID, f.ds.ID, legacyArtifactKey, stale))
	parseCountBeforeSync := f.parseCount.Load()
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	require.Contains(t, preview.Checks, types.SourcePreviewCheck{Name: "parser", Ready: true, Message: "source mode requires a healthy, versioned parser with every selected language grammar"})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	require.Greater(t, f.parseCount.Load(), parseCountBeforeSync, "the stale same-SHA artifact must not be reused")
	rebuilt, err := f.service.sourceSnapshots.GetParsedArtifact(f.ctx, f.ds.TenantID, f.ds.ID, currentArtifactKey)
	require.NoError(t, err)
	require.NotNil(t, rebuilt, "the current rules version must persist a rebuilt parse artifact")
	require.Equal(t, parserVersion, rebuilt.ParserVersion)
	hasImport := false
	for _, symbol := range rebuilt.Symbols {
		if symbol.Kind == "import" && symbol.QualifiedName == "src/reservation.py.pkg.booking:helper" {
			hasImport = true
			break
		}
	}
	require.True(t, hasImport, "the rebuilt artifact must include the new Python import symbol")

	var hit *types.SearchResult
	for _, params := range []types.SearchParams{
		{QueryText: "reserve_booking", MatchCount: 10, DisableVectorMatch: true},
		{QueryText: "reserve_booking", MatchCount: 10, DisableKeywordsMatch: true},
	} {
		results, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, searchErr)
		var routeHit *types.SearchResult
		for _, candidate := range results {
			if strings.Contains(candidate.Content, "reserve_booking") {
				routeHit = candidate
				break
			}
		}
		require.NotNil(t, routeHit, "both source index routes must find the Python method")
		if hit == nil {
			hit = routeHit
		}
	}

	grep := agenttools.NewSourceAwareGrepChunksTool(f.db, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}})
	grepResult, err := grep.Execute(f.ctx, json.RawMessage(`{"query":"reserve_booking"}`))
	require.NoError(t, err)
	require.Greater(t, grepResult.Data["result_count"].(int), 0)

	view, err := f.knowledge.GetSourceFile(f.ctx, hit.KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, raw, []byte(view.Content))
	require.Equal(t, "src/reservation.py", view.Path)
	require.Contains(t, string(view.Symbols), "src/reservation.py.ReservationService.reserve_booking")
	require.Contains(t, string(view.Symbols), "src/reservation.py.pkg.booking:helper", "the public read must expose the rebuilt Python import symbol")

	var degradedHit *types.SearchResult
	for _, params := range []types.SearchParams{
		{QueryText: "degraded_python_marker", MatchCount: 10, DisableVectorMatch: true},
		{QueryText: "degraded_python_marker", MatchCount: 10, DisableKeywordsMatch: true},
	} {
		results, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, searchErr)
		var routeHit *types.SearchResult
		for _, candidate := range results {
			if strings.Contains(candidate.Content, "degraded_python_marker") {
				routeHit = candidate
				break
			}
		}
		require.NotNil(t, routeHit, "both index routes must retain readable syntax-error source")
		if degradedHit == nil {
			degradedHit = routeHit
		}
	}
	var degradedEvidence struct {
		Source types.SourceEvidence `json:"source"`
	}
	require.NoError(t, json.Unmarshal(degradedHit.ChunkMetadata, &degradedEvidence))
	require.Equal(t, "syntax_error", degradedEvidence.Source.Quality)
	require.Equal(t, "src/syntax_error.py", degradedEvidence.Source.Path)
	require.Equal(t, f.sha, degradedEvidence.Source.CommitSHA)
	degradedView, err := f.knowledge.GetSourceFile(f.ctx, degradedHit.KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, "syntax_error", degradedView.Quality)
	require.Equal(t, brokenRaw, []byte(degradedView.Content))
	require.Equal(t, f.sha, degradedView.CommitSHA)
	require.NotEmpty(t, degradedView.FileVersionID)
	pinnedDegraded, err := f.knowledge.GetSourceFile(f.ctx, degradedHit.KnowledgeID, degradedView.FileVersionID)
	require.NoError(t, err)
	require.Equal(t, f.sha, pinnedDegraded.CommitSHA)
	require.Equal(t, degradedView.FileVersionID, pinnedDegraded.FileVersionID)
	require.Equal(t, "syntax_error", pinnedDegraded.Quality)
}

func TestSourcePublishedChunksRemainReadOnlyButDescriptionMayChange(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	stored, err := f.chunks.GetChunkByID(f.ctx, hits[0].ID)
	require.NoError(t, err)
	change := *stored
	change.Content = "manually rewritten source"
	require.ErrorContains(t, f.chunks.UpdateChunk(f.ctx, &change), "Git-managed")
	require.ErrorContains(t, f.chunks.DeleteChunk(f.ctx, stored.ID), "Git-managed")
	change = *stored
	change.IsEnabled = false
	require.ErrorContains(t, f.chunks.UpdateChunk(f.ctx, &change), "Git-managed")
	_, err = f.knowledge.ReparseKnowledge(f.ctx, stored.KnowledgeID, nil)
	require.ErrorContains(t, err, "Git-managed")
	require.ErrorContains(t, f.knowledge.DeleteKnowledge(f.ctx, stored.KnowledgeID), "Git-managed")
	_, err = f.knowledge.MoveKnowledgeToFolder(f.ctx, f.kb.ID, []string{stored.KnowledgeID}, "manual")
	require.ErrorContains(t, err, "Git-managed")
	require.ErrorContains(t, f.knowledge.RequestKnowledgeSummaryRefresh(f.ctx, stored.KnowledgeID), "Git-managed")
	for _, operation := range []struct {
		name string
		run  func() error
	}{
		{"regenerate questions", func() error { _, err := f.knowledge.RegenerateChunkQuestions(f.ctx, stored.ID); return err }},
		{"generate first summary", func() error { _, err := f.knowledge.RegenerateKnowledgeSummary(f.ctx, stored.KnowledgeID); return err }},
		{"delete all file chunks", func() error { return f.chunks.DeleteChunksByKnowledgeID(f.ctx, stored.KnowledgeID) }},
		{"delete chunks by file list", func() error { return f.chunks.DeleteByKnowledgeList(f.ctx, []string{stored.KnowledgeID}) }},
	} {
		t.Run(operation.name, func(t *testing.T) { require.ErrorContains(t, operation.run(), "Git-managed") })
	}
	require.NoError(t, f.knowledge.UpdateKnowledge(f.ctx, &types.Knowledge{ID: stored.KnowledgeID, Description: "排班服务说明", DescriptionSpecified: true}))
	unchanged, err := f.chunks.GetChunkByID(f.ctx, stored.ID)
	require.NoError(t, err)
	require.Equal(t, stored.Content, unchanged.Content)
	require.True(t, unchanged.IsEnabled)
	info, err := f.knowledge.GetKnowledgeByID(f.ctx, stored.KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, "排班服务说明", info.Description)
}

func TestSourcePublishedFileDownloadPreservesOriginalBytes(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	reader, filename, err := f.knowledge.GetKnowledgeFile(f.ctx, hits[0].KnowledgeID)
	require.NoError(t, err)
	defer reader.Close()
	original, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, "Service.java", filename)
	require.Equal(t, []byte("package demo;\r\n// 中文\r\npublic class Service {\r\n @Deprecated\r\n public String getPushSchedule(String 名称) {\r\n  return \"预约\";\r\n }\r\n}\r\n"), original)
	view, err := f.knowledge.GetSourceFile(f.ctx, hits[0].KnowledgeID)
	require.NoError(t, err)
	require.Equal(t, string(original), view.Content)
	require.Equal(t, f.sha, view.CommitSHA)
	require.Equal(t, "src/Service.java", view.Path)
	require.NotEmpty(t, view.FileVersionID)
	require.NotEmpty(t, view.Symbols)
	// Optional UI demonstration artifacts come only from public read results.
	if directory := os.Getenv("SOURCE_TEST_DEMO_DIR"); directory != "" {
		finished, readErr := f.service.GetSyncLog(f.ctx, log.ID)
		require.NoError(t, readErr)
		var progress types.SyncResult
		require.NoError(t, json.Unmarshal(finished.Result, &progress))
		require.NoError(t, os.MkdirAll(directory, 0755))
		fileJSON, encodeErr := json.Marshal(map[string]any{"data": view})
		require.NoError(t, encodeErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "source-file.json"), fileJSON, 0600))
		runJSON, encodeErr := json.Marshal(progress.Source)
		require.NoError(t, encodeErr)
		require.NoError(t, os.WriteFile(filepath.Join(directory, "source-run.json"), runJSON, 0600))
	}
	pinned, err := f.knowledge.GetSourceFile(f.ctx, hits[0].KnowledgeID, view.FileVersionID)
	require.NoError(t, err)
	require.Equal(t, view.CommitSHA, pinned.CommitSHA)
	_, err = f.knowledge.GetSourceFile(f.ctx, hits[0].KnowledgeID, uuid.NewString())
	require.Error(t, err)
	foreign := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2))
	_, err = f.knowledge.GetSourceFile(foreign, hits[0].KnowledgeID)
	require.Error(t, err)
	// Revoking a source hides its identity as well as its body on public reads.
	require.NoError(t, f.db.Exec("UPDATE data_sources SET deleted_at=now() WHERE id=?", f.ds.ID).Error)
	_, err = f.knowledge.GetKnowledgeByID(f.ctx, hits[0].KnowledgeID)
	require.Error(t, err)
}

func TestSourceSearchHonorsFileTenantAndTagScopes(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	fileID := hits[0].KnowledgeID
	for _, keywordsOnly := range []bool{true, false} {
		params := types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, KnowledgeIDs: []string{fileID}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly}
		hits, err = f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, err)
		require.NotEmpty(t, hits)
		params.KnowledgeIDs = []string{uuid.NewString()}
		hits, err = f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
		require.NoError(t, err)
		require.Empty(t, hits)
	}
	foreign := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2))
	_, err = f.kbs.HybridSearch(foreign, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.Error(t, err)
	_, err = f.kbs.HybridSearch(f.ctx, uuid.NewString(), types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.Error(t, err)
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "排班"}
	require.NoError(t, f.db.Create(tag).Error)
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, fileID, []string{tag.ID}))
	for _, keywordsOnly := range []bool{true, false} {
		for _, tagID := range []string{tag.ID, uuid.NewString()} {
			params := types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, TagIDs: []string{tagID}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly}
			hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, params)
			require.NoError(t, err)
			if tagID == tag.ID {
				require.NotEmpty(t, hits, "both index routes must honor document tags")
			} else {
				require.Empty(t, hits)
			}
		}
	}
	for _, tagID := range []string{tag.ID, uuid.NewString()} {
		grep := agenttools.NewSourceAwareGrepChunksTool(f.db, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, TagIDs: []string{tagID}}})
		result, err := grep.Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
		require.NoError(t, err)
		if tagID == tag.ID {
			require.Greater(t, result.Data["result_count"].(int), 0)
		} else {
			require.Equal(t, 0, result.Data["result_count"])
		}
	}
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, fileID, nil))
	for _, keywordsOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, TagIDs: []string{tag.ID}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly})
		require.NoError(t, err)
		require.Empty(t, hits, "removing a file tag must affect both indexes without rebuilding the snapshot")
	}
}

func TestSourceSearchSeparatesSamePathAcrossRepositorySources(t *testing.T) {
	f := newJavaSourceFixture(t)
	second := &types.DataSource{ID: uuid.NewString(), TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID, Name: "independent second source", Type: f.ds.Type, Status: types.DataSourceStatusPaused, Config: append(types.JSON{}, f.ds.Config...)}
	_, err := f.service.CreateDataSource(f.ctx, second)
	require.NoError(t, err)
	for _, ds := range []*types.DataSource{f.ds, second} {
		log, err := f.service.ManualSync(f.ctx, ds.ID)
		require.NoError(t, err)
		payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
		require.NoError(t, err)
		require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	}
	all, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, all, 2)
	require.NotEqual(t, all[0].KnowledgeID, all[1].KnowledgeID, "same path in two sources must retain separate file identities")
	for _, keywordsOnly := range []bool{true, false} {
		for _, id := range []string{f.ds.ID, second.ID, uuid.NewString()} {
			hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1, SourceIDs: []string{id}, DisableVectorMatch: keywordsOnly, DisableKeywordsMatch: !keywordsOnly})
			require.NoError(t, err)
			if id != f.ds.ID && id != second.ID {
				require.Empty(t, hits)
				continue
			}
			require.Len(t, hits, 1)
			require.Equal(t, id, hits[0].Metadata["datasource_id"], "repository scope must apply before topK")
		}
	}
}

func TestSourceInvalidEmbeddingNeverPublishes(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedVector = []float32{0, 0, 0}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)), "zero")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, finished.Status, "retryable indexing failure remains recoverable")
	require.Contains(t, finished.ErrorMessage, "zero")
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
}

func TestSourceUnreadableSelectedJavaPreventsPublication(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/Broken.java": {0xff, 0xfe, 'c', 0, 'l', 0}})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)), "not readable")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
	require.Contains(t, finished.ErrorMessage, "not readable")
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(finished.Result, &progress))
	require.True(t, progress.Source.Snapshot.ManifestComplete)
	require.Equal(t, 3, progress.Source.Snapshot.MemberCount)
	require.Empty(t, progress.Source.Members, "unreadable fixed-target members are rejected before staging")
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
}

type sourceNoObjectStorage struct{ interfaces.FileService }

func (sourceNoObjectStorage) GetFile(context.Context, string) (io.ReadCloser, error) {
	return nil, os.ErrNotExist
}

func TestSourceKeywordIndexFailureNeverPublishesFirstSnapshot(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	}()
	defer func() { close(f.embedRelease); <-done }()
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("source embedding boundary not reached")
	}
	// Controlled failure of the actual external keyword index, after preflight.
	require.NoError(t, f.db.Exec("DROP INDEX embeddings_search_idx").Error)
	f.embedRelease <- struct{}{}
	require.ErrorContains(t, <-done, "keyword index")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, finished.Status, "retryable publication failure remains recoverable")
	require.Contains(t, finished.ErrorMessage, "keyword index")
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(finished.Result, &progress))
	require.Equal(t, "failed", progress.Source.Snapshot.State)
	for _, member := range progress.Source.Members {
		if member.Status != "parsed" {
			continue
		}
		chunks, listErr := f.chunks.ListChunksByKnowledgeID(f.ctx, member.SourceFileID)
		require.NoError(t, listErr)
		require.Empty(t, chunks)
	}
	// A damaged enabled flag cannot turn a failed manifest into publication.
	require.NoError(t, f.db.Exec("UPDATE chunks SET is_enabled=true; UPDATE embeddings SET is_enabled=true").Error)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, DisableKeywordsMatch: true, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, hits)
	for _, member := range progress.Source.Members {
		if member.Status != "parsed" {
			continue
		}
		chunks, listErr := f.chunks.ListChunksByKnowledgeID(f.ctx, member.SourceFileID)
		require.NoError(t, listErr)
		require.Empty(t, chunks)
	}
	// Ordinary knowledge rows and enabled flags cannot bypass typed publication.
	for _, member := range progress.Source.Members {
		if member.Status == "parsed" {
			require.NoError(t, f.db.Create(&types.Knowledge{ID: member.SourceFileID, TenantID: 1, KnowledgeBaseID: f.kb.ID, Type: types.KnowledgeTypeSource, Title: member.Path, CustomMetadata: types.JSON(`{}`)}).Error)
		}
	}
	grep := agenttools.NewSourceAwareGrepChunksTool(f.db, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}})
	grepped, err := grep.Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.True(t, grepped.Success)
	require.Equal(t, 0, grepped.Data["result_count"])
}

func TestSourceStagingChunksCannotBeReadWhileEmbeddingIsPending(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, _ := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	}()
	defer func() { close(f.embedRelease); <-done }()
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("source embedding did not reach the controlled boundary")
	}
	running, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(running.Result, &progress))
	require.Equal(t, "indexing", progress.Source.Snapshot.State)
	var fileID string
	for _, member := range progress.Source.Members {
		if member.Status == "parsed" {
			fileID = member.SourceFileID
		}
	}
	require.NotEmpty(t, fileID)
	chunks, err := f.chunks.ListChunksByKnowledgeID(f.ctx, fileID)
	require.NoError(t, err)
	require.Empty(t, chunks, "staging must be absent from the public chunk list, even for its stable file ID")
	results, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10})
	require.NoError(t, err)
	require.Empty(t, results)
	// Release through a separate send-compatible channel so cleanup remains safe
	// when an assertion fails before publication.
	f.embedRelease <- struct{}{}
	require.NoError(t, <-done)
	chunks, err = f.chunks.ListChunksByKnowledgeID(f.ctx, fileID)
	require.NoError(t, err)
	require.NotEmpty(t, chunks)
}

type javaSourceFixture struct {
	ctx                     context.Context
	db                      *gorm.DB
	service                 *DataSourceService
	kbs                     interfacesKnowledgeBaseService
	ds                      *types.DataSource
	kb                      *types.KnowledgeBase
	sha                     string
	chunks                  interfaces.ChunkService
	knowledge               interfaces.KnowledgeService
	embedStarted            chan struct{}
	embedRelease            chan struct{}
	embedVector             []float32
	embeddingForText        func(string) []float32
	embedCount              atomic.Int64
	parseCount              atomic.Int64
	parseStarted            chan struct{}
	parseRelease            chan struct{}
	advanceFiles            func(map[string][]byte) string
	forcePush               func() string
	gitlabBranchMissing     bool
	gitlabTokenInvalid      bool
	gitTransportUnavailable bool
	modelService            interfaces.ModelService
	advanceJava             func(string) string
	shares                  interfaces.KBShareService
	agentShares             interfaces.AgentShareService
}

// A local alias keeps the fixture's public boundary explicit.
type interfacesKnowledgeBaseService interface {
	HybridSearch(context.Context, string, types.SearchParams) ([]*types.SearchResult, error)
}

func newJavaSourceFixture(t *testing.T, extraFiles ...map[string][]byte) *javaSourceFixture {
	t.Helper()
	f := &javaSourceFixture{}
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	python := os.Getenv("SOURCE_TEST_PYTHON")
	cache := os.Getenv("SOURCE_PARSER_CACHE")
	if dsn == "" || python == "" || cache == "" {
		t.Fatal("integration requires SOURCE_TEST_POSTGRES_DSN, SOURCE_TEST_PYTHON and prefetched SOURCE_PARSER_CACHE")
	}
	address, err := url.Parse(dsn)
	require.NoError(t, err)
	// These fixtures can create/drop schemas only in the dedicated test database.
	require.Equal(t, "/source_test", address.Path)
	require.Equal(t, "127.0.0.1", address.Hostname())
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	schema := "source_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE EXTENSION IF NOT EXISTS vector; CREATE EXTENSION IF NOT EXISTS pg_search").Error)
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		sqlDB, _ := admin.DB()
		_ = sqlDB.Close()
	})
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { sqlDB, _ := db.DB(); _ = sqlDB.Close() })
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}, &types.Model{}, &types.DataSource{}, &types.SyncLog{}, &types.TaskPendingOp{}, &types.SourceWikiAttempt{}, &types.KnowledgeTag{}, &types.KnowledgeTagRelation{}, &types.Organization{}, &types.OrganizationTenantMember{}, &types.KnowledgeBaseShare{}))
	require.NoError(t, db.Exec(`CREATE TABLE embeddings (
		id BIGSERIAL PRIMARY KEY, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ,
		source_id TEXT NOT NULL, source_type INTEGER NOT NULL, chunk_id TEXT, knowledge_id TEXT,
		knowledge_base_id TEXT, tag_id TEXT, content TEXT NOT NULL, dimension INTEGER NOT NULL,
		embedding HALFVEC NOT NULL, is_enabled BOOLEAN DEFAULT TRUE);
		CREATE INDEX embeddings_search_idx ON embeddings USING bm25 (id, content, knowledge_base_id, knowledge_id, tag_id) WITH (key_field='id');
		CREATE INDEX embeddings_test_vector_idx ON embeddings USING hnsw ((embedding::halfvec(3)) halfvec_cosine_ops) WHERE dimension=3;`).Error)

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	sourceMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000102_source_snapshots.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(sourceMigration)).Error)
	leaseMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000103_source_read_leases.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(leaseMigration)).Error)
	incrementalMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000104_source_incremental_artifacts.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(incrementalMigration)).Error)
	coordinationMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000108_source_sync_coordination.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(coordinationMigration)).Error)
	sourceWikiOutboxMigration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000109_source_wiki_outbox_acceptance.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(sourceWikiOutboxMigration)).Error)
	parser := exec.Command(python, filepath.Join(root, "sourceparser", "server.py"), "--host", "127.0.0.1", "--port", "0")
	parser.Env = append(os.Environ(), "SOURCE_PARSER_CACHE="+cache)
	stdout, err := parser.StdoutPipe()
	require.NoError(t, err)
	parser.Stderr = os.Stderr
	require.NoError(t, parser.Start())
	t.Cleanup(func() { _ = parser.Process.Kill(); _ = parser.Wait() })
	startup := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			startup <- scanner.Text()
		} else {
			startup <- ""
		}
	}()
	var ready struct {
		Port int `json:"port"`
	}
	select {
	case line := <-startup:
		require.NoError(t, json.Unmarshal([]byte(line), &ready))
	case <-time.After(10 * time.Second):
		t.Fatal("real Java parser did not start")
	}
	parserAddress, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", ready.Port))
	require.NoError(t, err)
	forward := httputil.NewSingleHostReverseProxy(parserAddress)
	parserProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/v1/parse" {
			f.parseCount.Add(1)
			if f.parseStarted != nil {
				select {
				case f.parseStarted <- struct{}{}:
				default:
				}
				select {
				case <-f.parseRelease:
				case <-r.Context().Done():
					return
				}
			}
		}
		forward.ServeHTTP(w, r)
	}))
	t.Cleanup(parserProxy.Close)
	t.Setenv("SOURCE_PARSER_URL", parserProxy.URL)
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.embedCount.Add(1)
		var request struct {
			Input []string `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		if f.embedStarted != nil {
			select {
			case f.embedStarted <- struct{}{}:
			default:
			}
			select {
			case <-f.embedRelease:
			case <-r.Context().Done():
				return
			}
		}
		items := make([]map[string]any, len(request.Input))
		vector := f.embedVector
		if vector == nil {
			vector = []float32{1, 0, 0}
		}
		for i := range items {
			if f.embeddingForText != nil {
				vector = f.embeddingForText(request.Input[i])
			}
			items[i] = map[string]any{"index": i, "embedding": vector}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(modelServer.Close)
	tenant := &types.Tenant{ID: 1, Name: "source integration", Business: "test", RetrieverEngines: types.RetrieverEngines{Engines: types.GetRetrieverEngineMapping()["postgres"]}}
	require.NoError(t, db.Create(tenant).Error)
	model := &types.Model{ID: uuid.NewString(), TenantID: 1, Name: "source-test-model", Type: types.ModelTypeEmbedding, Source: types.ModelSourceRemote, Status: types.ModelStatusActive,
		Parameters: types.ModelParameters{BaseURL: modelServer.URL, Provider: "openai", InterfaceType: "openai", EmbeddingParameters: types.EmbeddingParameters{Dimension: 3}}}
	require.NoError(t, db.Create(model).Error)
	kb := &types.KnowledgeBase{ID: uuid.NewString(), TenantID: 1, Name: "Java source", Type: "document", EmbeddingModelID: model.ID,
		IndexingStrategy: types.IndexingStrategy{KeywordEnabled: true, VectorEnabled: true}}
	require.NoError(t, db.Create(kb).Error)
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	ctx, err = access.WithKBTaskWrite(ctx, kb, 1)
	require.NoError(t, err)

	repoDir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, e := cmd.CombinedOutput()
		require.NoError(t, e, string(out))
		return strings.TrimSpace(string(out))
	}
	git("init", "--initial-branch=main")
	git("config", "core.autocrlf", "false")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "user.name", "Source integration")
	require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "Service.java"), []byte("package demo;\r\n// 中文\r\npublic class Service {\r\n @Deprecated\r\n public String getPushSchedule(String 名称) {\r\n  return \"预约\";\r\n }\r\n}\r\n"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(repoDir, "README.md"), []byte("outside chosen Java scope\n"), 0644))
	for _, files := range extraFiles {
		for name, content := range files {
			target := filepath.Join(repoDir, filepath.FromSlash(name))
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			require.NoError(t, os.WriteFile(target, content, 0644))
		}
	}
	git("add", ".")
	git("commit", "-m", "Java source fixture")
	sha := git("rev-parse", "HEAD")
	f.advanceFiles = func(changes map[string][]byte) string {
		for name, content := range changes {
			target := filepath.Join(repoDir, filepath.FromSlash(name))
			if content == nil {
				require.NoError(t, os.Remove(target))
				continue
			}
			require.NoError(t, os.MkdirAll(filepath.Dir(target), 0755))
			require.NoError(t, os.WriteFile(target, content, 0644))
		}
		git("add", ".")
		git("commit", "-m", "Complete manifest change")
		sha = git("rev-parse", "HEAD")
		return sha
	}
	f.advanceJava = func(content string) string {
		return f.advanceFiles(map[string][]byte{"src/Service.java": []byte(content)})
	}
	f.forcePush = func() string {
		git("checkout", "--orphan", "force-push")
		require.NoError(t, os.RemoveAll(filepath.Join(repoDir, "src")))
		require.NoError(t, os.MkdirAll(filepath.Join(repoDir, "src"), 0755))
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "ForcePushed.java"), []byte("class ForcePushed { int forcePushToken() { return 2; } }\n"), 0644))
		git("add", ".")
		git("commit", "-m", "Force-pushed unrelated history")
		sha = git("rev-parse", "HEAD")
		git("branch", "-f", "main", sha)
		git("checkout", "main")
		git("branch", "-D", "force-push")
		return sha
	}
	var gitlabServer *httptest.Server
	gitlabServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/user":
			fmt.Fprint(w, `{"id":1}`)
		case "/api/v4/personal_access_tokens/self":
			if f.gitlabTokenInvalid {
				http.Error(w, "invalid token", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"active":true,"scopes":["read_api","read_repository"]}`)
		case "/api/v4/projects/123":
			fmt.Fprintf(w, `{"id":123,"http_url_to_repo":%q}`, gitlabServer.URL+"/repo.git")
		case "/api/v4/projects/123/repository/branches/main":
			if f.gitlabBranchMissing {
				http.Error(w, "branch not found", http.StatusNotFound)
				return
			}
			fmt.Fprintf(w, `{"name":"main","commit":{"id":%q}}`, sha)
		case "/repo.git/info/refs", "/repo.git/git-upload-pack":
			if f.gitTransportUnavailable {
				http.Error(w, "repository unavailable", http.StatusBadGateway)
				return
			}
			args := []string{"upload-pack", "--stateless-rpc"}
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
				fmt.Fprint(w, "001e# service=git-upload-pack\n0000")
				args = append(args, "--advertise-refs")
			} else {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			}
			cmd := exec.Command("git", append(args, repoDir)...)
			cmd.Stdin, cmd.Stdout = r.Body, w
			require.NoError(t, cmd.Run())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gitlabServer.Close)
	config, err := json.Marshal(map[string]any{"type": "gitlab", "credentials": map[string]any{"base_url": gitlabServer.URL, "access_token": "fixture-token"},
		"settings": map[string]any{"content_mode": "source", "projects": []any{map[string]any{"project_id": "123", "ref": "main", "paths": []string{"src"}}}}})
	require.NoError(t, err)
	ds := &types.DataSource{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: kb.ID, Name: "Java fixture", Type: "gitlab", Status: types.DataSourceStatusPaused, Config: types.JSON(config)}
	dsRepo := repository.NewDataSourceRepository(db)
	require.NoError(t, dsRepo.Create(ctx, ds))
	engines := retriever.NewRetrieveEngineRegistry(nil, nil)
	require.NoError(t, engines.Register(retriever.NewKVHybridRetrieveEngine(pgrepo.NewSourceAwarePostgresRetrieveEngineRepository(db), types.PostgresRetrieverEngineType)))
	kbRepo := repository.NewKnowledgeBaseRepository(db)
	models := repository.NewModelRepository(db)
	modelService := NewModelService(models, kbRepo, nil, nil, nil, nil)
	f.shares = NewKBShareService(repository.NewKBShareRepository(db), repository.NewOrganizationRepository(db), kbRepo, repository.NewSourceAwareKnowledgeRepository(db), repository.NewSourceAwareChunkRepository(db), nil)
	f.agentShares = NewAgentShareService(repository.NewAgentShareRepository(db), repository.NewTenantDisabledSharedAgentRepository(db), repository.NewOrganizationRepository(db), repository.NewCustomAgentRepository(db), repository.NewUserRepository(db), nil)
	kbs := NewKnowledgeBaseService(kbRepo, repository.NewSourceAwareKnowledgeRepository(db), repository.NewSourceAwareChunkRepository(db), nil, f.shares, modelService, engines, nil, repository.NewTenantRepository(db), nil, nil, nil, nil, nil, nil, dsRepo, repository.NewSyncLogRepository(db), nil, nil, nil, nil, f.agentShares)
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(gitlab.NewConnector()))
	sourceSnapshots := repository.NewSourceSnapshotRepository(db)
	svc := NewDataSourceService(dsRepo, repository.NewSyncLogRepository(db), nil, kbs, kbDeleteTaskEnqueuer{}, registry, datasource.NewScheduler(dsRepo, repository.NewSyncLogRepository(db), kbDeleteTaskEnqueuer{}, sourceSnapshots), repository.NewTenantRepository(db), nil, nil, engines, nil, models, sourceSnapshots, modelService).(*DataSourceService)
	f.ctx, f.db, f.service, f.kbs, f.ds, f.kb, f.sha = ctx, db, svc, kbs, ds, kb, sha
	f.modelService = modelService
	f.chunks = NewChunkService(repository.NewSourceAwareChunkRepository(db), repository.NewSourceAwareKnowledgeRepository(db), kbRepo, modelService, engines, nil, nil, nil, kbs)
	f.knowledge = &knowledgeService{repo: repository.NewSourceAwareKnowledgeRepository(db), kbService: kbs, kbShareService: f.shares, chunkRepo: repository.NewSourceAwareChunkRepository(db), chunkService: f.chunks, modelService: modelService, retrieveEngine: engines, task: kbDeleteTaskEnqueuer{}, fileSvc: sourceNoObjectStorage{}, tagRepo: repository.NewKnowledgeTagRepository(db)}
	return f
}
