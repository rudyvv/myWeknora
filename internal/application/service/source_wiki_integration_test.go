//go:build integration

package service

import (
	"bytes"
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
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

func newSourceWikiFixture(t *testing.T, f *javaSourceFixture, response func(bool) string, override ...func(http.ResponseWriter, *http.Request, bool)) (interfaces.WikiPageService, interfaces.SourceWikiService) {
	t.Helper()
	require.NoError(t, f.db.AutoMigrate(&types.WikiFolder{}, &types.WikiPage{}, &types.WikiPageRevision{}))
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000105_source_wiki.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	retentionMigration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000112_source_wiki_revision_retention.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(retentionMigration)).Error)
	attemptLedgerMigration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000113_source_wiki_attempt_ledger.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(attemptLedgerMigration)).Error)
	batchMigration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(batchMigration)).Error)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, readErr := io.ReadAll(r.Body)
		require.NoError(t, readErr)
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		require.NoError(t, json.Unmarshal(body, &request))
		stage := ""
		if len(request.Messages) > 0 {
			stage = request.Messages[0].Content
		}
		r.Header.Set("X-Source-Wiki-Test-Stage", stage)
		qa := stage == "source_wiki_qa"
		if len(override) > 0 {
			r.Body = io.NopCloser(bytes.NewReader(body))
			override[0](w, r, qa)
			return
		}
		fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, response(qa))
	}))
	t.Cleanup(server.Close)
	models := NewModelService(repository.NewModelRepository(f.db), repository.NewKnowledgeBaseRepository(f.db), nil, nil, nil, nil)
	model := &types.Model{ID: uuid.NewString(), TenantID: 1, Name: "source-wiki-fixture", Type: types.ModelTypeKnowledgeQA, Source: types.ModelSourceRemote, Status: types.ModelStatusActive, Parameters: types.ModelParameters{BaseURL: server.URL, Provider: "openai", InterfaceType: "openai", ContextWindow: 65536}}
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
	require.Equal(t, "ready", attempt.Status, attempt.Reason)
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

