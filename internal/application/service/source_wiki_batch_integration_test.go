//go:build integration

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sourceWikiBatchSQLCapture struct {
	logger.Interface
	mu      sync.Mutex
	queries []string
}

func (c *sourceWikiBatchSQLCapture) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	query, rows := fc()
	c.mu.Lock()
	c.queries = append(c.queries, query)
	c.mu.Unlock()
	c.Interface.Trace(ctx, begin, func() (string, int64) { return query, rows }, err)
}

func (c *sourceWikiBatchSQLCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...)
}

func newSourceWikiBatchLedgerFixture(t *testing.T, f *javaSourceFixture) *repository.SourceWikiBatchLedger {
	t.Helper()
	_, _ = newSourceWikiFixture(t, f, func(bool) string { return `{}` })
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

func TestSourceWikiCoverageTelemetryRequiresCompleteSnapshotInventory(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` })
	reader, ok := generator.(interfaces.SourceWikiBatchReadService)
	require.True(t, ok)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	now := time.Now().UTC()
	keys := []string{"ready", "stale", "failed", "planned", "expansion", "missing"}
	inventory := types.SourceWikiImpactTopicInventory{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID,
		Complete: true, ExpectedTopicCount: len(keys),
	}
	for _, key := range keys {
		inventory.Topics = append(inventory.Topics, types.SourceWikiImpactTopicDependencies{TopicKey: key})
	}
	inventoryJSON, err := json.Marshal(inventory)
	require.NoError(t, err)
	var plan types.SourceWikiUpdatePlan
	require.NoError(t, f.db.Where("tenant_id=? AND knowledge_base_id=? AND source_id=? AND snapshot_id=?",
		f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID).Take(&plan).Error)
	plan.Status = "completed"
	plan.NextInventory = types.JSON(inventoryJSON)
	plan.UpdatedAt = now
	plan.CompletedAt = &now
	require.NoError(t, f.db.Save(&plan).Error)
	topics := []types.SourceWikiCoverageTopic{
		{ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "ready", Kind: "flow", Title: "Ready flow", Status: "ready", LastReadySnapshotID: publication.SnapshotID, UncertaintyReasons: types.JSON(`[]`), Relations: types.JSON(`[]`)},
		{ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "stale", Kind: "flow", Title: "Stale flow", Status: "ready", LastReadySnapshotID: "older-snapshot", UncertaintyReasons: types.JSON(`[]`), Relations: types.JSON(`[]`)},
		{ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "failed", Kind: "flow", Title: "Failed flow", Status: "failed", UncertaintyReasons: types.JSON(`[]`), Relations: types.JSON(`[]`)},
		{ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "planned", Kind: "flow", Title: "Planned flow", Status: "planned", Initial: true, UncertaintyReasons: types.JSON(`[]`), Relations: types.JSON(`[]`)},
		{ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "expansion", Kind: "flow", Title: "Expansion flow", Status: "expansion", UncertaintyReasons: types.JSON(`[]`), Relations: types.JSON(`[]`)},
	}
	require.NoError(t, f.db.Create(&topics).Error)
	coverage, err := reader.GetSourceWikiCoverageSummary(f.ctx, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.NotNil(t, coverage)
	require.Equal(t, int64(6), *coverage.EligibleTopics)
	require.Equal(t, int64(1), *coverage.ReadyTopics)
	require.Equal(t, int64(1), *coverage.StaleTopics)
	require.Equal(t, int64(1), *coverage.FailedTopics)
	require.Equal(t, int64(2), *coverage.UngeneratedTopics)
	require.Equal(t, int64(1), *coverage.DeferredTopics)

	wrongTenant := types.WithCaller(context.Background(), types.Caller{TenantID: 2})
	_, err = reader.GetSourceWikiCoverageSummary(wrongTenant, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.Error(t, err, "coverage summary must respect the same tenant/source read boundary as the UI")

	plan.NextInventory = types.JSON(`{"tenantid":1}`)
	plan.UpdatedAt = now.Add(time.Second)
	require.NoError(t, f.db.Save(&plan).Error)
	coverage, err = reader.GetSourceWikiCoverageSummary(f.ctx, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.Nil(t, coverage, "an incomplete or mismatched inventory is unmeasured, not fabricated zero coverage")
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
	expectedQACursor := 0
	for i := 0; i < types.SourceWikiBatchQAMaxCalls; i++ {
		_, err := ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
			BatchID: batch.ID, Phase: "batch_qa", ProviderPhase: "coverage_qa", ReservedTokens: 10, ExpectedQACursor: &expectedQACursor, Now: now.Add(5 * time.Second),
		})
		require.NoError(t, err)
	}
	_, err = ledger.ReserveCall(f.ctx, types.SourceWikiBatchReserveCallRequest{
		BatchID: batch.ID, Phase: "batch_qa", ProviderPhase: "coverage_qa", ReservedTokens: 10, ExpectedQACursor: &expectedQACursor, Now: now.Add(5 * time.Second),
	})
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchBudgetExhausted)
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "finished", "failed", "", "test batch terminalized", 2, now.Add(6*time.Second)))

	// Replanning the same stable topic in a later batch must not create a
	// duplicate coverage row or invalidate the current ready card.
	next := newSourceWikiTestBatch(f, publication.SnapshotID, now.Add(7*time.Second))
	require.NoError(t, ledger.Create(f.ctx, next))
	require.NoError(t, ledger.SavePlan(f.ctx, next.ID, topics, now.Add(8*time.Second)))
	coverage, err = ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	require.Len(t, coverage, 3)
	require.Equal(t, "ready", byCoverageTopic(t, coverage, "module/src/orders").Status)
	require.NoError(t, ledger.UpdateProgress(f.ctx, next.ID, "finished", "failed", "", "test batch terminalized", 2, now.Add(9*time.Second)))

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

func TestSourceWikiBatchReplanReplacesStaleAttemptLinks(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	ledger := newSourceWikiBatchLedgerFixture(t, f)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)

	now := time.Now().UTC().Truncate(time.Millisecond)
	oldSnapshot := uuid.NewString()
	oldBatch := newSourceWikiTestBatch(f, publication.SnapshotID, now.Add(-2*time.Minute))
	oldBatch.ID = uuid.NewString()
	oldBatch.Status = "failed"
	oldBatch.Phase = "finished"
	finishedAt := now.Add(-time.Minute)
	oldBatch.FinishedAt = &finishedAt
	oldBatch.UpdatedAt = finishedAt
	oldBatch.Reason = "previous batch failed"
	require.NoError(t, f.db.Create(oldBatch).Error)

	type priorTopic struct {
		key, status, snapshotID, lastReadySnapshotID, reason string
		attemptID                                            string
	}
	prior := []priorTopic{
		{key: "failed/topic", status: "failed", snapshotID: publication.SnapshotID, reason: "attempt target mismatch"},
		{key: "ready/current", status: "ready", snapshotID: publication.SnapshotID, lastReadySnapshotID: publication.SnapshotID},
		{key: "ready/old-snapshot", status: "ready", snapshotID: oldSnapshot, lastReadySnapshotID: oldSnapshot},
	}
	attempts := make([]types.SourceWikiAttempt, 0, len(prior))
	coverage := make([]types.SourceWikiCoverageTopic, 0, len(prior))
	for i, item := range prior {
		attempt := types.SourceWikiAttempt{
			ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID,
			SnapshotID: item.snapshotID, BatchID: oldBatch.ID, TopicKind: "flow", TopicKey: item.key,
			ModulePath: item.key, Title: item.key, Slug: "concept/source-" + f.ds.ID + "/" + item.key,
			Status: item.status, Reason: item.reason, Calls: i + 1, Tokens: 100 * (i + 1),
			SourceConfigFingerprint: oldBatch.SourceConfigFingerprint, SourceUpdatedAt: oldBatch.SourceUpdatedAt,
			ModelID: oldBatch.ModelID, ModelSettingsFingerprint: oldBatch.ModelSettingsFingerprint,
			ModelContextWindow: oldBatch.ModelContextWindow, MaxCompletionTokens: oldBatch.MaxCompletionTokens,
			MaxCalls: types.SourceWikiBatchChildMaxCalls, MaxTokens: types.SourceWikiBatchChildMaxTokens,
			MaxElapsedMS: types.SourceWikiAttemptMaxElapsedMS, MaxRepairs: types.SourceWikiAttemptMaxRepairs,
			DeadlineAt: oldBatch.CreatedAt.Add(3 * time.Minute), CreatedAt: oldBatch.CreatedAt, UpdatedAt: oldBatch.CreatedAt,
		}
		attempts = append(attempts, attempt)
		item.attemptID = attempt.ID
		prior[i] = item

		attemptID := attempt.ID
		batchID := oldBatch.ID
		coverage = append(coverage, types.SourceWikiCoverageTopic{
			ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID,
			TopicKey: item.key, SnapshotID: item.snapshotID, Kind: "flow", Title: item.key,
			Status: item.status, AttemptID: &attemptID, BatchID: &batchID, WikiSlug: attempt.Slug,
			LastReadySnapshotID: item.lastReadySnapshotID, Reason: item.reason,
			UncertaintyReasons: types.JSON("[]"), Relations: types.JSON("[]"), UpdatedAt: oldBatch.UpdatedAt,
		})
	}
	require.NoError(t, f.db.Create(&attempts).Error)
	require.NoError(t, f.db.Create(&coverage).Error)

	next := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	require.NotEqual(t, oldBatch.ID, next.ID)
	plan := []types.SourceWikiTopic{
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "failed/topic", Kind: "flow", Title: "failed/topic", Status: "planned"},
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "ready/current", Kind: "flow", Title: "ready/current", Status: "planned"},
		{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "ready/old-snapshot", Kind: "flow", Title: "ready/old-snapshot", Status: "planned"},
	}
	require.NoError(t, ledger.CreateWithPlan(f.ctx, next, plan, now.Add(time.Second)))

	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	byTopic := make(map[string]types.SourceWikiCoverageTopic, len(coverage))
	for _, topic := range coverage {
		byTopic[topic.TopicKey] = topic
	}
	require.Len(t, byTopic, 3)

	failed := byTopic["failed/topic"]
	require.Equal(t, "planned", failed.Status)
	require.Nil(t, failed.AttemptID, "a failed historical attempt cannot be reused by a fresh batch")
	require.NotNil(t, failed.BatchID)
	require.Equal(t, next.ID, *failed.BatchID)

	currentReady := byTopic["ready/current"]
	require.Equal(t, "ready", currentReady.Status)
	require.NotNil(t, currentReady.AttemptID)
	require.Equal(t, prior[1].attemptID, *currentReady.AttemptID)
	require.Equal(t, publication.SnapshotID, currentReady.LastReadySnapshotID)

	staleReady := byTopic["ready/old-snapshot"]
	require.Equal(t, "planned", staleReady.Status)
	require.Nil(t, staleReady.AttemptID, "a ready attempt from an older snapshot cannot become the new batch attempt")
	require.Equal(t, oldSnapshot, staleReady.LastReadySnapshotID)
	require.Equal(t, attempts[2].Slug, staleReady.WikiSlug)

	attemptLedger := repository.NewSourceWikiAttemptLedger(f.db)
	failedAttempt, err := attemptLedger.Get(f.ctx, prior[0].attemptID)
	require.NoError(t, err)
	require.Equal(t, "failed", failedAttempt.Status)
	require.Equal(t, "attempt target mismatch", failedAttempt.Reason)
	require.Equal(t, oldBatch.ID, failedAttempt.BatchID)
	require.Equal(t, 1, failedAttempt.Calls)
	oldReadyAttempt, err := attemptLedger.Get(f.ctx, prior[2].attemptID)
	require.NoError(t, err)
	require.Equal(t, "ready", oldReadyAttempt.Status)
	require.Equal(t, oldSnapshot, oldReadyAttempt.SnapshotID)
}

func TestSourceWikiBatchChildReservationsChargeParentAndAttemptAtomically(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	batchLedger := newSourceWikiBatchLedgerFixture(t, f)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	batch.MaxCalls = 3
	batch.MaxTokens = 100
	batch.SkeletonMaxCalls = 0
	batch.SkeletonMaxTokens = 0
	require.NoError(t, batchLedger.Create(f.ctx, batch))
	topic := types.SourceWikiTopic{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "system", Kind: "system", Title: "System overview", Priority: 100, Status: "planned"}
	require.NoError(t, batchLedger.SavePlan(f.ctx, batch.ID, []types.SourceWikiTopic{topic}, now.Add(time.Second)))
	batch, err := batchLedger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	child := &types.SourceWikiAttempt{
		ID: uuid.NewString(), TenantID: batch.TenantID, KnowledgeBaseID: batch.KnowledgeBaseID, SourceID: batch.SourceID,
		SnapshotID: batch.SnapshotID, BatchID: batch.ID, TopicKind: "system", TopicKey: "system",
		ModulePath: "system", Title: topic.Title, Slug: "concept/source-" + f.ds.ID + "/topic-system", Status: "running",
		SourceConfigFingerprint: batch.SourceConfigFingerprint, SourceUpdatedAt: batch.SourceUpdatedAt,
		ModelID: batch.ModelID, ModelSettingsFingerprint: batch.ModelSettingsFingerprint, ModelContextWindow: batch.ModelContextWindow,
		MaxCompletionTokens: 4096, MaxCalls: types.SourceWikiBatchChildMaxCalls, MaxTokens: types.SourceWikiBatchChildMaxTokens,
		MaxElapsedMS: types.SourceWikiAttemptMaxElapsedMS, MaxRepairs: types.SourceWikiAttemptMaxRepairs,
		DeadlineAt: batch.CreatedAt.Add(3 * time.Minute), CreatedAt: batch.CreatedAt, UpdatedAt: batch.CreatedAt,
	}
	ledger := repository.NewSourceWikiAttemptLedger(f.db)
	require.NoError(t, ledger.Create(f.ctx, child))
	lease, err := ledger.Claim(f.ctx, types.SourceWikiAttemptClaimRequest{AttemptID: child.ID, Owner: "batch-worker", Now: now.Add(2 * time.Second), LeaseFor: time.Minute})
	require.NoError(t, err)

	const dispatches = 12
	type reserveResult struct {
		reservation types.SourceWikiAttemptCallReservation
		err         error
	}
	results := make(chan reserveResult, dispatches)
	for i := 0; i < dispatches; i++ {
		go func() {
			reservation, reserveErr := ledger.ReserveCall(f.ctx, types.SourceWikiAttemptCallReservationRequest{
				Lease: lease, Phase: "generate", ReservedTokens: 20, Now: now.Add(3 * time.Second), LeaseFor: time.Minute,
			})
			results <- reserveResult{reservation: reservation, err: reserveErr}
		}()
	}
	reserved, exhausted := 0, 0
	reservations := make([]types.SourceWikiAttemptCallReservation, 0, 3)
	for i := 0; i < dispatches; i++ {
		result := <-results
		switch {
		case result.err == nil:
			reserved++
			reservations = append(reservations, result.reservation)
		case errors.Is(result.err, repository.ErrSourceWikiBatchBudgetExhausted):
			exhausted++
		default:
			t.Fatalf("unexpected parent/child reservation error: %v", result.err)
		}
	}
	require.Equal(t, 3, reserved)
	require.Equal(t, dispatches-3, exhausted)
	stored, err := ledger.Get(f.ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, 3, stored.Calls)
	require.Equal(t, 60, stored.Tokens)
	var parent types.SourceWikiBatch
	require.NoError(t, f.db.Where("id = ?", batch.ID).Take(&parent).Error)
	require.Equal(t, 3, parent.CallsReserved)
	require.Equal(t, 60, parent.TokensReserved)
	var parentReservationCount int64
	require.NoError(t, f.db.Model(&types.SourceWikiBatchCallReservation{}).Where("batch_id = ? AND phase = 'card'", batch.ID).Count(&parentReservationCount).Error)
	require.EqualValues(t, 3, parentReservationCount)
	for _, call := range reservations {
		actual := 10
		require.NoError(t, ledger.CompleteCall(f.ctx, types.SourceWikiAttemptCallCompletion{
			Lease: lease, ReservationID: call.ID, Outcome: "succeeded", ActualTokens: &actual, Now: now.Add(4 * time.Second),
		}))
	}
	stored, err = ledger.Get(f.ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, 30, stored.Tokens, "child counter settles to actual successful usage")
	require.NoError(t, f.db.Where("id = ?", batch.ID).Take(&parent).Error)
	require.Equal(t, 30, parent.TokensReserved, "parent counter settles in the same transaction")
	var settled int64
	require.NoError(t, f.db.Model(&types.SourceWikiBatchCallReservation{}).Where("batch_id = ? AND phase = 'card' AND outcome = 'succeeded' AND actual_tokens = 10", batch.ID).Count(&settled).Error)
	require.EqualValues(t, 3, settled)
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

	model, err := f.modelService.GetModelByID(f.ctx, f.kb.SummaryModelID)
	require.NoError(t, err)
	model.Parameters.ContextWindow = 8192
	model.Parameters.MaxOutputTokens = 3072
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))

	preflightService, ok := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	require.True(t, ok, "source Wiki exposes batch preflight through its public interface")
	wikiService := generator.(*sourceWikiService)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	legacySnapshot, err := repository.LoadSourceWikiSkeletonSnapshot(f.ctx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	legacyRelations, err := wikiService.resolveSourceWikiRelations(f.ctx, f.kb.TenantID, f.kb.ID, f.ds.ID,
		publication.SnapshotID, legacySnapshot, legacySnapshot.Relations)
	require.NoError(t, err)
	legacyFiles := make([]sourceWikiSkeletonFile, 0, len(legacySnapshot.Files))
	for _, file := range legacySnapshot.Files {
		legacyFiles = append(legacyFiles, sourceWikiSkeletonFile{Path: file.Path, Generated: file.Generated, Facts: file.Facts})
	}
	legacyPlan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, Files: legacyFiles, Relations: legacyRelations,
	}, types.SourceWikiBatchMaxInitialTopics)
	capture := &sourceWikiBatchSQLCapture{Interface: logger.Default.LogMode(logger.Silent)}
	wikiService.db = f.db.Session(&gorm.Session{Logger: capture})
	result, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	require.True(t, result.PreflightPassed)
	require.True(t, result.StartAvailable, "the T17-backed bounded topic runner is available for a confirmed context window")
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
	var legacyModules, preflightModules []types.SourceWikiTopic
	for _, topic := range legacyPlan.Topics {
		if topic.Kind == sourceWikiTopicModule {
			legacyModules = append(legacyModules, topic)
		}
	}
	for _, topic := range result.PlannedTopics {
		if topic.Kind == sourceWikiTopicModule {
			preflightModules = append(preflightModules, topic)
		}
	}
	require.Equal(t, legacyModules, preflightModules, "projection seeds preserve the ordinary module candidate plan")
	pageQueries, fullFactQueries := 0, 0
	for _, query := range capture.snapshot() {
		normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
		if strings.Contains(normalized, "jsonb_array_elements") && strings.Contains(normalized, "limit 128") {
			pageQueries++
		}
		if strings.Contains(normalized, "sm.generated, sv.facts") {
			fullFactQueries++
		}
	}
	require.NotZero(t, pageQueries, "public preflight uses the bounded module projection")
	require.Zero(t, fullFactQueries, "module planning does not load full parser facts without an HTTP route")
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

func TestSourceWikiBatchPreflightRejectsIncompleteInventoriesWithoutWikiWrites(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(bool) string {
		providerCalls.Add(1)
		return `{}`
	})
	preflight := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	var snapshot types.SourceSnapshot
	require.NoError(t, f.db.Where("id = ?", publication.SnapshotID).Take(&snapshot).Error)
	assertNoWikiWrites := func() {
		t.Helper()
		for _, table := range []string{"source_wiki_batches", "source_wiki_topics", "wiki_pages", "wiki_page_revisions"} {
			var count int64
			require.NoError(t, f.db.Table(table).Where("knowledge_base_id = ?", f.kb.ID).Count(&count).Error)
			require.Zero(t, count, "%s must remain untouched by preflight", table)
		}
		require.Zero(t, providerCalls.Load(), "preflight failures do not dispatch a model call")
	}

	require.NoError(t, f.db.Model(&types.SourceSnapshot{}).Where("id = ?", snapshot.ID).
		Update("relation_count", types.SourceWikiSkeletonMaxRelations+1).Error)
	_, err := preflight.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiDerivationDeferred)
	assertNoWikiWrites()
	require.NoError(t, f.db.Model(&types.SourceSnapshot{}).Where("id = ?", snapshot.ID).
		Update("relation_count", snapshot.RelationCount).Error)

	require.NoError(t, f.db.Model(&types.SourceSnapshot{}).Where("id = ?", snapshot.ID).
		Update("relation_count", snapshot.RelationCount+1).Error)
	_, err = preflight.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiDerivationUnavailable)
	assertNoWikiWrites()
	require.NoError(t, f.db.Model(&types.SourceSnapshot{}).Where("id = ?", snapshot.ID).
		Update("relation_count", snapshot.RelationCount).Error)

	var member types.SourceSnapshotMember
	require.NoError(t, f.db.Where("snapshot_id = ? AND status = 'parsed'", snapshot.ID).Order("path ASC").Take(&member).Error)
	require.NoError(t, f.db.Exec(`UPDATE source_file_versions SET facts = (
		SELECT jsonb_agg(jsonb_build_object('kind','irrelevant','name','irrelevant','quality','structural'))
		FROM generate_series(1, ?) AS fact_ordinal
	) WHERE id = ?`, types.SourceWikiImpactMaxFacts+1, member.FileVersionID).Error)
	_, err = preflight.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiDerivationDeferred)
	require.ErrorContains(t, err, "200000-fact hard bound")
	assertNoWikiWrites()

	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
		QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: true, SourceIDs: []string{f.ds.ID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "a Wiki preflight inventory failure must not disable source search")
}

func TestSourceWikiBatchCannotStartWithOnlySystemOverview(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(bool) string {
		providerCalls.Add(1)
		return `{}`
	})
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	// Keep the real searchable publication and manifest, but remove its only
	// structural seeds so this fixture exercises a genuinely system-only plan.
	require.NoError(t, f.db.Model(&types.SourceFileVersion{}).Where("snapshot_id = ?", publication.SnapshotID).
		Update("facts", types.JSON(`[]`)).Error)
	require.NoError(t, f.db.Where("tenant_id = ? AND data_source_id = ? AND snapshot_id = ?", f.kb.TenantID, f.ds.ID, publication.SnapshotID).
		Delete(&types.SourceCodeRelation{}).Error)
	require.NoError(t, f.db.Model(&types.SourceSnapshot{}).Where("id = ?", publication.SnapshotID).
		Update("relation_count", 0).Error)

	service := generator.(*sourceWikiService)
	readCtx, release, err := beginSourceRead(f.ctx, service.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID},
	}})
	require.NoError(t, err)
	defer release()
	projection, err := repository.LoadSourceWikiModuleProjection(readCtx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.NotEmpty(t, projection.Members)
	seedFiles := make([]sourceWikiSkeletonFile, 0, len(projection.Members))
	for _, member := range projection.Members {
		require.Nil(t, member.Seed)
		seedFiles = append(seedFiles, sourceWikiSkeletonFile{Path: member.Path, Generated: member.Generated})
	}
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, Files: seedFiles, ModuleSeedFiles: seedFiles,
	}, types.SourceWikiBatchMaxInitialTopics)
	require.Len(t, plan.Topics, 1)
	require.Equal(t, "system", plan.Topics[0].TopicKey)
	require.Zero(t, plan.ModuleCount)
	require.Zero(t, plan.FlowCount)

	preflightService := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	startService := generator.(interface {
		StartSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error)
	})
	_, err = preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiDerivationUnavailable)
	require.ErrorContains(t, err, "no structural module or HTTP flow candidates")
	_, err = startService.StartSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiDerivationUnavailable)
	for _, table := range []string{"source_wiki_batches", "source_wiki_topics", "wiki_pages", "wiki_page_revisions"} {
		var count int64
		require.NoError(t, f.db.Table(table).Where("knowledge_base_id = ?", f.kb.ID).Count(&count).Error)
		require.Zero(t, count, "%s must remain untouched by a system-only plan", table)
	}
	require.Zero(t, providerCalls.Load(), "a system-only plan must not invoke the model")
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
		QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: true, SourceIDs: []string{f.ds.ID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits, "unavailable Wiki coverage must leave source search usable")
}

func TestSourceWikiBatchLocalQAReceivesCompleteCandidateAndEvidenceFromDatabaseCheckpoint(t *testing.T) {
	extraFiles := make(map[string][]byte, 9)
	for i := 0; i < 9; i++ {
		filePath := fmt.Sprintf("src/module/orders/File%02d.java", i)
		content := fmt.Sprintf("package orders;\n// %s\npublic class File%02d {}\n", strings.Repeat("raw source marker ", 20), i)
		extraFiles[filePath] = []byte(content)
	}
	f := newJavaSourceFixture(t, extraFiles)
	syncSourceFixture(t, f)
	captured := make(chan []byte, 1)
	qaOverride := func(w http.ResponseWriter, r *http.Request, _ bool) {
		var request struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if json.NewDecoder(r.Body).Decode(&request) == nil && len(request.Messages) > 1 {
			captured <- []byte(request.Messages[len(request.Messages)-1].Content)
		} else {
			captured <- nil
		}
		reply := `{"supported":true,"reason":"","cards":[{"topic_key":"system","supported":true,"reason":""}]}`
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, reply)
	}
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, qaOverride)
	service := generator.(*sourceWikiService)
	model, err := service.models.GetModelByID(f.ctx, f.kb.SummaryModelID)
	require.NoError(t, err)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	batch.ModelSettingsFingerprint = sourceWikiModelFingerprint(model)
	batch.ModelContextWindow = model.Parameters.ContextWindow
	topic := types.SourceWikiTopic{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "system", Kind: "system",
		Title: "System overview", Priority: 120, Status: "planned"}
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	require.NoError(t, ledger.CreateWithPlan(f.ctx, batch, []types.SourceWikiTopic{topic}, now))
	var coverage types.SourceWikiCoverageTopic
	require.NoError(t, f.db.Where("batch_id = ? AND topic_key = ?", batch.ID, topic.TopicKey).Take(&coverage).Error)

	checkpoint := sourceWikiAttemptCheckpoint{}
	checkpoint.Evidence, err = service.collectTopicEvidence(f.ctx, f.kb.ID, &types.SourceWikiAttempt{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID,
		TopicKind: "system", TopicKey: "system", ModelContextWindow: 65536, MaxCompletionTokens: 1024,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(checkpoint.Evidence), 9, "the fixed source read must supply more than six real evidence records")
	longExcerpts := 0
	for _, item := range checkpoint.Evidence {
		if len(item.Text) > 160 {
			longExcerpts++
		}
	}
	require.GreaterOrEqual(t, longExcerpts, 9, "the checkpoint must include nine real excerpts longer than the former clip")
	draft := sourceWikiDraft{Title: "System overview", Summary: strings.Repeat("summary-", 60), Sections: make([]sourceWikiSection, 9)}
	for i := range draft.Sections {
		draft.Sections[i] = sourceWikiSection{Text: fmt.Sprintf("section-%d:%s", i, strings.Repeat("draft", 55)), EvidenceIDs: []string{checkpoint.Evidence[i].Evidence.ID}}
	}
	draftJSON, err := json.Marshal(draft)
	require.NoError(t, err)
	checkpointJSON, err := json.Marshal(checkpoint)
	require.NoError(t, err)
	attempt := types.SourceWikiAttempt{
		ID: uuid.NewString(), TenantID: batch.TenantID, KnowledgeBaseID: batch.KnowledgeBaseID,
		SourceID: batch.SourceID, SnapshotID: batch.SnapshotID, BatchID: batch.ID,
		TopicKind: topic.Kind, TopicKey: topic.TopicKey, ModulePath: topic.ModulePath, Title: topic.Title, Slug: coverage.WikiSlug,
		Status: "staged", SourceConfigFingerprint: batch.SourceConfigFingerprint, SourceUpdatedAt: batch.SourceUpdatedAt,
		ModelID: batch.ModelID, ModelSettingsFingerprint: batch.ModelSettingsFingerprint,
		ModelContextWindow: batch.ModelContextWindow, MaxCompletionTokens: batch.MaxCompletionTokens,
		MaxCalls: types.SourceWikiBatchChildMaxCalls, MaxTokens: types.SourceWikiBatchChildMaxTokens,
		MaxElapsedMS: types.SourceWikiAttemptMaxElapsedMS, MaxRepairs: types.SourceWikiAttemptMaxRepairs,
		DeadlineAt: batch.DeadlineAt, Phase: "staged", Draft: types.JSON(draftJSON), Checkpoint: types.JSON(checkpointJSON),
		CreatedAt: now, UpdatedAt: now,
	}
	require.NoError(t, f.db.Create(&attempt).Error)
	evidenceRecords := make([]types.SourceWikiEvidence, 0, len(checkpoint.Evidence))
	for _, item := range checkpoint.Evidence {
		evidenceRecords = append(evidenceRecords, item.Evidence)
	}
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return repository.RegisterSourceWikiAttemptEvidence(tx, attempt.ID, evidenceRecords)
	}))
	require.NoError(t, ledger.UpdateTopic(f.ctx, batch.ID, topic.TopicKey, "draft", attempt.ID, "", "", now))
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "batch_qa", "running", topic.TopicKey, "", 0, now))
	batch, err = ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)

	done, err := service.processSourceWikiBatchQA(f.ctx, ledger, batch)
	require.NoError(t, err)
	require.True(t, done)
	payload := <-captured
	require.NotEmpty(t, payload, "normal group QA must send its candidate payload to the provider")
	var input struct {
		Cards []struct {
			TopicKey string          `json:"topic_key"`
			Draft    sourceWikiDraft `json:"draft"`
			Evidence []struct {
				Record types.SourceWikiEvidence `json:"record"`
				Text   string                   `json:"text"`
			} `json:"evidence"`
		} `json:"cards"`
	}
	require.NoError(t, json.Unmarshal(payload, &input))
	require.Len(t, input.Cards, 1)
	require.Equal(t, topic.TopicKey, input.Cards[0].TopicKey)
	require.Equal(t, draft.Summary, input.Cards[0].Draft.Summary)
	require.Equal(t, draft.Sections, input.Cards[0].Draft.Sections, "the real QA request must include all nine full sections")
	require.Len(t, input.Cards[0].Evidence, len(checkpoint.Evidence), "the real QA request must include every registered evidence item")
	for i, item := range input.Cards[0].Evidence {
		require.Equal(t, checkpoint.Evidence[i].Evidence, item.Record)
		require.Equal(t, checkpoint.Evidence[i].Text, item.Text, "the full registered raw excerpt must reach group QA")
	}
}

func TestSourceWikiBatchStartGeneratesAndQAsAtLeastTwentyCards(t *testing.T) {
	extraFiles := make(map[string][]byte, 20)
	for i := 1; i <= 20; i++ {
		name := fmt.Sprintf("Batch%02dService", i)
		extraFiles[fmt.Sprintf("src/module%02d/%s.java", i, name)] = []byte(fmt.Sprintf(
			"package demo.module%02d;\npublic class %s {}\n", i, name,
		))
	}
	f := newJavaSourceFixture(t, extraFiles)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	var batchQACalls atomic.Int32
	var fullSetQACalls atomic.Int32
	batchQAEntered := make(chan struct{}, 2)
	batchQARelease := make(chan struct{})
	var releaseBatchQAOnce sync.Once
	releaseBatchQA := func() { releaseBatchQAOnce.Do(func() { close(batchQARelease) }) }
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		providerCalls.Add(1)
		stage := r.Header.Get("X-Source-Wiki-Test-Stage")
		reply := `{"title":"Supported architecture","summary":"The pinned source snapshot contains this documented component.","sections":[{"text":"The component is declared in the cited source file.","evidence_ids":["e001"],"uncertain":false}]}`
		if qa {
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		} else if strings.Contains(stage, "Independently check this small group") {
			batchQACalls.Add(1)
			select {
			case batchQAEntered <- struct{}{}:
			default:
			}
			select {
			case <-batchQARelease:
			case <-r.Context().Done():
				return
			}
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
			encoded, err := json.Marshal(map[string]any{"supported": true, "reason": "", "cards": cards})
			require.NoError(t, err)
			reply = string(encoded)
		} else if strings.Contains(stage, "full initial candidate set") {
			fullSetQACalls.Add(1)
			reply = `{"supported":true,"reason":"all candidate cards are mutually consistent"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, reply)
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	startService, ok := generator.(interface {
		StartSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error)
	})
	require.True(t, ok)
	preflightService, ok := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	require.True(t, ok)
	runner, ok := generator.(interface{ StopSourceWikiBatches() })
	require.True(t, ok)
	t.Cleanup(runner.StopSourceWikiBatches)
	service := generator.(*sourceWikiService)
	secondGenerator := NewSourceWikiService(service.wiki, service.kb, service.knowledge, service.models, service.db)
	secondRunner, ok := secondGenerator.(interface {
		ResumeSourceWikiBatch(context.Context, string)
		StopSourceWikiBatches()
	})
	require.True(t, ok)
	t.Cleanup(secondRunner.StopSourceWikiBatches)
	t.Cleanup(releaseBatchQA)

	preview, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, preview.InitialCount, 20)
	stalePreview := types.SourceWikiBatchPreflightRequest{
		ExpectedSnapshotID: "stale-snapshot", ExpectedSourceUpdatedAt: preview.SourceUpdatedAt,
		ExpectedModelID: preview.ModelID, ExpectedModelUpdatedAt: preview.ModelUpdatedAt,
	}
	_, err = startService.StartSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, stalePreview)
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchInvalidState, "a changed fixed source snapshot requires another preview")
	started, err := startService.StartSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{
		ExpectedSnapshotID: preview.SnapshotID, ExpectedSourceUpdatedAt: preview.SourceUpdatedAt,
		ExpectedModelID: preview.ModelID, ExpectedModelUpdatedAt: preview.ModelUpdatedAt,
	})
	require.NoError(t, err)
	require.GreaterOrEqual(t, started.InitialCount, 20, "the acceptance fixture exercises a substantial initial batch")
	require.LessOrEqual(t, started.InitialCount, types.SourceWikiBatchMaxInitialTopics)
	select {
	case <-batchQAEntered:
	case <-time.After(2 * time.Minute):
		t.Fatal("first batch worker did not reach whole-batch QA")
	}
	secondRunner.ResumeSourceWikiBatch(context.Background(), started.ID)
	select {
	case <-batchQAEntered:
	case <-time.After(10 * time.Second):
		t.Fatal("second worker did not contend for the same persisted QA cursor")
	}
	releaseBatchQA()

	ledger := repository.NewSourceWikiBatchLedger(f.db)
	deadline := time.Now().Add(3 * time.Minute)
	var finished *types.SourceWikiBatch
	for time.Now().Before(deadline) {
		finished, err = ledger.Get(f.ctx, f.kb.ID, started.ID)
		require.NoError(t, err)
		if finished.Status != "running" && finished.Status != "queued" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, finished)
	require.Equal(t, "completed", finished.Status, finished.Reason)
	require.Equal(t, "finished", finished.Phase)
	require.Equal(t, finished.InitialCount, finished.Cursor)
	require.Equal(t, finished.InitialCount, finished.QACursor, "concurrent stale QA results cannot skip coverage groups")
	require.Equal(t, int32(finished.CallsReserved), providerCalls.Load(), "each dispatched provider call is durably charged to the parent")
	require.Greater(t, finished.CallsReserved, finished.InitialCount)
	require.LessOrEqual(t, finished.CallsReserved, types.SourceWikiBatchMaxCalls)
	require.Greater(t, finished.TokensReserved, 0)
	require.LessOrEqual(t, finished.TokensReserved, types.SourceWikiBatchMaxTokens)

	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	ready := 0
	for _, topic := range coverage {
		if topic.BatchID != nil && *topic.BatchID == started.ID && topic.Initial {
			require.Equal(t, "ready", topic.Status, topic.TopicKey+": "+topic.Reason)
			require.Equal(t, started.SnapshotID, topic.LastReadySnapshotID)
			ready++
		}
	}
	require.Equal(t, started.InitialCount, ready, "whole-batch QA must publish every initial coverage verdict")
	var qaCalls int64
	require.NoError(t, f.db.Model(&types.SourceWikiBatchCallReservation{}).
		Where("batch_id = ? AND phase = 'batch_qa' AND outcome = 'succeeded'", started.ID).
		Count(&qaCalls).Error)
	require.GreaterOrEqual(t, qaCalls, int64((started.InitialCount+3)/4), "each group has at least one successful QA settlement")
	require.LessOrEqual(t, qaCalls, int64(types.SourceWikiBatchQAMaxCalls), "duplicate cross-instance QA dispatches still consume the fixed parent budget")
	require.GreaterOrEqual(t, batchQACalls.Load(), int32((started.InitialCount+3)/4))
	require.GreaterOrEqual(t, fullSetQACalls.Load(), int32(1), "a separate full-set consistency check must compare candidates across local groups")
}

