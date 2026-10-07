package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5"
)

func TestDefaultModeIsStatOnlyWithoutValidDSN(t *testing.T) {
	t.Setenv(dsnEnvName, "not-a-dsn")
	var stdout, stderr bytes.Buffer
	if code := run(nil, &stdout, &stderr); code != 0 {
		t.Fatalf("stat-only mode failed: code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"ready_no_environment_or_database_access"`) {
		t.Fatalf("unexpected stat-only result: %s", stdout.String())
	}
}

func TestFixedSourceScopeUsesPersistedUUIDs(t *testing.T) {
	if err := validateApprovedSourceScope(); err != nil {
		t.Fatalf("fixed source scope must fit the persisted varchar(36) contract: %v", err)
	}
	if got, want := approvedSourceIDs, [3]string{
		"ba6d2417-222e-4ff9-be22-eccf28659b32",
		"6168f382-c25d-4914-95c3-9bbecca7f18d",
		"ab31aae4-b21f-4dc4-90d6-d7e506c29c66",
	}; got != want {
		t.Fatalf("fixed source IDs mismatch: got %q want %q", got, want)
	}
}

func TestRunModesReachInjectedConnectorWithoutLegacyPortPreflight(t *testing.T) {
	t.Setenv(dsnEnvName, "test-only-not-a-dsn")
	cases := []struct {
		name string
		args []string
	}{
		{name: "upgrade", args: []string{"--upgrade-only", "--stop-proof=" + requiredStopProof}},
		{name: "backfill", args: []string{"--backfill", "--confirm-app-stopped", "--stop-proof=" + requiredStopProof}},
		{name: "read-only measurement", args: []string{"--measure", "--confirm-app-stopped"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connectCalls := 0
			var stdout, stderr bytes.Buffer
			code := runWithConnector(tc.args, &stdout, &stderr, func(context.Context, string) (*pgx.Conn, error) {
				connectCalls++
				return nil, errors.New("injected stop before any network connection")
			})
			if code != 2 || connectCalls != 1 || !strings.Contains(stderr.String(), `"guarded_clone_connection_rejected"`) {
				t.Fatalf("mode did not reach the injected connector without port preflight: code=%d calls=%d stderr=%q", code, connectCalls, stderr.String())
			}
		})
	}
}

func TestBackfillDeadlineReservesCleanupInsideTotalWallClockBudget(t *testing.T) {
	started := time.Date(2026, 10, 7, 1, 2, 3, 0, time.UTC)
	timing := newBackfillDeadline(started, maxRunDuration, backfillCleanupReserve)
	if want := started.Add(maxRunDuration); !timing.totalDeadline.Equal(want) {
		t.Fatalf("total deadline=%s, want %s", timing.totalDeadline, want)
	}
	if want := timing.totalDeadline.Add(-backfillCleanupReserve); !timing.workDeadline.Equal(want) {
		t.Fatalf("work deadline=%s, want %s", timing.workDeadline, want)
	}
	ctx, cancel := contextUntil(timing.totalDeadline)
	defer cancel()
	if got, ok := ctx.Deadline(); !ok || !got.Equal(timing.totalDeadline) {
		t.Fatalf("cleanup context deadline=(%s,%v), want %s", got, ok, timing.totalDeadline)
	}
}

func TestBatchOutcomeUnknownStopsWithoutCountingRows(t *testing.T) {
	cases := []struct {
		name   string
		input  backfillBatchOutcome
		status string
	}{
		{name: "rollback failed", input: backfillBatchOutcome{operationErr: errors.New("update failed"), rollbackAttempted: true, rollbackErr: errors.New("rollback failed")}, status: "abort_outcome_unknown"},
		{name: "already closed transaction", input: backfillBatchOutcome{operationErr: errors.New("update failed"), rollbackAttempted: true, rollbackErr: pgx.ErrTxClosed}, status: "abort_outcome_unknown"},
		{name: "commit unknown", input: backfillBatchOutcome{commitAttempted: true, commitErr: errCommitOutcomeUnknown, parsedRows: 64}, status: "commit_outcome_unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result := output{Status: "running", RowsUpdated: 12, ParsedRowsUpdated: 8, VectorRowsUpdated: 4, ResumeAllowed: true}
			calls := 0
			for calls < 3 {
				calls++
				disposition, _ := applyBackfillBatchOutcome(&result, tc.input)
				if disposition != batchContinue {
					break
				}
			}
			if calls != 1 || result.RowsUpdated != 12 || result.ParsedRowsUpdated != 8 || result.VectorRowsUpdated != 4 {
				t.Fatalf("uncertain batch continued or counted rows: calls=%d result=%+v", calls, result)
			}
			if result.Status != tc.status || !result.ResumeAllowed || result.RemainingNullRows != nil {
				t.Fatalf("uncertain batch status=%+v, want status %q and resumable", result, tc.status)
			}
		})
	}
}