func TestSourceWikiUnaffectedModuleCarriesApplicabilityAcrossPublishedSnapshot(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{
		"lib/Unrelated.java": []byte("package lib; class Unrelated { int stableValue() { return 1; } }\n"),
	})
	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	projects, ok := config.Settings["projects"].([]any)
	require.True(t, ok)
	project, ok := projects[0].(map[string]any)
	require.True(t, ok)
	project["paths"] = []any{"src", "lib"}
	updated := *f.ds
	updated.Config, err = config.ToJSON()
	require.NoError(t, err)
	f.ds, err = f.service.UpdateDataSource(f.ctx, &updated)
	require.NoError(t, err)
	syncSourceFixture(t, f)
	previous := latestIncrementalRun(t, f)
	parseCalls := f.parseCount.Load()

	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	svc := generator.(*sourceWikiService)
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Equal(t, previous.Snapshot.ID, page.SourceProvenance.ApplicableSnapshotID)
	require.Len(t, page.SourceProvenance.Evidence, 1)
	var contribution types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", page.ID, f.ds.ID).Take(&contribution).Error)
	require.Equal(t, "ready", contribution.State)
	require.Equal(t, "module/src", contribution.TopicKey)
	require.NotEmpty(t, contribution.ModuleMemberFileIDs, "complete module membership is persisted separately from selected evidence")
	oldContributionEvidenceSHA := contribution.EvidenceSHA256
	oldBody := page.Content
	oldEvidenceSHA := page.SourceProvenance.Evidence[0].SHA256

	f.advanceFiles(map[string][]byte{
		"lib/Unrelated.java": []byte("package lib; class Unrelated { int stableValue() { return 2; } }\n"),
	})
	syncSourceFixture(t, f)
	next := latestIncrementalRun(t, f)
	require.NotEqual(t, previous.Snapshot.ID, next.Snapshot.ID)
	require.Equal(t, parseCalls+1, f.parseCount.Load(), "the source parser HTTP adapter must process the changed file once")
	var updatePlan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&updatePlan).Error)
	require.Equal(t, previous.Snapshot.ID, updatePlan.PreviousSnapshotID)
	require.Equal(t, next.Snapshot.ID, updatePlan.SnapshotID)
	require.Greater(t, updatePlan.ConfigGeneration, int64(0))
	stalePage, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err, "ordinary Wiki browsing keeps the existing stale page visible for status/history")
	require.Equal(t, "stale", stalePage.SourceProvenance.State)
	scopes := agenttools.NewWikiScopesFromKBIDs([]string{f.kb.ID})
	read := agenttools.NewWikiReadPageTool(wiki, f.knowledge, scopes, nil)
	readArgs, _ := json.Marshal(map[string]any{"slugs": []string{attempt.Slug}})
	staleRead, err := read.Execute(f.ctx, readArgs)
	require.NoError(t, err)
	require.NotContains(t, staleRead.Output, "getPushSchedule returns a schedule.",
		"a stale card remains visible to Wiki browsing but not an authorized current-answer projection")
	search := agenttools.NewWikiSearchTool(wiki, f.knowledge, scopes, nil)
	staleSearch, err := search.Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.NotContains(t, staleSearch.Output, attempt.Slug)

	accepted, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 10)
	require.NoError(t, err)
	require.GreaterOrEqual(t, accepted, 1, "published source update events must enter the durable Wiki lane")
	pendingOps := repository.NewTaskPendingOpsRepository(f.db)
	updateTrigger, err := json.Marshal(types.SourceWikiUpdateTriggerPayload{TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID})
	require.NoError(t, err)
	worker := NewSourceWikiUpdateWorker(svc, pendingOps, nil)
	for attempt := 0; attempt < 4; attempt++ {
		pendingCount, pendingErr := pendingOps.PendingCount(f.ctx, types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase, f.kb.ID)
		require.NoError(t, pendingErr)
		if pendingCount == 0 {
			break
		}
		require.NoError(t, worker.Handle(f.ctx, asynq.NewTask(types.TypeSourceWikiUpdate, updateTrigger)))
	}
	remaining, err := pendingOps.PendingCount(f.ctx, types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase, f.kb.ID)
	require.NoError(t, err)
	require.Zero(t, remaining, "accepted update events must be acknowledged only after their durable plan is processed")
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&updatePlan).Error)
	require.Equal(t, "completed", updatePlan.Status)
	require.False(t, updatePlan.SourceWideStale)
	var carriedItem types.SourceWikiUpdatePlanItem
	require.NoError(t, f.db.Where("plan_id=? AND topic_key=?", updatePlan.ID, "module/src").Take(&carriedItem).Error)
	require.Equal(t, "carry_forward", carriedItem.Action)
	require.Equal(t, "completed", carriedItem.State)
	var supersededPlan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, previous.Snapshot.ID).Take(&supersededPlan).Error)
	require.Equal(t, "superseded", supersededPlan.Status, "older accepted work must not overwrite the newer publication")
	carried, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Equal(t, "ready", carried.SourceProvenance.State)
	require.Equal(t, next.Snapshot.ID, carried.SourceProvenance.ApplicableSnapshotID,
		"a complete unchanged module must be re-attested to the new published snapshot")
	require.Equal(t, oldBody, carried.Content, "carrying applicability forward must preserve the page body")
	require.Equal(t, oldEvidenceSHA, carried.SourceProvenance.Evidence[0].SHA256,
		"carrying applicability forward must preserve the exact evidence digest")
	byID, err := wiki.GetPageByID(f.ctx, page.ID)
	require.NoError(t, err)
	require.Equal(t, "ready", byID.SourceProvenance.State, "the direct UI page read must use the same current applicability check")
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", page.ID, f.ds.ID).Take(&contribution).Error)
	require.Equal(t, "ready", contribution.State)
	require.Equal(t, next.Snapshot.ID, contribution.ApplicableSnapshotID)
	require.Equal(t, oldContributionEvidenceSHA, contribution.EvidenceSHA256)

	readResult, err := read.Execute(f.ctx, readArgs)
	require.NoError(t, err)
	require.Contains(t, readResult.Output, "getPushSchedule returns a schedule.",
		"the authorized Agent read path must keep a fully revalidated unaffected page answerable")
	searchResult, err := search.Execute(f.ctx, json.RawMessage(`{"query":"getPushSchedule"}`))
	require.NoError(t, err)
	require.Contains(t, searchResult.Output, attempt.Slug,
		"the authorized Agent search path must keep a fully revalidated unaffected page discoverable")
}

func TestSourceWikiUnknownContextFailsBeforeProviderDispatch(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		providerCalls.Add(1)
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	svc := generator.(*sourceWikiService)
	model, err := svc.models.GetModelByID(f.ctx, f.kb.SummaryModelID)
	require.NoError(t, err)
	model.Parameters.ContextWindow = 0
	require.NoError(t, svc.models.UpdateModel(f.ctx, model))

	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.Error(t, err, "an unknown provider context window must be an actionable configuration error")
	require.Nil(t, attempt)
	require.Zero(t, providerCalls.Load(), "unknown context must stop before any model dispatch")
	var attempts int64
	require.NoError(t, f.db.Model(&types.SourceWikiAttempt{}).Where("knowledge_base_id=?", f.kb.ID).Count(&attempts).Error)
	require.Zero(t, attempts, "invalid model configuration must not consume a module attempt slot")
}

