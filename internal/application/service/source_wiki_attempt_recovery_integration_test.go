//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const sourceWikiRecoveryDraft = `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`

func startTestSourceWikiRecovery(t *testing.T, generator interfaces.SourceWikiService) *SourceWikiAttemptRecovery {
	t.Helper()
	svc := generator.(*sourceWikiService)
	recovery := NewSourceWikiAttemptRecovery(svc.db, svc.kb, svc.models, generator)
	recovery.interval = 15 * time.Millisecond
	require.NoError(t, recovery.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, recovery.Stop()) })
	return recovery
}

func makeSourceWikiRecoveryAttempt(t *testing.T, f *javaSourceFixture, generator interfaces.SourceWikiService) (*types.SourceWikiAttempt, *repository.SourceWikiAttemptLedger) {
	t.Helper()
	svc := generator.(*sourceWikiService)
	ledger := repository.NewSourceWikiAttemptLedger(svc.db)
	attempt, err := svc.loadOrCreateSourceWikiAttempt(f.ctx, ledger, f.kb, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
		TopicKind: "module", TopicKey: "module/src",
	}, "src")
	require.NoError(t, err)
	return attempt, ledger
}

func persistSourceWikiRecoveryQACheckpoint(t *testing.T, f *javaSourceFixture, generator interfaces.SourceWikiService, attempt *types.SourceWikiAttempt, ledger *repository.SourceWikiAttemptLedger) (types.SourceWikiAttemptLease, []collectedWikiEvidence) {
	t.Helper()
	svc := generator.(*sourceWikiService)
	readCtx, release, err := beginSourceRead(f.ctx, svc.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, SourceIDs: []string{f.ds.ID},
	}})
	require.NoError(t, err)
	evidence, err := svc.collectEvidence(readCtx, f.kb.ID, f.ds.ID, "src")
	release()
	require.NoError(t, err)
	require.NotEmpty(t, evidence)
	lease, err := ledger.Claim(f.ctx, types.SourceWikiAttemptClaimRequest{
		AttemptID: attempt.ID, Owner: uuid.NewString(), Now: time.Now(), LeaseFor: 700 * time.Millisecond,
	})
	require.NoError(t, err)
	checkpoint := sourceWikiAttemptCheckpoint{Evidence: evidence, SourceDraft: sourceWikiRecoveryDraft}
	require.NoError(t, svc.pinAttemptEvidence(f.ctx, lease, ledger, attempt, &checkpoint))
	draftData, err := json.Marshal(sourceWikiRecoveryDraft)
	require.NoError(t, err)
	require.NoError(t, svc.saveSourceWikiProgress(f.ctx, ledger, lease, attempt, &checkpoint, "qa", types.JSON(draftData), 0))
	return lease, evidence
}

func waitForSourceWikiAttempt(t *testing.T, ledger *repository.SourceWikiAttemptLedger, id string, status string) *types.SourceWikiAttempt {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		attempt, err := ledger.Get(context.Background(), id)
		if err == nil && attempt.Status == status {
			return attempt
		}
		time.Sleep(15 * time.Millisecond)
	}
	attempt, err := ledger.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equalf(t, status, attempt.Status, "attempt did not reach the expected state before the bounded test wait elapsed: reason=%q phase=%q epoch=%d calls=%d", attempt.Reason, attempt.Phase, attempt.Epoch, attempt.Calls)
	return attempt
}

func TestSourceWikiRecoveryStartFindsCrashBeforeAttemptIDWasReturned(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		providerCalls.Add(1)
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return sourceWikiRecoveryDraft
	})
	attempt, ledger := makeSourceWikiRecoveryAttempt(t, f, generator)
	require.Zero(t, attempt.Calls, "simulate process death after durable create but before the response exposes its ID")

	startTestSourceWikiRecovery(t, generator)
	recovered := waitForSourceWikiAttempt(t, ledger, attempt.ID, "ready")
	require.Equal(t, attempt.ID, recovered.ID, "startup recovery must resume the durable attempt ID")
	require.Equal(t, 2, recovered.Calls)
	require.EqualValues(t, 2, providerCalls.Load())
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Equal(t, "ready", page.SourceProvenance.State)
	var count int64
	require.NoError(t, f.db.Model(&types.SourceWikiAttempt{}).Where("knowledge_base_id = ? AND module_path = ?", f.kb.ID, "src").Count(&count).Error)
	require.EqualValues(t, 1, count, "recovery must not allocate a new manual retry slot")
}

