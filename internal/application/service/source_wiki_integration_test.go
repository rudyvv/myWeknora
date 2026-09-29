//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newSourceWikiFixture(t *testing.T, f *javaSourceFixture, response func(bool) string, override ...func(http.ResponseWriter, *http.Request, bool)) (interfaces.WikiPageService, interfaces.SourceWikiService) {
	t.Helper()
	require.NoError(t, f.db.AutoMigrate(&types.WikiFolder{}, &types.WikiPage{}, &types.WikiPageRevision{}))
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000105_source_wiki.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		qa := len(request.Messages) > 0 && request.Messages[0].Content == "source_wiki_qa"
		if len(override) > 0 {
			override[0](w, r, qa)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, response(qa))
	}))
	t.Cleanup(server.Close)
	models := NewModelService(repository.NewModelRepository(f.db), repository.NewKnowledgeBaseRepository(f.db), nil, nil, nil, nil)
	model := &types.Model{ID: uuid.NewString(), TenantID: 1, Name: "source-wiki-fixture", Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceRemote, Status: types.ModelStatusActive, Parameters: types.ModelParameters{BaseURL: server.URL, Provider: "openai", InterfaceType: "openai"}}
	require.NoError(t, models.CreateModel(f.ctx, model))
	f.kb.SummaryModelID = model.ID
	f.kb.IndexingStrategy.WikiEnabled = true
	require.NoError(t, f.db.Save(f.kb).Error)
	wiki := NewWikiPageService(repository.NewWikiPageRepository(f.db), nil, f.kbs.(interfaces.KnowledgeBaseService), nil, nil)
	return wiki, NewSourceWikiService(wiki, f.kbs.(interfaces.KnowledgeBaseService), f.knowledge, models, f.db)
}

func TestSourceWikiModuleGeneratesValidatedCardThroughExistingWikiTools(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"The service returns the push schedule.","sections":[{"text":"getPushSchedule returns the push schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Equal(t, 1, page.Version)
	require.Len(t, page.SourceProvenance.Evidence, 1)
	require.Equal(t, f.sha, page.SourceProvenance.Evidence[0].CommitSHA)
	read := agenttools.NewWikiReadPageTool(wiki, f.knowledge, agenttools.NewWikiScopesFromKBIDs([]string{f.kb.ID}), nil)
	args, _ := json.Marshal(map[string]any{"slugs": []string{page.Slug}})
	result, err := read.Execute(f.ctx, args)
	require.NoError(t, err)
	require.Contains(t, result.Output, "getPushSchedule returns")
	evidence, err := generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 0, "e001")
	require.NoError(t, err)
	require.Equal(t, f.sha, evidence.CommitSHA)
	require.Contains(t, evidence.Content, "getPushSchedule")
}

func TestSourceWikiCurrentAnswersStopAfterPublicationAndRevisionsKeepTheirOwnEvidence(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	req := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	first, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", first.Status)
	firstPage, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	oldSHA := f.sha
	newSHA := f.advanceJava("new schedule")
	syncSourceFixture(t, f)
	read := agenttools.NewWikiReadPageTool(wiki, f.knowledge, agenttools.NewWikiScopesFromKBIDs([]string{f.kb.ID}), nil)
	args, _ := json.Marshal(map[string]any{"slugs": []string{first.Slug}})
	result, err := read.Execute(f.ctx, args)
	require.NoError(t, err)
	require.NotContains(t, result.Output, "getPushSchedule returns a schedule")
	stale, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Equal(t, "stale", stale.SourceProvenance.State)
	second, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", second.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, second.Slug)
	require.NoError(t, err)
	require.Equal(t, 2, page.Version, "a new evidence version creates a body revision even if prose is unchanged")
	require.Equal(t, newSHA, page.SourceProvenance.Evidence[0].CommitSHA)
	rev, err := wiki.GetRevision(f.ctx, f.kb.ID, page.Slug, 1)
	require.NoError(t, err)
	require.Equal(t, firstPage.SourceRefs, rev.SourceRefs)
	require.Equal(t, oldSHA, rev.SourceProvenance.Evidence[0].CommitSHA)
	historical, err := generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 1, "e001")
	require.NoError(t, err)
	require.Equal(t, oldSHA, historical.CommitSHA)
	require.NotContains(t, historical.Content, "new schedule")
	_, err = wiki.RevertPageToVersion(f.ctx, f.kb.ID, page.Slug, 1)
	require.NoError(t, err)
	result, err = read.Execute(f.ctx, args)
	require.NoError(t, err)
	require.NotContains(t, result.Output, "getPushSchedule returns a schedule")
	historical, err = generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 0, "e001")
	require.NoError(t, err)
	require.Equal(t, oldSHA, historical.CommitSHA, "rollback restores the body evidence rather than current source evidence")
}