func TestSourceWikiBatchRejectedQADraftsAreNeverPublished(t *testing.T) {
	extraFiles := make(map[string][]byte, 6)
	for i := 1; i <= 6; i++ {
		name := fmt.Sprintf("Batch%02dService", i)
		extraFiles[fmt.Sprintf("src/module%02d/%s.java", i, name)] = []byte(fmt.Sprintf("package demo.module%02d;\npublic class %s {}\n", i, name))
	}
	f := newJavaSourceFixture(t, extraFiles)
	syncSourceFixture(t, f)
	var groupQACalls, fullSetQACalls atomic.Int32
	var fullSetCards atomic.Int32
	wiki, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		reply := `{"title":"System overview","summary":"The pinned source contains a declared component.","sections":[{"text":"The component is declared in the cited source file.","evidence_ids":["e001"],"uncertain":false}]}`
		if qa {
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		} else if strings.Contains(r.Header.Get("X-Source-Wiki-Test-Stage"), "Independently check this small group") {
			groupQACalls.Add(1)
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
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
			encoded, err := json.Marshal(map[string]any{"supported": true, "reason": "local groups pass", "cards": cards})
			require.NoError(t, err)
			reply = string(encoded)
		} else if strings.Contains(r.Header.Get("X-Source-Wiki-Test-Stage"), "full initial candidate set") {
			fullSetQACalls.Add(1)
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
			var input struct {
				Cards []map[string]json.RawMessage `json:"cards"`
			}
			require.NoError(t, json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &input))
			fullSetCards.Store(int32(len(input.Cards)))
			reply = `{"supported":false,"reason":"a contradiction spans candidates in different local groups"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, reply)
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	startService, ok := generator.(interface {
		StartSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error)
	})
	require.True(t, ok)
	systemSlug := sourceWikiAttemptSlug(f.ds.ID, "system", "system", "")
	oldPage, err := generator.(*sourceWikiService).wiki.CreatePage(f.ctx, &types.WikiPage{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, Slug: systemSlug,
		Title: "Existing system notes", Summary: "Previously reviewed content.", Content: "Keep this exact page before QA approves a replacement.",
		PageType: types.WikiPageTypeConcept, Status: types.WikiPageStatusPublished,
		SourceRefs: types.StringArray{"existing-document|README.md"},
	})
	require.NoError(t, err)
	var revisionsBefore int64
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Where("page_id = ?", oldPage.ID).Count(&revisionsBefore).Error)
	runner, ok := generator.(interface{ StopSourceWikiBatches() })
	require.True(t, ok)
	t.Cleanup(runner.StopSourceWikiBatches)
	started, err := startService.StartSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	deadline := time.Now().Add(90 * time.Second)
	var finished *types.SourceWikiBatch
	for time.Now().Before(deadline) {
		finished, err = ledger.Get(f.ctx, f.kb.ID, started.ID)
		require.NoError(t, err)
		if finished.Status != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.NotNil(t, finished)
	require.Equal(t, "failed", finished.Status, finished.Reason)
	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	var attempted, publishedReady int
	for _, topic := range coverage {
		if topic.BatchID == nil || *topic.BatchID != started.ID || !topic.Initial || topic.AttemptID == nil {
			continue
		}
		attempted++
		var child types.SourceWikiAttempt
		require.NoError(t, f.db.Where("id = ?", *topic.AttemptID).Take(&child).Error)
		require.Equal(t, "failed", child.Status, "whole-set rejection terminalizes the staged child")
		var retainedOwners int64
		require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", child.ID).Count(&retainedOwners).Error)
		require.Zero(t, retainedOwners, "rejected candidates release their exact evidence owners")
		page, readErr := wiki.GetPageBySlug(f.ctx, f.kb.ID, topic.WikiSlug)
		if errors.Is(readErr, repository.ErrWikiPageNotFound) {
			continue
		}
		require.NoError(t, readErr)
		if page != nil && page.Status == types.WikiPageStatusPublished && page.SourceProvenance != nil && page.SourceProvenance.State == "ready" {
			publishedReady++
		}
	}
	require.Greater(t, attempted, 0, "the fixture must reach candidate generation before the whole-batch rejection")
	require.GreaterOrEqual(t, groupQACalls.Load(), int32(2), "the fixture must exercise more than one local QA group")
	require.Equal(t, int32(1), fullSetQACalls.Load(), "the complete candidate set is checked once after all local groups")
	require.Greater(t, fullSetCards.Load(), int32(4), "the whole-set QA input includes candidates from multiple local groups")
	require.Zero(t, publishedReady, "a rejected whole-batch QA result must not leave candidate cards publicly readable as ready")
	preserved, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, systemSlug)
	require.NoError(t, err)
	require.Equal(t, oldPage.ID, preserved.ID)
	require.Equal(t, oldPage.Version, preserved.Version)
	require.Equal(t, oldPage.Title, preserved.Title)
	require.Equal(t, oldPage.Summary, preserved.Summary)
	require.Equal(t, oldPage.Content, preserved.Content)
	require.Equal(t, oldPage.SourceRefs, preserved.SourceRefs)
	require.Nil(t, preserved.SourceProvenance)
	var revisionsAfter int64
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Where("page_id = ?", oldPage.ID).Count(&revisionsAfter).Error)
	require.Equal(t, revisionsBefore, revisionsAfter, "rejected QA does not append or rewrite a page revision")
}

func TestSourceWikiBatchPageConflictRebasesWithinOriginalAttemptAndRevalidates(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	globalEntered := make(chan struct{}, 1)
	allowGlobalReturn := make(chan struct{})
	var releaseOnce sync.Once
	releaseGlobal := func() { releaseOnce.Do(func() { close(allowGlobalReturn) }) }
	var globalCalls atomic.Int32
	wiki, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		stage := r.Header.Get("X-Source-Wiki-Test-Stage")
		reply := `{"title":"System overview","summary":"The pinned source declares a component.","sections":[{"text":"The component is declared in the cited source file.","evidence_ids":["e001"],"uncertain":false}]}`
		if qa {
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		} else if stage == "source_wiki_merge" {
			reply = `{"title":"System overview","summary":"The concurrent user edit is retained with the source update.","sections":[{"text":"The component is declared in the cited source file.","evidence_ids":["e001"],"uncertain":false}]}`
		} else if strings.Contains(stage, "Independently check this small group") {
			var request struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
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
		} else if strings.Contains(stage, "full initial candidate set") {
			if globalCalls.Add(1) == 1 {
				globalEntered <- struct{}{}
				select {
				case <-allowGlobalReturn:
				case <-r.Context().Done():
					return
				}
			}
			reply = `{"supported":true,"reason":"the full candidate set is consistent"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, reply)
	})
	startService, ok := generator.(interface {
		StartSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error)
	})
	require.True(t, ok)
	runner, ok := generator.(interface{ StopSourceWikiBatches() })
	require.True(t, ok)
	t.Cleanup(func() { releaseGlobal(); runner.StopSourceWikiBatches() })
	started, err := startService.StartSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	select {
	case <-globalEntered:
	case <-time.After(90 * time.Second):
		t.Fatal("the batch did not reach its full-set QA checkpoint")
	}
	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	system := byCoverageTopic(t, coverage, "system")
	require.NotNil(t, system.AttemptID)
	childLedger := repository.NewSourceWikiAttemptLedger(f.db)
	childBefore, err := childLedger.Get(f.ctx, *system.AttemptID)
	require.NoError(t, err)
	require.Equal(t, "staged", childBefore.Status)
	originalCalls, originalDeadline := childBefore.Calls, childBefore.DeadlineAt
	service := generator.(*sourceWikiService)
	concurrentEdit, err := service.wiki.CreatePage(f.ctx, &types.WikiPage{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, Slug: sourceWikiAttemptSlug(f.ds.ID, "system", "system", ""),
		Title: "System overview", Summary: "Concurrent user edit.", Content: "User-authored context.",
		PageType: types.WikiPageTypeConcept, Status: types.WikiPageStatusPublished,
	})
	require.NoError(t, err)
	require.Equal(t, 1, concurrentEdit.Version)
	releaseGlobal()
	deadline := time.Now().Add(90 * time.Second)
	var finished *types.SourceWikiBatch
	for time.Now().Before(deadline) {
		finished, err = ledger.Get(f.ctx, f.kb.ID, started.ID)
		require.NoError(t, err)
		if finished.Status != "running" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.NotNil(t, finished)
	require.Equal(t, "completed", finished.Status, finished.Reason)
	require.Equal(t, finished.InitialCount, finished.PublishCursor)
	childAfter, err := childLedger.Get(f.ctx, *system.AttemptID)
	require.NoError(t, err)
	require.Equal(t, "ready", childAfter.Status)
	require.Equal(t, originalDeadline, childAfter.DeadlineAt, "rebase never renews the original child deadline")
	require.Greater(t, childAfter.Calls, originalCalls, "merge and per-card QA are charged to the same original child attempt")
	var checkpoint sourceWikiAttemptCheckpoint
	require.NoError(t, json.Unmarshal(childAfter.Checkpoint, &checkpoint))
	require.Equal(t, 1, checkpoint.RebaseRounds)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, concurrentEdit.Slug)
	require.NoError(t, err)
	require.Equal(t, "The concurrent user edit is retained with the source update.", page.Summary)
	require.GreaterOrEqual(t, globalCalls.Load(), int32(2), "candidate mutation after approval invalidates approval and reruns full-set QA")
}

