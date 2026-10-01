//go:build integration

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func newSourceWikiBatchLedgerFixture(t *testing.T, f *javaSourceFixture) *repository.SourceWikiBatchLedger {
	t.Helper()
	_, _ = newSourceWikiFixture(t, f, func(bool) string { return `{}` })
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	return repository.NewSourceWikiBatchLedger(f.db)
}

func newSourceWikiTestBatch(f *javaSourceFixture, snapshotID string, now time.Time) *types.SourceWikiBatch {
	return &types.SourceWikiBatch{
		ID: uuid.NewString(), TenantID: 1, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: snapshotID,
		SourceConfigFingerprint: strings.Repeat("a", 64), SourceUpdatedAt: f.ds.UpdatedAt,
		ModelID: f.kb.SummaryModelID, ModelSettingsFingerprint: strings.Repeat("b", 64), ModelContextWindow: 65536,
		MaxCompletionTokens: types.SourceWikiBatchMaxCompletionTokens, Status: "running", Phase: "skeleton",
		MaxCalls: types.SourceWikiBatchMaxCalls, MaxTokens: types.SourceWikiBatchMaxTokens,
		MaxElapsedMS: types.SourceWikiBatchMaxElapsed.Milliseconds(), MaxInitialTopics: types.SourceWikiBatchMaxInitialTopics,
		SkeletonMaxCalls: types.SourceWikiBatchSkeletonMaxCalls, SkeletonMaxTokens: types.SourceWikiBatchSkeletonMaxTokens,
		QAMaxCalls: types.SourceWikiBatchQAMaxCalls, QAMaxTokens: types.SourceWikiBatchQAMaxTokens,
		DeadlineAt: now.Add(types.SourceWikiBatchMaxElapsed), CreatedAt: now, UpdatedAt: now,
	}
}

func TestSourceWikiBatchLedgerReservesBudgetsAndPersistsStableCoverage(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	ledger := newSourceWikiBatchLedgerFixture(t, f)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	files, _, err := repository.LoadSourceWikiSkeletonEvidence(f.ctx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.NotEmpty(t, files, "inventory and parsed facts are pinned to the published snapshot")
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	require.NoError(t, ledger.Create(f.ctx, batch))

	var first *types.SourceWikiBatchCallReservation
	for i := 0; i < types.SourceWikiBatchSkeletonMaxCalls; i++ {
		reservation, err := ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
			BatchID: batch.ID, Phase: "skeleton", ProviderPhase: "skeleton_check", ReservedTokens: 10, Now: now,
		})
		require.NoError(t, err)
		if i == 0 {
			first = reservation
		}
	}
	_, err = ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
		BatchID: batch.ID, Phase: "skeleton", ProviderPhase: "skeleton_check", ReservedTokens: 10, Now: now,
	})
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchBudgetExhausted, "the skeleton call cap must reject before dispatch")
	actual := 5
	require.NoError(t, ledger.RecordCall(f.ctx, first.ID, "provider_error", &actual, now.Add(time.Second)))
	var budget types.SourceWikiBatch
	require.NoError(t, f.db.Where("id = ?", batch.ID).Take(&budget).Error)
	require.Equal(t, 60, budget.TokensReserved, "known failed-call usage does not refund a reservation")
	require.Equal(t, 60, budget.SkeletonTokensReserved)

	topics := []types.SourceWikiTopic{
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "system", Kind: "system", Title: "System overview", Priority: 120, Status: "planned"},
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "module/src/orders", Kind: "module", ModulePath: "src/orders", Title: "orders", Priority: 90, Status: "planned"},
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "flow/GET /orders", Kind: "flow", Title: "GET /orders", Priority: 100, Status: "expansion", Uncertain: true, UncertaintyReasons: []string{"route mapping is conditional"}},
	}
	require.NoError(t, ledger.SavePlan(f.ctx, batch.ID, topics, now.Add(2*time.Second)))
	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	require.Len(t, coverage, 3)
	byTopic := make(map[string]types.SourceWikiCoverageTopic, len(coverage))
	for _, topic := range coverage {
		byTopic[topic.TopicKey] = topic
	}
	require.Equal(t, "expansion", byTopic["flow/GET /orders"].Status)
	require.True(t, byTopic["flow/GET /orders"].Uncertain)
	require.Equal(t, "[\"route mapping is conditional\"]", string(byTopic["flow/GET /orders"].UncertaintyReasons))
	hash := sha256.Sum256([]byte("src/orders"))
	require.Equal(t, "concept/source-"+f.ds.ID+"/module-"+hex.EncodeToString(hash[:8]), byTopic["module/src/orders"].WikiSlug,
		"the established module slug is retained for existing directory links")
	require.NoError(t, ledger.UpdateTopic(f.ctx, batch.ID, "module/src/orders", "ready", "", "", publication.SnapshotID, now.Add(3*time.Second)))
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "batch_qa", "running", "", "", 2, now.Add(4*time.Second)))
	for i := 0; i < types.SourceWikiBatchQAMaxCalls; i++ {
		_, err := ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
			BatchID: batch.ID, Phase: "batch_qa", ProviderPhase: "coverage_qa", ReservedTokens: 10, Now: now.Add(5 * time.Second),
		})
		require.NoError(t, err)
	}
	_, err = ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
		BatchID: batch.ID, Phase: "batch_qa", ProviderPhase: "coverage_qa", ReservedTokens: 10, Now: now.Add(5 * time.Second),
	})
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchBudgetExhausted)
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "finished", "completed", "", "", 2, now.Add(6*time.Second)))

	// Replanning the same stable topic in a later batch must not create a
	// duplicate coverage row or invalidate the current ready card.
	next := newSourceWikiTestBatch(f, publication.SnapshotID, now.Add(7*time.Second))
	require.NoError(t, ledger.Create(f.ctx, next))
	require.NoError(t, ledger.SavePlan(f.ctx, next.ID, topics, now.Add(8*time.Second)))
	coverage, err = ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	require.Len(t, coverage, 3)
	require.Equal(t, "ready", byCoverageTopic(t, coverage, "module/src/orders").Status)
	require.NoError(t, ledger.UpdateProgress(f.ctx, next.ID, "finished", "completed", "", "", 2, now.Add(9*time.Second)))

	// A source update keeps the old slug but returns coverage to planned for the
	// new fixed snapshot; no stale ready state is presented as current.
	changedSnapshot := uuid.NewString()
	third := newSourceWikiTestBatch(f, changedSnapshot, now.Add(10*time.Second))
	require.NoError(t, ledger.Create(f.ctx, third))
	updatedTopics := append([]types.SourceWikiTopic(nil), topics...)
	for i := range updatedTopics {
		updatedTopics[i].SnapshotID = changedSnapshot
	}
	require.NoError(t, ledger.SavePlan(f.ctx, third.ID, updatedTopics, now.Add(11*time.Second)))
	coverage, err = ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	updated := byCoverageTopic(t, coverage, "module/src/orders")
	require.Equal(t, "planned", updated.Status)
	require.Equal(t, "concept/source-"+f.ds.ID+"/module-"+hex.EncodeToString(hash[:8]), updated.WikiSlug)
}