func TestSourceWikiRejectedEvidenceAndSemanticQAHaveBoundedManualRetries(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	mode := "fabricated"
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			if mode == "semantic" {
				return `{"supported":false,"reason":"claim is unsupported","sections":[0],"uncertain":false}`
			}
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		if mode == "invalid" {
			return "invalid JSON"
		}
		id := "e001"
		if mode == "fabricated" {
			id = "e999"
		}
		return fmt.Sprintf(`{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":[%q],"uncertain":false}]}`, id)
	})
	req := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	failed, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.Status)
	require.Contains(t, failed.Reason, "fabricated evidence")
	require.Equal(t, 3, failed.Calls)
	require.Equal(t, 2, failed.Repairs)
	require.NotEmpty(t, failed.Draft)
	_, err = wiki.GetPageBySlug(f.ctx, f.kb.ID, failed.Slug)
	require.Error(t, err)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 1})
	require.NoError(t, err)
	require.Len(t, hits, 1)
	mode = "ready"
	ready, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Status)
	original, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
	require.NoError(t, err)
	for _, bad := range []string{"semantic", "fabricated", "invalid"} {
		mode = bad
		attempt, err := generator.GenerateModule(f.ctx, req)
		require.NoError(t, err)
		require.Equal(t, "failed", attempt.Status)
		require.LessOrEqual(t, attempt.Calls, 6)
		require.Equal(t, 2, attempt.Repairs)
		unchanged, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
		require.NoError(t, err)
		require.Equal(t, original.Version, unchanged.Version)
		require.Equal(t, original.Content, unchanged.Content)
	}
	attempts, err := generator.ListAttempts(f.ctx, f.kb.ID)
	require.NoError(t, err)
	require.Len(t, attempts, 5)
	require.Equal(t, "failed", attempts[0].Status)
	require.NotEmpty(t, attempts[0].Reason)
}
func TestSourceWikiWholePageAndHistoryRequireEveryActualSourceInScope(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/Other.java": []byte("class Other { String getPushSchedule() { return \"other\"; } }\n")})
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Two implementations.","sections":[{"text":"Module uses Service and Other.","evidence_ids":["e001","e002"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Len(t, page.SourceRefs, 2)
	page.PageType = types.WikiPageTypeSummary
	page, err = wiki.UpdatePage(f.ctx, page)
	require.NoError(t, err)
	folder, err := wiki.CreateFolder(f.ctx, f.kb.ID, 1, "", "Scheduling")
	require.NoError(t, err)
	page, err = wiki.MovePage(f.ctx, f.kb.ID, page.Slug, folder.ID)
	require.NoError(t, err)
	anchor, err := wiki.CreatePage(f.ctx, &types.WikiPage{TenantID: 1, KnowledgeBaseID: f.kb.ID, Slug: "index-related-test", Title: "Directory", Content: "[[" + page.Slug + "]]", PageType: types.WikiPageTypeIndex, Status: types.WikiPageStatusPublished})
	require.NoError(t, err)
	emptyFolder, err := wiki.CreateFolder(f.ctx, f.kb.ID, 1, "", "Empty")
	require.NoError(t, err)
	firstID, secondID := page.SourceProvenance.Evidence[0].KnowledgeID, page.SourceProvenance.Evidence[1].KnowledgeID
	tagX := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "X"}
	tagY := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "Y"}
	require.NoError(t, f.db.Create(tagX).Error)
	require.NoError(t, f.db.Create(tagY).Error)
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, firstID, []string{tagY.ID}))
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, secondID, []string{tagX.ID}))
	all := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID}}}
	narrow := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{firstID}}}
	cross := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{firstID}, TagIDs: []string{tagX.ID}}, &types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{secondID}, TagIDs: []string{tagY.ID}}}
	args, _ := json.Marshal(map[string]any{"slugs": []string{page.Slug}})
	for _, targets := range []types.SearchTargets{narrow, cross} {
		read := agenttools.NewWikiReadPageTool(wiki, f.knowledge, agenttools.NewWikiScopesFromSearchTargets(targets, []string{f.kb.ID}), nil)
		result, err := read.Execute(f.ctx, args)
		require.NoError(t, err)
		require.NotContains(t, result.Output, "Module uses Service and Other")
		search := agenttools.NewWikiSearchTool(wiki, f.knowledge, agenttools.NewWikiScopesFromSearchTargets(targets, []string{f.kb.ID}), nil)
		result, err = search.Execute(f.ctx, json.RawMessage(`{"query":"Scheduling"}`))
		require.NoError(t, err)
		require.NotContains(t, result.Output, page.Slug)
		ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
		require.NoError(t, err)
		listed, err := wiki.ListPages(ctx, &types.WikiPageListRequest{KnowledgeBaseID: f.kb.ID})
		require.NoError(t, err)
		for _, listedPage := range listed.Pages {
			require.NotEqual(t, page.Slug, listedPage.Slug)
		}
		index, err := wiki.GetIndexView(ctx, f.kb.ID, nil, 20, "")
		require.NoError(t, err)
		for _, group := range index.Groups {
			require.Empty(t, group.Items)
		}
		summaries, err := wiki.ListSummariesByKnowledgeIDs(ctx, f.kb.ID, []string{firstID, secondID})
		require.NoError(t, err)
		require.Empty(t, summaries)
		related, err := wiki.ListBySlugs(ctx, f.kb.ID, []string{page.Slug})
		require.NoError(t, err)
		require.Empty(t, related)
		recent, err := wiki.ListByTypeRecent(ctx, f.kb.ID, types.WikiPageTypeSummary, 20)
		require.NoError(t, err)
		require.Empty(t, recent)
		graph, err := wiki.GetGraph(ctx, &types.WikiGraphRequest{KnowledgeBaseID: f.kb.ID, Mode: types.WikiGraphModeOverview, Limit: 20})
		require.NoError(t, err)
		for _, node := range graph.Nodes {
			require.NotEqual(t, page.Slug, node.Slug)
		}
		folders, err := wiki.ListChildFolders(ctx, f.kb.ID, "", []string{types.WikiPageTypeSummary})
		require.NoError(t, err)
		require.Empty(t, folders)
		merged, err := wiki.ListChildFolders(ctx, f.kb.ID, "", []string{types.WikiPageTypeSummary, types.WikiPageTypeConcept})
		require.NoError(t, err)
		require.Len(t, merged, 1, "source-only folder must not masquerade as an empty merged folder")
		require.Equal(t, emptyFolder.ID, merged[0].ID)
		linkedArgs, _ := json.Marshal(map[string]any{"slugs": []string{anchor.Slug}})
		linked, err := read.Execute(ctx, linkedArgs)
		require.NoError(t, err)
		require.NotContains(t, linked.Output, "Two implementations")
		require.NotContains(t, linked.Output, "Module uses Service and Other")
		attempts, err := generator.ListAttempts(ctx, f.kb.ID)
		require.NoError(t, err)
		require.Empty(t, attempts, "status titles/reasons require the complete attempted module scope")
		_, err = generator.ReadEvidence(ctx, f.kb.ID, page.Slug, 0, "e001")
		require.Error(t, err)
		release()
	}
	read := agenttools.NewWikiReadPageTool(wiki, f.knowledge, agenttools.NewWikiScopesFromSearchTargets(all, []string{f.kb.ID}), nil)
	result, err := read.Execute(f.ctx, args)
	require.NoError(t, err)
	require.Contains(t, result.Output, "Module uses Service and Other")
	edit := *page
	edit.Content += "\nHuman clarification."
	updated, err := wiki.UpdatePage(f.ctx, &edit)
	require.NoError(t, err)
	require.Equal(t, "unverified", updated.SourceProvenance.State)
	result, err = read.Execute(f.ctx, args)
	require.NoError(t, err)
	require.NotContains(t, result.Output, "Human clarification")
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, narrow)
	require.NoError(t, err)
	defer release()
	history, err := wiki.ListRevisions(ctx, f.kb.ID, page.Slug, 50, 0)
	require.NoError(t, err)
	require.Empty(t, history.Revisions)
	_, err = wiki.GetRevision(ctx, f.kb.ID, page.Slug, 1)
	require.Error(t, err)
}