func TestSourceWikiRecoveryStartWaitsForLiveLeaseThenResumesQACheckpoint(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var generateCalls, qaCalls atomic.Int32
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			qaCalls.Add(1)
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		generateCalls.Add(1)
		return sourceWikiRecoveryDraft
	})
	attempt, ledger := makeSourceWikiRecoveryAttempt(t, f, generator)
	oldLease, _ := persistSourceWikiRecoveryQACheckpoint(t, f, generator, attempt, ledger)

	startTestSourceWikiRecovery(t, generator)
	time.Sleep(120 * time.Millisecond)
	stillOwned, err := ledger.Get(f.ctx, attempt.ID)
	require.NoError(t, err)
	require.EqualValues(t, oldLease.Epoch, stillOwned.Epoch, "startup scanning must not steal a live lease")
	require.Equal(t, "running", stillOwned.Status)
	require.Zero(t, generateCalls.Load(), "no provider call occurs while the prior lease remains live")

	recovered := waitForSourceWikiAttempt(t, ledger, attempt.ID, "ready")
	require.Equal(t, attempt.ID, recovered.ID)
	require.Equal(t, oldLease.Epoch+1, recovered.Epoch)
	require.Equal(t, 1, recovered.Calls, "recovery resumes QA without repeating generation")
	require.Zero(t, generateCalls.Load())
	require.EqualValues(t, 1, qaCalls.Load())
	require.ErrorIs(t, ledger.Renew(f.ctx, oldLease, time.Now(), time.Second), repository.ErrSourceWikiAttemptFenced, "the reclaimed epoch fences the old worker")
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Equal(t, "ready", page.SourceProvenance.State)
}

func TestSourceWikiRecoveryStartExpiresUnreturnedCrashAndReleasesOwner(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		providerCalls.Add(1)
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return sourceWikiRecoveryDraft
	})
	attempt, ledger := makeSourceWikiRecoveryAttempt(t, f, generator)
	_, evidence := persistSourceWikiRecoveryQACheckpoint(t, f, generator, attempt, ledger)
	past := time.Now().UTC().Add(-10 * time.Minute)
	require.NoError(t, f.db.Model(&types.SourceWikiAttempt{}).Where("id=?", attempt.ID).Updates(map[string]any{
		"created_at": past, "deadline_at": past.Add(3 * time.Minute), "updated_at": past,
	}).Error)
	startTestSourceWikiRecovery(t, generator)
	expired := waitForSourceWikiAttempt(t, ledger, attempt.ID, "failed")
	require.Equal(t, "attempt absolute time budget exhausted", expired.Reason)
	require.True(t, expired.DeadlineAt.Before(time.Now()))
	require.NotEmpty(t, expired.Draft, "deadline cleanup keeps the saved draft")
	require.Equal(t, 0, expired.Calls)
	require.Zero(t, providerCalls.Load(), "expired work is never dispatched")
	var ownerCount int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=?", attempt.ID).Count(&ownerCount).Error)
	require.Zero(t, ownerCount, "expired status and exact evidence-owner release commit together")
	require.NotEmpty(t, evidence)

	fresh, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err, "an expired crashed attempt must not hold the unique module slot")
	require.NotEqual(t, attempt.ID, fresh.ID, "a manual retry is a new attempt only after expiry terminalization")
	require.Equal(t, "ready", fresh.Status)
}

