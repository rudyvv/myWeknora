//go:build integration

package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceManualAndCronPublishAndNotifyWikiWithoutWebhook(t *testing.T) {
	f := newJavaSourceFixture(t)
	require.False(t, f.service.gitLabWebhookExperimental)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log)))
	checkPublished := func(sha string) {
		published, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, f.ds.ID)
		require.NoError(t, err)
		require.NotNil(t, published.Snapshot)
		require.Equal(t, sha, published.Snapshot.CommitSHA)
		var count int64
		require.NoError(t, f.db.Table("source_publication_outbox").Where(
			"data_source_id=? AND snapshot_id=? AND event_type=?", f.ds.ID, published.Snapshot.ID, "source.wiki.update").Count(&count).Error)
		require.EqualValues(t, 1, count, "each publication retains its durable Wiki update notification")
	}
	checkPublished(f.sha)
	sha := f.advanceJava("package demo; public class Service { int scheduledRevision() { return 2; } }\n")
	require.NoError(t, f.db.Model(&types.DataSource{}).Where("id=?", f.ds.ID).Update("sync_schedule", "*/1 * * * * *").Error)
	tasks := make(chan *asynq.Task, 16)
	scheduler := datasource.NewScheduler(repository.NewDataSourceRepository(f.db), repository.NewSyncLogRepository(f.db),
		sourceTestTaskEnqueuer{tasks: tasks}, f.service.sourceSnapshots)
	require.NoError(t, scheduler.Start(context.Background()))
	t.Cleanup(scheduler.Stop)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	var scheduledTask *asynq.Task
	for scheduledTask == nil {
		select {
		case task := <-tasks:
			if task.Type() != types.TypeDataSourceSync {
				continue // Startup relays the existing Wiki notification as well.
			}
			var payload types.DataSourceSyncPayload
			require.NoError(t, json.Unmarshal(task.Payload(), &payload))
			require.Equal(t, "schedule", payload.Trigger)
			scheduledTask = task
		case <-deadline.C:
			t.Fatal("cron did not dispatch source sync with Webhook unavailable")
		}
	}
	scheduler.Stop()
	require.NoError(t, f.service.ProcessSync(f.ctx, scheduledTask))
	checkPublished(sha)
}

func TestSuspendGitLabWebhookMigrationKeepsManualWorkAndPublication(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log)))
	before, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, f.ds.ID)
	require.NoError(t, err)
	enabled, secret := true, "retained-fixture-secret"
	require.NoError(t, f.service.syncLogRepo.(interfaces.GitLabWebhookRepository).SetGitLabWebhookConfig(f.ctx, f.ds.ID, 1,
		types.GitLabWebhookUpdate{Enabled: &enabled, Secret: &secret}))
	hook := &types.SyncLog{ID: uuid.NewString(), DataSourceID: f.ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning, StartedAt: time.Now().UTC()}
	manual := &types.SyncLog{ID: uuid.NewString(), DataSourceID: f.ds.ID, TenantID: 1, Status: types.SyncLogStatusQueued, StartedAt: time.Now().UTC()}
	for _, item := range []struct {
		log     *types.SyncLog
		trigger string
	}{{hook, "gitlab_webhook"}, {manual, "manual"}} {
		require.NoError(t, f.service.syncLogRepo.Create(f.ctx, item.log))
		require.NoError(t, f.db.Exec(`INSERT INTO source_sync_runs(sync_log_id,data_source_id,tenant_id,config_generation,delivery_generation,trigger)
			SELECT ?,data_source_id,tenant_id,config_generation,1,? FROM source_sync_states WHERE data_source_id=?`, item.log.ID, item.trigger, f.ds.ID).Error)
	}
	require.NoError(t, f.db.Exec(`UPDATE source_sync_states SET active_sync_log_id=?,pending_sync_log_id=?,pending_trigger='manual',
		pending_delivery_generation=1,lease_owner=?,lease_expires_at=now()+interval '2 minutes' WHERE data_source_id=?`, hook.ID, manual.ID, uuid.NewString(), f.ds.ID).Error)
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000122_suspend_gitlab_push_webhooks.up.sql"))
	require.NoError(t, err)
	for i := 0; i < 2; i++ { // Repeat to verify the shutdown migration is idempotent.
		require.NoError(t, f.db.Exec(string(migration)).Error)
	}
	config, err := f.service.syncLogRepo.(interfaces.GitLabWebhookRepository).GetGitLabWebhookConfig(f.ctx, f.ds.ID, 1)
	require.NoError(t, err)
	require.False(t, config.Enabled)
	finished, err := f.service.syncLogRepo.FindByID(f.ctx, hook.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, finished.Status)
	queued, err := f.service.syncLogRepo.FindByID(f.ctx, manual.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, queued.Status)
	var state struct {
		ActiveSyncLogID  *string
		PendingSyncLogID *string
		LeaseOwner       *string
		PendingTrigger   string
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&state).Error)
	require.Nil(t, state.ActiveSyncLogID)
	require.Nil(t, state.LeaseOwner)
	require.NotNil(t, state.PendingSyncLogID)
	require.Equal(t, manual.ID, *state.PendingSyncLogID)
	require.Equal(t, "manual", state.PendingTrigger)
	dispatches, err := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository).RecoverSourceTriggers(f.ctx, f.ds)
	require.NoError(t, err)
	for _, dispatch := range dispatches {
		require.NotEqual(t, "gitlab_webhook", dispatch.Trigger)
	}
	after, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, before.Snapshot.ID, after.Snapshot.ID)
	var wikiCount int64
	require.NoError(t, f.db.Table("source_publication_outbox").Where("snapshot_id=?", before.Snapshot.ID).Count(&wikiCount).Error)
	require.EqualValues(t, 1, wikiCount)
	// Also withdraw a pending webhook without fencing an unrelated active run.
	require.NoError(t, f.db.Exec(`UPDATE source_sync_states SET active_sync_log_id=?,pending_sync_log_id=?,pending_trigger='gitlab_webhook',lease_owner=? WHERE data_source_id=?`, manual.ID, hook.ID, "manual-lease", f.ds.ID).Error)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&state).Error)
	require.NotNil(t, state.ActiveSyncLogID)
	require.Equal(t, manual.ID, *state.ActiveSyncLogID)
	require.Nil(t, state.PendingSyncLogID)
	require.NotNil(t, state.LeaseOwner)
	require.Equal(t, "manual-lease", *state.LeaseOwner)
}
