//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func newSourceWikiAttemptLedgerPostgres(t *testing.T) (*gorm.DB, *SourceWikiAttemptLedger) {
	t.Helper()
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SOURCE_TEST_POSTGRES_DSN is not configured")
	}
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse isolated PostgreSQL test DSN: %v", err)
	}
	if address.Path != "/source_test" || address.Hostname() != "127.0.0.1" {
		t.Fatal("refusing to run attempt-ledger integration test outside the isolated local source_test database")
	}
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL test database: %v", err)
	}
	schema := "source_wiki_attempt_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated attempt-ledger schema: %v", err)
	}
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		t.Fatalf("open isolated attempt-ledger schema: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			_ = sqlDB.Close()
		}
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, err := admin.DB(); err == nil {
			_ = sqlDB.Close()
		}
	})
	baseSchema := `CREATE TABLE source_wiki_attempts (
		id VARCHAR(36) PRIMARY KEY,
		tenant_id BIGINT NOT NULL,
		knowledge_base_id VARCHAR(36) NOT NULL,
		source_id VARCHAR(36) NOT NULL,
		snapshot_id VARCHAR(36) NOT NULL DEFAULT '',
		module_path TEXT NOT NULL,
		title TEXT NOT NULL,
		slug TEXT NOT NULL,
		status TEXT NOT NULL,
		reason TEXT NOT NULL DEFAULT '',
		evidence_knowledge_ids JSONB,
		draft JSONB,
		calls INTEGER NOT NULL DEFAULT 0,
		tokens INTEGER NOT NULL DEFAULT 0,
		repairs INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL
	);
	CREATE UNIQUE INDEX source_wiki_one_running_module
		ON source_wiki_attempts(tenant_id,knowledge_base_id,source_id,module_path) WHERE status='running';`
	if err := db.Exec(baseSchema).Error; err != nil {
		t.Fatalf("create source Wiki attempt baseline schema: %v", err)
	}
	migrationPath := filepath.Join("..", "..", "..", "migrations", "versioned", "000113_source_wiki_attempt_ledger.up.sql")
	migration, err := os.ReadFile(migrationPath)
	if err != nil {
		t.Fatalf("read attempt-ledger migration 113: %v", err)
	}
	if err := db.Exec(string(migration)).Error; err != nil {
		t.Fatalf("apply attempt-ledger migration 113: %v", err)
	}
	return db, NewSourceWikiAttemptLedger(db)
}

func newLedgerAttempt(id string, now time.Time) *types.SourceWikiAttempt {
	return &types.SourceWikiAttempt{
		ID: id, TenantID: 7, KnowledgeBaseID: "kb-one", SourceID: "source-one", SnapshotID: "snapshot-one",
		ModulePath: "src/module", Title: "Module", Slug: "concept/source-one/module-one", Status: "running",
		SourceConfigFingerprint: "source-config-a", SourceUpdatedAt: now, ModelID: "model-one",
		ModelSettingsFingerprint: "model-settings-a", ModelContextWindow: 8192, MaxCompletionTokens: 4096, BasePageVersion: 4,
		MaxCalls: 18, MaxTokens: 360000, MaxElapsedMS: 180000, MaxRepairs: 2,
		DeadlineAt: now.Add(3 * time.Minute), CreatedAt: now, UpdatedAt: now,
	}
}

