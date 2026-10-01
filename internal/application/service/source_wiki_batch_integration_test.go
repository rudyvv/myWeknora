//go:build integration

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
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
