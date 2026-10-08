//go:build integration

package service

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestDeleteSourceRetainsSearchWikiAndCitations(t *testing.T) {
	f := newJavaSourceFixture(t)
	ensureT19LifecycleSchema(t, f)
	syncSourceFixture(t, f)
	hit := t19HitForSource(t, t19Search(t, f, f.ctx), f.ds.ID)
	wiki, generator := newSourceWikiFixture(t, f, t22SourceWikiResponse("RETAINED_AFTER_DELETE"))
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	oldRead, releaseOld := t19BeginSourceRead(t, f, f.ds.ID)
	defer releaseOld()
	queued, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	task := sourceTestSyncTask(t, f, queued)
	require.NoError(t, f.service.DeleteDataSource(f.ctx, f.ds.ID))
	listed, err := f.service.ListDataSources(f.ctx, f.kb.ID)
	require.NoError(t, err)
	require.Empty(t, listed)
	allIdentities, err := f.service.dsRepo.FindByKnowledgeBase(f.ctx, f.kb.ID)
	require.NoError(t, err)
	require.Len(t, allIdentities, 1, "whole-KB deletion can still enumerate retained identities")
	_, err = f.service.GetDataSource(f.ctx, f.ds.ID)
	require.Error(t, err, "deleted connection cannot be managed")
	var retained types.DataSource
	require.NoError(t, f.db.Where("id=?", f.ds.ID).Take(&retained).Error)
	require.Equal(t, types.DataSourceStatusDeleted, retained.Status)
	require.Equal(t, types.SourceBindingUnbound, retained.SourceBindingState)
	require.False(t, retained.DeletedAt.Valid, "retain the authorization identity rather than soft-deleting it")
	require.True(t, retained.SourceQueryEnabled)
	cfg, err := retained.ParseConfig()
	require.NoError(t, err)
	require.Empty(t, cfg.Credentials)
	for _, ctx := range []struct {
		name string
		old  bool
	}{{"existing read", true}, {"new read", false}} {
		readCtx := oldRead
		if !ctx.old {
			var release func()
			readCtx, release = t19BeginSourceRead(t, f, f.ds.ID)
			defer release()
		}
		require.Equal(t, hit.ID, t19HitForSource(t, t19Search(t, f, readCtx), f.ds.ID).ID, ctx.name)
		file, err := f.knowledge.GetSourceFile(readCtx, hit.KnowledgeID)
		require.NoError(t, err)
		require.Contains(t, file.Content, "getPushSchedule")
		chunk, err := f.chunks.GetChunkByIDOnly(readCtx, hit.ID)
		require.NoError(t, err)
		require.Equal(t, hit.ID, chunk.ID)
	}
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Contains(t, page.Content, "RETAINED_AFTER_DELETE_BODY")
	evidence, err := generator.ReadEvidence(f.ctx, f.kb.ID, attempt.Slug, 0, "e001")
	require.NoError(t, err)
	require.Contains(t, evidence.Content, "getPushSchedule")
	answerCtx, releaseWiki, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseWiki()
	page, err = wiki.GetPageBySlug(answerCtx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Contains(t, page.Content, "RETAINED_AFTER_DELETE_BODY")
	require.NoError(t, f.service.ProcessSync(f.ctx, task), "old deliveries must not revive a deleted source")
	finished, err := f.service.syncLogRepo.FindByID(f.ctx, queued.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusCanceled, finished.Status)
	_, err = f.service.ManualSync(f.ctx, f.ds.ID)
	require.Error(t, err)
	require.Error(t, f.service.ResumeDataSource(f.ctx, f.ds.ID))
	_, err = f.service.UpdateDataSourceCredentials(f.ctx, f.ds.ID, map[string]interface{}{"access_token": "new-token"})
	require.Error(t, err)
	stale := *f.ds
	stale.Status = types.DataSourceStatusActive
	require.Error(t, f.service.dsRepo.Update(f.ctx, &stale), "stale repository writes cannot restore a removed source")
	dispatches, err := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository).RecoverSourceTriggers(f.ctx, &retained)
	require.NoError(t, err)
	require.Empty(t, dispatches, "scheduler recovery cannot revive this source")
}

func TestDeleteSourceGroupRetainsEveryRepositoryPublication(t *testing.T) {
	f := newJavaSourceFixture(t)
	ensureT19LifecycleSchema(t, f)
	root := configureSourceProjectGroup(t, f)
	_, err := f.service.ManualSync(f.ctx, root.ID)
	require.NoError(t, err)
	logs, err := f.service.GetSyncLogs(f.ctx, root.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2)
	for _, log := range logs {
		require.NoError(t, processProjectLog(t, f, log))
	}
	before := t19Search(t, f, f.ctx)
	require.Len(t, before, 2)
	_, err = f.service.ManualSync(f.ctx, root.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.DeleteDataSource(f.ctx, root.ID))
	listed, err := f.service.ListDataSources(f.ctx, f.kb.ID)
	require.NoError(t, err)
	require.Empty(t, listed)
	after := t19Search(t, f, f.ctx)
	require.Len(t, after, 2)
	for _, project := range root.SourceProjects {
		hit := t19HitForSource(t, after, project.ID)
		file, err := f.knowledge.GetSourceFile(f.ctx, hit.KnowledgeID)
		require.NoError(t, err)
		require.Equal(t, f.sha, file.CommitSHA)
		var retained types.DataSource
		require.NoError(t, f.db.Where("id=?", project.ID).Take(&retained).Error)
		require.Equal(t, types.DataSourceStatusDeleted, retained.Status)
		require.True(t, retained.SourceQueryEnabled)
		var unfinished int64
		require.NoError(t, f.db.Model(&types.SyncLog{}).Where("data_source_id=? AND status IN ('running','queued')", project.ID).Count(&unfinished).Error)
		require.Zero(t, unfinished)
	}
}

func TestDeleteSourceFencesRunningPublication(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	previous, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, f.ds.ID)
	require.NoError(t, err)
	f.advanceJava("package demo; public class Service { int deletedRunMustNotPublish() { return 2; } }\n")
	f.parseStarted = make(chan struct{}, 1)
	f.parseRelease = make(chan struct{})
	deliveries := make(chan *asynq.Task, 2)
	f.service.taskEnqueuer = sourceTestTaskEnqueuer{tasks: deliveries}
	_, err = f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	var task *asynq.Task
	select {
	case task = <-deliveries:
	case <-time.After(2 * time.Second):
		t.Fatal("no delivery")
	}
	done := make(chan error, 1)
	go func() { done <- f.service.ProcessSync(f.ctx, task) }()
	select {
	case <-f.parseStarted:
	case <-time.After(20 * time.Second):
		close(f.parseRelease)
		t.Fatal("parser did not start")
	}
	deleteErr := f.service.DeleteDataSource(f.ctx, f.ds.ID)
	close(f.parseRelease)
	require.NoError(t, deleteErr)
	require.NoError(t, <-done)
	current, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, previous.Snapshot.ID, current.Snapshot.ID)
	require.NotEmpty(t, t19Search(t, f, f.ctx))
}
