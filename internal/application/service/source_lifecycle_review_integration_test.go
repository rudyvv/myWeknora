//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type t19DataSourceLifecycle interface {
	UnbindDataSource(context.Context, string) (*types.DataSource, error)
	ClearSource(context.Context, string, bool, string) (*types.DataSource, error)
	RetryClearSource(context.Context, string, string) (*types.DataSource, error)
}

func t19LifecycleService(t *testing.T, f *javaSourceFixture) t19DataSourceLifecycle {
	t.Helper()
	lifecycle, ok := any(f.service).(t19DataSourceLifecycle)
	require.True(t, ok, "data source service must expose the approved source lifecycle methods")
	return lifecycle
}

func ensureT19LifecycleSchema(t *testing.T, f *javaSourceFixture) {
	t.Helper()
	hasBindingState := f.db.Migrator().HasColumn("data_sources", "source_binding_state")
	hasQueryEnabled := f.db.Migrator().HasColumn("data_sources", "source_query_enabled")
	hasCleanupTable := f.db.Migrator().HasTable("source_cleanup_operations")
	if hasCleanupTable {
		require.True(t, hasBindingState, "a loaded lifecycle migration must include source_binding_state")
		require.True(t, hasQueryEnabled, "a loaded lifecycle migration must include source_query_enabled")
		return
	}
	require.Equal(t, hasBindingState, hasQueryEnabled, "fixture must not have a partial lifecycle-column schema")
	if hasBindingState {
		// AutoMigrate may have created these lifecycle columns from the current
		// model. This is a per-test schema; remove only those two columns so the
		// production migration below is exercised without modification.
		require.NoError(t, f.db.Migrator().DropColumn("data_sources", "source_binding_state"))
		require.NoError(t, f.db.Migrator().DropColumn("data_sources", "source_query_enabled"))
	}

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000117_source_lifecycle_cleanup.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	require.True(t, f.db.Migrator().HasColumn("data_sources", "source_binding_state"))
	require.True(t, f.db.Migrator().HasColumn("data_sources", "source_query_enabled"))
	require.True(t, f.db.Migrator().HasTable("source_cleanup_operations"))
}

func t19BeginSourceRead(t *testing.T, f *javaSourceFixture, sourceIDs ...string) (context.Context, func()) {
	t.Helper()
	targets := types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID,
		TenantID: 1, SourceIDs: sourceIDs,
	}}
	reader, ok := f.kbs.(interfaces.SourceReadService)
	require.True(t, ok, "knowledge-base service must expose question-scoped source reads")
	ctx, release, err := reader.BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	return ctx, release
}

func t19Search(t *testing.T, f *javaSourceFixture, ctx context.Context) []*types.SearchResult {
	t.Helper()
	results, err := f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 20})
	require.NoError(t, err)
	return results
}

func t19HitForSource(t *testing.T, results []*types.SearchResult, sourceID string) *types.SearchResult {
	t.Helper()
	for _, result := range results {
		if result.Metadata["datasource_id"] == sourceID {
			return result
		}
	}
	require.FailNow(t, "search did not return a hit for the requested source", "source_id=%s", sourceID)
	return nil
}

func t19Grep(t *testing.T, f *javaSourceFixture, ctx context.Context, sourceIDs ...string) *types.ToolResult {
	t.Helper()
	targets := types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID,
		TenantID: 1, SourceIDs: sourceIDs,
	}}
	result, err := agenttools.NewSourceAwareGrepChunksTool(f.db, targets).Execute(ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	return result
}

func t19RequireSearchDenied(t *testing.T, f *javaSourceFixture, ctx context.Context) {
	t.Helper()
	results, err := f.kbs.HybridSearch(ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 20})
	if err == nil {
		require.Empty(t, results, "a revoked source lease must not return source chunks")
	}
}

func t19RequireGrepDenied(t *testing.T, f *javaSourceFixture, ctx context.Context, sourceIDs ...string) {
	t.Helper()
	targets := types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID,
		TenantID: 1, SourceIDs: sourceIDs,
	}}
	result, err := agenttools.NewSourceAwareGrepChunksTool(f.db, targets).Execute(ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	if err == nil {
		require.Equal(t, 0, result.Data["result_count"], "a revoked source lease must not return grep matches")
	}
}