func TestAttemptLedgerReservesBeforeDispatchAndFencesRecoveredWorker(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	first, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker-a", Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	reservation, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: first, Phase: "generate", ReservedTokens: 700, Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("reserve first provider call: %v", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read persisted attempt before provider dispatch: %v", err)
	}
	if stored.Calls != 1 || stored.Tokens != 700 {
		t.Fatalf("provider dispatch reservation is not durable before call: calls=%d tokens=%d", stored.Calls, stored.Tokens)
	}

	second, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker-b", Now: now.Add(31 * time.Second), LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("recover expired attempt lease: %v", err)
	}
	if second.Epoch <= first.Epoch {
		t.Fatalf("recovery epoch did not advance: first=%d second=%d", first.Epoch, second.Epoch)
	}
	if err := ledger.Renew(ctx, first, now.Add(31*time.Second), 30*time.Second); !errors.Is(err, ErrSourceWikiAttemptFenced) {
		t.Fatalf("old worker heartbeat error = %v, want fenced", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{
		Lease: first, ReservationID: reservation.ID, Outcome: "succeeded", ActualTokens: intPointer(900),
		Now: now.Add(32 * time.Second),
	}); !errors.Is(err, ErrSourceWikiAttemptFenced) {
		t.Fatalf("old worker completion error = %v, want fenced", err)
	}
	if _, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: first, Phase: "late-retry", ReservedTokens: 10, Now: now.Add(32 * time.Second), LeaseFor: time.Minute}); !errors.Is(err, ErrSourceWikiAttemptFenced) {
		t.Fatalf("old worker reservation error = %v, want fenced", err)
	}
	if err := ledger.SaveProgress(ctx, first, types.SourceWikiAttemptProgress{Phase: "late-write", Repairs: 0, Now: now.Add(32 * time.Second)}); !errors.Is(err, ErrSourceWikiAttemptFenced) {
		t.Fatalf("old worker checkpoint error = %v, want fenced", err)
	}
	if err := ledger.Finish(ctx, first, "failed", "stale failure", now.Add(32*time.Second)); !errors.Is(err, ErrSourceWikiAttemptFenced) {
		t.Fatalf("old worker finalization error = %v, want fenced", err)
	}
	stored, err = ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read recovered attempt: %v", err)
	}
	if stored.Calls != 1 || stored.Tokens != 700 || stored.Epoch != second.Epoch {
		t.Fatalf("recovery reset or stale worker changed ledger: calls=%d tokens=%d epoch=%d", stored.Calls, stored.Tokens, stored.Epoch)
	}
}

func intPointer(value int) *int { return &value }

func jsonMatches(got types.JSON, want string) bool {
	var gotValue, wantValue any
	return json.Unmarshal(got, &gotValue) == nil && json.Unmarshal([]byte(want), &wantValue) == nil && reflect.DeepEqual(gotValue, wantValue)
}

func TestAttemptLedgerConcurrentReservationsStopAtPersistedCallCap(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 11, 0, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	attempt.MaxCalls = 10
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	const dispatches = 20
	results := make(chan error, dispatches)
	for i := 0; i < dispatches; i++ {
		go func() {
			_, reserveErr := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "nested", ReservedTokens: 5, Now: now, LeaseFor: time.Minute})
			results <- reserveErr
		}()
	}
	reserved, capped := 0, 0
	for i := 0; i < dispatches; i++ {
		err := <-results
		switch {
		case err == nil:
			reserved++
		case errors.Is(err, ErrSourceWikiAttemptBudgetExhausted):
			capped++
		default:
			t.Fatalf("unexpected concurrent reservation error: %v", err)
		}
	}
	if reserved != 10 || capped != 10 {
		t.Fatalf("successful reservations=%d capped=%d, want 10 each", reserved, capped)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read attempt counters: %v", err)
	}
	if stored.Calls != 10 || stored.Tokens != 50 {
		t.Fatalf("atomic reservation counters calls=%d tokens=%d, want 10 and 50", stored.Calls, stored.Tokens)
	}
}