func TestSourceWikiGenerationRecoversSameAttemptFromPersistedQAPhase(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var generateCalls, qaCalls atomic.Int32
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	}, func(w http.ResponseWriter, r *http.Request, qa bool) {
		reply := `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
		if qa {
			qaCalls.Add(1)
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		} else if r.Header.Get("X-Source-Wiki-Test-Stage") == "source_wiki_generate" {
			generateCalls.Add(1)
		} else {
			t.Fatalf("unexpected provider stage on recovery: %s", r.Header.Get("X-Source-Wiki-Test-Stage"))
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"total_tokens":40}}`, reply)
	})
	service := generator.(*sourceWikiService)
	readCtx, release, err := beginSourceRead(f.ctx, service.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID},
	}})
	require.NoError(t, err)
	defer release()
	evidence, err := service.collectEvidence(readCtx, f.kb.ID, f.ds.ID, "src")
	require.NoError(t, err)
	var sourceConfig types.DataSource
	require.NoError(t, f.db.Where("id=?", f.ds.ID).Take(&sourceConfig).Error)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id=?", f.ds.ID).Take(&publication).Error)
	model, err := service.models.GetModelByID(f.ctx, f.kb.SummaryModelID)
	require.NoError(t, err)
	now := time.Now().UTC()
	slug := sourceWikiModuleSlug(f.ds.ID, "src")
	draft := `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	checkpointData, err := json.Marshal(sourceWikiAttemptCheckpoint{Evidence: evidence, SourceDraft: draft})
	require.NoError(t, err)
	draftData, err := json.Marshal(draft)
	require.NoError(t, err)
	ids := make(types.StringArray, 0, len(evidence))
	all := make([]types.SourceWikiEvidence, 0, len(evidence))
	for _, item := range evidence {
		ids = append(ids, item.Evidence.KnowledgeID)
		all = append(all, item.Evidence)
	}
	attempt := &types.SourceWikiAttempt{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID,
		SnapshotID: publication.SnapshotID, ModulePath: "src", Title: "Scheduling module", Slug: slug,
		Status: "running", EvidenceKnowledgeIDs: ids, Draft: types.JSON(draftData), Phase: "qa", Checkpoint: types.JSON(checkpointData),
		SourceConfigFingerprint: sourceWikiSourceFingerprint(&sourceConfig), SourceUpdatedAt: sourceConfig.UpdatedAt,
		ModelID: model.ID, ModelSettingsFingerprint: sourceWikiModelFingerprint(model), ModelContextWindow: model.Parameters.ContextWindow,
		MaxCompletionTokens: types.SourceWikiAttemptMaxCompletionTokens, BasePageVersion: 0,
		DeadlineAt: now.Add(3 * time.Minute), MaxCalls: types.SourceWikiAttemptMaxCalls, MaxTokens: types.SourceWikiAttemptMaxTokens,
		MaxElapsedMS: types.SourceWikiAttemptMaxElapsedMS, MaxRepairs: types.SourceWikiAttemptMaxRepairs,
		CreatedAt: now, UpdatedAt: now,
	}
	ledger := repository.NewSourceWikiAttemptLedger(f.db)
	require.NoError(t, ledger.Create(f.ctx, attempt))
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return repository.RegisterSourceWikiAttemptEvidence(tx, attempt.ID, all)
	}))

	recovered, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, AttemptID: attempt.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, attempt.ID, recovered.ID, "recovery must not allocate a fresh attempt or budget")
	require.Equal(t, "ready", recovered.Status)
	require.Equal(t, 1, recovered.Calls, "the persisted QA phase resumes without repeating generation")
	require.EqualValues(t, 1, qaCalls.Load())
	require.Zero(t, generateCalls.Load())
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, slug)
	require.NoError(t, err)
	require.Equal(t, "ready", page.SourceProvenance.State)
	var ownerCount int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=?", attempt.ID).Count(&ownerCount).Error)
	require.Zero(t, ownerCount, "publication transfers protection to the page/revision owner in the same transaction")
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

func TestSourceWikiRevisionLeasePinOutlivesPruneAndGCDropsOnlyOldIndex(t *testing.T) {
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
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	var pins int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=?", first.ID).Count(&pins).Error)
	require.Zero(t, pins, "a ready attempt transfers protection to the page/revision owner")
	oldEvidence := page.SourceProvenance.Evidence[0]
	oldSnapshotID := oldEvidence.SnapshotID
	var oldChunkIDs []string
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", oldSnapshotID).Order("chunk_id").Pluck("chunk_id", &oldChunkIDs).Error)
	require.NotEmpty(t, oldChunkIDs)
	sharedOwner := &types.WikiPage{
		ID: uuid.NewString(), TenantID: page.TenantID, KnowledgeBaseID: page.KnowledgeBaseID,
		Slug: "concept/shared-evidence-owner", Title: "Shared evidence owner", PageType: types.WikiPageTypeConcept,
		Status: types.WikiPageStatusPublished, Content: page.Content, Summary: page.Summary,
		SourceRefs: append(types.StringArray(nil), page.SourceRefs...), ChunkRefs: append(types.StringArray(nil), page.ChunkRefs...),
		PageMetadata: append(types.JSON(nil), page.PageMetadata...), SourceProvenance: page.SourceProvenance, Version: 1,
	}
	require.NoError(t, repository.NewWikiPageRepository(f.db).Create(f.ctx, sharedOwner))

	f.advanceJava("new schedule")
	syncSourceFixture(t, f)
	second, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", second.Status)

	readCtx, release, err := wiki.(interfaces.WikiReadService).BeginWikiRead(f.ctx, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID,
	}})
	require.NoError(t, err)
	defer release()
	revisions := repository.NewWikiPageRepository(f.db)
	oldRevision, err := revisions.GetRevision(readCtx, f.kb.ID, page.ID, 1)
	require.NoError(t, err)
	require.Equal(t, oldEvidence.FileVersionID, oldRevision.SourceProvenance.Evidence[0].FileVersionID)

	require.NoError(t, f.db.Table("source_read_wiki_evidence_refs").Where("file_version_id=?", oldEvidence.FileVersionID).Count(&pins).Error)
	require.EqualValues(t, 1, pins, "the authorized history read must pin its exact raw version")

	drainSourceWikiReviewUpdateLane(t, f, generator)
	var pendingOldSnapshotPlans int64
	require.NoError(t, f.db.Model(&types.SourceWikiUpdatePlan{}).
		Where("source_id=? AND previous_snapshot_id=? AND status IN ?", f.ds.ID, oldSnapshotID, []string{"pending", "running"}).
		Count(&pendingOldSnapshotPlans).Error)
	require.Zero(t, pendingOldSnapshotPlans, "retired-source GC must not race an unfinished impact plan")

	gc := repository.NewSourceSnapshotRepository(f.db)
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.NoError(t, err)

	var oldIndex int64
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", oldSnapshotID).Count(&oldIndex).Error)
	require.Zero(t, oldIndex, "historical Wiki ownership must not retain the old retrieval index")
	require.NoError(t, f.db.Table("source_snapshot_members").Where("snapshot_id=?", oldSnapshotID).Count(&oldIndex).Error)
	require.Zero(t, oldIndex, "old file membership is not retained as a substitute for raw evidence ownership")
	var oldArtifacts int64
	require.NoError(t, f.db.Table("embeddings").Where("chunk_id IN ?", oldChunkIDs).Count(&oldArtifacts).Error)
	require.Zero(t, oldArtifacts, "old embeddings are collected independently of retained evidence")
	require.NoError(t, f.db.Unscoped().Table("chunks").Where("id IN ?", oldChunkIDs).Count(&oldArtifacts).Error)
	require.Zero(t, oldArtifacts, "old chunks are collected independently of retained evidence")
	var rawCount int64
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&rawCount).Error)
	require.EqualValues(t, 1, rawCount, "the revision owner and active read pin preserve the raw version")

	historical, err := generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 1, oldEvidence.ID)
	require.NoError(t, err, "historical raw reads must not depend on snapshot membership retained for indexing")
	require.Equal(t, oldEvidence.SHA256, historical.SHA256)
	require.Equal(t, oldEvidence.Path, historical.Path)
	require.Equal(t, oldEvidence.CommitSHA, historical.CommitSHA)
	require.Equal(t, string(historical.RawContent[oldEvidence.Range.StartByte:oldEvidence.Range.EndByte]), string(historical.RawContent), "the body evidence range remains anchored to exact old bytes")

	type pruneResult struct{ err error }
	type evidenceResult struct {
		file *types.SourceFileView
		err  error
	}
	start := make(chan struct{})
	pruned := make(chan pruneResult, 1)
	read := make(chan evidenceResult, 1)
	go func() {
		<-start
		pruned <- pruneResult{err: revisions.PruneRevisions(f.ctx, types.WikiRevisionPruneRequest{
			PageID: page.ID, HardKeepFromVersion: 2,
		})}
	}()
	go func() {
		<-start
		readFile, readErr := repository.ReadSourceWikiEvidence(readCtx, f.db, oldRevision.PageID, &oldRevision.ID, oldRevision.Version, oldRevision.SourceProvenance.Evidence[0])
		read <- evidenceResult{file: readFile, err: readErr}
	}()
	close(start)
	readResult := <-read
	require.NoError(t, readResult.err, "a read pin acquired before pruning keeps that exact body readable")
	require.Equal(t, oldEvidence.SHA256, readResult.file.SHA256)
	require.NoError(t, (<-pruned).err)
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&rawCount).Error)
	require.EqualValues(t, 1, rawCount, "the lease pin must survive concurrent revision pruning")

	release()
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&rawCount).Error)
	require.EqualValues(t, 1, rawCount, "another page's evidence owner must keep the shared immutable version")

	wikiRepo := repository.NewWikiPageRepository(f.db)
	require.NoError(t, wikiRepo.DeleteByID(f.ctx, sharedOwner.ID))
	require.NoError(t, wikiRepo.DeleteRevisionsByPage(f.ctx, sharedOwner.ID))
	require.NoError(t, f.db.Exec("CREATE TABLE source_gc_test_blockers (version_id VARCHAR(36) REFERENCES source_file_versions(id))").Error)
	require.NoError(t, f.db.Exec("INSERT INTO source_gc_test_blockers(version_id) VALUES (?)", oldEvidence.FileVersionID).Error)
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.Error(t, err, "an unexpected restrictive owner must fail closed")
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&rawCount).Error)
	require.EqualValues(t, 1, rawCount, "a failed release does not delete protected bytes")
	var attempts int
	require.NoError(t, f.db.Table("source_snapshot_gc_candidates").Where("snapshot_id=?", oldSnapshotID).Count(&pins).Error)
	require.EqualValues(t, 1, pins, "the failed candidate remains queued for retry")
	require.NoError(t, f.db.Table("source_snapshot_gc_candidates").Where("snapshot_id=?", oldSnapshotID).Select("attempt_count").Scan(&attempts).Error)
	require.Equal(t, 1, attempts)
	require.NoError(t, f.db.Exec("DROP TABLE source_gc_test_blockers").Error)
	require.NoError(t, f.db.Table("source_snapshot_gc_candidates").Where("snapshot_id=?", oldSnapshotID).Update("next_attempt_at", time.Now().UTC().Add(-time.Second)).Error)
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&rawCount).Error)
	require.Zero(t, rawCount, "the raw version becomes collectible after its final owner and lease pin release")
	require.NoError(t, f.db.Table("source_snapshots").Where("id=?", oldSnapshotID).Count(&rawCount).Error)
	require.Zero(t, rawCount, "orphan snapshot metadata is collected after the last exact raw owner releases")

	require.NoError(t, wikiRepo.DeleteRevisionsByKnowledgeBaseID(f.ctx, f.kb.TenantID, f.kb.ID))
	require.NoError(t, f.db.Table("source_wiki_evidence_refs").Joins("JOIN wiki_pages ON wiki_pages.id=source_wiki_evidence_refs.page_id").Where("wiki_pages.knowledge_base_id=?", f.kb.ID).Count(&rawCount).Error)
	require.Zero(t, rawCount, "knowledge-base cleanup releases current evidence owners as well as revisions")
}

func TestSourceWikiGCRequeuesWhenFinalRevisionOwnerReleasesDuringCollection(t *testing.T) {
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
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	oldEvidence := page.SourceProvenance.Evidence[0]

	gc := repository.NewSourceSnapshotRepository(f.db)
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 100)
	require.NoError(t, err)
	f.advanceJava("new schedule")
	syncSourceFixture(t, f)
	second, err := generator.GenerateModule(f.ctx, req)
	require.NoError(t, err)
	require.Equal(t, "ready", second.Status)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	generator.(*sourceWikiService).StopSourceWikiBatches()
	var updatePlan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, second.SnapshotID).Take(&updatePlan).Error)
	require.Equal(t, "completed", updatePlan.Status, "the accepted publication must finish through the durable Wiki update lane")
	var activePlanCount int64
	require.NoError(t, f.db.Model(&types.SourceWikiUpdatePlan{}).
		Where("source_id=? AND previous_snapshot_id=? AND status IN ?", f.ds.ID, oldEvidence.SnapshotID, []string{"pending", "running"}).
		Count(&activePlanCount).Error)
	require.Zero(t, activePlanCount, "the durable Wiki update lane must release its old-snapshot plan protection before collection")
	page, err = wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.NotEqual(t, oldEvidence.SnapshotID, page.SourceProvenance.Evidence[0].SnapshotID,
		"the second publication must move the current page to a new snapshot")
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id=?", f.ds.ID).Take(&publication).Error)
	require.NotEqual(t, oldEvidence.SnapshotID, publication.SnapshotID,
		"the old evidence snapshot must be retired before the race is arranged")
	var owners int64
	require.NoError(t, f.db.Table("source_wiki_evidence_refs").Where("page_id=? AND file_version_id=?", page.ID, oldEvidence.FileVersionID).Count(&owners).Error)
	require.EqualValues(t, 1, owners, "the old body is retained only by its revision")
	require.NoError(t, f.db.Exec(`DELETE FROM source_read_leases
		WHERE id IN (SELECT lease_id FROM source_read_scopes WHERE snapshot_id=?)`, oldEvidence.SnapshotID).Error)
	require.NoError(t, f.db.Exec("DELETE FROM source_snapshot_gc_candidates WHERE snapshot_id<>?", oldEvidence.SnapshotID).Error)
	var queued int64
	require.NoError(t, f.db.Table("source_snapshot_gc_candidates").Count(&queued).Error)
	require.EqualValues(t, 1, queued, "the fixture isolates the retired snapshot from unrelated queue work")
	var candidateSnapshotID string
	require.NoError(t, f.db.Table("source_snapshot_gc_candidates").Where("snapshot_id=?", oldEvidence.SnapshotID).
		Select("snapshot_id").Scan(&candidateSnapshotID).Error)
	require.Equal(t, oldEvidence.SnapshotID, candidateSnapshotID, "the collector candidate must be the retired evidence snapshot")

	const advisoryKey1, advisoryKey2 = 198342, 118
	require.NoError(t, f.db.Exec("CREATE SEQUENCE source_gc_test_candidate_update_seq").Error)
	// Skip the collector's claim UPDATE; block its next candidate UPDATE, which
	// occurs after the raw-owner count. The DELETE trigger is the equivalent
	// pre-fix interception point. Statement triggers pause before tuple locking,
	// allowing the independent revision-prune transaction to commit its owner
	// release before collection resumes.
	require.NoError(t, f.db.Exec(`CREATE OR REPLACE FUNCTION source_gc_test_barrier() RETURNS trigger LANGUAGE plpgsql AS $$
		DECLARE update_number BIGINT;
		BEGIN
			IF current_query() LIKE 'UPDATE %source_snapshot_gc_candidates%' THEN
				update_number := nextval('source_gc_test_candidate_update_seq');
				IF update_number = 2 THEN
					PERFORM pg_advisory_xact_lock(198342, 118);
				END IF;
			ELSIF current_query() LIKE 'DELETE FROM %source_snapshot_gc_candidates%' THEN
				PERFORM pg_advisory_xact_lock(198342, 118);
			END IF;
			RETURN NULL;
		END;
		$$`).Error)
	require.NoError(t, f.db.Exec(`CREATE TRIGGER source_gc_test_before_candidate_delete
		BEFORE DELETE ON source_snapshot_gc_candidates FOR EACH STATEMENT
		EXECUTE FUNCTION source_gc_test_barrier()`).Error)
	require.NoError(t, f.db.Exec(`CREATE TRIGGER source_gc_test_before_candidate_update
		BEFORE UPDATE ON source_snapshot_gc_candidates FOR EACH STATEMENT
		EXECUTE FUNCTION source_gc_test_barrier()`).Error)
	t.Cleanup(func() {
		_ = f.db.Exec("DROP TRIGGER IF EXISTS source_gc_test_before_candidate_delete ON source_snapshot_gc_candidates").Error
		_ = f.db.Exec("DROP TRIGGER IF EXISTS source_gc_test_before_candidate_update ON source_snapshot_gc_candidates").Error
		_ = f.db.Exec("DROP FUNCTION IF EXISTS source_gc_test_barrier()").Error
		_ = f.db.Exec("DROP SEQUENCE IF EXISTS source_gc_test_candidate_update_seq").Error
	})

	sqlDB, err := f.db.DB()
	require.NoError(t, err)
	barrierConn, err := sqlDB.Conn(f.ctx)
	require.NoError(t, err)
	var locked bool
	require.NoError(t, barrierConn.QueryRowContext(f.ctx,
		"SELECT pg_advisory_lock($1,$2) IS NOT NULL", advisoryKey1, advisoryKey2).Scan(&locked))
	require.True(t, locked)
	barrierReleased := false
	releaseBarrier := func() {
		if barrierReleased {
			return
		}
		var unlocked bool
		_ = barrierConn.QueryRowContext(context.Background(),
			"SELECT pg_advisory_unlock($1,$2)", advisoryKey1, advisoryKey2).Scan(&unlocked)
		_ = barrierConn.Close()
		barrierReleased = true
	}
	t.Cleanup(releaseBarrier)

	type collectResult struct {
		count int
		err   error
	}
	collectCtx, cancelCollect := context.WithCancel(f.ctx)
	t.Cleanup(cancelCollect)
	collected := make(chan collectResult, 1)
	go func() {
		count, collectErr := gc.CollectRetiredSourceVersions(collectCtx, 1)
		collected <- collectResult{count: count, err: collectErr}
	}()
	gcFinished := false
	t.Cleanup(func() {
		if gcFinished {
			return
		}
		cancelCollect()
		releaseBarrier()
		select {
		case <-collected:
		case <-time.After(5 * time.Second):
			t.Error("collector did not stop after cancellation")
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		var blocked bool
		require.NoError(t, f.db.Raw(`SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND state='active' AND wait_event='advisory'
			AND query LIKE '%source_snapshot_gc_candidates%')`).Scan(&blocked).Error)
		if blocked {
			break
		}
		if time.Now().After(deadline) {
			cancelCollect()
			t.Fatal("collector did not reach the candidate-release barrier")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// This real repository transaction releases the final exact evidence owner
	// after the collector's remaining-owner check but before candidate cleanup.
	revisions := repository.NewWikiPageRepository(f.db)
	require.NoError(t, revisions.PruneRevisions(f.ctx, types.WikiRevisionPruneRequest{
		PageID: page.ID, HardKeepFromVersion: 2,
	}))
	require.NoError(t, f.db.Table("source_wiki_evidence_refs").Where("page_id=? AND file_version_id=?", page.ID, oldEvidence.FileVersionID).Count(&owners).Error)
	require.Zero(t, owners, "pruning releases the final retained revision owner")

	releaseBarrier()
	var result collectResult
	select {
	case result = <-collected:
	case <-time.After(5 * time.Second):
		cancelCollect()
		t.Fatal("collector did not finish after the candidate barrier was released")
	}
	gcFinished = true
	require.NoError(t, result.err)
	require.Equal(t, 1, result.count)

	var count int64
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the first pass observed the pre-release owner and must leave raw bytes for retry")
	require.NoError(t, f.db.Table("source_snapshot_gc_candidates").Where("snapshot_id=?", oldEvidence.SnapshotID).Count(&count).Error)
	require.EqualValues(t, 1, count, "owner release during collection must leave a retry candidate")
	var due bool
	require.NoError(t, f.db.Raw(`SELECT EXISTS (SELECT 1 FROM source_snapshot_gc_candidates
		WHERE snapshot_id=? AND next_attempt_at<=now() AND (claimed_until IS NULL OR claimed_until<=now()))`, oldEvidence.SnapshotID).Scan(&due).Error)
	require.True(t, due, "a notification received during collection must make the retained candidate immediately eligible")
	require.NoError(t, f.db.Table("source_snapshot_members").Where("snapshot_id=?", oldEvidence.SnapshotID).Count(&count).Error)
	require.Zero(t, count, "the first pass collected retired index membership before deferring raw bytes")
	require.NoError(t, f.db.Raw(`SELECT
		(SELECT count(*) FROM source_wiki_evidence_refs WHERE file_version_id=?) +
		(SELECT count(*) FROM source_read_wiki_evidence_refs WHERE file_version_id=?) +
		(SELECT count(*) FROM source_wiki_attempt_evidence_refs WHERE file_version_id=?)`,
		oldEvidence.FileVersionID, oldEvidence.FileVersionID, oldEvidence.FileVersionID).Scan(&owners).Error)
	require.Zero(t, owners, "no exact raw-version owner remains after the barrier release")
	var chunkRefs, activeScopes int64
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", oldEvidence.SnapshotID).Count(&chunkRefs).Error)
	require.Zero(t, chunkRefs, "the retired search index was removed before the final owner released")
	require.NoError(t, f.db.Raw(`SELECT count(*) FROM source_read_scopes rs JOIN source_read_leases rl ON rl.id=rs.lease_id
		WHERE rs.snapshot_id=? AND rl.expires_at>now()`, oldEvidence.SnapshotID).Scan(&activeScopes).Error)
	require.Zero(t, activeScopes, "no active source read may defer this retired snapshot")

	processed, err := gc.CollectRetiredSourceVersions(f.ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 1, processed)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&count).Error)
	require.Zero(t, count, "the requeued candidate collects bytes after the final owner releases")
}

func TestSourceWikiRollbackRevalidatesRestoredEvidenceAgainstCurrentSnapshot(t *testing.T) {
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
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	originalRefs := append(types.StringArray(nil), page.SourceRefs...)
	originalEvidenceVersion := page.SourceProvenance.Evidence[0].FileVersionID

	edit := *page
	edit.Content += "\nA temporary unverified edit.\n"
	page, err = wiki.UpdatePage(f.ctx, &edit)
	require.NoError(t, err)
	require.Equal(t, "unverified", page.SourceProvenance.State)

	page, err = wiki.RevertPageToVersion(f.ctx, f.kb.ID, page.Slug, 1)
	require.NoError(t, err)
	require.Equal(t, "ready", page.SourceProvenance.State, "a still-current validated revision remains suitable after rollback")
	require.Equal(t, originalRefs, page.SourceRefs)
	require.Equal(t, originalEvidenceVersion, page.SourceProvenance.Evidence[0].FileVersionID)
	require.Equal(t, "getPushSchedule returns a schedule.", strings.TrimSpace(strings.SplitN(strings.TrimPrefix(page.Content, "# Scheduling module\n\n"), " [", 2)[0]))
}

func TestSourceWikiRunningAttemptPinsExactRawVersionAfterReadLeaseExpiry(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var block atomic.Bool
	block.Store(true)
	entered := make(chan struct{}, 1)
	resume := make(chan struct{})
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if !qa && block.CompareAndSwap(true, false) {
			entered <- struct{}{}
			<-resume
		}
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	done := make(chan *types.SourceWikiAttempt, 1)
	failed := make(chan error, 1)
	go func() {
		attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
			KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
		})
		done <- attempt
		failed <- err
	}()
	t.Cleanup(func() {
		select {
		case resume <- struct{}{}:
		default:
		}
	})
	select {
	case <-entered:
	case <-time.After(15 * time.Second):
		t.Fatal("generation did not reach the controlled model call")
	}

	var attempt types.SourceWikiAttempt
	require.NoError(t, f.db.Where("status='running'").Take(&attempt).Error)
	var pins int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=? AND snapshot_id=?", attempt.ID, attempt.SnapshotID).Count(&pins).Error)
	require.EqualValues(t, 1, pins, "the running attempt pins the selected immutable version before model calls")
	var version types.SourceFileVersion
	require.NoError(t, f.db.Table("source_file_versions").Where("snapshot_id=?", attempt.SnapshotID).Take(&version).Error)

	f.advanceJava("new schedule")
	syncSourceFixture(t, f)
	var oldChunks []string
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", attempt.SnapshotID).Pluck("chunk_id", &oldChunks).Error)
	require.NotEmpty(t, oldChunks)
	require.NoError(t, f.db.Exec(`UPDATE source_read_leases SET expires_at=now()-interval '1 second'
		WHERE id IN (SELECT lease_id FROM source_read_wiki_scopes WHERE jsonb_exists(source_ids,?))`, f.ds.ID).Error)

	gc := repository.NewSourceSnapshotRepository(f.db)
	var err error
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", version.ID).Count(&pins).Error)
	require.EqualValues(t, 1, pins, "the exact attempt owner survives ordinary read-lease expiry")
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", attempt.SnapshotID).Count(&pins).Error)
	require.Zero(t, pins, "attempt raw ownership does not keep the retired search index")
	var artifactCount int64
	require.NoError(t, f.db.Table("embeddings").Where("chunk_id IN ?", oldChunks).Count(&artifactCount).Error)
	require.Zero(t, artifactCount)

	resume <- struct{}{}
	completed := <-done
	require.NoError(t, <-failed)
	require.Equal(t, "failed", completed.Status, "the expired read lease still wins on authorization")
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=?", attempt.ID).Count(&pins).Error)
	require.Zero(t, pins, "terminal attempt status releases its exact version owner")
	_, err = gc.CollectRetiredSourceVersions(f.ctx, 10)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", version.ID).Count(&pins).Error)
	require.Zero(t, pins, "the raw version is collected after terminal release")
}

func TestSourceWikiRevisionOwnersFollow50And200PostgresWindows(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	repo := repository.NewWikiPageRepository(f.db)
	addRevision := func(author string, label int) {
		revision := &types.WikiPageRevision{
			ID: uuid.NewString(), TenantID: page.TenantID, KnowledgeBaseID: page.KnowledgeBaseID,
			PageID: page.ID, Slug: page.Slug, Version: page.Version, Title: page.Title,
			PageType: page.PageType, Status: page.Status, Content: page.Content, Summary: page.Summary,
			Aliases: append(types.StringArray(nil), page.Aliases...), SourceRefs: append(types.StringArray(nil), page.SourceRefs...),
			ChunkRefs: append(types.StringArray(nil), page.ChunkRefs...), PageMetadata: append(types.JSON(nil), page.PageMetadata...),
			SourceProvenance: page.SourceProvenance, EditSource: author, EditorID: page.LastEditorID,
			EditedAt: page.UpdatedAt, CreatedAt: time.Now(),
		}
		next := *page
		next.Version = page.Version
		next.UpdatedAt = time.Now()
		next.LastEditSource = author
		require.NoError(t, repo.UpdateWithRevision(f.ctx, &next, revision), "snapshot version %d", label)
		page = &next
	}
	for version := 1; version <= 204; version++ {
		author := types.WikiEditSourcePipeline
		if version == 1 {
			author = types.WikiEditSourceUser
		} else if version == 2 {
			author = types.WikiEditSourceAgent
		}
		addRevision(author, version)
	}
	require.Equal(t, 205, page.Version)
	require.NoError(t, repo.PruneRevisions(f.ctx, types.WikiRevisionPruneRequest{
		PageID: page.ID, KeepFromVersion: page.Version - types.WikiMaxRevisionsPerPage,
		PrunableSources: types.WikiPrunableEditSources,
	}))

	var kept int64
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Where("page_id=?", page.ID).Count(&kept).Error)
	require.EqualValues(t, types.WikiMaxRevisionsPerPage+2, kept, "the soft window keeps recent pipeline revisions plus authored history")
	var bounds struct{ MinVersion, MaxVersion int }
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Select("min(version) AS min_version,max(version) AS max_version").Where("page_id=?", page.ID).Scan(&bounds).Error)
	require.Equal(t, 1, bounds.MinVersion, "the soft cap preserves old human revisions")
	require.Equal(t, 204, bounds.MaxVersion)
	var retainedOwners int64
	require.NoError(t, f.db.Table("source_wiki_evidence_refs").Where("page_id=? AND revision_id IS NOT NULL", page.ID).Count(&retainedOwners).Error)
	require.EqualValues(t, types.WikiMaxRevisionsPerPage+2, retainedOwners, "the soft prune releases only evicted automatic revision owners")

	for version := 205; version <= 359; version++ {
		addRevision(types.WikiEditSourceUser, version)
	}
	require.Equal(t, 360, page.Version)
	require.NoError(t, repo.PruneRevisions(f.ctx, types.WikiRevisionPruneRequest{
		PageID: page.ID, HardKeepFromVersion: page.Version - types.WikiMaxRevisionsHardCap,
	}))
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Where("page_id=?", page.ID).Count(&kept).Error)
	require.EqualValues(t, types.WikiMaxRevisionsHardCap, kept, "the hard ceiling bounds pages authored entirely by people")
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Select("min(version) AS min_version,max(version) AS max_version").Where("page_id=?", page.ID).Scan(&bounds).Error)
	require.Equal(t, 160, bounds.MinVersion, "the hard cap prunes older human and agent revisions")
	require.Equal(t, 359, bounds.MaxVersion)
	require.NoError(t, f.db.Table("source_wiki_evidence_refs").Where("page_id=? AND revision_id IS NOT NULL", page.ID).Count(&retainedOwners).Error)
	require.EqualValues(t, types.WikiMaxRevisionsHardCap, retainedOwners, "hard pruning releases the evicted exact-version owners")
	var currentOwners int64
	require.NoError(t, f.db.Table("source_wiki_evidence_refs").Where("page_id=? AND revision_id IS NULL", page.ID).Count(&currentOwners).Error)
	require.EqualValues(t, 1, currentOwners, "the current page retains its independent evidence owner")
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
	}, func(w http.ResponseWriter, r *http.Request, qa bool) {
		if !qa && block.CompareAndSwap(true, false) {
			entered <- struct{}{}
			select {
			case <-resume:
			case <-time.After(15 * time.Second):
			}
		}
		reply := `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		if r.Header.Get("X-Source-Wiki-Test-Stage") == "source_wiki_merge" {
			reply = `{"title":"Scheduling module","summary":"Returns a schedule and retains the human clarification.","sections":[{"text":"getPushSchedule returns a schedule. Human clarification: keep the scheduling boundary explicit.","evidence_ids":["e001"],"uncertain":false}]}`
		} else if !qa {
			reply = `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"total_tokens":40}}`, reply)
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
			after, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, ready.Slug)
			require.NoError(t, err)
			if mutation.name == "page_version" {
				require.Equal(t, "ready", attempt.Status, "a bounded merge and QA should resolve a concurrent page edit")
				require.Equal(t, before.Version+2, after.Version, "the human edit and rebased source write create separate revisions")
				require.Contains(t, after.Content, "Human clarification: keep the scheduling boundary explicit")
				require.Equal(t, "ready", after.SourceProvenance.State)
			} else {
				require.Equal(t, "failed", attempt.Status)
				require.NotEmpty(t, attempt.Reason)
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
	var currentContribution types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", page.ID, f.ds.ID).Take(&currentContribution).Error)
	require.Equal(t, "unverified", currentContribution.State)
	var archivedContribution types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NOT NULL", page.ID, f.ds.ID).Take(&archivedContribution).Error)
	require.Equal(t, "ready", archivedContribution.State, "the exact pre-edit source contribution remains attached to its historical revision")
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
