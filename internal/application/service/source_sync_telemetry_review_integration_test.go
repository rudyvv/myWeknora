//go:build integration

package service

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceSyncLogCoverageIsEnrichedFromItsExactPublishedSnapshot(t *testing.T) {
	f := newJavaSourceFixture(t)
	oldLog, oldRun := runSourceTelemetryReviewSync(t, f)
	require.NotNil(t, oldRun.Snapshot)
	oldSnapshotID := oldRun.Snapshot.ID

	f.advanceJava("package demo; public class Service { public String getPushSchedule() { return \"updated\"; } }\n")
	newLog, newRun := runSourceTelemetryReviewSync(t, f)
	require.NotNil(t, newRun.Snapshot)
	newSnapshotID := newRun.Snapshot.ID
	require.NotEqual(t, oldSnapshotID, newSnapshotID)
	sameSnapshotLog, sameSnapshotRun := runSourceTelemetryReviewSync(t, f)
	require.NotNil(t, sameSnapshotRun.Snapshot)
	require.Equal(t, newSnapshotID, sameSnapshotRun.Snapshot.ID)
	require.NotEqual(t, sameSnapshotLog.ID, sameSnapshotRun.Snapshot.SyncLogID,
		"a no-op sync log must retain the original published snapshot owner")
	coverageSchema, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql"))
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(string(coverageSchema)).Error)

	insertSourceTelemetryReviewCoverage(t, f, oldSnapshotID, "old-topic", "ready", oldSnapshotID)
	newPlanID := insertSourceTelemetryReviewCoverage(t, f, newSnapshotID, "new-topic", "failed", "")

	oldResponse, err := f.service.GetSyncLog(f.ctx, oldLog.ID)
	require.NoError(t, err)
	oldResult, err := oldResponse.ParseResult()
	require.NoError(t, err)
	oldCoverage := oldResult.Source.Telemetry.WikiCoverage
	require.NotNil(t, oldCoverage)
	require.EqualValues(t, 1, *oldCoverage.EligibleTopics)
	require.EqualValues(t, 1, *oldCoverage.ReadyTopics)
	require.EqualValues(t, 0, *oldCoverage.FailedTopics)
	wrongTenant := types.WithCaller(context.Background(), types.Caller{TenantID: 2})
	_, err = f.service.GetSyncLog(wrongTenant, oldLog.ID)
	require.Error(t, err, "sync-log enrichment must keep the source Wiki read authorization boundary")

	newResponse, err := f.service.GetSyncLog(f.ctx, newLog.ID)
	require.NoError(t, err)
	newResult, err := newResponse.ParseResult()
	require.NoError(t, err)
	newCoverage := newResult.Source.Telemetry.WikiCoverage
	require.NotNil(t, newCoverage)
	require.EqualValues(t, 1, *newCoverage.EligibleTopics)
	require.EqualValues(t, 0, *newCoverage.ReadyTopics)
	require.EqualValues(t, 1, *newCoverage.FailedTopics)
	noOpResponse, err := f.service.GetSyncLog(f.ctx, sameSnapshotLog.ID)
	require.NoError(t, err)
	noOpResult, err := noOpResponse.ParseResult()
	require.NoError(t, err)
	require.Equal(t, newSnapshotID, noOpResult.Source.Snapshot.ID)
	require.EqualValues(t, 1, *noOpResult.Source.Telemetry.WikiCoverage.FailedTopics,
		"a no-op log must refresh coverage through its exact reused snapshot")

	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 20, 0)
	require.NoError(t, err)
	coverageByLog := make(map[string]*types.SourceWikiCoverageTelemetry, len(logs))
	for _, item := range logs {
		parsed, parseErr := item.ParseResult()
		require.NoError(t, parseErr)
		if parsed != nil && parsed.Source != nil && parsed.Source.Telemetry != nil {
			coverageByLog[item.ID] = parsed.Source.Telemetry.WikiCoverage
		}
	}
	require.EqualValues(t, 1, *coverageByLog[oldLog.ID].ReadyTopics, "the history list must retain the old log's exact snapshot summary")
	require.EqualValues(t, 1, *coverageByLog[newLog.ID].FailedTopics, "the history list must not substitute the current snapshot summary")
	require.EqualValues(t, 1, *coverageByLog[sameSnapshotLog.ID].FailedTopics, "the no-op log must retain reused-snapshot coverage")

	var response map[string]any
	require.NoError(t, json.Unmarshal(newResponse.Result, &response))
	sourceResult := response["source"].(map[string]any)
	telemetry := sourceResult["telemetry"].(map[string]any)
	wikiCoverage := telemetry["wiki_coverage"].(map[string]any)
	for _, key := range []string{"eligible", "ready", "stale", "failed", "ungenerated", "deferred"} {
		require.Contains(t, wikiCoverage, key)
	}
	for _, oldKey := range []string{"eligible_topics", "ready_topics", "stale_topics", "failed_topics", "ungenerated_topics", "deferred_topics"} {
		require.NotContains(t, wikiCoverage, oldKey)
	}
	require.NoError(t, f.db.Model(&types.SyncLog{}).Where("id=?", sameSnapshotLog.ID).Update("result", noOpResponse.Result).Error)
	require.NoError(t, f.db.Model(&types.SourceWikiUpdatePlan{}).Where("id=?", newPlanID).Update("status", "pending").Error)
	unknownResponse, err := f.service.GetSyncLog(f.ctx, sameSnapshotLog.ID)
	require.NoError(t, err)
	unknownResult, err := unknownResponse.ParseResult()
	require.NoError(t, err)
	require.Nil(t, unknownResult.Source.Telemetry.WikiCoverage,
		"incomplete/currently unavailable coverage must not leave a stale cached summary")
}