func TestSourceWikiBatchDeadlineRetainsChargesAndTopicOutcomes(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	ledger := newSourceWikiBatchLedgerFixture(t, f)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	require.NoError(t, ledger.Create(f.ctx, batch))
	first, err := ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
		BatchID: batch.ID, Phase: "skeleton", ProviderPhase: "skeleton_check", ReservedTokens: 10, Now: now.Add(time.Second),
	})
	require.NoError(t, err)
	second, err := ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
		BatchID: batch.ID, Phase: "skeleton", ProviderPhase: "skeleton_check", ReservedTokens: 10, Now: now.Add(time.Second),
	})
	require.NoError(t, err)
	topics := []types.SourceWikiTopic{
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "planned", Kind: "system", Title: "Planned", Status: "planned"},
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "draft", Kind: "module", ModulePath: "src/draft", Title: "Draft", Status: "planned"},
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "expansion", Kind: "flow", Title: "Expansion", Status: "expansion"},
	}
	require.NoError(t, ledger.SavePlan(f.ctx, batch.ID, topics, now.Add(2*time.Second)))
	require.NoError(t, ledger.UpdateTopic(f.ctx, batch.ID, "draft", "draft", "", "partial evidence", "", now.Add(2500*time.Millisecond)))

	actual := 11
	err = ledger.RecordCall(f.ctx, first.ID, "succeeded", &actual, batch.DeadlineAt)
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchDeadline)
	var savedBatch types.SourceWikiBatch
	require.NoError(t, f.db.Where("id = ?", batch.ID).Take(&savedBatch).Error)
	require.Equal(t, "expired", savedBatch.Status)
	require.Equal(t, 2, savedBatch.CallsReserved)
	require.Equal(t, 21, savedBatch.TokensReserved, "settled usage remains charged when the absolute deadline expires")
	var savedFirst, savedSecond types.SourceWikiBatchCallReservation
	require.NoError(t, f.db.Where("id = ?", first.ID).Take(&savedFirst).Error)
	require.NoError(t, f.db.Where("id = ?", second.ID).Take(&savedSecond).Error)
	require.Equal(t, "succeeded", savedFirst.Outcome)
	require.Equal(t, "unknown", savedSecond.Outcome, "an in-flight reservation at expiry is not refunded")

	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", byCoverageTopic(t, coverage, "planned").Status)
	require.Equal(t, "draft", byCoverageTopic(t, coverage, "draft").Status)
	require.Equal(t, "expansion", byCoverageTopic(t, coverage, "expansion").Status)
	require.ErrorIs(t, ledger.UpdateTopic(f.ctx, batch.ID, "draft", "ready", "", "", publication.SnapshotID, batch.DeadlineAt), repository.ErrSourceWikiBatchInvalidState)
}