func TestSourceWikiBatchQAPhaseDeadlineIsFixedAndParentBounded(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	ledger := newSourceWikiBatchLedgerFixture(t, f)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	batch.MaxElapsedMS = (10 * time.Minute).Milliseconds()
	batch.DeadlineAt = now.Add(10 * time.Minute)
	require.NoError(t, ledger.Create(f.ctx, batch))
	plan := []types.SourceWikiTopic{{
		SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "system", Kind: "system",
		Title: "System overview", Priority: 120, Status: "planned",
	}}
	require.NoError(t, ledger.SavePlan(f.ctx, batch.ID, plan, now.Add(time.Second)))
	phaseStart := now.Add(10 * time.Second)
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "batch_qa", "running", "", "", 1, phaseStart))
	started, err := ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	require.NotNil(t, started.QADueAt)
	require.True(t, phaseStart.Add(types.SourceWikiBatchQAMaxElapsed).Equal(*started.QADueAt), "the first QA entry fixes one absolute phase deadline")
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "batch_qa", "running", "", "", 1, started.QADueAt.Add(-time.Second)))
	resumed, err := ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	require.Equal(t, *started.QADueAt, *resumed.QADueAt, "recovery cannot renew the absolute QA phase deadline")
	err = ledger.UpdateProgress(f.ctx, batch.ID, "batch_qa", "running", "", "", 1, *resumed.QADueAt)
	require.ErrorIs(t, err, repository.ErrSourceWikiBatchQATimeout)
	finished, err := ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", finished.Status)
	require.Equal(t, "batch QA phase deadline exhausted", finished.Reason)

	short := newSourceWikiTestBatch(f, publication.SnapshotID, now.Add(20*time.Minute))
	short.MaxElapsedMS = time.Minute.Milliseconds()
	short.DeadlineAt = short.CreatedAt.Add(time.Minute)
	require.NoError(t, ledger.Create(f.ctx, short))
	require.NoError(t, ledger.SavePlan(f.ctx, short.ID, plan, short.CreatedAt.Add(time.Second)))
	shortPhaseStart := short.CreatedAt.Add(10 * time.Second)
	require.NoError(t, ledger.UpdateProgress(f.ctx, short.ID, "batch_qa", "running", "", "", 1, shortPhaseStart))
	parentBounded, err := ledger.Get(f.ctx, f.kb.ID, short.ID)
	require.NoError(t, err)
	require.NotNil(t, parentBounded.QADueAt)
	require.True(t, short.DeadlineAt.Equal(*parentBounded.QADueAt), "the five-minute QA maximum cannot extend the parent deadline")
	require.NoError(t, ledger.UpdateProgress(f.ctx, short.ID, "batch_qa", "failed", "", "test cleanup", 1, shortPhaseStart.Add(time.Second)))
}