func TestSourceLeaseRecoveryCountIsDurableAndExcludesOrdinaryRetry(t *testing.T) {
	f := newJavaSourceFixture(t)
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	ds, err := f.service.GetDataSource(f.ctx, f.ds.ID)
	require.NoError(t, err)
	control := f.service.syncLogRepo.(interfaces.SourceSyncControlRepository)

	firstLease, claimed, err := control.ClaimSourceRun(f.ctx, ds, log.ID, 1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Zero(t, firstLease.LeaseRecoveryCount)
	dispatch, err := control.ReleaseSourceRun(f.ctx, firstLease, true)
	require.NoError(t, err)
	require.NotNil(t, dispatch)

	retryLease, claimed, err := control.ClaimSourceRun(f.ctx, ds, log.ID, dispatch.DeliveryGeneration, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Zero(t, retryLease.LeaseRecoveryCount, "a deliberate ordinary retry is not an expired-lease takeover")
	require.NoError(t, f.db.Exec("UPDATE source_sync_states SET lease_expires_at=now()-interval '1 second' WHERE data_source_id=?", f.ds.ID).Error)

	recovered, err := control.RecoverSourceTriggers(f.ctx, ds)
	require.NoError(t, err)
	require.Len(t, recovered, 1)
	require.Equal(t, dispatch.DeliveryGeneration+1, recovered[0].DeliveryGeneration)
	var persisted int64
	require.NoError(t, f.db.Table("source_sync_runs").Select("lease_recovery_count").Where("sync_log_id=?", log.ID).Scan(&persisted).Error)
	require.EqualValues(t, 1, persisted)

	// Re-polling an already-cleared expired lease can redeliver the same durable
	// run, but must not increment its recovery telemetry a second time.
	repolled, err := control.RecoverSourceTriggers(f.ctx, ds)
	require.NoError(t, err)
	require.Len(t, repolled, 1)
	require.NoError(t, f.db.Table("source_sync_runs").Select("lease_recovery_count").Where("sync_log_id=?", log.ID).Scan(&persisted).Error)
	require.EqualValues(t, 1, persisted)

	recoveredLease, claimed, err := control.ClaimSourceRun(f.ctx, ds, log.ID, recovered[0].DeliveryGeneration, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.EqualValues(t, 1, recoveredLease.LeaseRecoveryCount)
	require.NoError(t, f.db.Table("source_sync_runs").Select("lease_recovery_count").Where("sync_log_id=?", log.ID).Scan(&persisted).Error)
	require.EqualValues(t, 1, persisted)

	// Model a previously observed cleanup residue in the original durable log;
	// this resumed attempt observes zero and must preserve the earlier count.
	priorTelemetry := types.NewSourceRunTelemetry()
	priorResidue := int64(1)
	priorTelemetry.CleanupResidueCount = &priorResidue
	priorResult := &types.SyncResult{Source: &types.SourceRunResult{
		Snapshot: &types.SourceSnapshot{ID: uuid.NewString(), TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID,
			DataSourceID: f.ds.ID, SyncLogID: log.ID, State: "fetching"},
		Members: []types.SourceSnapshotMember{}, Telemetry: priorTelemetry,
	}}
	priorResultJSON, err := priorResult.ToJSON()
	require.NoError(t, err)
	require.NoError(t, f.db.Model(&types.SyncLog{}).Where("id=?", log.ID).Update("result", priorResultJSON).Error)
	currentLog, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	kb, err := f.kbs.GetKnowledgeBaseByID(f.ctx, f.kb.ID)
	require.NoError(t, err)
	connector, err := f.service.connectorRegistry.Get(ds.Type)
	require.NoError(t, err)
	config, err := ds.ParseConfig()
	require.NoError(t, err)
	require.NoError(t, f.service.runSourceSyncWithLease(f.ctx, control, recoveredLease, ds, currentLog, kb, connector, config, false))

	completed, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	result, err := completed.ParseResult()
	require.NoError(t, err)
	require.NotNil(t, result.Source.Telemetry.LeaseRecoveries)
	require.EqualValues(t, 1, *result.Source.Telemetry.LeaseRecoveries)
	require.NotNil(t, result.Source.Telemetry.CleanupResidueCount)
	require.EqualValues(t, 1, *result.Source.Telemetry.CleanupResidueCount,
		"a measured zero during recovery must not overwrite the persisted prior residue")

	directLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	directLease, claimed, err := control.ClaimSourceRun(f.ctx, ds, directLog.ID, 1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Zero(t, directLease.LeaseRecoveryCount)
	require.NoError(t, f.db.Exec("UPDATE source_sync_states SET lease_expires_at=now()-interval '1 second' WHERE data_source_id=?", f.ds.ID).Error)
	directTakeover, claimed, err := control.ClaimSourceRun(f.ctx, ds, directLog.ID, 1, uuid.NewString(), time.Minute)
	require.NoError(t, err)
	require.True(t, claimed)
	require.EqualValues(t, 1, directTakeover.LeaseRecoveryCount,
		"ClaimSourceRun must count an immediate takeover of an expired active lease")
}

func runSourceTelemetryReviewSync(t *testing.T, f *javaSourceFixture) (*types.SyncLog, *types.SourceRunResult) {
	t.Helper()
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: f.ds.TenantID, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	response, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	result, err := response.ParseResult()
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.Source)
	return log, result.Source
}

func insertSourceTelemetryReviewCoverage(t *testing.T, f *javaSourceFixture, snapshotID, topicKey, status, lastReadySnapshotID string) string {
	t.Helper()
	now := time.Now().UTC()
	inventory, err := json.Marshal(types.SourceWikiImpactTopicInventory{
		TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, SnapshotID: snapshotID,
		Complete: true, ExpectedTopicCount: 1,
		Topics: []types.SourceWikiImpactTopicDependencies{{TopicKey: topicKey}},
	})
	require.NoError(t, err)
	plan := &types.SourceWikiUpdatePlan{
		ID: uuid.NewString(), TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID,
		SnapshotID: snapshotID, ConfigGeneration: 1, PlanDigest: strings.Repeat("a", 64), Status: "completed",
		Plan: types.JSON(`{}`), NextInventory: types.JSON(inventory), CreatedAt: now, UpdatedAt: now, CompletedAt: &now,
	}
	require.NoError(t, f.db.Create(plan).Error)
	topic := &types.SourceWikiCoverageTopic{
		ID: uuid.NewString(), TenantID: f.ds.TenantID, KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID,
		TopicKey: topicKey, SnapshotID: snapshotID, Kind: "flow", Title: topicKey, Status: status,
		UncertaintyReasons: types.JSON(`[]`), Relations: types.JSON(`[]`),
		LastReadySnapshotID: lastReadySnapshotID, UpdatedAt: now,
	}
	require.NoError(t, f.db.Create(topic).Error)
	return plan.ID
}