func TestSourceWikiRecoveryStartStopsAfterPublishedSourceIsCleared(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		providerCalls.Add(1)
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return sourceWikiRecoveryDraft
	})
	attempt, ledger := makeSourceWikiRecoveryAttempt(t, f, generator)
	_, _ = persistSourceWikiRecoveryQACheckpoint(t, f, generator, attempt, ledger)
	require.NoError(t, f.db.Exec("UPDATE source_wiki_attempts SET lease_expires_at=now()-interval '1 second' WHERE id=?", attempt.ID).Error)
	require.NoError(t, f.db.Exec("DELETE FROM source_publications WHERE data_source_id=?", f.ds.ID).Error)
	startTestSourceWikiRecovery(t, generator)
	failed := waitForSourceWikiAttempt(t, ledger, attempt.ID, "failed")
	require.Contains(t, failed.Reason, "publication")
	require.Zero(t, providerCalls.Load(), "cleared source publication cannot authorize model dispatch")
	var ownerCount int64
	require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=?", attempt.ID).Count(&ownerCount).Error)
	require.Zero(t, ownerCount)
}

func TestSourceWikiRecoveryStartRejectsCrossTenantAttemptBinding(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		providerCalls.Add(1)
		return sourceWikiRecoveryDraft
	})
	attempt, ledger := makeSourceWikiRecoveryAttempt(t, f, generator)
	require.NoError(t, f.db.Model(&types.SourceWikiAttempt{}).Where("id=?", attempt.ID).Update("tenant_id", f.kb.TenantID+1).Error)
	startTestSourceWikiRecovery(t, generator)
	time.Sleep(100 * time.Millisecond)
	isolated, err := ledger.Get(f.ctx, attempt.ID)
	require.NoError(t, err)
	require.Equal(t, "failed", isolated.Status, "an unleased attempt with invalid tenant binding must be isolated instead of repeatedly occupying a scan batch")
	require.Contains(t, isolated.Reason, "KB/tenant binding")
	require.Zero(t, isolated.Epoch, "an unbound cross-tenant row is rejected before claim")
	require.Zero(t, providerCalls.Load())
}

func TestSourceWikiRecoveryStartUnboundExpiredOwnersCannotStarveDueRecovery(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	var providerCalls atomic.Int32
	_, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		providerCalls.Add(1)
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return sourceWikiRecoveryDraft
	})
	svc := generator.(*sourceWikiService)
	ledger := repository.NewSourceWikiAttemptLedger(f.db)
	var staleIDs []string
	for i := 0; i < sourceWikiRecoveryBatch; i++ {
		modulePath := fmt.Sprintf("orphan-%d", i)
		attempt, err := svc.loadOrCreateSourceWikiAttempt(f.ctx, ledger, f.kb, types.SourceWikiGenerateRequest{
			KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: modulePath, Title: "Scheduling module",
		}, modulePath)
		require.NoError(t, err)
		persistSourceWikiRecoveryQACheckpoint(t, f, generator, attempt, ledger)
		past := time.Now().UTC().Add(-10 * time.Minute)
		require.NoError(t, f.db.Model(&types.SourceWikiAttempt{}).Where("id = ?", attempt.ID).Updates(map[string]any{
			"tenant_id": f.kb.TenantID + 1, "created_at": past, "deadline_at": past.Add(3 * time.Minute),
			"lease_expires_at": past, "updated_at": past,
		}).Error)
		staleIDs = append(staleIDs, attempt.ID)
	}
	valid, err := svc.loadOrCreateSourceWikiAttempt(f.ctx, ledger, f.kb, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	}, "src")
	require.NoError(t, err)

	startTestSourceWikiRecovery(t, generator)
	recovered := waitForSourceWikiAttempt(t, ledger, valid.ID, "ready")
	require.Equal(t, valid.ID, recovered.ID, "unrecoverable front-of-queue rows must not starve a valid due attempt")
	require.EqualValues(t, 2, providerCalls.Load(), "only the valid due attempt may dispatch provider calls")
	for _, id := range staleIDs {
		attempt, err := ledger.Get(f.ctx, id)
		require.NoError(t, err)
		require.Equal(t, "failed", attempt.Status, "expired unbound attempt must be terminalized")
		require.NotEmpty(t, attempt.Draft, "terminalization preserves the in-progress draft")
		require.Zero(t, attempt.Calls, "terminalization preserves call counts")
		require.Zero(t, attempt.Tokens, "terminalization preserves token counts")
		var owners int64
		require.NoError(t, f.db.Table("source_wiki_attempt_evidence_refs").Where("attempt_id = ?", id).Count(&owners).Error)
		require.Zero(t, owners, "expired unbound attempt must release its exact evidence owner")
	}
}