func TestManualModulePublishCompletesMatchingExpansionCoverageWithoutAdvancingBatch(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"The source declares a schedule.","sections":[{"text":"The module declares the cited component.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	require.NoError(t, ledger.Create(f.ctx, batch))
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "cards", "running", "", "", 0, now.Add(time.Millisecond)))
	topic := types.SourceWikiCoverageTopic{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID,
		SourceID: f.ds.ID, TopicKey: "module/src", SnapshotID: publication.SnapshotID,
		Kind: "module", ModulePath: "src", Title: "Scheduling module", Priority: 10,
		Initial: false, Status: "expansion", UncertaintyReasons: types.JSON("[]"), Relations: types.JSON("[]"),
		BatchID: &batch.ID, WikiSlug: sourceWikiModuleSlug(f.ds.ID, "src"), UpdatedAt: now,
	}
	require.NoError(t, f.db.Create(&topic).Error)
	manualAttempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", manualAttempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, topic.WikiSlug)
	require.NoError(t, err)
	require.Equal(t, types.WikiPageStatusPublished, page.Status)
	readService, ok := generator.(interfaces.SourceWikiBatchReadService)
	require.True(t, ok)
	coverage, err := readService.ListSourceWikiCoverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	updated := byCoverageTopic(t, coverage, topic.TopicKey)
	require.Equal(t, "ready", updated.Status)
	require.Equal(t, publication.SnapshotID, updated.LastReadySnapshotID)
	require.NotNil(t, updated.AttemptID)
	require.Equal(t, manualAttempt.ID, *updated.AttemptID)
	require.NotNil(t, updated.BatchID)
	require.Equal(t, batch.ID, *updated.BatchID, "the pre-existing batch association remains untouched")
	unchangedBatch, err := ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	require.Equal(t, "cards", unchangedBatch.Phase)
	require.Zero(t, unchangedBatch.Cursor)
	require.Zero(t, unchangedBatch.QACursor)
	require.Zero(t, unchangedBatch.CallsReserved, "manual T14 calls do not consume the active parent budget")
	require.Equal(t, 0, unchangedBatch.CandidateCount)
}

