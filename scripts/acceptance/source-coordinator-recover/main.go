//go:build windows

// t22-source-coordinator-recover is a narrowly scoped one-shot recovery helper
// for the isolated T22 rehearsal database. The first nsb publication attempt
// ended with the backend force-stopped mid-cycle, leaving the durable source
// coordinator holding a dead run: an expired lease, an exhausted retry budget
// and a non-terminal sync log, while the launcher preflight requires a quiet
// database before the acceptance backend may start. This helper reproduces
// the product recovery (SyncLogRepository.RecoverSourceTriggers finalizes the
// dead run as failed) and the product pause (SyncLogRepository.PauseSourceSync
// returns the source to its pre-window paused state) — the same code paths a
// real backend restart plus operator pause would run. It never enqueues work.
//
// Default mode only inspects state read-only and classifies the shape.
// --recover is the sole mutating mode and requires the exact known dead-run
// shape. The helper never prints credential values.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const (
	sourceDSNEnv = "SOURCE_TEST_POSTGRES_DSN"
	aclGateEnv   = "T22_RUNTIME_ACL_GATE"

	acceptedHost = "127.0.0.1"
	acceptedPort = uint16(57822)
	seedDatabase = "source_test"
	testDatabase = "source_t22_live_rehearsal_20261006"

	tenantID  = uint64(10000)
	sourceID  = "ba6d2417-222e-4ff9-be22-eccf28659b32"
	sourceRef = "nsb"

	// Mirrors sourceRunMaxRetries in internal/application/repository/source_sync.go.
	sourceRunMaxRetries = 5

	transactionTimeout = 30 * time.Second
)

type safeResult struct {
	Stage       string `json:"stage"`
	Status      string `json:"status"`
	Shape       string `json:"shape,omitempty"`
	LogStatus   string `json:"sync_log_status,omitempty"`
	SourceState string `json:"source_status,omitempty"`
	Note        string `json:"note,omitempty"`
}

// coordinatorSnapshot is a read-only projection of the durable source
// coordinator for the approved nsb source.
type coordinatorSnapshot struct {
	SourceStatus     string
	ActiveSyncLogID  string
	PendingSyncLogID string
	LeaseOwner       string
	LeaseExpiresAt   *time.Time
	LogStatus        string
	LogFinishedAt    *time.Time
	LogErrorMessage  string
	RunPhase         string
	RunRetryCount    int
}

// deadRunRecoverable reports whether the snapshot matches the one known
// dead-run shape this helper may finalize: the failed publication left the
// source in error status holding an active run whose lease expired, whose
// retry budget is exhausted, and whose worker already recorded terminal
// progress (a finished timestamp on a still-running log) with no pending
// trigger. Anything else is rejected — the helper never guesses. Lease
// expiry semantics mirror the product coordinator: a lease not strictly
// after now is expired.
func deadRunRecoverable(s coordinatorSnapshot, now time.Time) bool {
	return s.SourceStatus == types.DataSourceStatusError &&
		s.ActiveSyncLogID != "" &&
		s.PendingSyncLogID == "" &&
		s.LeaseOwner != "" &&
		s.LeaseExpiresAt != nil && !s.LeaseExpiresAt.After(now) &&
		s.LogStatus == types.SyncLogStatusRunning &&
		s.LogFinishedAt != nil &&
		s.RunRetryCount >= sourceRunMaxRetries
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		result, code := inspect()
		writeResult(result)
		return code
	}
	if len(args) != 1 || args[0] != "--recover" {
		writeResult(safeResult{Stage: "arguments", Status: "rejected"})
		return 2
	}

	log.SetOutput(io.Discard)
	logger.SetOutput(io.Discard)
	result, code := recoverCoordinator()
	writeResult(result)
	return code
}