func TestSourceWikiTitleAndSummaryCannotHideModelVisibleSourcesAndQAIndicesAreStrict(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/Other.java": []byte("class Other { String getPushSchedule() { return \"other\"; } }\n")})
	syncSourceFixture(t, f)
	duplicateQA := false
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			if duplicateQA {
				return `{"supported":true,"reason":"","sections":[0,0],"uncertain":false}`
			}
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Other and Service module","summary":"Other supplies a second schedule implementation.","sections":[{"text":"Service returns a schedule.","evidence_ids":["e002"],"uncertain":false}]}`
	})
	req := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	attempt, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Len(t, page.SourceRefs, 2, "title/summary and QA see every supplied evidence file, so all must be registered")
	require.Len(t, page.SourceProvenance.Evidence, 2)
	duplicateQA = true
	failed, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "failed", failed.Status, "duplicate QA section indices must never produce ready prose")
	require.Equal(t, 2, failed.Repairs)
	unchanged, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, page.Slug)
	require.NoError(t, err)
	require.Equal(t, page.Version, unchanged.Version)
}

func TestSourceWikiGenerationPublicationGatesPreserveExistingBody(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var block atomic.Bool
	entered := make(chan struct{}, 1)
	resume := make(chan struct{}, 1)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if !qa && block.CompareAndSwap(true, false) {
			entered <- struct{}{}
			select {
			case <-resume:
			case <-time.After(15 * time.Second):
			}
		}
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	req := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	ready, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Status)
	models := NewModelService(repository.NewModelRepository(f.db), repository.NewKnowledgeBaseRepository(f.db), nil, nil, nil, nil)
	mutations := []struct {
		name  string
		apply func(*testing.T)
	}{
		{"model_parameters", func(t *testing.T) {
			m, err := models.GetModelByID(f.ctx, f.kb.SummaryModelID)
			require.NoError(t, err)
			m.Parameters.ContextWindow = 60000
			require.NoError(t, models.UpdateModel(f.ctx, m))
		}},
		{"synthesis_model", func(t *testing.T) {
			m, err := models.GetModelByID(f.ctx, f.kb.SummaryModelID)
			require.NoError(t, err)
			m.ID = uuid.NewString()
			require.NoError(t, models.CreateModel(f.ctx, m))
			kb, err := f.kbs.(interfaces.KnowledgeBaseService).UpdateKnowledgeBase(f.ctx, f.kb.ID, f.kb.Name, f.kb.Description, &types.KnowledgeBaseConfig{WikiConfig: &types.WikiConfig{SynthesisModelID: m.ID}})
			require.NoError(t, err)
			f.kb = kb
		}},
		{"source_config", func(t *testing.T) {
			ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
			require.NoError(t, err)
			var config map[string]any
			require.NoError(t, json.Unmarshal(ds.Config, &config))
			config["settings"].(map[string]any)["include_globs"] = []string{"src/**"}
			ds.Config, _ = json.Marshal(config)
			_, err = f.service.UpdateDataSource(f.ctx, ds)
			require.NoError(t, err)
		}},
		{"page_version", func(t *testing.T) {
			page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
			require.NoError(t, err)
			page.Content += "\nHuman clarification."
			_, err = wiki.UpdatePage(f.ctx, page)
			require.NoError(t, err)
		}},
		{"publication", func(t *testing.T) { f.advanceJava("new schedule"); syncSourceFixture(t, f) }},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			before, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
			require.NoError(t, err)
			block.Store(true)
			done := make(chan *types.SourceWikiAttempt, 1)
			errs := make(chan error, 1)
			go func() { a, e := generator.GenerateModule(f.ctx, req); done <- a; errs <- e }()
			select {
			case <-entered:
			case <-time.After(15 * time.Second):
				t.Fatal("generation did not reach controlled model")
			}
			released := false
			defer func() {
				if !released {
					resume <- struct{}{}
				}
			}()
			mutation.apply(t)
			resume <- struct{}{}
			released = true
			attempt := <-done
			require.NoError(t, <-errs)
			require.Equal(t, "failed", attempt.Status)
			require.NotEmpty(t, attempt.Reason)
			after, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
			require.NoError(t, err)
			if mutation.name == "page_version" {
				require.Equal(t, before.Version+1, after.Version)
				require.Contains(t, after.Content, "Human clarification")
			} else {
				require.Equal(t, before.Version, after.Version)
				require.Equal(t, before.Content, after.Content)
			}
		})
	}
}