func TestSourceLifecycleUnbindRetainsPublishedReadsAndBlocksManualSync(t *testing.T) {
	f := newJavaSourceFixture(t)
	ensureT19LifecycleSchema(t, f)
	syncSourceFixture(t, f)

	before := t19Search(t, f, f.ctx)
	require.NotEmpty(t, before)
	hit := t19HitForSource(t, before, f.ds.ID)
	readCtx, release := t19BeginSourceRead(t, f, f.ds.ID)
	defer release()

	var syncLogsBefore int64
	require.NoError(t, f.db.Model(&types.SyncLog{}).Where("data_source_id=?", f.ds.ID).Count(&syncLogsBefore).Error)
	unbound, err := t19LifecycleService(t, f).UnbindDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, unbound)
	var lifecycleState struct {
		BindingState string `gorm:"column:source_binding_state"`
		QueryEnabled bool   `gorm:"column:source_query_enabled"`
	}
	require.NoError(t, f.db.Table("data_sources").Select("source_binding_state, source_query_enabled").Where("id=?", f.ds.ID).Take(&lifecycleState).Error)
	require.Equal(t, types.SourceBindingUnbound, lifecycleState.BindingState)
	require.True(t, lifecycleState.QueryEnabled, "unbind must preserve published source query rights")

	file, err := f.knowledge.GetSourceFile(readCtx, hit.KnowledgeID)
	require.NoError(t, err)
	require.Contains(t, file.Content, "预约")
	retained := t19Search(t, f, readCtx)
	require.Equal(t, hit.ID, t19HitForSource(t, retained, f.ds.ID).ID)
	chunk, err := f.chunks.GetChunkByIDOnly(readCtx, hit.ID)
	require.NoError(t, err)
	require.Equal(t, hit.ID, chunk.ID)

	_, err = f.service.ManualSync(f.ctx, f.ds.ID)
	require.Error(t, err, "unbound sources must reject a new manual sync")
	var syncLogsAfter int64
	require.NoError(t, f.db.Model(&types.SyncLog{}).Where("data_source_id=?", f.ds.ID).Count(&syncLogsAfter).Error)
	require.Equal(t, syncLogsBefore, syncLogsAfter, "rejected manual sync must not create a sync log")
}