func TestBatchWithoutReadableEvidenceUsesDedicatedCoverageOutcome(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var unexpectedProviderCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		unexpectedProviderCalls.Add(1)
		http.Error(w, "provider call is not expected without evidence", http.StatusInternalServerError)
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	service := generator.(*sourceWikiService)
	model, err := service.models.GetModelByID(f.ctx, f.kb.SummaryModelID)
	require.NoError(t, err)
	var source types.DataSource
	require.NoError(t, f.db.Where("id = ?", f.ds.ID).Take(&source).Error)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	batch.SourceConfigFingerprint = sourceWikiSourceFingerprint(&source)
	batch.SourceUpdatedAt = source.UpdatedAt
	batch.ModelID = model.ID
	batch.ModelSettingsFingerprint = sourceWikiModelFingerprint(model)
	batch.ModelContextWindow = model.Parameters.ContextWindow
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	require.NoError(t, ledger.Create(f.ctx, batch))
	topics := []types.SourceWikiTopic{{
		SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "module/not-present",
		Kind: "module", ModulePath: "not-present", Title: "Missing module", Priority: 10, Status: "planned",
	}}
	require.NoError(t, ledger.SavePlan(f.ctx, batch.ID, topics, now.Add(time.Millisecond)))
	runner, ok := generator.(interfaces.SourceWikiBatchExecutionService)
	require.True(t, ok)
	t.Cleanup(runner.StopSourceWikiBatches)
	runner.ResumeSourceWikiBatch(context.Background(), batch.ID)
	deadline := time.Now().Add(30 * time.Second)
	var finished *types.SourceWikiBatch
	for time.Now().Before(deadline) {
		finished, err = ledger.Get(f.ctx, f.kb.ID, batch.ID)
		require.NoError(t, err)
		if finished.Status != "running" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	require.NotNil(t, finished)
	require.Equal(t, "failed", finished.Status)
	coverageReader, ok := generator.(interfaces.SourceWikiBatchReadService)
	require.True(t, ok)
	coverage, err := coverageReader.ListSourceWikiCoverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	missing := byCoverageTopic(t, coverage, "module/not-present")
	require.Equal(t, "insufficient_evidence", missing.Status)
	require.NotNil(t, missing.AttemptID)
	attempts, err := service.ListAttempts(f.ctx, f.kb.ID)
	require.NoError(t, err)
	var attempt *types.SourceWikiAttempt
	for _, candidate := range attempts {
		if candidate.ID == *missing.AttemptID {
			attempt = candidate
			break
		}
	}
	require.NotNil(t, attempt)
	require.Equal(t, types.SourceWikiAttemptResultKindInsufficientEvidence, attempt.ResultKind)
	require.Zero(t, finished.CallsReserved)
	require.Zero(t, unexpectedProviderCalls.Load())
}

func TestSourceWikiBatchStopPreservesCheckpointAndRestartExpiresParent(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	providerEntered := make(chan struct{}, 1)
	providerCanceled := make(chan struct{}, 1)
	allowProviderReturn := make(chan struct{})
	var releaseProviderOnce sync.Once
	releaseProvider := func() { releaseProviderOnce.Do(func() { close(allowProviderReturn) }) }
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` }, func(w http.ResponseWriter, r *http.Request, qa bool) {
		providerCalls.Add(1)
		stage := r.Header.Get("X-Source-Wiki-Test-Stage")
		if stage == "source_wiki_generate" {
			providerEntered <- struct{}{}
			select {
			case <-r.Context().Done():
				providerCanceled <- struct{}{}
				return
			case <-allowProviderReturn:
			}
		}
		reply := `{"title":"System overview","summary":"The fixed snapshot contains a source declaration.","sections":[{"text":"The source declares a component.","evidence_ids":["e001"],"uncertain":false}]}`
		if qa {
			reply = `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		} else if strings.Contains(stage, "Independently check this small group") {
			reply = `{"supported":true,"reason":"","cards":[{"topic_key":"system","supported":true,"reason":""}]}`
		} else if strings.Contains(stage, "full initial candidate set") {
			reply = `{"supported":true,"reason":"consistent"}`
		}
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":%q}}],"usage":{"prompt_tokens":20,"completion_tokens":20,"total_tokens":40}}`, reply)
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	startService, ok := generator.(interface {
		StartSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error)
	})
	require.True(t, ok)
	service := generator.(*sourceWikiService)
	runner, ok := generator.(interface{ StopSourceWikiBatches() })
	require.True(t, ok)
	t.Cleanup(func() {
		releaseProvider()
		runner.StopSourceWikiBatches()
	})
	started, err := startService.StartSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	select {
	case <-providerEntered:
	case <-time.After(20 * time.Second):
		t.Fatal("batch did not reach the blocking provider fixture")
	}
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	var running types.SourceWikiBatch
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		loaded, getErr := ledger.Get(f.ctx, f.kb.ID, started.ID)
		if getErr != nil {
			t.Fatal(getErr)
		}
		running = *loaded
		if running.CurrentTopicKey != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.NotEmpty(t, running.CurrentTopicKey)
	coverage, err := ledger.Coverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	topic := byCoverageTopic(t, coverage, running.CurrentTopicKey)
	require.NotNil(t, topic.AttemptID)
	childLedger := repository.NewSourceWikiAttemptLedger(f.db)
	child, err := childLedger.Get(f.ctx, *topic.AttemptID)
	require.NoError(t, err)
	require.Equal(t, "running", child.Status)
	require.NotEmpty(t, child.Checkpoint, "source evidence checkpoint is durable before provider dispatch")
	require.NotEmpty(t, child.LeaseOwner, "the provider-blocked child still owns its lease")
	var evidenceOwners int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", child.ID).Count(&evidenceOwners).Error)
	require.Greater(t, evidenceOwners, int64(0))
	var reservation types.SourceWikiBatchCallReservation
	require.NoError(t, f.db.Where("batch_id = ? AND outcome = 'reserved'", started.ID).Take(&reservation).Error)

	stopBegan := time.Now()
	runner.StopSourceWikiBatches()
	require.Less(t, time.Since(stopBegan), 5*time.Second, "worker shutdown must wait for cancellation, not the full batch deadline")
	select {
	case <-providerCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("worker cancellation did not reach the in-flight provider request")
	}
	require.Equal(t, int32(1), providerCalls.Load(), "a stopped worker does not reschedule or re-dispatch")
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, int32(1), providerCalls.Load())
	pausedChild, err := childLedger.Get(f.ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, "running", pausedChild.Status, "shutdown leaves the durable child checkpoint for a later recovery")
	require.Equal(t, child.LeaseOwner, pausedChild.LeaseOwner)
	pausedBatch, err := ledger.Get(f.ctx, f.kb.ID, started.ID)
	require.NoError(t, err)
	require.Equal(t, "running", pausedBatch.Status, "shutdown is not misreported as failure or expiry")

	oldCreatedAt := time.Now().UTC().Truncate(time.Millisecond).Add(-2 * time.Hour)
	oldDeadline := oldCreatedAt.Add(types.SourceWikiBatchMaxElapsed)
	require.NoError(t, f.db.Model(&types.SourceWikiBatch{}).Where("id = ?", started.ID).Updates(map[string]any{
		"created_at": oldCreatedAt, "updated_at": oldCreatedAt, "deadline_at": oldDeadline,
		"max_elapsed_ms": types.SourceWikiBatchMaxElapsed.Milliseconds(),
	}).Error)
	restartedService := NewSourceWikiService(service.wiki, service.kb, service.knowledge, service.models, service.db)
	restartedRunner, ok := restartedService.(interface {
		ResumeSourceWikiBatch(context.Context, string)
		StopSourceWikiBatches()
	})
	require.True(t, ok)
	t.Cleanup(restartedRunner.StopSourceWikiBatches)
	restartedRunner.ResumeSourceWikiBatch(context.Background(), started.ID)
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		pausedBatch, err = ledger.Get(f.ctx, f.kb.ID, started.ID)
		require.NoError(t, err)
		if pausedBatch.Status != "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	require.Equal(t, "expired", pausedBatch.Status, "a restarted worker terminalizes an already-expired parent before creating a deadline context")
	closedChild, err := childLedger.Get(f.ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", closedChild.Status)
	var remainingOwners int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", child.ID).Count(&remainingOwners).Error)
	require.Zero(t, remainingOwners, "parent expiry releases the child evidence owner")
	var savedCall types.SourceWikiAttemptCall
	require.NoError(t, f.db.Where("attempt_id = ?", child.ID).Take(&savedCall).Error)
	require.Equal(t, "unknown", savedCall.Outcome, "an unanswered stopped call remains charged and unknown")
	require.Equal(t, int32(1), providerCalls.Load())
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

func TestSourceWikiManualRetryRepairsTerminalFailedInitialCoverage(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"The source declares a schedule.","sections":[{"text":"The module declares the cited component.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	require.NoError(t, ledger.Create(f.ctx, batch))
	require.NoError(t, ledger.UpdateProgress(f.ctx, batch.ID, "cards", "failed", "", "whole-batch QA failed", 0, now.Add(time.Millisecond)))
	topic := types.SourceWikiCoverageTopic{
		ID: uuid.NewString(), TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID,
		SourceID: f.ds.ID, TopicKey: "module/src", SnapshotID: publication.SnapshotID,
		Kind: "module", ModulePath: "src", Title: "Scheduling module", Priority: 10,
		Initial: true, Status: "failed", UncertaintyReasons: types.JSON("[]"), Relations: types.JSON("[]"),
		BatchID: &batch.ID, WikiSlug: sourceWikiModuleSlug(f.ds.ID, "src"), UpdatedAt: now,
	}
	require.NoError(t, f.db.Create(&topic).Error)
	parentBefore, err := ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	manualAttempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", manualAttempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, topic.WikiSlug)
	require.NoError(t, err)
	require.Equal(t, types.WikiPageStatusPublished, page.Status)
	readService, ok := generator.(interfaces.SourceWikiBatchReadService)
	require.True(t, ok)
	coverage, err := readService.ListSourceWikiCoverage(f.ctx, f.kb.ID, f.ds.ID)
	require.NoError(t, err)
	updated := byCoverageTopic(t, coverage, topic.TopicKey)
	require.Equal(t, "ready", updated.Status)
	require.Equal(t, publication.SnapshotID, updated.LastReadySnapshotID)
	require.NotNil(t, updated.AttemptID)
	require.Equal(t, manualAttempt.ID, *updated.AttemptID)
	require.NotNil(t, updated.BatchID)
	require.Equal(t, batch.ID, *updated.BatchID, "manual repair must preserve the terminal batch association")
	unchangedBatch, err := ledger.Get(f.ctx, f.kb.ID, batch.ID)
	require.NoError(t, err)
	require.Equal(t, parentBefore.Status, unchangedBatch.Status)
	require.Equal(t, parentBefore.Phase, unchangedBatch.Phase)
	require.Equal(t, parentBefore.Cursor, unchangedBatch.Cursor)
	require.Equal(t, parentBefore.QACursor, unchangedBatch.QACursor)
	require.Equal(t, parentBefore.PublishCursor, unchangedBatch.PublishCursor)
	require.Equal(t, parentBefore.CallsReserved, unchangedBatch.CallsReserved)
	require.Equal(t, parentBefore.TokensReserved, unchangedBatch.TokensReserved)
	require.Equal(t, parentBefore.CandidateCount, unchangedBatch.CandidateCount)
}

func TestSourceWikiFlowEvidenceCapturesLateRelationRanges(t *testing.T) {
	padding := strings.Repeat("// padding to keep the real handler after the file header\n", 100)
	f := newJavaSourceFixture(t, map[string][]byte{
		"src/LateMapper.java":       []byte("package demo;\npublic interface LateMapper {\n" + padding + " String find();\n}\n"),
		"src/mapper/LateMapper.xml": []byte("<mapper namespace=\"demo.LateMapper\"><select id=\"find\">SELECT id FROM users</select></mapper>\n"),
	})
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` })
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	var relations []types.SourceCodeRelation
	require.NoError(t, f.db.Where("snapshot_id = ? AND kind = 'mapper_statement' AND from_path = ? AND to_path = ?", publication.SnapshotID, "src/LateMapper.java", "src/mapper/LateMapper.xml").Find(&relations).Error)
	require.NotEmpty(t, relations)
	var fromRange types.SourceRange
	require.NoError(t, json.Unmarshal(relations[0].FromRange, &fromRange))
	require.Greater(t, fromRange.StartByte, 2048)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, time.Now().UTC().Truncate(time.Millisecond))
	require.NoError(t, repository.NewSourceWikiBatchLedger(f.db).CreateWithPlan(f.ctx, batch, []types.SourceWikiTopic{{
		SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "flow/GET /late", Kind: "flow",
		Title: "GET /late", Priority: 100, Status: "planned", Relations: relations,
	}}, time.Now()))
	var coverage types.SourceWikiCoverageTopic
	require.NoError(t, f.db.Where("batch_id = ? AND topic_key = ?", batch.ID, "flow/GET /late").Take(&coverage).Error)
	evidence, err := generator.(*sourceWikiService).collectTopicEvidence(f.ctx, f.kb.ID, &types.SourceWikiAttempt{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID,
		BatchID: batch.ID, TopicKey: coverage.TopicKey, TopicKind: "flow", ModelContextWindow: 65536, MaxCompletionTokens: 1024,
	})
	require.NoError(t, err)
	records := make([]types.SourceWikiEvidence, 0, len(evidence))
	for _, item := range evidence {
		records = append(records, item.Evidence)
	}
	for _, relation := range relations {
		for _, endpoint := range []struct {
			fileID, versionID, path string
			rawRange                types.JSON
		}{{relation.FromFileID, relation.FromVersionID, relation.FromPath, relation.FromRange}, {relation.ToFileID, relation.ToVersionID, relation.ToPath, relation.ToRange}} {
			if endpoint.fileID == "" {
				continue
			}
			var required types.SourceRange
			require.NoError(t, json.Unmarshal(endpoint.rawRange, &required))
			covered := false
			for _, item := range records {
				if item.DataSourceID == relation.DataSourceID && item.SnapshotID == relation.SnapshotID &&
					item.KnowledgeID == endpoint.fileID && item.FileVersionID == endpoint.versionID && item.Path == endpoint.path &&
					sourceWikiFlowRangeCovers(item.Range, required) {
					covered = true
					break
				}
			}
			require.True(t, covered, "each exact relation endpoint needs registered evidence")
		}
	}
	diagram, err := BuildSourceWikiFlowDiagram(relations, records)
	require.NoError(t, err)
	require.NotEmpty(t, diagram.Markdown)
	totalWindowBytes := 0
	for _, item := range records {
		file, err := generator.(*sourceWikiService).knowledge.GetSourceFile(f.ctx, item.KnowledgeID, item.FileVersionID)
		require.NoError(t, err)
		window := file.RawContent[item.Range.StartByte:item.Range.EndByte]
		textHash := sha256.Sum256(window)
		require.Equal(t, hex.EncodeToString(textHash[:]), item.TextSHA256)
		require.Equal(t, sourceWikiRangeForBytes(file.RawContent, item.Range.StartByte, item.Range.EndByte), item.Range)
		require.Equal(t, publication.SnapshotID, item.SnapshotID)
		totalWindowBytes += len(window)
	}
	require.LessOrEqual(t, totalWindowBytes, sourceWikiMaxEvidenceBytes)
	wikiService := generator.(*sourceWikiService)
	ownerAttempt, err := wikiService.loadOrCreateSourceWikiAttempt(
		f.ctx, repository.NewSourceWikiAttemptLedger(f.db), f.kb,
		types.SourceWikiGenerateRequest{SourceID: f.ds.ID, ModulePath: "src", Title: "Evidence owner fixture"}, "src",
	)
	require.NoError(t, err)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return repository.RegisterSourceWikiAttemptEvidence(tx, ownerAttempt.ID, records)
	}))
	t.Cleanup(func() {
		_ = f.db.Transaction(func(tx *gorm.DB) error {
			return repository.ReleaseSourceWikiAttemptEvidence(tx, ownerAttempt.ID)
		})
	})
	var ownerCount int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", ownerAttempt.ID).Count(&ownerCount).Error)
	require.EqualValues(t, 2, ownerCount, "both exact endpoint versions stay pinned while the attempt owns their evidence")
	_, err = generator.(*sourceWikiService).collectTopicEvidence(f.ctx, f.kb.ID, &types.SourceWikiAttempt{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID,
		BatchID: batch.ID, TopicKey: coverage.TopicKey, TopicKind: "flow", ModelContextWindow: 800, MaxCompletionTokens: 100,
	})
	require.ErrorContains(t, err, "complete source evidence exceeds the bounded model context")
}

func TestSourceWikiUnmatchedVueRequestIsPublishedAndCollectedAsUncertainFlowEvidence(t *testing.T) {
	raw := []byte(`<template><div /></template>
<script>
export default { methods: { loadOrders() {
  this.$_HTTP.get('/api/orders', { params: {} })
} } }
</script>`)
	f := newJavaSourceFixture(t, map[string][]byte{"src/web/Orders.vue": raw})
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` })
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	snapshot, err := repository.LoadSourceWikiSkeletonSnapshot(f.ctx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.True(t, snapshot.Complete)
	var requestFact *types.ParsedSourceFact
	var requestFileID, requestVersionID string
	for _, member := range snapshot.Members {
		if member.Path != "src/web/Orders.vue" {
			continue
		}
		requestFileID, requestVersionID = member.FileID, member.VersionID
		for i := range member.Facts {
			if member.Facts[i].Kind == "api_request" && member.Facts[i].RoutePath == "/api/orders" {
				requestFact = &member.Facts[i]
				break
			}
		}
	}
	require.NotNil(t, requestFact, "the published complete source snapshot must contain the parser-authored request fact")
	var publishedAnchor *types.SourceCodeRelation
	for i := range snapshot.Relations {
		if snapshot.Relations[i].Kind == "http_route" && snapshot.Relations[i].FromPath == "src/web/Orders.vue" {
			publishedAnchor = &snapshot.Relations[i]
			break
		}
	}
	require.NotNil(t, publishedAnchor, "whole-repository sync must publish a request-only relation anchor")
	require.Equal(t, "uncertain", publishedAnchor.Determinacy)
	require.Equal(t, requestFileID, publishedAnchor.FromFileID)
	require.Equal(t, requestVersionID, publishedAnchor.FromVersionID)
	require.Equal(t, "src/web/Orders.vue", publishedAnchor.FromPath)
	require.Empty(t, publishedAnchor.ToFileID, "the unmatched request must not invent a backend edge")
	require.Empty(t, publishedAnchor.ToKey)
	require.Equal(t, "No statically validated backend route relationship was found for this request", publishedAnchor.ResolutionReason)
	var publishedRange types.SourceRange
	require.NoError(t, json.Unmarshal(publishedAnchor.FromRange, &publishedRange))
	require.Equal(t, requestFact.Range, publishedRange)

	preflightService := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	publicPreview, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	var publicFlow *types.SourceWikiTopic
	for i := range publicPreview.PlannedTopics {
		if publicPreview.PlannedTopics[i].TopicKey == "flow/GET /api/orders" {
			publicFlow = &publicPreview.PlannedTopics[i]
			break
		}
	}
	require.NotNil(t, publicFlow, "public preflight must preserve the unmatched request flow")
	require.True(t, publicFlow.Uncertain)
	require.Contains(t, publicFlow.UncertaintyReasons, "No statically validated backend route relationship was found for this request")
	require.Len(t, publicFlow.Relations, 1)
	require.Equal(t, "uncertain", publicFlow.Relations[0].Determinacy)
	var publicRefs []types.SourceRelationFactRef
	require.NoError(t, json.Unmarshal(publicFlow.Relations[0].Context, &publicRefs))
	require.Empty(t, publicRefs, "request-only anchors keep causal configuration refs empty; request identity is carried by the relation endpoint")
	require.Equal(t, publishedAnchor.FromFileID, publicFlow.Relations[0].FromFileID)
	require.Equal(t, publishedAnchor.FromVersionID, publicFlow.Relations[0].FromVersionID)
	require.Equal(t, publishedAnchor.FromPath, publicFlow.Relations[0].FromPath)
	require.Equal(t, publishedAnchor.FromRange, publicFlow.Relations[0].FromRange)
	var publicRequestRange types.SourceRange
	require.NoError(t, json.Unmarshal(publicFlow.Relations[0].FromRange, &publicRequestRange))
	require.Equal(t, requestFact.Range, publicRequestRange)
	for _, table := range []string{"source_wiki_batches", "source_wiki_topics", "wiki_pages", "wiki_page_revisions"} {
		var count int64
		require.NoError(t, f.db.Table(table).Where("knowledge_base_id = ?", f.kb.ID).Count(&count).Error)
		require.Zero(t, count, "%s must remain untouched by public preflight", table)
	}

	service := generator.(*sourceWikiService)
	relations, err := service.resolveSourceWikiRelations(f.ctx, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID, snapshot, []types.SourceCodeRelation{*publishedAnchor})
	require.NoError(t, err, "the published request anchor must replay against its exact complete snapshot")
	files := make([]sourceWikiSkeletonFile, 0, len(snapshot.Files))
	for _, file := range snapshot.Files {
		files = append(files, sourceWikiSkeletonFile{Path: file.Path, Generated: file.Generated, Facts: file.Facts})
	}
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, Files: files, Relations: relations}, 40)
	var flow *types.SourceWikiTopic
	for i := range plan.Topics {
		if plan.Topics[i].TopicKey == "flow/GET /api/orders" {
			flow = &plan.Topics[i]
			break
		}
	}
	require.NotNil(t, flow)
	require.True(t, flow.Uncertain)
	require.Len(t, flow.Relations, 1)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, time.Now().UTC().Truncate(time.Millisecond))
	flow.Status = "planned"
	ledger := repository.NewSourceWikiBatchLedger(f.db)
	require.NoError(t, ledger.CreateWithPlan(f.ctx, batch, []types.SourceWikiTopic{*flow}, time.Now().UTC()))
	evidence, err := service.collectTopicEvidence(f.ctx, f.kb.ID, &types.SourceWikiAttempt{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID,
		BatchID: batch.ID, TopicKey: flow.TopicKey, TopicKind: "flow", ModelContextWindow: 65536, MaxCompletionTokens: 1024,
	})
	require.NoError(t, err)
	require.NotEmpty(t, evidence, "request-only anchor must make the frontend request source readable as flow evidence")
	var matched *collectedWikiEvidence
	for i := range evidence {
		if evidence[i].Evidence.Path == "src/web/Orders.vue" {
			matched = &evidence[i]
			break
		}
	}
	require.NotNil(t, matched)
	require.True(t, sourceWikiFlowRangeCovers(matched.Evidence.Range, requestFact.Range))
	require.Contains(t, matched.Text, `this.$_HTTP.get('/api/orders'`)
	diagram, err := BuildSourceWikiFlowDiagram(relations, []types.SourceWikiEvidence{matched.Evidence})
	require.NoError(t, err)
	require.True(t, diagram.Uncertain)
	require.Contains(t, diagram.Markdown, "-.->|uncertain HTTP route|")
	require.Contains(t, diagram.Markdown, "unresolved endpoint")
}

