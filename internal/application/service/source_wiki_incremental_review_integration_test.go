//go:build integration

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceWikiIncrementalPublicationConsumesRegenerationToTerminalOutcome(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int64
	wiki, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		providerCalls.Add(1)
		stage := r.Header.Get("X-Source-Wiki-Test-Stage")
		var reply string
		switch {
		case stage == "source_wiki_generate":
			reply = `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
		case qa || stage == "source_wiki_qa":
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		case strings.Contains(stage, "Independently check this small group"):
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			require.NotEmpty(t, request.Messages)
			var input struct {
				Cards []struct {
					TopicKey string `json:"topic_key"`
				} `json:"cards"`
			}
			require.NoError(t, json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &input))
			cards := make([]map[string]any, 0, len(input.Cards))
			for _, card := range input.Cards {
				cards = append(cards, map[string]any{"topic_key": card.TopicKey, "supported": true, "reason": ""})
			}
			encoded, err := json.Marshal(map[string]any{"supported": true, "reason": "local group is consistent", "cards": cards})
			require.NoError(t, err)
			reply = string(encoded)
		case strings.Contains(stage, "full initial candidate set"):
			reply = `{"supported":true,"reason":"the full candidate set is consistent"}`
		default:
			t.Errorf("unexpected provider stage in successful regeneration fixture: %q", stage)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, reply)
	})
	initial, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", initial.Status)
	callsBeforePublication := providerCalls.Load()

	f.advanceJava("package demo; public class Service { public String getPushSchedule(String name) { return \"changed\"; } }\n")
	syncSourceFixture(t, f)
	next := latestIncrementalRun(t, f)
	deliveryErr, pending := t16RegressionDrainUpdateLane(t, f, generator.(*sourceWikiService))
	require.NoError(t, deliveryErr, "the accepted publication must be consumed by the durable Wiki update lane")
	require.Zero(t, pending)

	var plan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&plan).Error)
	require.Equal(t, "completed", plan.Status)

	service := generator.(*sourceWikiService)
	t.Cleanup(service.StopSourceWikiBatches)
	deadline := time.Now().Add(30 * time.Second)
	var attempts []types.SourceWikiAttempt
	var batches []types.SourceWikiBatch
	var item types.SourceWikiUpdatePlanItem
	var targetAttempt types.SourceWikiAttempt
	itemFound := false
	targetAttemptFound := false
	targetTerminal := false
	for time.Now().Before(deadline) {
		attempts = nil
		batches = nil
		require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Order("created_at ASC, id ASC").Find(&attempts).Error)
		require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Order("created_at ASC").Find(&batches).Error)
		targetAttempt = types.SourceWikiAttempt{}
		for _, attempt := range attempts {
			if attempt.TopicKey == "module/src" {
				targetAttempt = attempt
			}
		}
		targetAttemptFound = targetAttempt.ID != ""
		targetTerminal = false
		if targetAttemptFound {
			if targetAttempt.Status == "ready" || targetAttempt.Status == "failed" {
				targetTerminal = true
			}
			if targetAttempt.BatchID != "" {
				for _, batch := range batches {
					if batch.ID == targetAttempt.BatchID && batch.Status != "queued" && batch.Status != "running" && batch.FinishedAt != nil {
						targetTerminal = true
					}
				}
			}
		}
		item = types.SourceWikiUpdatePlanItem{}
		itemQuery := f.db.Where("plan_id=? AND topic_key=?", plan.ID, "module/src").Find(&item)
		require.NoError(t, itemQuery.Error)
		itemFound = itemQuery.RowsAffected > 0
		itemTerminal := itemFound && item.State != "pending" && item.State != "running"
		if targetAttemptFound && targetTerminal && itemTerminal && providerCalls.Load() > callsBeforePublication {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.True(t, itemFound, "the persisted regeneration plan item must remain queryable after the provider work")
	require.Equal(t, "regenerate", item.Action)
	require.NotContains(t, []string{"pending", "running"}, item.State,
		"the affected topic must leave pending only after durable generation work reaches an outcome")
	require.True(t, targetAttemptFound, "the target module must have its own durable generation attempt")
	require.Equal(t, "ready", targetAttempt.Status, "the target module attempt itself must publish successfully")
	require.Equal(t, f.ds.ID, targetAttempt.SourceID)
	require.Equal(t, "module", targetAttempt.TopicKind)
	require.Equal(t, "module/src", targetAttempt.TopicKey)
	require.Equal(t, next.Snapshot.ID, targetAttempt.SnapshotID)
	require.Greater(t, targetAttempt.Calls, 0, "the target module attempt must execute its own provider calls")
	require.True(t, targetTerminal, "the target attempt or its exact parent batch must reach a terminal, recoverable result")
	var completedCalls []types.SourceWikiAttemptCall
	require.NoError(t, f.db.Where("attempt_id=? AND phase=? AND outcome=? AND completed_at IS NOT NULL", targetAttempt.ID, "source_wiki_generate", "succeeded").Find(&completedCalls).Error)
	require.NotEmpty(t, completedCalls, "the target attempt must persist its own successfully completed generation call")
	require.Greater(t, providerCalls.Load(), callsBeforePublication, "the changed topic must actually pass through the configured generation provider")

	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, targetAttempt.Slug)
	require.NoError(t, err)
	require.Equal(t, types.WikiPageStatusPublished, page.Status)
	require.NotNil(t, page.SourceProvenance)
	require.Equal(t, targetAttempt.SourceID, page.SourceProvenance.SourceID)
	require.Equal(t, targetAttempt.TopicKind, page.SourceProvenance.TopicKind)
	require.Equal(t, targetAttempt.TopicKey, page.SourceProvenance.TopicKey)
	require.Equal(t, "ready", page.SourceProvenance.State)
	require.Equal(t, targetAttempt.SnapshotID, page.SourceProvenance.ApplicableSnapshotID,
		"the published page must be applicable to the exact snapshot generated by the target attempt")
	var contribution types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND topic_kind=? AND topic_key=? AND revision_id IS NULL",
		page.ID, targetAttempt.SourceID, targetAttempt.TopicKind, targetAttempt.TopicKey).Take(&contribution).Error)
	require.Equal(t, "ready", contribution.State)
	require.Equal(t, targetAttempt.SnapshotID, contribution.ApplicableSnapshotID)
	require.Equal(t, targetAttempt.SnapshotID, contribution.TargetSnapshotID)
	require.Equal(t, page.Version, contribution.PageVersion)
}

func TestSourceWikiIncrementalOversizedFactsPersistSourceWideFallback(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"The service changed.","sections":[{"text":"The updated service is available.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	f.advanceJava("package demo; public class Service { public int updated() { return 2; } }\n")
	syncSourceFixture(t, f)
	next := latestIncrementalRun(t, f)
	require.NoError(t, f.db.Exec(
		`UPDATE source_file_versions SET facts=(SELECT jsonb_agg(jsonb_build_object('kind','identifier','name','x')) FROM generate_series(1,?)) WHERE snapshot_id=?`,
		types.SourceWikiImpactMaxFacts+1, next.Snapshot.ID,
	).Error)

	deliveryErr, pending := t16RegressionDrainUpdateLane(t, f, generator.(*sourceWikiService))
	var plan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&plan).Error)
	require.Equal(t, "completed", plan.Status,
		"fact-budget overflow is deterministic and must be committed as fallback, not left for delivery retry")
	require.True(t, plan.SourceWideStale, "overflow must conservatively mark all source Wiki contributions stale")
	require.NotEmpty(t, plan.ReasonCode, "the durable fallback records why exact impact was unavailable")
	require.NoError(t, deliveryErr, "oversized facts must not escape as a transient task error")
	require.Zero(t, pending, "the publication delivery must be acknowledged after durable fallback")
}

func TestSourceWikiIncrementalCompleteManifestRemovesOnlyDeletedMixedPageContribution(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var generationCalls atomic.Int64
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		if generationCalls.Add(1) == 1 {
			return `{"title":"Scheduling module","summary":"Source A documents schedule retrieval.","sections":[{"text":"Source A: getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
		}
		return `{"title":"Scheduling module","summary":"Source B documents a named lookup.","sections":[{"text":"Source B: getPushSchedule accepts a name and returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	request := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	first, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", first.Status)
	pageA, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)

	sourceB := *f.ds
	sourceB.ID = uuid.NewString()
	sourceB.Name = "Independent source B"
	_, err = f.service.CreateDataSource(f.ctx, &sourceB)
	require.NoError(t, err)
	syncSourceFixture(t, f, sourceB.ID)
	request.SourceID = sourceB.ID
	second, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", second.Status)
	pageB, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, second.Slug)
	require.NoError(t, err)
	require.Contains(t, pageA.Content, "Source A: getPushSchedule returns a schedule.")
	require.NotContains(t, pageA.Content, "Source B: getPushSchedule accepts a name and returns a schedule.")
	require.Contains(t, pageB.Content, "Source B: getPushSchedule accepts a name and returns a schedule.")
	require.NotContains(t, pageB.Content, "Source A: getPushSchedule returns a schedule.")

	var contributionA, contributionB types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", pageA.ID, f.ds.ID).Take(&contributionA).Error)
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", pageB.ID, sourceB.ID).Take(&contributionB).Error)
	var contributionBBody struct {
		Content          string                     `json:"content"`
		SourceProvenance types.SourceWikiProvenance `json:"source_provenance"`
	}
	require.NoError(t, json.Unmarshal(contributionB.Contribution, &contributionBBody))
	require.Contains(t, contributionBBody.Content, "Source B: getPushSchedule accepts a name and returns a schedule.")
	require.NotContains(t, contributionBBody.Content, "Source A: getPushSchedule returns a schedule.")
	require.Equal(t, sourceB.ID, contributionBBody.SourceProvenance.SourceID,
		"B's text and evidence are independently attributed before composing the shared page")
	preservedBJSON := append(types.JSON(nil), contributionB.Contribution...)
	preservedBRefs := append(types.StringArray(nil), pageB.SourceRefs...)
	preservedBBody := pageB.Content
	pageA.Content += "\n" + pageB.Content
	pageA.SourceRefs = append(pageA.SourceRefs, pageB.SourceRefs...)
	require.NoError(t, f.db.Save(pageA).Error)
	contributionB.PageID = pageA.ID
	contributionB.PageVersion = pageA.Version
	require.NoError(t, f.db.Save(&contributionB).Error)

	f.advanceFiles(map[string][]byte{"src/Service.java": nil})
	syncSourceFixture(t, f)
	next := latestIncrementalRun(t, f)
	deliveryErr, pending := t16RegressionDrainUpdateLane(t, f, generator.(*sourceWikiService))
	require.NoError(t, deliveryErr)
	require.Zero(t, pending)

	updatedPage, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, pageA.Slug)
	require.NoError(t, err)
	require.NotContains(t, updatedPage.Content, "Source A: getPushSchedule returns a schedule.",
		"complete manifest deletion withdraws only the deleted source's independently attributed body")
	require.Contains(t, updatedPage.Content, "Source B: getPushSchedule accepts a name and returns a schedule.")
	require.Equal(t, preservedBBody, updatedPage.Content, "the surviving source body is rendered without stale A prose")
	require.Equal(t, preservedBRefs, updatedPage.SourceRefs, "only the deleted source's source references are removed")
	require.NotNil(t, updatedPage.SourceProvenance)
	require.Equal(t, sourceB.ID, updatedPage.SourceProvenance.SourceID)
	require.Equal(t, "ready", updatedPage.SourceProvenance.State)
	var remaining []types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND revision_id IS NULL", pageA.ID).Order("source_id").Find(&remaining).Error)
	require.Len(t, remaining, 1)
	require.Equal(t, sourceB.ID, remaining[0].SourceID)
	require.JSONEq(t, string(preservedBJSON), string(remaining[0].Contribution),
		"B's exact typed contribution, evidence and body remain byte-for-byte equivalent as JSON")
	require.Equal(t, contributionB.EvidenceSHA256, remaining[0].EvidenceSHA256)
	require.Equal(t, contributionB.ApplicableSnapshotID, remaining[0].ApplicableSnapshotID)

	var plan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&plan).Error)
	var removal types.SourceWikiUpdatePlanItem
	require.NoError(t, f.db.Where("plan_id=? AND topic_key=?", plan.ID, "module/src").Take(&removal).Error)
	require.Equal(t, "remove", removal.Action)
	require.Equal(t, "completed", removal.State)
}

func t16RegressionDrainUpdateLane(t *testing.T, f *javaSourceFixture, processor interfaces.SourceWikiUpdateProcessor) (error, int64) {
	t.Helper()
	_, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 100)
	require.NoError(t, err)
	pendingOps := repository.NewTaskPendingOpsRepository(f.db)
	worker := NewSourceWikiUpdateWorker(processor, pendingOps, nil)
	trigger, err := json.Marshal(types.SourceWikiUpdateTriggerPayload{TenantID: 1, KnowledgeBaseID: f.kb.ID})
	require.NoError(t, err)
	var deliveryErr error
	for i := 0; i < 20; i++ {
		var pending int64
		require.NoError(t, f.db.Model(&types.TaskPendingOp{}).
			Where("tenant_id=? AND scope_id=? AND task_type=?", 1, f.kb.ID, types.TypeSourceWikiUpdate).
			Count(&pending).Error)
		if pending == 0 {
			return nil, 0
		}
		deliveryErr = worker.Handle(f.ctx, asynq.NewTask(types.TypeSourceWikiUpdate, trigger))
		if deliveryErr != nil {
			break
		}
	}
	var pending int64
	require.NoError(t, f.db.Model(&types.TaskPendingOp{}).
		Where("tenant_id=? AND scope_id=? AND task_type=?", 1, f.kb.ID, types.TypeSourceWikiUpdate).
		Count(&pending).Error)
	return deliveryErr, pending
}