func TestSourceLifecycleClearRevokesOnlySelectedSourceFromExistingReads(t *testing.T) {
	f := newJavaSourceFixture(t)
	ensureT19LifecycleSchema(t, f)

	secondRepoURL := newT19GitLabRepository(t, "456", map[string]string{
		"src/Service.java": "package demo;\npublic class Service { public String getPushSchedule() { return \"B_REPOSITORY_MARKER\"; } }\n",
	})
	other := &types.DataSource{
		ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID,
		Name: "independent lifecycle repository", Type: f.ds.Type,
		Status: types.DataSourceStatusActive,
		Config: t19GitLabSourceConfig(t, secondRepoURL, "456"),
	}
	_, err := f.service.CreateDataSource(f.ctx, other)
	require.NoError(t, err)
	syncSourceFixture(t, f, f.ds.ID)
	syncSourceFixture(t, f, other.ID)

	allBefore := t19Search(t, f, f.ctx)
	require.Len(t, allBefore, 2, "same relative paths from separate GitLab projects must remain distinct")
	aHit := t19HitForSource(t, allBefore, f.ds.ID)
	bHit := t19HitForSource(t, allBefore, other.ID)
	require.Equal(t, "src/Service.java", aHit.Metadata["source_path"])
	require.Equal(t, "src/Service.java", bHit.Metadata["source_path"])

	aCtx, releaseA := t19BeginSourceRead(t, f, f.ds.ID)
	defer releaseA()
	mixedCtx, releaseMixed := t19BeginSourceRead(t, f, f.ds.ID, other.ID)
	defer releaseMixed()
	require.Equal(t, aHit.ID, t19HitForSource(t, t19Search(t, f, aCtx), f.ds.ID).ID)
	require.Len(t, t19Search(t, f, mixedCtx), 2)
	require.Equal(t, 1, t19Grep(t, f, aCtx, f.ds.ID).Data["result_count"])
	require.Equal(t, 2, t19Grep(t, f, mixedCtx, f.ds.ID, other.ID).Data["result_count"])
	_, err = f.knowledge.GetSourceFile(aCtx, aHit.KnowledgeID)
	require.NoError(t, err)
	_, err = f.knowledge.GetSourceFile(mixedCtx, bHit.KnowledgeID)
	require.NoError(t, err)
	_, err = f.chunks.GetChunkByIDOnly(mixedCtx, aHit.ID)
	require.NoError(t, err)
	_, err = f.chunks.GetChunkByIDOnly(mixedCtx, bHit.ID)
	require.NoError(t, err)

	_, err = t19LifecycleService(t, f).ClearSource(f.ctx, f.ds.ID, true, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)

	_, err = f.knowledge.GetSourceFile(aCtx, aHit.KnowledgeID)
	require.Error(t, err, "the pre-clear A-only handle must no longer read A's raw source")
	_, err = f.chunks.GetChunkByIDOnly(aCtx, aHit.ID)
	require.Error(t, err, "the pre-clear A-only handle must no longer read A's chunks")
	t19RequireSearchDenied(t, f, aCtx)
	t19RequireGrepDenied(t, f, aCtx, f.ds.ID)

	remaining := t19Search(t, f, mixedCtx)
	require.Len(t, remaining, 1, "the same A+B handle must retain only the independent B source")
	require.Equal(t, bHit.ID, remaining[0].ID)
	require.Equal(t, other.ID, remaining[0].Metadata["datasource_id"])
	require.Equal(t, 1, t19Grep(t, f, mixedCtx, f.ds.ID, other.ID).Data["result_count"])
	_, err = f.knowledge.GetSourceFile(mixedCtx, aHit.KnowledgeID)
	require.Error(t, err, "the mixed handle must not keep A's raw source readable")
	bFile, err := f.knowledge.GetSourceFile(mixedCtx, bHit.KnowledgeID)
	require.NoError(t, err)
	require.Contains(t, bFile.Content, "B_REPOSITORY_MARKER")
	_, err = f.chunks.GetChunkByIDOnly(mixedCtx, aHit.ID)
	require.Error(t, err, "the mixed handle must not keep A's chunks readable")
	bChunk, err := f.chunks.GetChunkByIDOnly(mixedCtx, bHit.ID)
	require.NoError(t, err)
	require.Equal(t, bHit.ID, bChunk.ID)
}

func TestSourceLifecycleRepeatedClearKeepsActiveIntentAndFenceStable(t *testing.T) {
	f := newJavaSourceFixture(t)
	ensureT19LifecycleSchema(t, f)
	syncSourceFixture(t, f)
	readCtx, release := t19BeginSourceRead(t, f, f.ds.ID)
	defer release()
	require.NotEmpty(t, t19Search(t, f, readCtx))

	lifecycle := t19LifecycleService(t, f)
	_, err := lifecycle.ClearSource(f.ctx, f.ds.ID, true, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)
	firstOperation, firstState := t19CleanupAndGeneration(t, f)
	require.Equal(t, types.SourceCleanupPending, firstOperation.Status)
	require.Equal(t, firstState.FencingToken, firstOperation.FencingToken)
	t19RequireSearchDenied(t, f, readCtx)

	_, err = lifecycle.ClearSource(f.ctx, f.ds.ID, true, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)
	secondOperation, secondState := t19CleanupAndGeneration(t, f)
	require.Equal(t, firstOperation.ID, secondOperation.ID, "repeated acceptance must return the same durable cleanup intent")
	require.Equal(t, firstOperation.FencingToken, secondOperation.FencingToken, "the active operation fence must not become stale")
	require.Equal(t, firstOperation.Status, secondOperation.Status)
	require.Equal(t, firstState.ConfigGeneration, secondState.ConfigGeneration, "idempotent clear must not advance the active generation")
	require.Equal(t, firstState.FencingToken, secondState.FencingToken, "idempotent clear must not advance the active fence")
	t19RequireSearchDenied(t, f, readCtx)
	var queryEnabled bool
	require.NoError(t, f.db.Table("data_sources").Select("source_query_enabled").Where("id=?", f.ds.ID).Take(&queryEnabled).Error)
	require.False(t, queryEnabled, "repeated clear must not restore query visibility")
}