func TestSourceWikiBatchPreflightPinsPublishedInputsWithoutCreatingOrDispatching(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(bool) string {
		providerCalls.Add(1)
		return `{}`
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)

	model, err := f.modelService.GetModelByID(f.ctx, f.kb.SummaryModelID)
	require.NoError(t, err)
	model.Parameters.ContextWindow = 8192
	model.Parameters.MaxOutputTokens = 3072
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))

	preflightService, ok := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	require.True(t, ok, "source Wiki exposes batch preflight through its public interface")
	result, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	require.True(t, result.PreflightPassed)
	require.False(t, result.StartAvailable, "preflight must not imply the T17-backed dispatcher is available")
	require.Equal(t, f.ds.ID, result.SourceID)
	require.Equal(t, publication.SnapshotID, result.SnapshotID)
	require.Equal(t, 8192, result.ModelContextWindow)
	require.True(t, result.ModelContextKnown)
	require.Equal(t, 3072, result.MaxCompletionTokens, "preflight caps completion to the configured model output limit")
	require.Equal(t, 2, result.CandidateCount, "the bounded fixture has the system and service module topics")
	require.Equal(t, 2, result.InitialCount)
	require.Equal(t, 1, result.ModuleCount)
	require.Len(t, result.InitialTopics, 2)
	require.Equal(t, "system", result.InitialTopics[0].TopicKey)
	require.LessOrEqual(t, len(result.InitialTopics), types.SourceWikiBatchMaxInitialTopics)
	require.NotEmpty(t, result.SourceConfigFingerprint)
	require.NotEmpty(t, result.ModelSettingsFingerprint)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "source_config_fingerprint")
	require.NotContains(t, string(encoded), "model_settings_fingerprint")
	var batchCount int64
	require.NoError(t, f.db.Model(&types.SourceWikiBatch{}).Where("source_id = ?", f.ds.ID).Count(&batchCount).Error)
	require.Zero(t, batchCount, "preflight does not leave an abandoned queued or running batch")
	require.Zero(t, providerCalls.Load(), "preflight reads model configuration but never calls the provider")

	unauthorized := context.WithValue(f.ctx, types.KBGrantsContextKey, nil)
	_, err = preflightService.PreflightSourceWikiBatch(unauthorized, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.Error(t, err, "service-side KB write authorization remains required even if an HTTP route is miswired")

	model.Parameters.ContextWindow = 7000
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))
	_, err = preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.Error(t, err, "known contexts that cannot fit the bounded prompt and completion are rejected")
	model.Parameters.ContextWindow = 0
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))
	unknownContext, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	require.False(t, unknownContext.ModelContextKnown)
	require.NotEmpty(t, unknownContext.Warnings)

	ledger := repository.NewSourceWikiBatchLedger(f.db)
	previous := newSourceWikiTestBatch(f, publication.SnapshotID, time.Now().UTC().Truncate(time.Millisecond))
	require.NoError(t, ledger.Create(f.ctx, previous))
	_, err = preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchAlreadyActive)
	failedAt := previous.CreatedAt.Add(time.Second)
	require.NoError(t, ledger.UpdateProgress(f.ctx, previous.ID, "finished", "failed", "", "provider budget exhausted", 0, failedAt))
	restarted, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID,
		types.SourceWikiBatchPreflightRequest{RestartOfBatchID: previous.ID})
	require.NoError(t, err)
	require.NotNil(t, restarted.RestartFrom)
	require.Equal(t, previous.ID, restarted.RestartFrom.ID)
	require.Equal(t, "failed", restarted.RestartFrom.Status)
	require.Equal(t, 0, restarted.RestartFrom.Cursor)
	var persistedPrevious types.SourceWikiBatch
	require.NoError(t, f.db.Where("id = ?", previous.ID).Take(&persistedPrevious).Error)
	require.Equal(t, "failed", persistedPrevious.Status, "restart planning never resets or resumes the prior budget")
	require.Zero(t, providerCalls.Load())
}

func byCoverageTopic(t *testing.T, topics []types.SourceWikiCoverageTopic, key string) types.SourceWikiCoverageTopic {
	t.Helper()
	for _, topic := range topics {
		if topic.TopicKey == key {
			return topic
		}
	}
	require.FailNow(t, "coverage topic missing", key)
	return types.SourceWikiCoverageTopic{}
}