func TestAttemptLedgerDeadlineFinalizesOnHeartbeatAndPreservesCheckpoint(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker", Now: now, LeaseFor: 3 * time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	reservation, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "generate", ReservedTokens: 30, Now: now, LeaseFor: 3 * time.Minute})
	if err != nil {
		t.Fatalf("reserve provider call: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{
		Lease: lease, ReservationID: reservation.ID, Outcome: "succeeded", ActualTokens: intPointer(20),
		Checkpoint: types.JSON(`{"stage":"generated"}`), Draft: types.JSON(`{"title":"draft"}`), NextPhase: "qa", Now: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("persist draft checkpoint: %v", err)
	}
	if err := ledger.Renew(ctx, lease, attempt.DeadlineAt, time.Minute); !errors.Is(err, ErrSourceWikiAttemptDeadline) {
		t.Fatalf("heartbeat at absolute deadline error=%v, want deadline", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read expired attempt: %v", err)
	}
	if stored.Status != "failed" || stored.Reason == "" || !jsonMatches(stored.Draft, `{"title":"draft"}`) ||
		!jsonMatches(stored.Checkpoint, `{"stage":"generated"}`) || stored.Calls != 1 || stored.Tokens != 20 || stored.LeaseOwner != "" || stored.LeaseExpiresAt != nil {
		t.Fatalf("deadline terminal transition lost persisted state: status=%q reason=%q draft=%s checkpoint=%s calls=%d tokens=%d owner=%q lease=%v", stored.Status, stored.Reason, stored.Draft, stored.Checkpoint, stored.Calls, stored.Tokens, stored.LeaseOwner, stored.LeaseExpiresAt)
	}
}

func TestAttemptLedgerExpiredCrashAttemptFailsWithoutResettingDraftOrReservations(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "crashed-worker", Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	first, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "generate", ReservedTokens: 100, Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("reserve generation call: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{
		Lease: lease, ReservationID: first.ID, Outcome: "succeeded", ActualTokens: intPointer(70),
		Checkpoint: types.JSON(`{"generation":"complete"}`), Draft: types.JSON(`{"title":"saved before restart"}`),
		NextPhase: "qa", Now: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("persist completed generation phase: %v", err)
	}
	if _, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "qa", ReservedTokens: 80, Now: now.Add(2 * time.Second), LeaseFor: 30 * time.Second}); err != nil {
		t.Fatalf("reserve QA call that was in flight at crash: %v", err)
	}
	_, err = ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "restart-recovery", Now: attempt.DeadlineAt, LeaseFor: time.Minute})
	if !errors.Is(err, ErrSourceWikiAttemptDeadline) {
		t.Fatalf("claim after absolute deadline error=%v, want deadline", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read expired attempt: %v", err)
	}
	if stored.Status != "failed" || stored.Reason == "" || stored.Phase != "qa" || stored.Calls != 2 || stored.Tokens != 150 ||
		!jsonMatches(stored.Draft, `{"title":"saved before restart"}`) || !jsonMatches(stored.Checkpoint, `{"generation":"complete"}`) {
		t.Fatalf("expired crash attempt reset durable state: status=%q reason=%q phase=%q calls=%d tokens=%d draft=%s checkpoint=%s", stored.Status, stored.Reason, stored.Phase, stored.Calls, stored.Tokens, stored.Draft, stored.Checkpoint)
	}
}

func TestAttemptLedgerSettlesKnownUsageAndNeverRefundsProviderErrors(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 13, 0, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	attempt.MaxTokens = 250
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	known, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "generate", ReservedTokens: 100, Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("reserve known-usage call: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{Lease: lease, ReservationID: known.ID, Outcome: "succeeded", ActualTokens: intPointer(60), Now: now}); err != nil {
		t.Fatalf("settle known provider usage: %v", err)
	}
	failed, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "qa", ReservedTokens: 100, Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("reserve retryable provider call: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{Lease: lease, ReservationID: failed.ID, Outcome: "provider_error", ActualTokens: intPointer(30), Now: now}); err != nil {
		t.Fatalf("record provider error with unknown final charge: %v", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read accumulated counters: %v", err)
	}
	if stored.Calls != 2 || stored.Tokens != 160 {
		t.Fatalf("known success and provider error charged calls=%d tokens=%d, want 2 and 160", stored.Calls, stored.Tokens)
	}
	if err := ledger.SaveProgress(ctx, lease, types.SourceWikiAttemptProgress{Phase: "repair", Draft: types.JSON(`{"title":"last valid draft"}`), Now: now}); err != nil {
		t.Fatalf("save the last valid draft before budget exhaustion: %v", err)
	}
	last, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "repair", ReservedTokens: 60, Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("reserve call whose actual usage may exceed cap: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{Lease: lease, ReservationID: last.ID, Outcome: "succeeded", ActualTokens: intPointer(100), Now: now}); !errors.Is(err, ErrSourceWikiAttemptBudgetExhausted) {
		t.Fatalf("actual usage over cumulative cap error=%v, want exhaustion", err)
	}
	stored, err = ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read over-budget usage: %v", err)
	}
	if stored.Tokens != 260 || !jsonMatches(stored.Draft, `{"title":"last valid draft"}`) {
		t.Fatalf("provider overrun failed to persist charge or preserve draft: tokens=%d draft=%s", stored.Tokens, stored.Draft)
	}
	if err := ledger.Finish(ctx, lease, "failed", "provider token budget exhausted", now); err != nil {
		t.Fatalf("finish exhausted attempt: %v", err)
	}
	stored, err = ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read terminal budget failure: %v", err)
	}
	if stored.Status != "failed" || stored.Reason != "provider token budget exhausted" || !jsonMatches(stored.Draft, `{"title":"last valid draft"}`) {
		t.Fatalf("budget-exhausted reason/draft was lost: status=%q reason=%q draft=%s", stored.Status, stored.Reason, stored.Draft)
	}
}