func writeResult(result safeResult) {
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func reject(stage, note string) (safeResult, int) {
	return safeResult{Stage: stage, Status: "rejected", Note: note}, 2
}

func fail(stage, note string) (safeResult, int) {
	return safeResult{Stage: stage, Status: "failed", Note: note}, 1
}

func inspect() (safeResult, int) {
	if os.Getenv(aclGateEnv) != "verified" {
		return reject("root_acl_gate", "root ACL attestation required before any database access")
	}
	db, pgConfig, res, code := connect()
	if db == nil {
		return *res, code
	}
	defer closeDB(db)
	if res, code := verifyDatabase(db, pgConfig); res != nil {
		return *res, code
	}
	snapshot, res, code := readSnapshot(db)
	if res != nil {
		return *res, code
	}
	shape := "not_dead_run"
	if deadRunRecoverable(*snapshot, time.Now().UTC()) {
		shape = "dead_run_recoverable"
	}
	return safeResult{
		Stage:       "inspect",
		Status:      "inspected",
		Shape:       shape,
		LogStatus:   snapshot.LogStatus,
		SourceState: snapshot.SourceStatus,
		Note:        "read-only inspection; no mutation performed",
	}, 0
}

func recoverCoordinator() (safeResult, int) {
	if os.Getenv(aclGateEnv) != "verified" {
		return reject("root_acl_gate", "root ACL attestation required before any database access")
	}
	db, pgConfig, res, code := connect()
	if db == nil {
		return *res, code
	}
	defer closeDB(db)
	if res, code := verifyDatabase(db, pgConfig); res != nil {
		return *res, code
	}
	control, ok := apprepo.NewSyncLogRepository(db).(interfaces.SourceSyncControlRepository)
	if !ok {
		return fail("coordinator_adapter", "sync log repository does not expose the control boundary")
	}
	var ds types.DataSource
	if err := db.Where("id = ? AND tenant_id = ?", sourceID, tenantID).Take(&ds).Error; err != nil {
		return fail("data_source", "approved "+sourceRef+" data source missing")
	}
	if ds.DeletedAt.Valid {
		return reject("data_source", "approved "+sourceRef+" data source is deleted")
	}
	snapshot, res, code := readSnapshot(db)
	if res != nil {
		return *res, code
	}
	if !deadRunRecoverable(*snapshot, time.Now().UTC()) {
		return reject("shape", "coordinator state does not match the known dead-run incident; inspect manually")
	}
	deadLogID := snapshot.ActiveSyncLogID

	ctx, cancel := context.WithTimeout(context.Background(), transactionTimeout)
	defer cancel()
	dispatches, err := control.RecoverSourceTriggers(ctx, &ds)
	if err != nil {
		return fail("product_recovery", fmt.Sprintf("RecoverSourceTriggers failed: %v", err))
	}
	if len(dispatches) != 0 {
		return fail("product_recovery", "recovery produced dispatches; this helper never enqueues work")
	}
	if err := control.PauseSourceSync(ctx, &ds); err != nil {
		return fail("product_pause", fmt.Sprintf("PauseSourceSync failed: %v", err))
	}

	// Post-verification is read-only: the dead run must be terminal, the
	// coordinator quiet, and the source paused.
	var finalLogStatus string
	if err := db.Raw(`SELECT status FROM public.sync_logs WHERE id::text = ?`, deadLogID).
		Row().Scan(&finalLogStatus); err != nil || finalLogStatus != types.SyncLogStatusFailed {
		return fail("postcheck", fmt.Sprintf("dead run is not failed after recovery: %q", finalLogStatus))
	}
	after, res, code := readSnapshot(db)
	if res != nil {
		return *res, code
	}
	if after.SourceStatus != types.DataSourceStatusPaused ||
		after.ActiveSyncLogID != "" || after.PendingSyncLogID != "" ||
		after.LeaseOwner != "" || after.LeaseExpiresAt != nil {
		return fail("postcheck", fmt.Sprintf("unexpected post-recovery state: source=%s active=%q pending=%q lease_owner=%q",
			after.SourceStatus, after.ActiveSyncLogID, after.PendingSyncLogID, after.LeaseOwner))
	}
	var stray int64
	if err := db.Raw(`SELECT count(*) FROM public.sync_logs WHERE status IN ('queued','running')`).Scan(&stray).Error; err != nil || stray != 0 {
		return fail("postcheck", "queued or running sync logs remain after recovery")
	}
	return safeResult{
		Stage:       "recover",
		Status:      "recovered",
		LogStatus:   finalLogStatus,
		SourceState: after.SourceStatus,
		Note:        "product recovery finalized the dead run as failed; product pause returned the source to paused",
	}, 0
}

// connect opens the rehearsal database through the approved seed DSN.
func connect() (*gorm.DB, *pgx.ConnConfig, *safeResult, int) {
	pgConfig, err := parseSourceDSN(os.Getenv(sourceDSNEnv))
	if err != nil {
		result, code := reject("source_database_dsn", "DSN is not the approved isolated seed database")
		return nil, nil, &result, code
	}
	sqlDB := stdlib.OpenDB(*pgConfig)
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(0)
	if err := sqlDB.Ping(); err != nil {
		_ = sqlDB.Close()
		result, code := fail("database_connect", "rehearsal database is unreachable")
		return nil, nil, &result, code
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger:               gormlogger.Default.LogMode(gormlogger.Silent),
		DisableAutomaticPing: true,
	})
	if err != nil {
		_ = sqlDB.Close()
		result, code := fail("database_adapter", "gorm adapter failed")
		return nil, nil, &result, code
	}
	return db, pgConfig, nil, 0
}

func closeDB(db *gorm.DB) {
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
}