func TestSourceWikiBookkeepingCannotForgeEvidenceAndMachineEditsKeepRevision(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	forged := *page
	p := *page.SourceProvenance
	p.Evidence = nil
	forged.SourceProvenance = &p
	forged.SourceRefs = nil
	require.Error(t, wiki.UpdatePageMeta(f.ctx, &forged), "bookkeeping cannot remove the actual sources")
	edit := *page
	edit.Content += "\nMachine-written clarification."
	require.NoError(t, wiki.UpdateAutoLinkedContent(f.ctx, &edit))
	after, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Equal(t, page.Version+1, after.Version)
	require.Equal(t, "unverified", after.SourceProvenance.State)
	rev, err := wiki.GetRevision(f.ctx, f.kb.ID, attempt.Slug, page.Version)
	require.NoError(t, err)
	require.Equal(t, page.Content, rev.Content)
	require.Equal(t, page.SourceProvenance, rev.SourceProvenance)
}

type sourceWikiAgentModel struct {
	steps []struct{ name, args string }
	calls [][]chat.Message
	tools []string
}

func (m *sourceWikiAgentModel) GetModelName() string { return "source-wiki-agent-fixture" }
func (m *sourceWikiAgentModel) GetModelID() string   { return "source-wiki-agent-fixture" }
func (m *sourceWikiAgentModel) Chat(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
	return nil, fmt.Errorf("unexpected non-streaming provider call")
}
func (m *sourceWikiAgentModel) ChatStream(ctx context.Context, messages []chat.Message, opts *chat.ChatOptions) (<-chan types.StreamResponse, error) {
	step := len(m.calls)
	m.calls = append(m.calls, append([]chat.Message(nil), messages...))
	if step == 0 {
		for _, tool := range opts.Tools {
			m.tools = append(m.tools, tool.Function.Name)
		}
	}
	response := types.StreamResponse{ResponseType: types.ResponseTypeAnswer, Done: true, FinishReason: "stop", Content: "Scheduling evidence checked."}
	if step < len(m.steps) {
		response.Content = ""
		response.FinishReason = "tool_calls"
		call := m.steps[step]
		response.ToolCalls = []types.LLMToolCall{{ID: fmt.Sprintf("wiki-fixture-%d", step), Function: types.FunctionCall{Name: call.name, Arguments: call.args}}}
	}
	ch := make(chan types.StreamResponse, 1)
	ch <- response
	close(ch)
	return ch, nil
}
func TestSourceWikiOriginalThreeAgentPresetsUsePublicWikiAndFixedQuestionScope(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	require.NoError(t, types.LoadAgentTypePresetsConfig(filepath.Join("..", "..", "..", "config")))
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	for _, presetID := range []string{"rag-qa", "wiki-qa", "hybrid-rag-wiki"} {
		t.Run(presetID, func(t *testing.T) {
			preset := types.GetAgentTypePreset(presetID)
			require.NotNil(t, preset)
			model := &sourceWikiAgentModel{}
			if presetID != "rag-qa" {
				args, _ := json.Marshal(map[string]any{"slugs": []string{attempt.Slug}})
				model.steps = append(model.steps, struct{ name, args string }{agenttools.ToolWikiSearch, `{"queries":["Scheduling"]}`}, struct{ name, args string }{agenttools.ToolWikiReadPage, string(args)})
			}
			if presetID != "wiki-qa" {
				model.steps = append(model.steps, struct{ name, args string }{agenttools.ToolGrepChunks, `{"query":"getPushSchedule"}`})
			}
			cfg := &config.Config{Conversation: &config.ConversationConfig{}, KnowledgeBase: &config.KnowledgeBaseConfig{}}
			svc := NewAgentService(cfg, nil, f.kbs.(interfaces.KnowledgeBaseService), f.knowledge, nil, f.chunks, nil, nil, event.NewEventBus(), f.db, nil, nil, wiki, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			engine, err := svc.CreateAgentEngine(f.ctx, &types.AgentConfig{MaxIterations: 6, KnowledgeBases: []string{f.kb.ID}, SearchTargets: targets, AllowedTools: preset.Config.AllowedTools, UseCustomSystemPrompt: true, SystemPrompt: "Use evidence to answer."}, model, nil, event.NewEventBus(), "wiki-question", "wiki-answer")
			require.NoError(t, err)
			if presetID == "hybrid-rag-wiki" {
				f.advanceJava("class Service { String getPushSchedule() { return \"later schedule\"; } }")
				syncSourceFixture(t, f)
			}
			state, err := engine.Execute(f.ctx, "wiki-question", "wiki-answer", "Explain scheduling.", nil)
			require.NoError(t, err)
			require.Contains(t, state.FinalAnswer, "Scheduling evidence checked")
			var input strings.Builder
			for _, messages := range model.calls {
				for _, message := range messages {
					if message.Role == "tool" {
						input.WriteString(message.Content)
					}
				}
			}
			if presetID == "rag-qa" {
				require.NotContains(t, model.tools, agenttools.ToolWikiReadPage)
				require.Contains(t, input.String(), "预约")
			} else {
				require.Contains(t, model.tools, agenttools.ToolWikiReadPage)
				require.Contains(t, input.String(), "getPushSchedule returns a schedule.")
			}
			if presetID == "wiki-qa" {
				require.NotContains(t, model.tools, agenttools.ToolGrepChunks)
			}
			if presetID == "hybrid-rag-wiki" {
				require.Contains(t, input.String(), "预约")
				require.NotContains(t, input.String(), "later schedule")
			}
		})
	}
	fresh := agenttools.NewWikiReadPageTool(wiki, f.knowledge, agenttools.NewWikiScopesFromSearchTargets(targets, []string{f.kb.ID}), nil)
	args, _ := json.Marshal(map[string]any{"slugs": []string{attempt.Slug}})
	result, err := fresh.Execute(f.ctx, args)
	require.NoError(t, err)
	require.NotContains(t, result.Output, "getPushSchedule returns a schedule")
}

func TestSourceWikiAllReadProjectionsRejectCorruptedRegisteredRaw(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	// Inject corruption at the real raw store; assertions use public readers.
	raw, err := generator.ReadEvidence(f.ctx, f.kb.ID, attempt.Slug, 0, "e001")
	require.NoError(t, err)
	require.NoError(t, f.db.Exec("UPDATE source_file_versions SET content=? WHERE id=?", []byte(strings.Replace(raw.Content, "预约", "伪造", 1)), page.SourceProvenance.Evidence[0].FileVersionID).Error)
	_, err = wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.Error(t, err)
	listed, err := wiki.ListBySlugs(f.ctx, f.kb.ID, []string{attempt.Slug})
	require.NoError(t, err)
	require.Empty(t, listed)
	_, err = generator.ReadEvidence(f.ctx, f.kb.ID, attempt.Slug, 0, "e001")
	require.Error(t, err)
}

func TestSourceWikiHistoricalFilteredFileRetainsScopeAndClearWins(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	edit := *page
	edit.Content += "\nHuman clarification."
	_, err = wiki.UpdatePage(f.ctx, &edit)
	require.NoError(t, err)
	kid := page.SourceProvenance.Evidence[0].KnowledgeID
	oldVersion := page.SourceProvenance.Evidence[0].FileVersionID
	tag := &types.KnowledgeTag{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Name: "historical"}
	require.NoError(t, f.db.Create(tag).Error)
	require.NoError(t, f.knowledge.SetKnowledgeTags(f.ctx, kid, []string{tag.ID}))
	f.advanceJava("class Service { String getPushSchedule() { return \"new schedule\"; } }")
	syncSourceFixture(t, f)
	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	config.Settings["exclude_paths"] = []string{page.SourceProvenance.Evidence[0].Path}
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, config.Settings)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	updated := *f.ds
	updated.Config, err = config.ToJSON()
	require.NoError(t, err)
	f.ds, err = f.service.UpdateDataSource(f.ctx, &updated)
	require.NoError(t, err)
	syncSourceFixture(t, f)
	published := latestIncrementalRun(t, f)
	require.Equal(t, "published", published.Snapshot.State)
	require.Zero(t, published.Snapshot.FileCount, "excluding the final file publishes a complete empty version")
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID}, KnowledgeIDs: []string{kid}, TagIDs: []string{tag.ID}}}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	raw, err := generator.ReadEvidence(ctx, f.kb.ID, page.Slug, 1, "e001")
	require.NoError(t, err)
	require.Equal(t, oldVersion, raw.FileVersionID)
	require.Equal(t, f.sha, raw.CommitSHA)
	_, err = f.knowledge.GetSourceFile(ctx, kid, oldVersion)
	require.Error(t, err, "general source reader must retain publication semantics")
	require.NoError(t, f.db.Exec("DELETE FROM source_publications WHERE data_source_id=?", f.ds.ID).Error)
	_, err = generator.ReadEvidence(ctx, f.kb.ID, page.Slug, 1, "e001")
	require.Error(t, err, "explicit clear overrides retained body ownership")
}

