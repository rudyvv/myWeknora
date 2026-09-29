//go:build integration

package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/gitlab"
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
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
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
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(finished.Result, &progress))
	require.True(t, progress.Source.Snapshot.ManifestComplete)
	require.Len(t, progress.Source.Members, 3)
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
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
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
	ctx          context.Context
	db           *gorm.DB
	service      *DataSourceService
	kbs          interfacesKnowledgeBaseService
	ds           *types.DataSource
	kb           *types.KnowledgeBase
	sha          string
	chunks       interfaces.ChunkService
	knowledge    interfaces.KnowledgeService
	embedStarted chan struct{}
	embedRelease chan struct{}
	embedVector  []float32
	advanceJava  func(string) string
	shares       interfaces.KBShareService
	agentShares  interfaces.AgentShareService
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
	require.NoError(t, db.AutoMigrate(&types.Tenant{}, &types.KnowledgeBase{}, &types.Knowledge{}, &types.Chunk{}, &types.Model{}, &types.DataSource{}, &types.SyncLog{}, &types.KnowledgeTag{}, &types.KnowledgeTagRelation{}, &types.Organization{}, &types.OrganizationTenantMember{}, &types.KnowledgeBaseShare{}))
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
	t.Setenv("SOURCE_PARSER_URL", fmt.Sprintf("http://127.0.0.1:%d", ready.Port))
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)

	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	f.advanceJava = func(content string) string {
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, "src", "Service.java"), []byte(content), 0644))
		git("add", ".")
		git("commit", "-m", "Advance external GitLab fixture")
		sha = git("rev-parse", "HEAD")
		return sha
	}
	var gitlabServer *httptest.Server
	gitlabServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/user":
			fmt.Fprint(w, `{"id":1}`)
		case "/api/v4/personal_access_tokens/self":
			fmt.Fprint(w, `{"active":true,"scopes":["read_api","read_repository"]}`)
		case "/api/v4/projects/123":
			fmt.Fprintf(w, `{"id":123,"http_url_to_repo":%q}`, gitlabServer.URL+"/repo.git")
		case "/api/v4/projects/123/repository/branches/main":
			fmt.Fprintf(w, `{"name":"main","commit":{"id":%q}}`, sha)
		case "/repo.git/info/refs", "/repo.git/git-upload-pack":
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
	svc := NewDataSourceService(dsRepo, repository.NewSyncLogRepository(db), nil, kbs, kbDeleteTaskEnqueuer{}, registry, datasource.NewScheduler(dsRepo, repository.NewSyncLogRepository(db), kbDeleteTaskEnqueuer{}), repository.NewTenantRepository(db), nil, nil, engines, nil, models, repository.NewSourceSnapshotRepository(db), modelService).(*DataSourceService)
	f.ctx, f.db, f.service, f.kbs, f.ds, f.kb, f.sha = ctx, db, svc, kbs, ds, kb, sha
	f.chunks = NewChunkService(repository.NewSourceAwareChunkRepository(db), repository.NewSourceAwareKnowledgeRepository(db), kbRepo, modelService, engines, nil, nil, nil, kbs)
	f.knowledge = &knowledgeService{repo: repository.NewSourceAwareKnowledgeRepository(db), kbService: kbs, kbShareService: f.shares, chunkRepo: repository.NewSourceAwareChunkRepository(db), chunkService: f.chunks, modelService: modelService, retrieveEngine: engines, task: kbDeleteTaskEnqueuer{}, fileSvc: sourceNoObjectStorage{}, tagRepo: repository.NewKnowledgeTagRepository(db)}
	return f
}