func TestSourceWikiFlowEvidencePinsParticipatingCrossFileConfigurationFacts(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{
		"web/app/views/detail.js": []byte("http.get('/detail')\n"),
		"web/app/api.js":          []byte("export const apiRoot = '/app'\n"),
		"web/app/vue.config.js":   []byte("module.exports = { proxy: '/app' }\n"),
		"web/app/unused.js":       []byte("export const unused = '/unused'\n"),
		"web/other/vue.config.js": []byte("module.exports = { proxy: '/app' }\n"),
		"server/Controller.java":  []byte("@RequestMapping(\"/svc\")\n@GetMapping(\"/detail\")\npublic String detail() { return \"ok\"; }\n"),
	})
	require.NoError(t, f.db.Exec(`UPDATE data_sources SET config = jsonb_set(config, '{settings,projects,0,paths}', '["src","web","server"]'::jsonb) WHERE id = ?`, f.ds.ID).Error)
	require.NoError(t, f.db.Where("id = ?", f.ds.ID).Take(f.ds).Error)
	syncSourceFixture(t, f)
	_, generator := newSourceWikiFixture(t, f, func(bool) string { return `{}` })
	migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(migration)).Error)
	var publication types.SourcePublication
	require.NoError(t, f.db.Where("data_source_id = ?", f.ds.ID).Take(&publication).Error)
	var members []types.SourceSnapshotMember
	require.NoError(t, f.db.Where("snapshot_id = ? AND status = 'parsed'", publication.SnapshotID).Find(&members).Error)
	memberByPath := make(map[string]types.SourceSnapshotMember, len(members))
	for _, member := range members {
		memberByPath[member.Path] = member
	}
	member := func(filePath string) types.SourceSnapshotMember {
		t.Helper()
		found, ok := memberByPath[filePath]
		require.True(t, ok, "fixture snapshot is missing %s", filePath)
		require.NotEmpty(t, found.SourceFileID)
		require.NotEmpty(t, found.FileVersionID)
		return found
	}
	rangeFor := func(filePath, snippet string) types.SourceRange {
		t.Helper()
		raw := map[string][]byte{
			"web/app/views/detail.js": []byte("http.get('/detail')\n"),
			"web/app/api.js":          []byte("export const apiRoot = '/app'\n"),
			"web/app/vue.config.js":   []byte("module.exports = { proxy: '/app' }\n"),
			"web/app/unused.js":       []byte("export const unused = '/unused'\n"),
			"web/other/vue.config.js": []byte("module.exports = { proxy: '/app' }\n"),
			"server/Controller.java":  []byte("@RequestMapping(\"/svc\")\n@GetMapping(\"/detail\")\npublic String detail() { return \"ok\"; }\n"),
		}[filePath]
		start := bytes.Index(raw, []byte(snippet))
		require.GreaterOrEqual(t, start, 0, "range fixture %q not found in %s", snippet, filePath)
		end := start + len(snippet)
		return types.SourceRange{
			StartByte: start, EndByte: end,
			StartLine: 1 + bytes.Count(raw[:start], []byte("\n")),
			EndLine:   1 + bytes.Count(raw[:end-1], []byte("\n")),
		}
	}
	requestMember := member("web/app/views/detail.js")
	apiFact := types.ParsedSourceFact{Kind: "api_request", Name: "detail", RoutePath: "/detail", HTTPMethod: "GET", Quality: "structural",
		Range: rangeFor(requestMember.Path, "http.get('/detail')")}
	prefixMember := member("web/app/api.js")
	foreignFileID, foreignVersionID := uuid.NewString(), uuid.NewString()
	require.NoError(t, f.db.Create(&types.SourceFile{ID: foreignFileID, TenantID: f.kb.TenantID,
		KnowledgeBaseID: f.kb.ID, DataSourceID: uuid.NewString(), Path: "foreign/facts.js"}).Error)
	require.NoError(t, f.db.Create(&types.SourceFileVersion{ID: foreignVersionID, SourceFileID: foreignFileID,
		SnapshotID: publication.SnapshotID, BlobSHA: "foreign", SHA256: strings.Repeat("f", 64), Content: []byte{},
		Encoding: "utf-8", ParserVersion: "fixture", Quality: "structural", Symbols: types.JSON(`[]`),
		Facts: types.JSON(`[]`), Diagnostics: types.JSON(`[]`), CreatedAt: time.Now().UTC()}).Error)
	require.NoError(t, f.db.Table("source_snapshot_members").
		Where("snapshot_id = ? AND path = ?", publication.SnapshotID, prefixMember.Path).
		Updates(map[string]any{"source_file_id": foreignFileID, "file_version_id": foreignVersionID}).Error)
	_, err = repository.LoadSourceWikiSkeletonSnapshot(f.ctx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.ErrorContains(t, err, "incomplete immutable identity", "members cannot borrow an otherwise valid same-snapshot version from another source")
	require.NoError(t, f.db.Table("source_snapshot_members").
		Where("snapshot_id = ? AND path = ?", publication.SnapshotID, prefixMember.Path).
		Updates(map[string]any{"source_file_id": prefixMember.SourceFileID, "file_version_id": prefixMember.FileVersionID}).Error)
	prefixFact := types.ParsedSourceFact{Kind: "api_prefix", Name: "/app", RoutePath: "/app", Certainty: "certain", Quality: "structural",
		Range: rangeFor(prefixMember.Path, "apiRoot = '/app'")}
	proxyMember := member("web/app/vue.config.js")
	proxyFact := types.ParsedSourceFact{Kind: "api_proxy", Name: "/app", RoutePath: "/svc", TargetName: "^/app", OwnerName: "web/app", Certainty: "certain", Quality: "structural",
		Range: rangeFor(proxyMember.Path, "proxy: '/app'")}
	unusedMember := member("web/app/unused.js")
	unusedFact := types.ParsedSourceFact{Kind: "api_prefix", Name: "/unused", RoutePath: "/unused", Certainty: "certain", Quality: "structural",
		Range: rangeFor(unusedMember.Path, "unused = '/unused'")}
	siblingProxyMember := member("web/other/vue.config.js")
	require.NoError(t, f.db.Table("source_snapshot_members").
		Where("snapshot_id = ? AND path = ?", publication.SnapshotID, prefixMember.Path).
		Update("file_version_id", siblingProxyMember.FileVersionID).Error)
	_, err = repository.LoadSourceWikiSkeletonSnapshot(f.ctx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.ErrorContains(t, err, "incomplete immutable identity", "a member must not borrow facts from another file's version")
	require.NoError(t, f.db.Table("source_snapshot_members").
		Where("snapshot_id = ? AND path = ?", publication.SnapshotID, prefixMember.Path).
		Update("file_version_id", prefixMember.FileVersionID).Error)
	siblingProxyFact := proxyFact
	siblingProxyFact.OwnerName = "web/other"
	siblingProxyFact.Range = rangeFor(siblingProxyMember.Path, "proxy: '/app'")
	controllerMember := member("server/Controller.java")
	classFact := types.ParsedSourceFact{Kind: "spring_mapping", Name: "Controller", Namespace: "demo.Controller", RoutePath: "/svc",
		StatementType: "type", OwnerKind: "type", Quality: "structural", Range: rangeFor(controllerMember.Path, `@RequestMapping("/svc")`)}
	handlerFact := types.ParsedSourceFact{Kind: "spring_mapping", Name: "detail", Namespace: "demo.Controller", RoutePath: "/detail",
		StatementType: "method", OwnerKind: "method", OwnerName: "detail", HTTPMethod: "GET", Quality: "structural",
		Range: rangeFor(controllerMember.Path, `@GetMapping("/detail")`)}
	factsByVersion := map[string][]types.ParsedSourceFact{
		requestMember.FileVersionID:      {apiFact},
		prefixMember.FileVersionID:       {prefixFact},
		proxyMember.FileVersionID:        {proxyFact},
		unusedMember.FileVersionID:       {unusedFact},
		siblingProxyMember.FileVersionID: {siblingProxyFact},
		controllerMember.FileVersionID:   {classFact, handlerFact},
	}
	for versionID, facts := range factsByVersion {
		encoded, err := json.Marshal(facts)
		require.NoError(t, err)
		require.NoError(t, f.db.Table("source_file_versions").Where("id = ?", versionID).Update("facts", types.JSON(encoded)).Error)
	}
	correlationMembers := []source.SourceRelationMember{
		{Path: requestMember.Path, FileID: requestMember.SourceFileID, VersionID: requestMember.FileVersionID, Facts: []types.ParsedSourceFact{apiFact}},
		{Path: prefixMember.Path, FileID: prefixMember.SourceFileID, VersionID: prefixMember.FileVersionID, Facts: []types.ParsedSourceFact{prefixFact, unusedFact}},
		{Path: proxyMember.Path, FileID: proxyMember.SourceFileID, VersionID: proxyMember.FileVersionID, Facts: []types.ParsedSourceFact{proxyFact}},
		{Path: siblingProxyMember.Path, FileID: siblingProxyMember.SourceFileID, VersionID: siblingProxyMember.FileVersionID, Facts: []types.ParsedSourceFact{siblingProxyFact}},
		{Path: controllerMember.Path, FileID: controllerMember.SourceFileID, VersionID: controllerMember.FileVersionID, Facts: []types.ParsedSourceFact{classFact, handlerFact}},
	}
	correlated := source.CorrelateSourceFacts(f.kb.TenantID, f.ds.ID, publication.SnapshotID, correlationMembers)
	var routeRelations []types.SourceCodeRelation
	for _, relation := range correlated {
		if relation.Kind == "http_route" {
			routeRelations = append(routeRelations, relation)
		}
	}
	require.Len(t, routeRelations, 1)
	route := routeRelations[0]
	require.Equal(t, "certain", route.Determinacy)
	var routeRefs []types.SourceRelationFactRef
	require.NoError(t, json.Unmarshal(route.Context, &routeRefs))
	require.Len(t, routeRefs, 3)
	roles := make(map[string]string, len(routeRefs))
	for _, ref := range routeRefs {
		roles[ref.Role] = ref.Path
	}
	require.Equal(t, "web/app/api.js", roles["api_prefix"])
	require.Equal(t, "web/app/vue.config.js", roles["api_proxy"])
	require.Equal(t, "server/Controller.java", roles["spring_class_mapping"])
	for _, path := range roles {
		require.NotContains(t, path, "unused")
		require.NotContains(t, path, "web/other")
	}
	require.NoError(t, f.db.Where("snapshot_id = ?", publication.SnapshotID).Delete(&types.SourceCodeRelation{}).Error)
	require.NoError(t, f.db.Create(&route).Error)

	preflightService := generator.(interface {
		PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
	})
	originalRouteContext := append(types.JSON(nil), route.Context...)
	var tamperedRefs []types.SourceRelationFactRef
	require.NoError(t, json.Unmarshal(route.Context, &tamperedRefs))
	require.NotEmpty(t, tamperedRefs)
	tamperedRefs[0].FileID = uuid.NewString()
	tamperedContext, err := json.Marshal(tamperedRefs)
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&types.SourceCodeRelation{}).Where("id = ?", route.ID).
		Update("context", types.JSON(tamperedContext)).Error)
	_, err = preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.ErrorIs(t, err, repository.ErrSourceWikiDerivationUnavailable,
		"public preflight must resolve exact fact references instead of trusting relation JSON")
	for _, table := range []string{"source_wiki_batches", "source_wiki_topics", "wiki_pages", "wiki_page_revisions"} {
		var count int64
		require.NoError(t, f.db.Table(table).Where("knowledge_base_id = ?", f.kb.ID).Count(&count).Error)
		require.Zero(t, count, "%s must remain untouched after a failed public preflight", table)
	}
	require.NoError(t, f.db.Model(&types.SourceCodeRelation{}).Where("id = ?", route.ID).
		Update("context", originalRouteContext).Error)
	publicPreview, err := preflightService.PreflightSourceWikiBatch(f.ctx, f.kb.ID, f.ds.ID, types.SourceWikiBatchPreflightRequest{})
	require.NoError(t, err)
	var publicFlow *types.SourceWikiTopic
	for i := range publicPreview.PlannedTopics {
		if publicPreview.PlannedTopics[i].TopicKey == "flow/GET /detail" {
			publicFlow = &publicPreview.PlannedTopics[i]
			break
		}
	}
	require.NotNil(t, publicFlow, "public preflight must preserve the cross-file HTTP flow candidate")
	require.Len(t, publicFlow.Relations, 1)
	require.JSONEq(t, string(originalRouteContext), string(publicFlow.Relations[0].Context),
		"public preflight must carry the exact resolved relation references")
	for _, table := range []string{"source_wiki_batches", "source_wiki_topics", "wiki_pages", "wiki_page_revisions"} {
		var count int64
		require.NoError(t, f.db.Table(table).Where("knowledge_base_id = ?", f.kb.ID).Count(&count).Error)
		require.Zero(t, count, "%s must remain untouched after a successful preflight", table)
	}

	snapshotEvidence, err := repository.LoadSourceWikiSkeletonSnapshot(f.ctx, f.db, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.True(t, snapshotEvidence.Complete)
	service := generator.(*sourceWikiService)
	resolvedRelations, err := service.resolveSourceWikiRelations(f.ctx, f.kb.TenantID, f.kb.ID, f.ds.ID, publication.SnapshotID,
		snapshotEvidence, snapshotEvidence.Relations)
	require.NoError(t, err)
	targets, err := sourceWikiFlowEvidenceRanges(resolvedRelations, f.ds.ID, publication.SnapshotID)
	require.NoError(t, err)
	require.Len(t, targets, 4, "only the request, handler, and three causal config fact files are eligible")
	for target := range targets {
		require.NotContains(t, target.Path, "unused")
		require.NotContains(t, target.Path, "web/other")
	}

	ledger := repository.NewSourceWikiBatchLedger(f.db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := newSourceWikiTestBatch(f, publication.SnapshotID, now)
	topic := types.SourceWikiTopic{SourceID: f.ds.ID, SnapshotID: publication.SnapshotID, TopicKey: "flow/GET /detail", Kind: "flow",
		Title: "GET /detail", Priority: 100, Status: "planned", Relations: resolvedRelations}
	require.NoError(t, ledger.CreateWithPlan(f.ctx, batch, []types.SourceWikiTopic{topic}, now))
	var coverage types.SourceWikiCoverageTopic
	require.NoError(t, f.db.Where("batch_id = ? AND topic_key = ?", batch.ID, topic.TopicKey).Take(&coverage).Error)
	evidence, err := service.collectTopicEvidence(f.ctx, f.kb.ID, &types.SourceWikiAttempt{
		TenantID: f.kb.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: publication.SnapshotID,
		BatchID: batch.ID, TopicKey: topic.TopicKey, TopicKind: "flow", ModelContextWindow: 65536, MaxCompletionTokens: 1024,
	})
	require.NoError(t, err)
	records := make([]types.SourceWikiEvidence, 0, len(evidence))
	seenPaths := make(map[string]bool, len(evidence))
	for _, item := range evidence {
		records = append(records, item.Evidence)
		seenPaths[item.Evidence.Path] = true
	}
	require.Len(t, records, 4)
	for _, path := range []string{"web/app/views/detail.js", "web/app/api.js", "web/app/vue.config.js", "server/Controller.java"} {
		require.True(t, seenPaths[path], "missing exact relation/config evidence for %s", path)
	}
	require.False(t, seenPaths["web/app/unused.js"])
	require.False(t, seenPaths["web/other/vue.config.js"])
	for _, ref := range routeRefs {
		covered := false
		for _, record := range records {
			if record.DataSourceID == ref.DataSourceID && record.SnapshotID == ref.SnapshotID && record.KnowledgeID == ref.FileID &&
				record.FileVersionID == ref.FileVersionID && record.Path == ref.Path && sourceWikiFlowRangeCovers(record.Range, ref.Range) {
				covered = true
				break
			}
		}
		require.True(t, covered, "exact referenced config fact range was not included: %s", ref.Path)
	}
	diagram, err := BuildSourceWikiFlowDiagram(resolvedRelations, records)
	require.NoError(t, err)
	require.NotEmpty(t, diagram.Markdown, "the verified route remains renderable after exact config evidence is collected")
	require.NoError(t, sourceWikiValidateDiagramFactEvidence(resolvedRelations, records, diagram))
	for _, ref := range routeRefs {
		var coveringEvidenceID string
		for _, record := range records {
			if record.DataSourceID == ref.DataSourceID && record.SnapshotID == ref.SnapshotID && record.KnowledgeID == ref.FileID &&
				record.FileVersionID == ref.FileVersionID && record.Path == ref.Path && sourceWikiFlowRangeCovers(record.Range, ref.Range) {
				coveringEvidenceID = record.ID
				break
			}
		}
		require.NotEmpty(t, coveringEvidenceID, "fact evidence must have a stable ID")
		require.Contains(t, diagram.EvidenceIDs, coveringEvidenceID, "the diagram itself must cite every causal config fact")
	}
	ownerAttempt, err := service.loadOrCreateSourceWikiAttempt(f.ctx, repository.NewSourceWikiAttemptLedger(f.db), f.kb,
		types.SourceWikiGenerateRequest{SourceID: f.ds.ID, ModulePath: "src", Title: "Cross-file flow evidence"}, "src")
	require.NoError(t, err)
	require.NoError(t, f.db.Transaction(func(tx *gorm.DB) error {
		return repository.RegisterSourceWikiAttemptEvidence(tx, ownerAttempt.ID, records)
	}))
	t.Cleanup(func() {
		_ = f.db.Transaction(func(tx *gorm.DB) error { return repository.ReleaseSourceWikiAttemptEvidence(tx, ownerAttempt.ID) })
	})
	var pinCount int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", ownerAttempt.ID).Count(&pinCount).Error)
	require.EqualValues(t, 4, pinCount, "exact config fact files remain pinned with the flow endpoint evidence")
}
