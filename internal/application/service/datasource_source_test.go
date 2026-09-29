package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	pgrepo "github.com/Tencent/WeKnora/internal/application/repository/retriever/postgres"
	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/datasource/connector/gitlab"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/stretchr/testify/require"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestSourceParseArtifactKeyChangesWithParserVersionAndIsDeterministic(t *testing.T) {
	raw := []byte("from pkg.booking import helper\n")
	oldRulesKey := sourceParseArtifactKey("src/reservation.py", raw, "source-pack-1.19.0-rules-3-old", "settings-v1")
	newRulesKey := sourceParseArtifactKey("src/reservation.py", raw, "source-pack-1.19.0-rules-4-new", "settings-v1")
	require.NotEqual(t, oldRulesKey, newRulesKey)
	require.Equal(t, newRulesKey, sourceParseArtifactKey("src/reservation.py", raw, "source-pack-1.19.0-rules-4-new", "settings-v1"))
}

func TestUpdateDataSourceRejectsSourceModeWithoutSpecifiedBranch(t *testing.T) {
	stored := &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: "kb-one", Type: types.ConnectorTypeGitLab,
		Config: types.JSON(`{"type":"gitlab","settings":{"projects":[{"project_id":"123"}]}}`)}
	repo := newKBDeleteDSRepo("kb-one", stored)
	svc := &DataSourceService{dsRepo: repo, scheduler: datasource.NewScheduler(repo, nil, nil)}
	incoming := *stored
	incoming.Config = types.JSON(`{"type":"gitlab","settings":{"content_mode":"source","projects":[{"project_id":"123"}]}}`)

	_, err := svc.UpdateDataSource(context.Background(), &incoming)
	require.ErrorContains(t, err, "specified branch")
	unchanged, err := svc.GetDataSource(context.Background(), stored.ID)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"gitlab","settings":{"projects":[{"project_id":"123"}]}}`, string(unchanged.Config))
}

func TestSourceSettingsAreVersionedWhenSavedWithoutChangingDocumentDefaults(t *testing.T) {
	stored := &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: "kb-one", Type: "gitlab", Config: types.JSON(`{"settings":{"projects":[{"project_id":"123"}]}}`)}
	repo := &sourceSettingsRepo{kbDeleteDSRepo: newKBDeleteDSRepo("kb-one", stored)}
	svc := &DataSourceService{dsRepo: repo, scheduler: datasource.NewScheduler(repo, nil, nil)}
	incoming := *stored
	incoming.Config = types.JSON(`{"settings":{"content_mode":"source","projects":[{"project_id":"123","ref":"main","paths":["src"]}],"exclude_paths":["src/generated"]}}`)
	_, err := svc.UpdateDataSource(context.Background(), &incoming)
	require.NoError(t, err)
	saved, err := svc.GetDataSource(context.Background(), stored.ID)
	require.NoError(t, err)
	config, err := saved.ParseConfig()
	require.NoError(t, err)
	version, ok := config.Settings["rules_version"].(string)
	require.True(t, ok)
	require.Contains(t, version, "v1:")
	incoming = *saved
	incoming.Config = types.JSON(`{"settings":{"content_mode":"source","projects":[{"project_id":"123","ref":"main","paths":["src"]}],"exclude_paths":["src/other"]}}`)
	_, err = svc.UpdateDataSource(context.Background(), &incoming)
	require.NoError(t, err)
	saved, err = svc.GetDataSource(context.Background(), stored.ID)
	require.NoError(t, err)
	config, err = saved.ParseConfig()
	require.NoError(t, err)
	require.NotEqual(t, version, config.Settings["rules_version"])
	incoming = *saved
	incoming.Config = types.JSON(`{"settings":{"projects":[{"project_id":"123"}]}}`)
	_, err = svc.UpdateDataSource(context.Background(), &incoming)
	require.NoError(t, err)
	saved, err = svc.GetDataSource(context.Background(), stored.ID)
	require.NoError(t, err)
	config, err = saved.ParseConfig()
	require.NoError(t, err)
	mode, err := datasource.ContentMode(config)
	require.NoError(t, err)
	require.Equal(t, datasource.ContentModeDocument, mode)
	require.NotContains(t, config.Settings, "rules_version")
}

type sourceSettingsRepo struct{ *kbDeleteDSRepo }

func (r *sourceSettingsRepo) Update(_ context.Context, ds *types.DataSource) error {
	copy := *ds
	copy.Config = append(types.JSON(nil), ds.Config...)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byKB[ds.KnowledgeBaseID] = []*types.DataSource{&copy}
	return nil
}

func TestManualSyncDoesNotSendSourceModeThroughDocumentIngestion(t *testing.T) {
	stored := &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: "kb-one", Type: "gitlab", Status: types.DataSourceStatusActive,
		Config: types.JSON(`{"settings":{"content_mode":"source","projects":[{"project_id":"123","ref":"main"}]}}`)}
	svc := &DataSourceService{dsRepo: newKBDeleteDSRepo("kb-one", stored), syncLogRepo: &kbDeleteSyncLogRepo{}, taskEnqueuer: kbDeleteTaskEnqueuer{}}
	log, err := svc.ManualSync(context.Background(), stored.ID)
	require.ErrorContains(t, err, "source ingestion pipeline is not available")
	require.Nil(t, log)
}

func TestSourceCredentialsCanRotateAfterSelectedBranchIsDeleted(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "replacement-token", r.Header.Get("PRIVATE-TOKEN"))
		switch r.URL.Path {
		case "/api/v4/user":
			fmt.Fprint(w, `{"id":1}`)
		case "/api/v4/personal_access_tokens/self":
			fmt.Fprint(w, `{"active":true,"scopes":["read_api","read_repository"]}`)
		case "/api/v4/projects/123":
			fmt.Fprint(w, `{"id":123}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	config, err := json.Marshal(map[string]interface{}{
		"type": "gitlab", "credentials": map[string]string{"base_url": server.URL, "access_token": "expired-token"},
		"settings": map[string]interface{}{"content_mode": "source", "projects": []interface{}{map[string]interface{}{"project_id": "123", "ref": "deleted-branch"}}},
	})
	require.NoError(t, err)
	stored := &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: "kb-one", Type: "gitlab", Config: types.JSON(config)}
	repo := &sourceSettingsRepo{kbDeleteDSRepo: newKBDeleteDSRepo("kb-one", stored)}
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(gitlab.NewConnector()))
	kbRepo := newFakeKBRepo()
	kbRepo.rows["kb-one"] = &types.KnowledgeBase{ID: "kb-one", TenantID: 1}
	svc := &DataSourceService{dsRepo: repo, connectorRegistry: registry, kbService: &knowledgeBaseService{repo: kbRepo}}
	_, err = svc.UpdateDataSourceCredentials(context.Background(), stored.ID, map[string]interface{}{"base_url": server.URL, "access_token": "replacement-token"})
	require.NoError(t, err, "branch availability must not block read-only credential rotation")
	saved, err := svc.GetDataSource(context.Background(), stored.ID)
	require.NoError(t, err)
	parsed, err := saved.ParseConfig()
	require.NoError(t, err)
	require.Equal(t, "replacement-token", parsed.Credentials["access_token"])
	_, err = svc.PreviewSource(context.Background(), stored.ID, nil)
	require.Error(t, err, "the deleted branch must still fail source preflight")
}