// verifyDatabase mirrors the launcher preflight identity checks: exact
// rehearsal database, migration ledger exactly 121 clean.
func verifyDatabase(db *gorm.DB, cfg *pgx.ConnConfig) (*safeResult, int) {
	var databaseName, currentUser string
	if err := db.Raw("SELECT current_database(), current_user").Row().Scan(&databaseName, &currentUser); err != nil ||
		databaseName != testDatabase || currentUser != cfg.User {
		result, code := reject("database_identity", "wrong connected database identity")
		return &result, code
	}
	rows, err := db.Raw("SELECT version, dirty FROM public.schema_migrations").Rows()
	if err != nil {
		result, code := fail("migration_ledger", "migration ledger unavailable")
		return &result, code
	}
	defer rows.Close()
	count := 0
	var version int64
	var dirty bool
	for rows.Next() {
		count++
		if count > 1 || rows.Scan(&version, &dirty) != nil {
			result, code := fail("migration_ledger", "migration ledger invalid")
			return &result, code
		}
	}
	if rows.Err() != nil || count != 1 || version != 121 || dirty {
		result, code := fail("migration_ledger", "migration ledger is not exactly 121 clean")
		return &result, code
	}
	return nil, 0
}

// readSnapshot loads the coordinator projection read-only. A missing state
// row yields an empty coordinator with the persisted source status.
func readSnapshot(db *gorm.DB) (*coordinatorSnapshot, *safeResult, int) {
	snapshot := &coordinatorSnapshot{}
	var active, pending, leaseOwner sql.NullString
	var leaseExpiry sql.NullTime
	err := db.Raw(`SELECT ds.status,
		st.active_sync_log_id::text, st.pending_sync_log_id::text,
		st.lease_owner, st.lease_expires_at
		FROM public.data_sources ds
		LEFT JOIN public.source_sync_states st
			ON st.data_source_id = ds.id AND st.tenant_id = ds.tenant_id
		WHERE ds.id::text = ? AND ds.tenant_id = ?`, sourceID, tenantID).
		Row().Scan(&snapshot.SourceStatus, &active, &pending, &leaseOwner, &leaseExpiry)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			result, code := fail("data_source", "approved "+sourceRef+" data source missing")
			return nil, &result, code
		}
		result, code := fail("coordinator_state", "coordinator projection read failed")
		return nil, &result, code
	}
	snapshot.ActiveSyncLogID = active.String
	snapshot.PendingSyncLogID = pending.String
	snapshot.LeaseOwner = leaseOwner.String
	if leaseExpiry.Valid {
		snapshot.LeaseExpiresAt = &leaseExpiry.Time
	}
	if snapshot.ActiveSyncLogID == "" {
		return snapshot, nil, 0
	}
	var finished sql.NullTime
	var errorMessage sql.NullString
	if err := db.Raw(`SELECT l.status, l.finished_at, coalesce(l.error_message,''), coalesce(r.phase,''), coalesce(r.retry_count,0)
		FROM public.sync_logs l LEFT JOIN public.source_sync_runs r ON r.sync_log_id = l.id
		WHERE l.id::text = ?`, snapshot.ActiveSyncLogID).
		Row().Scan(&snapshot.LogStatus, &finished, &errorMessage, &snapshot.RunPhase, &snapshot.RunRetryCount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			result, code := fail("active_run", "active sync log row missing")
			return nil, &result, code
		}
		result, code := fail("active_run", "active sync log read failed")
		return nil, &result, code
	}
	if finished.Valid {
		snapshot.LogFinishedAt = &finished.Time
	}
	snapshot.LogErrorMessage = errorMessage.String
	return snapshot, nil, 0
}

// parseSourceDSN accepts only the approved isolated seed DSN form and targets
// the fixed rehearsal database, mirroring source-fixture-window.
func parseSourceDSN(raw string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Opaque != "" || u.Host != fmt.Sprintf("%s:%d", acceptedHost, acceptedPort) ||
		u.Fragment != "" || u.RawFragment != "" || u.ForceQuery || u.User == nil {
		return nil, errors.New("invalid PostgreSQL URI")
	}
	username := u.User.Username()
	password, hasPassword := u.User.Password()
	if username == "" || !hasPassword || password == "" {
		return nil, errors.New("source DSN credentials are incomplete")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) > 1 {
		return nil, errors.New("source DSN query is not allowed")
	}
	if values, ok := query["sslmode"]; len(query) != 0 && (!ok || len(values) != 1 || values[0] == "") {
		return nil, errors.New("source DSN query is not allowed")
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil || cfg == nil || cfg.Host != acceptedHost || cfg.Port != acceptedPort ||
		cfg.Database != seedDatabase || cfg.User != username || cfg.Password != password || len(cfg.Fallbacks) != 0 {
		return nil, errors.New("parsed DSN is not the approved seed database")
	}
	cfg = cfg.Copy()
	cfg.Database = testDatabase
	cfg.RuntimeParams = map[string]string{"application_name": "t22-source-coordinator-recover"}
	return cfg, nil
}
