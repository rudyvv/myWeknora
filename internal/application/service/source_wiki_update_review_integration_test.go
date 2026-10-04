//go:build integration

package service

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func sourceWikiReviewModelOverride(w http.ResponseWriter, r *http.Request, _ bool) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	var request struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &request) != nil || len(request.Messages) == 0 {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	stage := request.Messages[0].Content
	content := `{"supported":true}`
	switch {
	case stage == "source_wiki_generate":
		content = `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	case stage == "source_wiki_qa":
		content = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
	case strings.Contains(stage, "small group of source-topic cards"):
		var input struct {
			Cards []struct {
				TopicKey string `json:"topic_key"`
			} `json:"cards"`
		}
		if len(request.Messages) < 2 || json.Unmarshal([]byte(request.Messages[1].Content), &input) != nil {
			http.Error(w, "bad batch QA input", http.StatusBadRequest)
			return
		}
		cards := make([]map[string]any, 0, len(input.Cards))
		for _, card := range input.Cards {
			cards = append(cards, map[string]any{"topic_key": card.TopicKey, "supported": true, "reason": ""})
		}
		encoded, _ := json.Marshal(map[string]any{"supported": true, "reason": "", "cards": cards})
		content = string(encoded)
	case strings.Contains(stage, "full initial candidate set"):
		content = `{"supported":true,"reason":""}`
	}
	fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, content)
}

func drainSourceWikiReviewUpdateLane(t *testing.T, f *javaSourceFixture, generator interfaces.SourceWikiService) {
	t.Helper()
	_, err := f.service.sourceSnapshots.RelaySourcePublicationOutbox(f.ctx, 100)
	require.NoError(t, err)
	worker := NewSourceWikiUpdateWorker(generator.(*sourceWikiService), repository.NewTaskPendingOpsRepository(f.db), nil)
	trigger, err := json.Marshal(types.SourceWikiUpdateTriggerPayload{TenantID: 1, KnowledgeBaseID: f.kb.ID})
	require.NoError(t, err)
	for i := 0; i < 20; i++ {
		var pending int64
		require.NoError(t, f.db.Model(&types.TaskPendingOp{}).
			Where("tenant_id=? AND scope_id=? AND task_type=?", 1, f.kb.ID, types.TypeSourceWikiUpdate).
			Count(&pending).Error)
		if pending == 0 {
			return
		}
		require.NoError(t, worker.Handle(f.ctx, asynq.NewTask(types.TypeSourceWikiUpdate, trigger)))
	}
	t.Fatal("source Wiki update lane did not drain within the fixture bound")
}

func TestSourceWikiAffectedPublicationHandsOffToBoundedGeneration(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{"supported":true}` }, sourceWikiReviewModelOverride)
	initial, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", initial.Status)
	f.advanceJava("package demo; public class Service { public String getPushSchedule(String name) { return \"changed\"; } }\n")
	syncSourceFixture(t, f)
	next := latestIncrementalRun(t, f)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	var plan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&plan).Error)
	var item types.SourceWikiUpdatePlanItem
	require.NoError(t, f.db.Where("plan_id=? AND topic_key=?", plan.ID, "module/src").Take(&item).Error)
	var batches int64
	require.NoError(t, f.db.Model(&types.SourceWikiBatch{}).Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Count(&batches).Error)
	require.Positive(t, batches, "affected publication must create a durable bounded T17 generation batch")
	require.True(t, item.State == "running" || item.State == "completed" || item.State == "failed")
}

func TestSourceWikiOversizedFactsPersistFallbackWithoutRetry(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{"supported":true}` }, sourceWikiReviewModelOverride)
	f.advanceJava("package demo; public class Service { public int updated() { return 2; } }\n")
	syncSourceFixture(t, f)
	next := latestIncrementalRun(t, f)
	require.NoError(t, f.db.Exec(`UPDATE source_file_versions SET facts=(SELECT jsonb_agg(jsonb_build_object('kind','identifier','name','x')) FROM generate_series(1,?)) WHERE snapshot_id=?`,
		types.SourceWikiImpactMaxFacts+1, next.Snapshot.ID).Error)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	var plan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("source_id=? AND snapshot_id=?", f.ds.ID, next.Snapshot.ID).Take(&plan).Error)
	require.True(t, plan.SourceWideStale)
	require.Equal(t, "completed", plan.Status)
	var pending int64
	require.NoError(t, f.db.Model(&types.SourceWikiUpdatePlanItem{}).Where("plan_id=? AND state='pending'", plan.ID).Count(&pending).Error)
	require.Zero(t, pending, "an unbounded impact proof must persist a terminal conservative fallback")
}

func TestSourceWikiConfirmedDeletionPreservesOtherAttributedContribution(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(bool) string { return `{"supported":true}` }, sourceWikiReviewModelOverride)
	request := types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"}
	a, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", a.Status)
	pageA, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, a.Slug)
	require.NoError(t, err)
	otherSource := *f.ds
	otherSource.ID, otherSource.Name = uuid.NewString(), "Independent published source"
	_, err = f.service.CreateDataSource(f.ctx, &otherSource)
	require.NoError(t, err)
	syncSourceFixture(t, f, otherSource.ID)
	request.SourceID = otherSource.ID
	b, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", b.Status)
	pageB, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, b.Slug)
	require.NoError(t, err)
	var contributionA, contributionB types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", pageA.ID, f.ds.ID).Take(&contributionA).Error)
	require.NoError(t, f.db.Where("page_id=? AND source_id=? AND revision_id IS NULL", pageB.ID, otherSource.ID).Take(&contributionB).Error)
	preserved := append(types.JSON(nil), contributionB.Contribution...)
	pageA.Content += "\n" + pageB.Content
	pageA.SourceRefs = append(pageA.SourceRefs, pageB.SourceRefs...)
	require.NoError(t, f.db.Save(pageA).Error)
	contributionB.PageID, contributionB.PageVersion = pageA.ID, pageA.Version
	require.NoError(t, f.db.Save(&contributionB).Error)
	f.advanceFiles(map[string][]byte{"src/Service.java": nil})
	syncSourceFixture(t, f)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	var survivors []types.SourceWikiPageContribution
	require.NoError(t, f.db.Where("page_id=? AND revision_id IS NULL", pageA.ID).Order("source_id").Find(&survivors).Error)
	require.Len(t, survivors, 1)
	require.Equal(t, otherSource.ID, survivors[0].SourceID)
	require.JSONEq(t, string(preserved), string(survivors[0].Contribution))
}