func TestSourceWikiModuleIdentityIsPerSourceAndEscapesFixedGitPaths(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{"src/other # space/Strange.java": []byte("class Strange { String getPushSchedule(){return \"strange\";} }\n")})
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns schedules.","sections":[{"text":"Module implementations return schedules.","evidence_ids":["e001","e002"],"uncertain":false}]}`
	})
	req := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	first, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", first.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Len(t, page.SourceRefs, 2)
	var evidence types.SourceWikiEvidence
	for _, item := range page.SourceProvenance.Evidence {
		if strings.Contains(item.Path, "#") {
			evidence = item
		}
	}
	require.NotEmpty(t, evidence.ID)
	link, err := url.Parse(evidence.GitLabURL)
	require.NoError(t, err)
	require.Empty(t, link.RawQuery)
	require.Equal(t, "L1-1", link.Fragment)
	require.True(t, strings.HasSuffix(link.Path, "/src/other # space/Strange.java"))
	raw, err := generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 1, evidence.ID)
	require.NoError(t, err)
	require.Equal(t, evidence.Path, raw.Path)
	require.Equal(t, evidence.CommitSHA, raw.CommitSHA)
	secondSource := *f.ds
	secondSource.ID = uuid.NewString()
	secondSource.Name = "Second repository"
	_, err = f.service.CreateDataSource(f.ctx, &secondSource)
	require.NoError(t, err)
	syncSourceFixture(t, f, secondSource.ID)
	req.SourceID = secondSource.ID
	second, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", second.Status)
	require.NotEqual(t, first.Slug, second.Slug)
	for _, slug := range []string{first.Slug, second.Slug} {
		found, readErr := wiki.GetPageBySlug(f.ctx, f.kb.ID, slug)
		require.NoError(t, readErr, slug)
		require.Equal(t, types.WikiPageTypeConcept, found.PageType)
	}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID}})
	require.NoError(t, err)
	defer release()
	for _, slug := range []string{first.Slug, second.Slug} {
		_, readErr := wiki.GetPageBySlug(ctx, f.kb.ID, slug)
		require.NoError(t, readErr, slug)
	}
	pages, err := wiki.ListByType(ctx, f.kb.ID, types.WikiPageTypeConcept)

	require.NoError(t, err)
	require.Len(t, pages, 2, "two repositories produce two module cards, never one page per file or a merged same-title page")
}

