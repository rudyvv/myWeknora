//go:build integration

package service_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourceWikiHTTPGeneratesReadsAndScopesRegisteredEvidence(t *testing.T) {
	f := service.NewSourceIntegrationFixture(t, map[string][]byte{"src/Other.java": []byte("class Other { String getPushSchedule() {return \"other\";} }\n")})
	f.Sync()
	require.NoError(t, f.DB.AutoMigrate(&types.WikiFolder{}, &types.WikiPage{}, &types.WikiPageRevision{}))
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000105_source_wiki.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.DB.Exec(string(migration)).Error)
	batchMigration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.DB.Exec(string(batchMigration)).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		reply := `{"title":"Scheduling module","summary":"Both implementations return a schedule.","sections":[{"text":"Service and Other supply schedules.","evidence_ids":["e001","e002"],"uncertain":false}]}`
		if req.Messages[0].Content == "source_wiki_qa" {
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"total_tokens":40}}`, reply)
	}))
	t.Cleanup(server.Close)
	models := service.NewModelService(repository.NewModelRepository(f.DB), repository.NewKnowledgeBaseRepository(f.DB), nil, nil, nil, nil)
	model := &types.Model{ID: uuid.NewString(), TenantID: 1, Name: "wiki-http-fixture", Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceRemote, Status: types.ModelStatusActive, Parameters: types.ModelParameters{BaseURL: server.URL, Provider: "openai", InterfaceType: "openai"}}
	require.NoError(t, models.CreateModel(f.Ctx, model))
	f.KB.SummaryModelID = model.ID
	f.KB.IndexingStrategy.WikiEnabled = true
	require.NoError(t, f.DB.Save(f.KB).Error)
	kb := f.KBs
	wiki := service.NewWikiPageService(repository.NewWikiPageRepository(f.DB), nil, kb, nil, nil)
	generator := service.NewSourceWikiService(wiki, kb, f.Knowledge, models, f.DB)
	h := handler.NewWikiPageHandler(wiki, kb, nil, nil, nil, generator)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(1))
		c.Request = c.Request.WithContext(f.Ctx)
		c.Next()
	})
	group := r.Group("/api/v1/knowledgebase/:kb_id/wiki", h.WikiReadScope)
	group.POST("/source/generate", h.GenerateSourceModule)
	group.GET("/source/evidence", h.ReadSourceWikiEvidence)
	group.GET("/source/attempts", h.ListSourceWikiAttempts)
	group.GET("/source/batches", h.ListSourceWikiBatches)
	group.GET("/source/batches/:batch_id", h.GetSourceWikiBatch)
	group.GET("/source/coverage", h.ListSourceWikiCoverage)
	group.GET("/pages/*slug", h.GetPage)
	group.GET("/index", h.GetIndex)
	group.GET("/search", h.SearchPages)
	baseURL := "/api/v1/knowledgebase/" + f.KB.ID + "/wiki"
	invoke := func(method, path string, body []byte) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, baseURL+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		out := httptest.NewRecorder()
		r.ServeHTTP(out, req)
		return out
	}
	req, _ := json.Marshal(types.SourceWikiGenerateRequest{SourceID: f.Source.ID, ModulePath: "src", Title: "Scheduling module"})
	out := invoke(http.MethodPost, "/source/generate", req)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	var response struct {
		Data types.SourceWikiAttempt `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out.Body.Bytes(), &response))
	require.Equal(t, "ready", response.Data.Status)
	page, err := wiki.GetPageBySlug(f.Ctx, f.KB.ID, response.Data.Slug)
	require.NoError(t, err)
	require.Len(t, page.SourceProvenance.Evidence, 2)
	var publication types.SourcePublication
	require.NoError(t, f.DB.Where("data_source_id = ?", f.Source.ID).Take(&publication).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := &types.SourceWikiBatch{
		ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.KB.ID, SourceID: f.Source.ID, SnapshotID: publication.SnapshotID,
		SourceConfigFingerprint: strings.Repeat("a", 64), SourceUpdatedAt: f.Source.UpdatedAt,
		ModelID: model.ID, ModelSettingsFingerprint: strings.Repeat("b", 64), ModelContextWindow: 65536,
		MaxCompletionTokens: types.SourceWikiBatchMaxCompletionTokens, Status: "running", Phase: "skeleton",
		MaxCalls: types.SourceWikiBatchMaxCalls, MaxTokens: types.SourceWikiBatchMaxTokens,
		MaxElapsedMS: types.SourceWikiBatchMaxElapsed.Milliseconds(), MaxInitialTopics: types.SourceWikiBatchMaxInitialTopics,
		SkeletonMaxCalls: types.SourceWikiBatchSkeletonMaxCalls, SkeletonMaxTokens: types.SourceWikiBatchSkeletonMaxTokens,
		QAMaxCalls: types.SourceWikiBatchQAMaxCalls, QAMaxTokens: types.SourceWikiBatchQAMaxTokens,
		DeadlineAt: now.Add(types.SourceWikiBatchMaxElapsed), CreatedAt: now, UpdatedAt: now,
	}
	batchLedger := repository.NewSourceWikiBatchLedger(f.DB)
	require.NoError(t, batchLedger.Create(f.Ctx, batch))
	topics := []types.SourceWikiTopic{
		{SourceID: f.Source.ID, SnapshotID: publication.SnapshotID, TopicKey: "system", Kind: "system", Title: "System overview", Priority: 120, Status: "planned"},
		{SourceID: f.Source.ID, SnapshotID: publication.SnapshotID, TopicKey: "module/src", Kind: "module", ModulePath: "src", Title: "src", Priority: 90, Status: "planned"},
	}
	require.NoError(t, batchLedger.SavePlan(f.Ctx, batch.ID, topics, now.Add(time.Second)))
	out = invoke(http.MethodGet, "/source/coverage?source_id="+url.QueryEscape(f.Source.ID), nil)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	require.Contains(t, out.Body.String(), `"topic_key":"module/src"`)
	require.Contains(t, out.Body.String(), `"status":"planned"`)
	out = invoke(http.MethodGet, "/source/batches?source_id="+url.QueryEscape(f.Source.ID), nil)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	require.Contains(t, out.Body.String(), batch.ID)
	out = invoke(http.MethodGet, "/source/batches/"+batch.ID+"?source_id="+url.QueryEscape(f.Source.ID), nil)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	require.Contains(t, out.Body.String(), `"deadline_at"`)
	query := "?slug=" + url.QueryEscape(page.Slug) + "&evidence_id=e001&version=1"
	out = invoke(http.MethodGet, "/source/evidence"+query, nil)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	require.Contains(t, out.Body.String(), page.SourceProvenance.Evidence[0].CommitSHA)
	out = invoke(http.MethodGet, "/source/evidence"+query+"&knowledge_ids="+page.SourceProvenance.Evidence[0].KnowledgeID, nil)
	require.Equal(t, http.StatusNotFound, out.Code)
	out = invoke(http.MethodGet, "/source/attempts?source_ids="+f.Source.ID+"&knowledge_ids="+page.SourceProvenance.Evidence[0].KnowledgeID, nil)
	require.Equal(t, http.StatusOK, out.Code, out.Body.String())
	require.JSONEq(t, `{"data":[]}`, out.Body.String())
	out = invoke(http.MethodGet, "/pages/"+page.Slug+"?knowledge_ids="+page.SourceProvenance.Evidence[0].KnowledgeID, nil)
	require.Equal(t, http.StatusNotFound, out.Code, out.Body.String())
	out = invoke(http.MethodGet, "/source/evidence?slug="+url.QueryEscape(page.Slug)+"&evidence_id=e001&version=999", nil)
	require.Equal(t, http.StatusNotFound, out.Code)
}
