//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sourceWikiControlledChat struct {
	call func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error)
}

func (m sourceWikiControlledChat) Chat(ctx context.Context, messages []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
	return m.call(ctx, messages, options)
}

func newSourceWikiRunnerPostgres(t *testing.T) (*gorm.DB, *repository.SourceWikiAttemptLedger) {
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
		t.Fatal("refusing to run attempt-runner test outside the isolated local source_test database")
	}
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL test database: %v", err)
	}
	schema := "source_wiki_runner_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated attempt-runner schema: %v", err)
	}
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		t.Fatalf("open isolated attempt-runner schema: %v", err)
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
		ON source_wiki_attempts(tenant_id,knowledge_base_id,source_id,module_path) WHERE status='running';
	CREATE TABLE source_wiki_attempt_evidence_refs (attempt_id VARCHAR(36) NOT NULL);`
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
	batchMigrationPath := filepath.Join("..", "..", "..", "migrations", "versioned", "000114_source_wiki_batches.up.sql")
	batchMigration, err := os.ReadFile(batchMigrationPath)
	if err != nil {
		t.Fatalf("read source Wiki batch migration 114: %v", err)
	}
	if err := db.Exec(string(batchMigration)).Error; err != nil {
		t.Fatalf("apply source Wiki batch migration 114: %v", err)
	}
	return db, repository.NewSourceWikiAttemptLedger(db)
}

func newSourceWikiRunnerAttempt(now time.Time) *types.SourceWikiAttempt {
	return &types.SourceWikiAttempt{
		ID: uuid.NewString(), TenantID: 7, KnowledgeBaseID: "kb-runner", SourceID: "source-runner",
		SnapshotID: "snapshot-runner", ModulePath: "src", Title: "Runner card", Slug: "concept/runner",
		Status: "running", SourceConfigFingerprint: "config-a", SourceUpdatedAt: now, ModelID: "model-a",
		ModelSettingsFingerprint: "model-a-settings", ModelContextWindow: 8192, MaxCompletionTokens: 4096, MaxCalls: 18, MaxTokens: 360000,
		MaxElapsedMS: 180000, MaxRepairs: 2, DeadlineAt: now.Add(3 * time.Minute),
		CreatedAt: now, UpdatedAt: now,
	}
}

func TestSourceWikiRunnerPersistsReservationBeforeCallingControlledModel(t *testing.T) {
	_, ledger := newSourceWikiRunnerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 16, 0, 0, 0, time.UTC)
	attempt := newSourceWikiRunnerAttempt(now)
	attempt.MaxCompletionTokens = 40
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "model-worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	messages := []chat.Message{{Role: "system", Content: "check evidence"}, {Role: "user", Content: "bounded request"}}
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("encode fixture messages: %v", err)
	}
	wantReservation := len(encoded) + 40
	dispatches := 0
	model := sourceWikiControlledChat{call: func(_ context.Context, _ []chat.Message, options *chat.ChatOptions) (*types.ChatResponse, error) {
		dispatches++
		stored, readErr := ledger.Get(ctx, attempt.ID)
		if readErr != nil {
			t.Fatalf("read ledger at provider boundary: %v", readErr)
		}
		if stored.Calls != 1 || stored.Tokens != wantReservation {
			t.Fatalf("provider invoked before durable reservation: calls=%d tokens=%d want_tokens=%d", stored.Calls, stored.Tokens, wantReservation)
		}
		if options.CompletionBudget() != 40 {
			t.Fatalf("provider completion cap=%d, want 40", options.CompletionBudget())
		}
		return &types.ChatResponse{Content: "supported", Usage: types.TokenUsage{TotalTokens: 17}}, nil
	}}
	runner := sourceWikiAttemptCallRunner{
		ledger: ledger, lease: lease, model: model, modelID: attempt.ModelID, modelSettingsFingerprint: attempt.ModelSettingsFingerprint,
		leaseFor: time.Minute, now: func() time.Time { return now },
	}
	response, err := runner.Call(ctx, "qa", messages)
	if err != nil {
		t.Fatalf("run controlled provider call: %v", err)
	}
	if response == nil || response.Content != "supported" || dispatches != 1 {
		t.Fatalf("controlled response=%v dispatches=%d, want one supported response", response, dispatches)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read settled attempt: %v", err)
	}
	if stored.Calls != 1 || stored.Tokens != 17 {
		t.Fatalf("settled usage counters calls=%d tokens=%d, want 1 and actual 17", stored.Calls, stored.Tokens)
	}
}

func TestSourceWikiRunnerRateLimitRetriesConsumeSharedAttemptBudget(t *testing.T) {
	_, ledger := newSourceWikiRunnerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 16, 30, 0, 0, time.UTC)
	const completion = 32
	attempt := newSourceWikiRunnerAttempt(now)
	attempt.MaxCompletionTokens = completion
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "model-worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	messages := []chat.Message{{Role: "system", Content: "generate"}, {Role: "user", Content: "bounded request"}}
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("encode fixture messages: %v", err)
	}
	dispatches := 0
	runner := sourceWikiAttemptCallRunner{
		ledger: ledger, lease: lease, modelID: attempt.ModelID, modelSettingsFingerprint: attempt.ModelSettingsFingerprint,
		model: sourceWikiControlledChat{call: func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
			dispatches++
			return nil, errors.New("429 rate limited")
		}},
		leaseFor: time.Minute,
		now:      func() time.Time { return now },
	}
	_, err = runner.CallWithRetries(ctx, "generate", messages, 9)
	if !errors.Is(err, errSourceWikiProviderCallFailed) {
		t.Fatalf("rate-limit result error=%v, want sanitized provider failure", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read retry attempt: %v", err)
	}
	wantTokens := 3 * (len(encoded) + completion)
	if dispatches != 3 || stored.Calls != 3 || stored.Tokens != wantTokens {
		t.Fatalf("rate-limit retries dispatches=%d calls=%d tokens=%d, want 3 calls and %d reserved tokens", dispatches, stored.Calls, stored.Tokens, wantTokens)
	}
}

func TestSourceWikiRunnerDiscardsLateResultAfterLeaseIsLost(t *testing.T) {
	_, ledger := newSourceWikiRunnerPostgres(t)
	ctx := context.Background()
	base := time.Date(2026, 10, 1, 17, 0, 0, 0, time.UTC)
	attempt := newSourceWikiRunnerAttempt(base)
	attempt.MaxCompletionTokens = 40
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	const leaseFor = 300 * time.Millisecond
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "old-worker", Now: base, LeaseFor: leaseFor})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	messages := []chat.Message{{Role: "system", Content: "generate"}, {Role: "user", Content: "module"}}
	var offset atomic.Int64
	now := func() time.Time { return base.Add(time.Duration(offset.Load())) }
	started := make(chan struct{})
	model := sourceWikiControlledChat{call: func(ctx context.Context, _ []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
		close(started)
		<-ctx.Done()
		// Simulate a provider response arriving after the worker was fenced.
		return &types.ChatResponse{Content: "stale", Usage: types.TokenUsage{TotalTokens: 12000}}, nil
	}}
	runner := sourceWikiAttemptCallRunner{
		ledger: ledger, lease: lease, model: model, modelID: attempt.ModelID, modelSettingsFingerprint: attempt.ModelSettingsFingerprint,
		leaseFor: leaseFor, now: now,
	}
	result := make(chan error, 1)
	go func() {
		_, callErr := runner.Call(ctx, "generate", messages)
		result <- callErr
	}()
	<-started
	offset.Store(int64(time.Second))
	select {
	case callErr := <-result:
		if !errors.Is(callErr, repository.ErrSourceWikiAttemptFenced) {
			t.Fatalf("late model result error=%v, want fenced worker", callErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("lost lease did not cancel and fence the old provider call")
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read attempt after stale response: %v", err)
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("encode fixture messages: %v", err)
	}
	wantReserved := len(encoded) + 40
	if stored.Calls != 1 || stored.Tokens != wantReserved || stored.Epoch != lease.Epoch {
		t.Fatalf("stale provider result changed ledger: calls=%d tokens=%d epoch=%d, want 1/%d/%d", stored.Calls, stored.Tokens, stored.Epoch, wantReserved, lease.Epoch)
	}
	recovered, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "new-worker", Now: now(), LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt from newer worker: %v", err)
	}
	if recovered.Epoch <= lease.Epoch {
		t.Fatalf("recovery epoch=%d did not advance from %d", recovered.Epoch, lease.Epoch)
	}
}

func TestSourceWikiRunnerTimeoutLeavesProviderReservationCharged(t *testing.T) {
	_, ledger := newSourceWikiRunnerPostgres(t)
	base := time.Date(2026, 10, 1, 18, 0, 0, 0, time.UTC)
	attempt := newSourceWikiRunnerAttempt(base)
	attempt.MaxCompletionTokens = 32
	if err := ledger.Create(context.Background(), attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(context.Background(), types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "timeout-worker", Now: base, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	messages := []chat.Message{{Role: "system", Content: "generate"}, {Role: "user", Content: "module"}}
	encoded, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("encode fixture messages: %v", err)
	}
	model := sourceWikiControlledChat{call: func(ctx context.Context, _ []chat.Message, _ *chat.ChatOptions) (*types.ChatResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	runner := sourceWikiAttemptCallRunner{
		ledger: ledger, lease: lease, model: model, modelID: attempt.ModelID, modelSettingsFingerprint: attempt.ModelSettingsFingerprint,
		leaseFor: time.Minute,
		now:      func() time.Time { return base },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = runner.Call(ctx, "generate", messages)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("provider timeout error=%v, want deadline exceeded", err)
	}
	stored, err := ledger.Get(context.Background(), attempt.ID)
	if err != nil {
		t.Fatalf("read timed-out attempt: %v", err)
	}
	wantTokens := len(encoded) + attempt.MaxCompletionTokens
	if stored.Calls != 1 || stored.Tokens != wantTokens || stored.Status != "running" {
		t.Fatalf("timeout refunded or lost the provider reservation: calls=%d tokens=%d status=%q want_tokens=%d", stored.Calls, stored.Tokens, stored.Status, wantTokens)
	}
}

func TestSourceWikiRunnerUsesPersistedContextPreflightBeforeReservation(t *testing.T) {
	_, ledger := newSourceWikiRunnerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 18, 30, 0, 0, time.UTC)
	attempt := newSourceWikiRunnerAttempt(now)
	attempt.ModelContextWindow = 64
	attempt.MaxCompletionTokens = 32
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt with bounded model preflight: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "preflight-worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	dispatches := 0
	runner := sourceWikiAttemptCallRunner{
		ledger: ledger, lease: lease, modelID: attempt.ModelID, modelSettingsFingerprint: attempt.ModelSettingsFingerprint,
		model: sourceWikiControlledChat{call: func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
			dispatches++
			return &types.ChatResponse{Content: "must not run"}, nil
		}},
		leaseFor: time.Minute, now: func() time.Time { return now },
	}
	messages := []chat.Message{{Role: "system", Content: "generate"}, {Role: "user", Content: strings.Repeat("x", 100)}}
	_, err = runner.Call(ctx, "generate", messages)
	if !errors.Is(err, errSourceWikiAttemptCallContext) {
		t.Fatalf("oversized request preflight error=%v, want model context rejection", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read preflight attempt: %v", err)
	}
	if dispatches != 0 || stored.Calls != 0 || stored.Tokens != 0 {
		t.Fatalf("model call crossed preflight: dispatches=%d calls=%d tokens=%d", dispatches, stored.Calls, stored.Tokens)
	}
}

func TestSourceWikiRunnerRejectsModelSettingsChangedSinceAttemptClaim(t *testing.T) {
	_, ledger := newSourceWikiRunnerPostgres(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 1, 19, 0, 0, 0, time.UTC)
	attempt := newSourceWikiRunnerAttempt(now)
	if err := ledger.Create(ctx, attempt); err != nil {
		t.Fatalf("create attempt: %v", err)
	}
	lease, err := ledger.Claim(ctx, types.SourceWikiAttemptClaimRequest{AttemptID: attempt.ID, Owner: "model-worker", Now: now, LeaseFor: time.Minute})
	if err != nil {
		t.Fatalf("claim attempt: %v", err)
	}
	dispatches := 0
	runner := sourceWikiAttemptCallRunner{
		ledger: ledger, lease: lease, modelID: "model-b", modelSettingsFingerprint: "model-b-settings",
		model: sourceWikiControlledChat{call: func(context.Context, []chat.Message, *chat.ChatOptions) (*types.ChatResponse, error) {
			dispatches++
			return &types.ChatResponse{Content: "wrong model"}, nil
		}},
		leaseFor: time.Minute, now: func() time.Time { return now },
	}
	_, err = runner.Call(ctx, "generate", []chat.Message{{Role: "user", Content: "request"}})
	if !errors.Is(err, errSourceWikiAttemptModelChanged) {
		t.Fatalf("changed model settings error=%v, want immutable-target rejection", err)
	}
	stored, err := ledger.Get(ctx, attempt.ID)
	if err != nil {
		t.Fatalf("read attempt after model mismatch: %v", err)
	}
	if dispatches != 0 || stored.Calls != 0 || stored.Tokens != 0 {
		t.Fatalf("changed model reached provider or consumed budget: dispatches=%d calls=%d tokens=%d", dispatches, stored.Calls, stored.Tokens)
	}
}