func TestSourceWikiProviderRetriesTokenBudgetAndCancellationAreBounded(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var mode atomic.Int32
	started := make(chan struct{}, 1)
	wiki, generator := newSourceWikiFixture(t, f, func(bool) string { return "" }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		switch mode.Load() {
		case 1:
			http.Error(w, "controlled provider error", http.StatusInternalServerError)
			return
		case 3:
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		reply := `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
		if qa {
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		tokens := 40
		if mode.Load() == 2 {
			tokens = 500000
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"total_tokens":%d}}`, reply, tokens)
	})
	req := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	ready, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", ready.Status)
	before, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
	require.NoError(t, err)
	for _, failure := range []int32{1, 2, 3} {
		mode.Store(failure)
		ctx := f.ctx
		cancel := func() {}
		if failure == 3 {
			ctx, cancel = context.WithCancel(f.ctx)
			go func() {
				select {
				case <-started:
					cancel()
				case <-time.After(5 * time.Second):
					cancel()
				}
			}()
		}
		attempt, err := generator.GenerateModule(ctx, req)
		cancel()
		require.NoError(t, err)
		require.Equal(t, "failed", attempt.Status)
		require.NotEmpty(t, attempt.Reason)
		if failure == 1 {
			require.Equal(t, 3, attempt.Calls)
		} else {
			require.Equal(t, 1, attempt.Calls)
		}
		require.Equal(t, 0, attempt.Repairs)
		after, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
		require.NoError(t, err)
		require.Equal(t, before.Version, after.Version)
		require.Equal(t, before.Content, after.Content)
	}
}

