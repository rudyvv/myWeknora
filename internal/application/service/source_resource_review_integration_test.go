//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceSelectedByteLimitKeepsPublishedSourceAndOtherKnowledgeReadable(t *testing.T) {
	initialService := []byte(`package demo; public class Service { public String stableMarker() { return "stableMarker"; } }` + "\n")
	otherSource := []byte(`package demo; public class Other { public String sharedSourceMarker() { return "sharedSourceMarker"; } }` + "\n")
	f := newJavaSourceFixture(t, map[string][]byte{
		"src/Service.java": initialService,
		"src/Other.java":   otherSource,
	})
	syncSourceFixture(t, f)
	oldCommit := f.sha
	oldHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "stableMarker")
	require.Len(t, oldHits, 1)
	oldHandle := oldHits[0].KnowledgeID
	oldView, err := f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err)
	require.Equal(t, oldCommit, oldView.CommitSHA)
	require.Contains(t, oldView.Content, "stableMarker")

	other := *f.ds
	other.ID, other.Name = uuid.NewString(), "independent small source"
	sourceResourceRegressionSetPaths(t, &other, []string{"src/Other.java"})
	_, err = f.service.CreateDataSource(f.ctx, &other)
	require.NoError(t, err)

	document := &types.Knowledge{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID,
		Type: "file", Title: "ordinary-handbook", FileName: "handbook.md", ParseStatus: types.ParseStatusCompleted,
	}
	require.NoError(t, f.db.Create(document).Error)
	documentChunk := &types.Chunk{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID,
		KnowledgeID: document.ID, Content: "ordinaryDocumentMarker", ChunkType: types.ChunkTypeText,
		IsEnabled: true, IndexStatus: "ready",
	}
	require.NoError(t, repository.NewChunkRepository(f.db).CreateChunks(f.ctx, []*types.Chunk{documentChunk}))
	require.NoError(t, f.db.Exec(`INSERT INTO embeddings
		(source_id,source_type,chunk_id,knowledge_id,knowledge_base_id,content,dimension,embedding,is_enabled)
		VALUES (?,1,?,?,?,?,3,'[1,0,0]',true)`,
		documentChunk.ID, documentChunk.ID, document.ID, f.kb.ID, documentChunk.Content).Error)

	policy := source.DefaultResourcePolicy()
	policy.MaxSelectedBytes = 256
	controller, err := source.NewResourceController(policy)
	require.NoError(t, err)
	f.service.sourceResources = controller

	largeService := []byte(fmt.Sprintf(
		`package demo; public class Service { public String changedMarker() { return "changedMarker"; } String payload = "%s"; }`+"\n",
		strings.Repeat("x", 512),
	))
	newCommit := f.advanceJava(string(largeService))
	require.NotEqual(t, oldCommit, newCommit)
	failedLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	processErr := f.service.ProcessSync(f.ctx, sourceResourceRegressionTask(t, f, f.ds.ID, failedLog.ID))
	require.ErrorIs(t, processErr, source.ErrResourceLimitExceeded)

	currentHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "stableMarker")
	require.Len(t, currentHits, 1, "a rejected commit must not replace the published source result")
	require.Equal(t, oldHandle, currentHits[0].KnowledgeID)
	changedHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "changedMarker")
	require.Empty(t, changedHits, "a rejected commit must not become searchable")
	currentView, err := f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err, "the prior published file handle must remain readable")
	require.Equal(t, oldCommit, currentView.CommitSHA)
	require.Contains(t, currentView.Content, "stableMarker")

	otherLog, err := f.service.ManualSync(f.ctx, other.ID)
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, sourceResourceRegressionTask(t, f, other.ID, otherLog.ID)),
		"the failed source run must release the shared run-admission slot")

	targets := types.SearchTargets{
		&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: f.kb.TenantID, SourceIDs: []string{other.ID}},
		&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, TenantID: f.kb.TenantID, KnowledgeIDs: []string{document.ID}},
	}
	readCtx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()

	otherHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "sharedSourceMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Len(t, otherHits, 1)
	require.Equal(t, other.ID, otherHits[0].Metadata["datasource_id"])
	documentHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "ordinaryDocumentMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Len(t, documentHits, 1)
	require.Equal(t, document.ID, documentHits[0].KnowledgeID)
	excludedSourceHits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText: "stableMarker", MatchCount: 10, DisableVectorMatch: true,
	})
	require.NoError(t, err)
	require.Empty(t, excludedSourceHits, "the mixed read scope must not widen to source A")
}