func TestAttemptLedgerCheckpointSurvivesProcessRecoveryOnSameAttempt(t *testing.T) {
	db, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 14, 0, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	first, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker-before-restart", Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("claim attempt before simulated restart: %v", err)
	}
	reservation, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: first, Phase: "generate", ReservedTokens: 200, Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("reserve model call: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{Lease: first, ReservationID: reservation.ID, Outcome: "succeeded", ActualTokens: intPointer(120), Now: now}); err != nil {
		t.Fatalf("settle model call: %v", err)
	}
	if err := ledger.SaveProgress(ctx, first, types.SourceWikiAttemptProgress{
		Phase: "qa", Checkpoint: types.JSON(`{"generated":true}`), Draft: types.JSON(`{"title":"preserved draft"}`),
		Repairs: 1, Now: now,
	}); err != nil {
		t.Fatalf("save resumable phase and draft: %v", err)
	}

	// A fresh repository instance simulates a restarted process. It must reclaim
	// the same row after lease expiry, not create or budget a new attempt.
	restarted := NewSourceWikiAttemptLedger(db)
	second, err := restarted.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker-after-restart", Now: now.Add(31 * time.Second), LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("recover existing attempt after restart: %v", err)
	}
	stored, err := restarted.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read recovered attempt: %v", err)
	}
	if second.AttemptID != attempt.ID || second.Epoch <= first.Epoch || stored.ID != attempt.ID || stored.Phase != "qa" ||
		!jsonMatches(stored.Checkpoint, `{"generated":true}`) || !jsonMatches(stored.Draft, `{"title":"preserved draft"}`) ||
		stored.Repairs != 1 || stored.Calls != 1 || stored.Tokens != 120 {
		t.Fatalf("restart lost or reset attempt state: lease=%+v phase=%q checkpoint=%s draft=%s repairs=%d calls=%d tokens=%d", second, stored.Phase, stored.Checkpoint, stored.Draft, stored.Repairs, stored.Calls, stored.Tokens)
	}
}

func TestAttemptLedgerClaimCarriesPersistedModelPreflightAcrossRecovery(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 15, 0, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	attempt.ModelContextWindow = 8192
	attempt.MaxCompletionTokens = 2048
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt with fixed model preflight: %v", err)
	}
	first, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker-one", Now: now, LeaseFor: 30 * time.Second})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	second, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker-two", Now: now.Add(31 * time.Second), LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("recover attempt: %v", err)
	}
	if first.ModelContextWindow != 8192 || first.MaxCompletionTokens != 2048 ||
		second.ModelContextWindow != first.ModelContextWindow || second.MaxCompletionTokens != first.MaxCompletionTokens {
		t.Fatalf("recovery changed fixed per-call preflight: first=%+v second=%+v", first, second)
	}
}

func TestAttemptLedgerRejectsCallerChangesToFixedModelSettings(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 15, 15, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	attempt.ModelContextWindow = 8192
	attempt.MaxCompletionTokens = 2048
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	modified := lease
	modified.ModelContextWindow = 16384
	modified.MaxCompletionTokens = 4096
	modified.ModelID = "different-model"
	_, err = ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: modified, Phase: "qa", ReservedTokens: 300, Now: now, LeaseFor: time.Minute})
	if !errors.Is(err, ErrSourceWikiAttemptFenced) {
		t.Fatalf("caller-modified model settings reservation error=%v, want fenced", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read attempt after tampered reservation: %v", err)
	}
	if stored.Calls != 0 || stored.Tokens != 0 {
		t.Fatalf("tampered model settings consumed budget: calls=%d tokens=%d", stored.Calls, stored.Tokens)
	}
}