type t19CleanupOperationState struct {
	ID           string `gorm:"column:id"`
	Status       string `gorm:"column:status"`
	FencingToken int64  `gorm:"column:fencing_token"`
}

type t19SyncGeneration struct {
	ConfigGeneration int64 `gorm:"column:config_generation"`
	FencingToken     int64 `gorm:"column:fencing_token"`
}

func t19CleanupAndGeneration(t *testing.T, f *javaSourceFixture) (t19CleanupOperationState, t19SyncGeneration) {
	t.Helper()
	var operation t19CleanupOperationState
	require.NoError(t, f.db.Table("source_cleanup_operations").Select("id, status, fencing_token").Where("data_source_id=?", f.ds.ID).Take(&operation).Error)
	var state t19SyncGeneration
	require.NoError(t, f.db.Table("source_sync_states").Select("config_generation, fencing_token").Where("data_source_id=?", f.ds.ID).Take(&state).Error)
	return operation, state
}

func t19GitLabSourceConfig(t *testing.T, baseURL, projectID string) types.JSON {
	t.Helper()
	config, err := json.Marshal(map[string]any{
		"type":        "gitlab",
		"credentials": map[string]any{"base_url": baseURL, "access_token": "fixture-token"},
		"settings": map[string]any{
			"content_mode": "source",
			"projects":     []any{map[string]any{"project_id": projectID, "ref": "main", "paths": []string{"src"}}},
		},
	})
	require.NoError(t, err)
	return types.JSON(config)
}

func newT19GitLabRepository(t *testing.T, projectID string, files map[string]string) string {
	t.Helper()
	repoDir := t.TempDir()
	runGit := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, string(output))
		return strings.TrimSpace(string(output))
	}
	runGit("init", "--initial-branch=main")
	runGit("config", "core.autocrlf", "false")
	runGit("config", "user.email", "fixture@example.invalid")
	runGit("config", "user.name", "T19 lifecycle integration")
	for name, content := range files {
		path := filepath.Join(repoDir, filepath.FromSlash(name))
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0644))
	}
	runGit("add", ".")
	runGit("commit", "-m", "Independent source lifecycle repository")
	sha := runGit("rev-parse", "HEAD")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/user":
			_, _ = io.WriteString(w, `{"id":1}`)
		case "/api/v4/personal_access_tokens/self":
			_, _ = io.WriteString(w, `{"active":true,"scopes":["read_api","read_repository"]}`)
		case "/api/v4/projects/" + projectID:
			_ = json.NewEncoder(w).Encode(map[string]any{"http_url_to_repo": serverRepositoryURL(r, "/repo.git")})
		case "/api/v4/projects/" + projectID + "/repository/branches/main":
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "main", "commit": map[string]string{"id": sha}})
		case "/repo.git/info/refs", "/repo.git/git-upload-pack":
			args := []string{"upload-pack", "--stateless-rpc"}
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
				_, _ = io.WriteString(w, "001e# service=git-upload-pack\n0000")
				args = append(args, "--advertise-refs")
			} else {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			}
			cmd := exec.Command("git", append(args, repoDir)...)
			cmd.Stdin, cmd.Stdout, cmd.Stderr = r.Body, w, io.Discard
			if err := cmd.Run(); err != nil {
				http.Error(w, "git upload-pack failed", http.StatusInternalServerError)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func serverRepositoryURL(r *http.Request, path string) string {
	return fmt.Sprintf("http://%s%s", r.Host, path)
}