func TestCanceledPendingSourceAdmissionDoesNotEnterPipelineAndCanRunAgain(t *testing.T) {
	initialService := []byte(`package demo; public class Service { public String oldMarker() { return "oldMarker"; } }` + "\n")
	f := newJavaSourceFixture(t, map[string][]byte{"src/Service.java": initialService})
	syncSourceFixture(t, f)
	oldCommit := f.sha
	oldHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "oldMarker")
	require.Len(t, oldHits, 1)
	oldHandle := oldHits[0].KnowledgeID

	newCommit := f.advanceJava(`package demo; public class Service { public String newMarker() { return "newMarker"; } }` + "\n")
	require.NotEqual(t, oldCommit, newCommit)
	policy := source.DefaultResourcePolicy()
	controller, err := source.NewResourceController(policy)
	require.NoError(t, err)
	f.service.sourceResources = controller

	releaseAdmission, err := controller.AcquireRun(f.ctx)
	require.NoError(t, err)
	defer releaseAdmission()
	waitCtx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	started := make(chan struct{})
	finished := make(chan struct{})
	var processErr error
	pendingLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, pendingLog.Status)
	pendingTask := sourceResourceRegressionTask(t, f, f.ds.ID, pendingLog.ID)
	gitBefore := f.gitTransportRequests.Load()
	parseBefore := f.parseCount.Load()
	embedBefore := f.embedCount.Load()
	go func() {
		close(started)
		processErr = f.service.ProcessSync(waitCtx, pendingTask)
		close(finished)
	}()
	defer func() {
		cancel()
		select {
		case <-finished:
		case <-time.After(3 * time.Second):
			t.Error("pending source ProcessSync did not stop during cleanup")
		}
	}()
	<-started
	select {
	case <-finished:
		t.Fatalf("source processing finished while its only admission slot was held: %v", processErr)
	case <-time.After(250 * time.Millisecond):
	}
	observedLog, err := f.service.GetSyncLog(f.ctx, pendingLog.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusQueued, observedLog.Status,
		"the delivered run must remain pending until source admission is available")
	cancel()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("canceled source admission did not return within the fixture bound")
	}
	require.ErrorIs(t, processErr, context.Canceled)
	require.Equal(t, gitBefore, f.gitTransportRequests.Load(), "a pending run must not enter Git transport")
	require.Equal(t, parseBefore, f.parseCount.Load(), "a pending run must not enter Java parsing")
	require.Equal(t, embedBefore, f.embedCount.Load(), "a pending run must not call the embedding provider")

	oldView, err := f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err, "cancellation before admission must leave the published file readable")
	require.Equal(t, oldCommit, oldView.CommitSHA)
	require.Contains(t, oldView.Content, "oldMarker")
	require.Len(t, sourceResourceRegressionSearch(t, f, f.ds.ID, "oldMarker"), 1)
	require.Empty(t, sourceResourceRegressionSearch(t, f, f.ds.ID, "newMarker"))

	releaseAdmission()
	require.NoError(t, f.service.ProcessSync(f.ctx, pendingTask),
		"a later source run must be able to acquire the released slot")
	newHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "newMarker")
	require.Len(t, newHits, 1)
	require.Equal(t, newCommit, newHits[0].Metadata["commit_sha"])
	newView, err := f.knowledge.GetSourceFile(f.ctx, oldHandle)
	require.NoError(t, err)
	require.Equal(t, newCommit, newView.CommitSHA)
	require.Contains(t, newView.Content, "newMarker")
}

func sourceResourceRegressionSetPaths(t *testing.T, ds *types.DataSource, paths []string) {
	t.Helper()
	var config map[string]any
	require.NoError(t, json.Unmarshal(ds.Config, &config))
	settings, ok := config["settings"].(map[string]any)
	require.True(t, ok)
	projects, ok := settings["projects"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, projects)
	project, ok := projects[0].(map[string]any)
	require.True(t, ok)
	project["paths"] = paths
	updated, err := json.Marshal(config)
	require.NoError(t, err)
	ds.Config = types.JSON(updated)
}

func sourceResourceRegressionTask(t *testing.T, f *javaSourceFixture, sourceID, syncLogID string) *asynq.Task {
	t.Helper()
	payload, err := json.Marshal(types.DataSourceSyncPayload{
		DataSourceID: sourceID, TenantID: f.ds.TenantID, SyncLogID: syncLogID, Trigger: "manual",
	})
	require.NoError(t, err)
	return asynq.NewTask(types.TypeDataSourceSync, payload)
}

func sourceResourceRegressionSearch(t *testing.T, f *javaSourceFixture, sourceID, query string) []*types.SearchResult {
	t.Helper()
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
		QueryText: query, MatchCount: 10, DisableVectorMatch: true, SourceIDs: []string{sourceID},
	})
	require.NoError(t, err)
	return hits
}