func TestSourceWikiMixedDocumentContributionsRequireWholeScopeAndOrdinaryWikiKeepsUnion(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	allowed := &types.Knowledge{ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, Type: "file", Title: "Handbook", ParseStatus: types.ParseStatusCompleted}
	other := *allowed
	other.ID = uuid.NewString()
	other.Title = "Other handbook"
	require.NoError(t, f.db.Create(allowed).Error)
	require.NoError(t, f.db.Create(&other).Error)
	doc, err := wiki.CreatePage(f.ctx, &types.WikiPage{TenantID: 1, KnowledgeBaseID: f.kb.ID, Slug: "concept/ordinary-handbooks", Title: "Handbooks", Summary: "Original document union", Content: "Both handbooks contribute ordinary prose.", PageType: types.WikiPageTypeConcept, SourceRefs: types.StringArray{allowed.ID, other.ID}, Status: types.WikiPageStatusPublished})
	require.NoError(t, err)
	docTargets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: f.kb.ID, KnowledgeIDs: []string{allowed.ID}}}
	docRead := agenttools.NewWikiReadPageTool(wiki, f.knowledge, agenttools.NewWikiScopesFromSearchTargets(docTargets, []string{f.kb.ID}), nil)
	args, _ := json.Marshal(map[string]any{"slugs": []string{doc.Slug}})
	result, err := docRead.Execute(f.ctx, args)
	require.NoError(t, err)
	require.Contains(t, result.Output, "Both handbooks contribute ordinary prose")
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	page.Content += "\nHandbook contributes this human clarification."
	page.SourceRefs = append(page.SourceRefs, allowed.ID)
	page, err = wiki.UpdatePage(f.ctx, page)
	require.NoError(t, err)
	require.Equal(t, "unverified", page.SourceProvenance.State)
	sourceTargets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID}}}
	ctx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, sourceTargets)
	require.NoError(t, err)
	_, err = wiki.GetPageBySlug(ctx, f.kb.ID, page.Slug)
	require.Error(t, err, "source evidence IDs do not cover an ordinary document contribution")
	release()
	combined := append(sourceTargets, docTargets...)
	ctx, release, err = f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, combined)
	require.NoError(t, err)
	defer release()
	visible, err := wiki.GetPageBySlug(ctx, f.kb.ID, page.Slug)
	require.NoError(t, err)
	require.Contains(t, visible.Content, "Handbook contributes")
}