func TestBackfillBatchAndTotalLimitsRemainResumableUntilNullCountIsZero(t *testing.T) {
	for _, tc := range []struct{ rows, want int64 }{{0, 64}, {9999, 1}, {10000, 0}, {10001, 0}} {
		if got := backfillBatchLimit(tc.rows); got != tc.want {
			t.Errorf("batch limit at %d rows=%d, want %d", tc.rows, got, tc.want)
		}
	}
	partial := output{Status: "partial_row_limit", ResumeAllowed: true}
	applyBackfillRemaining(&partial, 7, "partial_row_limit")
	if partial.Status == "complete" || !partial.ResumeAllowed || partial.RemainingNullRows == nil || *partial.RemainingNullRows != 7 {
		t.Fatalf("nonzero remaining rows were not left partial/resumable: %+v", partial)
	}
	applyBackfillRemaining(&partial, 0, "partial_row_limit")
	if partial.Status != "complete" || partial.ResumeAllowed || partial.RemainingNullRows == nil || *partial.RemainingNullRows != 0 {
		t.Fatalf("zero remaining rows did not finalize: %+v", partial)
	}
}

func TestGuardConnectionConfigPinsOnlyApprovedClone(t *testing.T) {
	config, err := guardConnectionConfig("postgres://tester:secret@127.0.0.1:57822/source_test?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != adminHost || config.Port != adminPort || config.Database != cloneDatabase {
		t.Fatalf("unexpected pinned endpoint: host=%q port=%d database=%q", config.Host, config.Port, config.Database)
	}
	if config.TLSConfig != nil || len(config.Fallbacks) != 0 || len(config.RuntimeParams) != 1 || config.RuntimeParams["search_path"] != cloneSchema {
		t.Fatalf("unexpected transport/session policy: tls=%v fallbacks=%d runtime_params=%v", config.TLSConfig != nil, len(config.Fallbacks), config.RuntimeParams)
	}
}

func TestGuardConnectionConfigRejectsEndpointAndOptionDrift(t *testing.T) {
	invalid := []string{
		"postgres://tester:secret@localhost:57822/source_test?sslmode=disable",
		"postgres://tester:secret@127.0.0.1:57822/other?sslmode=disable",
		"postgres://tester:secret@127.0.0.1:57822/source_test?sslmode=require",
		"postgres://tester:secret@127.0.0.1:57822/source_test?sslmode=disable&application_name=other",
		"postgres://tester@127.0.0.1:57822/source_test?sslmode=disable",
	}
	for _, dsn := range invalid {
		t.Run(strings.ReplaceAll(dsn, "/", "_"), func(t *testing.T) {
			if _, err := guardConnectionConfig(dsn); err == nil {
				t.Fatal("guard accepted a DSN outside the fixed local endpoint")
			}
		})
	}
}

func TestMigrationAdvisoryKeyMatchesMigrateURLPath(t *testing.T) {
	got, err := database.GenerateAdvisoryLockId("/"+cloneDatabase, cloneSchema, "schema_migrations")
	if err != nil {
		t.Fatal(err)
	}
	if got != "902578274" {
		t.Fatalf("migration advisory key mismatch: got %s want 902578274", got)
	}
}