func TestPreviewSourceUsesFixedCommitAndReportsActualFileAvailability(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
	repoDir := t.TempDir()
	git := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repoDir
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, string(out))
		return out
	}
	git("init", "--initial-branch=main")
	git("config", "user.email", "fixture@example.invalid")
	git("config", "user.name", "Source fixture")
	for path, content := range map[string][]byte{
		"dist/Business.java":  []byte("class Business {}\r\n"),
		"plugins/Custom.java": []byte("// @Generated\nclass Custom {}\n"),
		"vendor/library.js":   []byte("export const thirdParty = true;\n"),
		"assets/model.bin":    []byte("version https://git-lfs.github.com/spec/v1\noid sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\nsize 123\n"),
		"src/legacy.java":     {0xff, 0x80, 0x81},
		"src/utf16.java":      {0xff, 0xfe, 0x41, 0x00},
		"src/huge.java":       []byte(strings.Repeat("class Huge {}\n", 30)),
		"src/业务.java":         []byte("class Business {}\r\n"),
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(repoDir, path)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(repoDir, path), content, 0o600))
	}
	git("add", ".")
	git("commit", "-m", "source fixture")
	sha := string(git("rev-parse", "HEAD"))[:40]
	git("update-index", "--add", "--cacheinfo", "160000", sha, "external/module")
	git("commit", "-m", "submodule fixture")
	sha = string(git("rev-parse", "HEAD"))[:40]
	var server *httptest.Server
	writeToken := false
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/personal_access_tokens/self":
			if writeToken {
				fmt.Fprint(w, `{"active":true,"scopes":["api"]}`)
			} else {
				fmt.Fprint(w, `{"active":true,"scopes":["read_api","read_repository"]}`)
			}
		case "/api/v4/projects/123":
			require.Equal(t, "fixture-token", r.Header.Get("PRIVATE-TOKEN"))
			fmt.Fprintf(w, `{"id":123,"path_with_namespace":"team/project","http_url_to_repo":%q}`, server.URL+"/repo.git")
		case "/api/v4/projects/123/repository/branches/main":
			fmt.Fprintf(w, `{"name":"main","commit":{"id":%q}}`, sha)
		case "/repo.git/info/refs", "/repo.git/git-upload-pack":
			user, token, ok := r.BasicAuth()
			require.True(t, ok)
			require.Equal(t, "oauth2", user)
			require.Equal(t, "fixture-token", token)
			args := []string{"upload-pack", "--stateless-rpc"}
			if r.Method == http.MethodGet {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
				fmt.Fprint(w, "001e# service=git-upload-pack\n0000")
				args = append(args, "--advertise-refs")
			} else {
				w.Header().Set("Content-Type", "application/x-git-upload-pack-result")
			}
			args = append(args, repoDir)
			cmd := exec.Command("git", args...)
			cmd.Stdin, cmd.Stdout = r.Body, w
			require.NoError(t, cmd.Run())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	config, err := json.Marshal(map[string]interface{}{
		"type": "gitlab", "credentials": map[string]string{"base_url": server.URL, "access_token": "fixture-token"},
		"settings": map[string]interface{}{"content_mode": "source", "projects": []interface{}{map[string]interface{}{"project_id": "123", "ref": "main"}}, "exclude_paths": []string{"vendor"}, "max_file_bytes": 200},
	})
	require.NoError(t, err)
	stored := &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: "kb-one", Type: "gitlab", Config: types.JSON(config)}
	registry := datasource.NewConnectorRegistry()
	require.NoError(t, registry.Register(gitlab.NewConnector()))
	kb := &types.KnowledgeBase{ID: "kb-one", TenantID: 1}
	kbRepo := newFakeKBRepo()
	kbRepo.rows[kb.ID] = kb
	svc := &DataSourceService{dsRepo: newKBDeleteDSRepo("kb-one", stored), connectorRegistry: registry,
		kbService: &knowledgeBaseService{repo: kbRepo}}
	preview, err := svc.PreviewSource(context.Background(), stored.ID, nil)
	require.NoError(t, err)
	require.Equal(t, sha, preview.CommitSHA)
	require.NotEmpty(t, preview.RulesVersion)
	require.False(t, preview.CanSync)
	require.Contains(t, preview.Warnings, "Wiki is disabled for this knowledge base")
	files := make(map[string]types.SourcePreviewFile)
	for _, file := range preview.Files {
		files[file.Path] = file
	}
	require.Equal(t, "included", files["dist/Business.java"].Status)
	require.True(t, files["plugins/Custom.java"].Generated)
	require.Equal(t, "excluded", files["vendor/library.js"].Status)
	require.Equal(t, "lfs_unavailable", files["assets/model.bin"].Status)
	require.Equal(t, "unknown_encoding", files["src/legacy.java"].Status)
	require.Equal(t, "unsupported_encoding", files["src/utf16.java"].Status)
	require.Equal(t, "utf-16le", files["src/utf16.java"].Encoding)
	require.Equal(t, "oversize", files["src/huge.java"].Status)
	require.Equal(t, "included", files["src/业务.java"].Status)
	require.Equal(t, "submodule_unavailable", files["external/module"].Status)
	unchanged, err := svc.GetDataSource(context.Background(), stored.ID)
	require.NoError(t, err)
	require.Equal(t, types.JSON(config), unchanged.Config)
	require.False(t, kb.IsWikiEnabled())
	kb.IndexingStrategy.VectorEnabled = true
	kb.IndexingStrategy.KeywordEnabled = true
	kb.EmbeddingModelID = "not-an-installed-model"
	preview, err = svc.PreviewSource(context.Background(), stored.ID, nil)
	require.NoError(t, err)
	for _, check := range preview.Checks {
		if check.Name == "indexes" {
			require.False(t, check.Ready, "KB flags alone cannot verify the deployed index backend")
		}
	}
	connection, database, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = connection.Close() })
	pg, err := gorm.Open(pgdriver.New(pgdriver.Config{Conn: connection}), &gorm.Config{})
	require.NoError(t, err)
	engines := retriever.NewRetrieveEngineRegistry(nil, nil)
	require.NoError(t, engines.Register(retriever.NewKVHybridRetrieveEngine(pgrepo.NewPostgresRetrieveEngineRepository(pg), types.PostgresRetrieverEngineType)))
	svc.sourceRetrieve = engines
	ctx := context.WithValue(context.Background(), types.TenantInfoContextKey, &types.Tenant{ID: 1, RetrieverEngines: types.RetrieverEngines{Engines: types.GetRetrieverEngineMapping()["postgres"]}})
	database.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"vector", "bm25"}).AddRow(true, false))
	preview, err = svc.PreviewSource(ctx, stored.ID, nil)
	require.NoError(t, err)
	for _, check := range preview.Checks {
		if check.Name == "indexes" {
			require.False(t, check.Ready, "missing keyword extension must block source indexing")
		}
	}
	require.NoError(t, database.ExpectationsWereMet())
	database.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"vector", "bm25"}).AddRow(true, true))
	svc.sourceModels = sourcePreviewModels{model: nil}
	preview, err = svc.PreviewSource(ctx, stored.ID, nil)
	require.NoError(t, err)
	for _, check := range preview.Checks {
		if check.Name == "indexes" {
			require.False(t, check.Ready, "nonexistent embedding model cannot be ready")
		}
	}
	require.NoError(t, database.ExpectationsWereMet())
	parser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/health", r.URL.Path)
		fmt.Fprint(w, `{"ready":true,"parser_version":"fixture-java-1","languages":["java"]}`)
	}))
	t.Cleanup(parser.Close)
	t.Setenv("SOURCE_PARSER_URL", parser.URL)
	model := &types.Model{ID: kb.EmbeddingModelID, TenantID: 2, Type: types.ModelTypeEmbedding, Status: types.ModelStatusActive,
		Parameters: types.ModelParameters{EmbeddingParameters: types.EmbeddingParameters{Dimension: 3}}}
	svc.sourceModels = sourcePreviewModels{model: model}
	database.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"vector", "bm25"}).AddRow(true, true))
	preview, err = svc.PreviewSource(ctx, stored.ID, nil)
	require.NoError(t, err)
	for _, check := range preview.Checks {
		if check.Name == "indexes" {
			require.False(t, check.Ready, "another tenant's embedding model cannot be ready")
		}
	}
	model.TenantID = kb.TenantID
	database.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"vector", "bm25"}).AddRow(true, true))
	preview, err = svc.PreviewSource(ctx, stored.ID, nil)
	require.NoError(t, err)
	checks := map[string]bool{}
	for _, check := range preview.Checks {
		checks[check.Name] = check.Ready
	}
	require.True(t, checks["indexes"], "installed active embedding model and both extensions are ready")
	require.True(t, checks["parser"], "versioned Java health response is ready")
	require.False(t, preview.CanSync, "T01 cannot enable the not-yet-installed source ingestion pipeline")
	require.NoError(t, database.ExpectationsWereMet())
	writeToken = true
	_, err = svc.PreviewSource(context.Background(), stored.ID, nil)
	require.ErrorContains(t, err, "read-only")
}

type sourcePreviewModels struct {
	interfaces.ModelRepository
	model *types.Model
}

func (r sourcePreviewModels) GetByID(_ context.Context, _ uint64, _ string) (*types.Model, error) {
	return r.model, nil
}