func TestManualAttemptRetryStartsANewFiniteBudget(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 15, 30, 0, 0, time.UTC)
	first := newLedgerAttempt(uuid.NewString(), now)
	if err := ledger.Create(ctx, first); err != nil {
		t.Fatalf("create first attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: first.ID, Owner: "worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim first attempt: %v", err)
	}
	reservation, err := ledger.ReserveCall(ctx, types.SourceWikiAttemptCallReservationRequest{Lease: lease, Phase: "generate", ReservedTokens: 80, Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("reserve first attempt call: %v", err)
	}
	if err := ledger.CompleteCall(ctx, types.SourceWikiAttemptCallCompletion{Lease: lease, ReservationID: reservation.ID, Outcome: "provider_error", Now: now}); err != nil {
		t.Fatalf("record failed call: %v", err)
	}
	if err := ledger.Finish(ctx, lease, "failed", "provider retry budget exhausted", now); err != nil {
		t.Fatalf("finish failed attempt: %v", err)
	}
	second := newLedgerAttempt(uuid.NewString(), now.Add(time.Second))
	if err := ledger.Create(ctx, second); err != nil {
		t.Fatalf("create explicit manual retry: %v", err)
	}
	stored, err := ledger.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("read manual retry: %v", err)
	}
	if second.ID == first.ID || stored.Calls != 0 || stored.Tokens != 0 || stored.Repairs != 0 ||
		!stored.DeadlineAt.Equal(second.CreatedAt.Add(3*time.Minute)) || stored.MaxCalls != 18 || stored.MaxTokens != 360000 || stored.MaxElapsedMS != 180000 || stored.MaxRepairs != 2 {
		t.Fatalf("manual retry did not receive a new finite budget: id=%q calls=%d tokens=%d repairs=%d limits=%d/%d/%d/%d deadline=%s", stored.ID, stored.Calls, stored.Tokens, stored.Repairs, stored.MaxCalls, stored.MaxTokens, stored.MaxElapsedMS, stored.MaxRepairs, stored.DeadlineAt)
	}
}

func TestAttemptLedgerRejectsMoreThanTwoRepairs(t *testing.T) {
	_, ledger := newSourceWikiAttemptLedgerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 15, 45, 0, 0, time.UTC)
	attempt := newLedgerAttempt(uuid.NewString(), now)
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "repair-worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	if err := ledger.SaveProgress(ctx, lease, types.SourceWikiAttemptProgress{Phase: "repair-2", Repairs: 2, Now: now}); err != nil {
		t.Fatalf("persist final allowed repair: %v", err)
	}
	if err := ledger.SaveProgress(ctx, lease, types.SourceWikiAttemptProgress{Phase: "repair-3", Repairs: 3, Now: now}); !errors.Is(err, ErrSourceWikiAttemptInvalidState) {
		t.Fatalf("third repair error=%v, want fixed repair limit", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read repair count: %v", err)
	}
	if stored.Repairs != 2 || stored.Phase != "repair-2" {
		t.Fatalf("rejected repair changed checkpoint: repairs=%d phase=%q", stored.Repairs, stored.Phase)
	}
}

func TestAttemptLedgerMigrationCanBeReappliedAndReverted(t *testing.T) {
	db, ledger := newSourceWikiAttemptLedgerPostgres(t)
	up, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000113_source_wiki_attempt_ledger.up.sql"))
	if err != nil {
		t.Fatalf("read migration 113 up: %v", err)
	}
	down, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "versioned", "000113_source_wiki_attempt_ledger.down.sql"))
	if err != nil {
		t.Fatalf("read migration 113 down: %v", err)
	}
	if err := db.Exec(string(up)).Error; err != nil {
		t.Fatalf("reapply migration 113: %v", err)
	}
	if err := db.Exec(string(down)).Error; err != nil {
		t.Fatalf("revert migration 113: %v", err)
	}
	if err := db.Exec(string(up)).Error; err != nil {
		t.Fatalf("reapply reverted migration 113: %v", err)
	}
	attempt := newLedgerAttempt(uuid.NewString(), time.Date(2026, 10, 1, 16, 0, 0, 0, time.UTC))
	if err := ledger.Create(context.Background(), attempt); err != nil {
		t.Fatalf("use ledger after migration reapply: %v", err)
	}
}
