// Temporary, opt-in source artifact payload rehearsal helper.
// Default mode is static-only: no environment reads, DB connection, or writes.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang-migrate/migrate/v4/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	dsnEnvName             = "SOURCE_TEST_POSTGRES_DSN"
	adminHost              = "127.0.0.1"
	adminPort              = uint16(57822)
	adminDatabase          = "source_test"
	cloneDatabase          = "source_t22_live_rehearsal_20261006"
	cloneSchema            = "public"
	tenantID               = int64(10000)
	knowledgeBaseID        = "65658207-a2ec-47fb-bf0f-11e7b685369e"
	baseMigration          = int64(120)
	targetMigration        = int64(121)
	migrationSHA256        = "2D09D4CAEEA5B2528661CBCB282A3CF7BFFC059225995C5387FD2D14B9D86540"
	migrationFile          = "migrations/versioned/000121_source_artifact_logical_payload_bytes.up.sql"
	maxRowsPerRun          = int64(10000)
	maxRowsPerBatch        = int64(64)
	maxRunDuration         = 180 * time.Second
	backfillCleanupReserve = 8 * time.Second
	maxConnectionTime      = 8 * time.Second
	maxStatementTime       = 8 * time.Second
	maxMeasurementTime     = 30 * time.Second
	requiredStopProof      = "process-and-clone-sessions"
	pinnedBackendPath      = `C:\Users\28211\.codex\test-runners\weknora-t22-capacity-2430a0e8.exe`
	pinnedBackendSHA256    = "a7c29a071b552b6008d8b6e827853aac56114fe4487204b2e6564ce0269ce803"
)

var approvedSourceIDs = [...]string{
	"ba6d2417-222e-4ff9-be22-eccf28659b32",
	"6168f382-c25d-4914-95c3-9bbecca7f18d",
	"ab31aae4-b21f-4dc4-90d6-d7e506c29c66",
}

type runMode uint8

const (
	modeStat runMode = iota
	modeUpgradeOnly
	modeBackfill
	modeMeasure
	modePreflight
)

type options struct {
	mode              runMode
	confirmAppStopped bool
	stopProof         string
}

type backfillDeadline struct {
	startedAt     time.Time
	workDeadline  time.Time
	totalDeadline time.Time
}

type output struct {
	Mode                  string           `json:"mode"`
	Status                string           `json:"status"`
	Database              string           `json:"database,omitempty"`
	MigrationVersion      int64            `json:"migration_version,omitempty"`
	RowsUpdated           int64            `json:"rows_updated,omitempty"`
	ParsedRowsUpdated     int64            `json:"parsed_rows_updated,omitempty"`
	VectorRowsUpdated     int64            `json:"vector_rows_updated,omitempty"`
	RemainingNullRows     *int64           `json:"remaining_null_rows,omitempty"`
	ElapsedMilliseconds   int64            `json:"elapsed_ms,omitempty"`
	ResumeAllowed         bool             `json:"resume_allowed,omitempty"`
	Trials                []measureTrial   `json:"trials,omitempty"`
	OldMedianMilliseconds int64            `json:"old_median_ms,omitempty"`
	NewMedianMilliseconds int64            `json:"new_median_ms,omitempty"`
	ValuesEqual           bool             `json:"values_equal,omitempty"`
	StopProof             *stopProofCounts `json:"stop_proof,omitempty"`
}

type stopProofCounts struct {
	TargetProcesses      int64 `json:"target_processes"`
	OtherCloneSessions   int64 `json:"other_clone_sessions"`
	PreparedTransactions int64 `json:"prepared_transactions"`
}

type sourceUsage struct {
	OriginalBytes    int64 `json:"original_bytes"`
	ParsedCacheBytes int64 `json:"parsed_cache_bytes"`
	VectorBytes      int64 `json:"vector_bytes"`
}

type measureTrial struct {
	Number          int           `json:"trial"`
	OldMilliseconds int64         `json:"old_ms"`
	NewMilliseconds int64         `json:"new_ms"`
	OldValues       []sourceUsage `json:"old_values"`
	NewValues       []sourceUsage `json:"new_values"`
	ValuesEqual     bool          `json:"values_equal"`
	SourcesCompared int           `json:"sources_compared"`
}

type queryRower interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

type rowQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

type processVerifier func(context.Context) (int64, error)
type fileOpener func(string) (io.ReadCloser, error)
type processImageEnumerator func(context.Context, string) ([]string, error)

var errCommitOutcomeUnknown = errors.New("commit outcome is unknown")
var errStopProofRejected = errors.New("stop proof rejected")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

type cloneConnector func(context.Context, string) (*pgx.Conn, error)

func run(args []string, stdout, stderr io.Writer) int {
	return runWithConnector(args, stdout, stderr, openCloneConnection)
}

func runWithConnector(args []string, stdout, stderr io.Writer, connect cloneConnector) int {
	opts, err := parseOptions(args)
	if err != nil {
		writeFailure(stderr, "arguments_rejected")
		return 2
	}
	if opts.mode == modeStat {
		return writeJSON(stdout, output{
			Mode:     "stat-only",
			Status:   "ready_no_environment_or_database_access",
			Database: cloneDatabase,
		})
	}
	var backfillTiming backfillDeadline
	if opts.mode == modeBackfill {
		backfillTiming = newBackfillDeadline(time.Now(), maxRunDuration, backfillCleanupReserve)
	}
	if (opts.mode == modeBackfill || opts.mode == modeMeasure) && !opts.confirmAppStopped {
		writeFailure(stderr, "application_stop_confirmation_required")
		return 2
	}
	if err := validateApprovedSourceScope(); err != nil {
		writeFailure(stderr, "fixed_source_scope_contract_invalid")
		return 2
	}
	if connect == nil {
		writeFailure(stderr, "guarded_clone_connection_rejected")
		return 2
	}
	if opts.mode == modeBackfill {
		connectCtx, connectCancel := contextWithDeadlineLimit(context.Background(), backfillTiming.workDeadline, maxConnectionTime)
		conn, err := connect(connectCtx, os.Getenv(dsnEnvName))
		connectCancel()
		if err != nil {
			writeFailure(stderr, "guarded_clone_connection_rejected")
			return 2
		}
		workCtx, workCancel := contextUntil(backfillTiming.workDeadline)
		result, exitCode := runBackfill(workCtx, conn, backfillTiming, opts.stopProof, stderr)
		workCancel()
		if err := closeConnectionUntil(conn, backfillTiming.totalDeadline); err != nil {
			result.Status = "close_outcome_unknown"
			result.ResumeAllowed = true
			exitCode = 3
		}
		result.ElapsedMilliseconds = time.Since(backfillTiming.startedAt).Milliseconds()
		if writeJSON(stdout, result) != 0 {
			return 2
		}
		return exitCode
	}

	ctx, cancel := context.WithTimeout(context.Background(), maxConnectionTime)
	defer cancel()
	conn, err := connect(ctx, os.Getenv(dsnEnvName))
	if err != nil {
		writeFailure(stderr, "guarded_clone_connection_rejected")
		return 2
	}
	defer closeConnection(conn)

	switch opts.mode {
	case modeUpgradeOnly:
		result := output{Mode: "upgrade-only", Status: "upgraded_atomically", Database: cloneDatabase, MigrationVersion: targetMigration}
		if err := upgradeOnly(ctx, conn, opts.stopProof, stderr, &result); err != nil {
			if errors.Is(err, errCommitOutcomeUnknown) {
				writeFailure(stderr, "commit_outcome_unknown")
				return 3
			}
			writeFailure(stderr, "migration_upgrade_rejected")
			return 2
		}
		return writeJSON(stdout, result)
	case modeMeasure:
		return runMeasure(stdout, conn)
	case modePreflight:
		return runPreflight(ctx, stdout, stderr, conn)
	default:
		writeFailure(stderr, "mode_rejected")
		return 2
	}
}

func parseOptions(args []string) (options, error) {
	fs := flag.NewFlagSet("t22-payload-backfill", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	upgrade := fs.Bool("upgrade-only", false, "atomically apply the pinned migration from version 120 to 121")
	backfill := fs.Bool("backfill", false, "fill scoped NULL artifact byte-count metadata")
	measure := fs.Bool("measure", false, "measure old and cached quota aggregates read-only")
	preflight := fs.Bool("preflight", false, "check migration transaction settings and fixed clone guards read-only")
	appStopped := fs.Bool("confirm-app-stopped", false, "confirm Root stopped the application for backfill/measurement")
	stopProof := fs.String("stop-proof", "", "required for writes: process-and-clone-sessions")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		return options{}, errors.New("invalid arguments")
	}
	selected := 0
	mode := modeStat
	for _, candidate := range []struct {
		set  bool
		mode runMode
	}{{*upgrade, modeUpgradeOnly}, {*backfill, modeBackfill}, {*measure, modeMeasure}, {*preflight, modePreflight}} {
		if candidate.set {
			selected++
			mode = candidate.mode
		}
	}
	if selected > 1 || (mode == modeStat && (*appStopped || *stopProof != "")) {
		return options{}, errors.New("incompatible mode flags")
	}
	if (mode == modeUpgradeOnly || mode == modeBackfill) && *stopProof != requiredStopProof {
		return options{}, errors.New("write mode requires explicit stop proof")
	}
	if mode == modeMeasure && *stopProof != "" {
		return options{}, errors.New("stop proof flag is not valid for read-only measurement")
	}
	if mode == modePreflight && (*appStopped || *stopProof != "") {
		return options{}, errors.New("write assertions are not valid for read-only preflight")
	}
	return options{mode: mode, confirmAppStopped: *appStopped, stopProof: *stopProof}, nil
}

func newBackfillDeadline(startedAt time.Time, total, cleanupReserve time.Duration) backfillDeadline {
	totalDeadline := startedAt.Add(total)
	return backfillDeadline{
		startedAt:     startedAt,
		workDeadline:  totalDeadline.Add(-cleanupReserve),
		totalDeadline: totalDeadline,
	}
}

func contextUntil(deadline time.Time) (context.Context, context.CancelFunc) {
	return context.WithDeadline(context.Background(), deadline)
}

func contextWithDeadlineLimit(parent context.Context, deadline time.Time, maxWait time.Duration) (context.Context, context.CancelFunc) {
	limit := time.Now().Add(maxWait)
	if deadline.Before(limit) {
		limit = deadline
	}
	return context.WithDeadline(parent, limit)
}

func minDuration(left, right time.Duration) time.Duration {
	if left < right {
		return left
	}
	return right
}

func validateApprovedSourceScope() error {
	if len(approvedSourceIDs) != 3 {
		return errors.New("three fixed source IDs are required")
	}
	seen := make(map[string]struct{}, len(approvedSourceIDs))
	for _, sourceID := range approvedSourceIDs {
		if sourceID == "" || len(sourceID) > 36 {
			return errors.New("fixed source ID does not fit data_sources.id varchar(36)")
		}
		if _, exists := seen[sourceID]; exists {
			return errors.New("fixed source IDs must be distinct")
		}
		seen[sourceID] = struct{}{}
	}
	return nil
}

func guardConnectionConfig(sourceDSN string) (*pgx.ConnConfig, error) {
	if strings.TrimSpace(sourceDSN) == "" {
		return nil, errors.New("admin DSN is unavailable")
	}
	parsed, err := url.Parse(sourceDSN)
	if err != nil || parsed == nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") ||
		parsed.Opaque != "" || parsed.Host != net.JoinHostPort(adminHost, strconv.Itoa(int(adminPort))) ||
		parsed.Path != "/"+adminDatabase || parsed.Fragment != "" || parsed.User == nil {
		return nil, errors.New("admin DSN URI endpoint is rejected")
	}
	username := parsed.User.Username()
	password, hasPassword := parsed.User.Password()
	if username == "" || !hasPassword || password == "" {
		return nil, errors.New("admin DSN credentials are incomplete")
	}
	query := parsed.Query()
	if len(query) != 1 || len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" {
		return nil, errors.New("admin DSN must explicitly disable TLS and omit extra options")
	}
	base, err := pgx.ParseConfig(sourceDSN)
	if err != nil {
		return nil, errors.New("admin DSN is invalid")
	}
	if base.Host != adminHost || base.Port != adminPort || base.Database != adminDatabase || base.TLSConfig != nil || len(base.Fallbacks) != 0 {
		return nil, errors.New("admin DSN endpoint or TLS policy is rejected")
	}
	config := base.Copy()
	config.Database = cloneDatabase
	config.RuntimeParams = map[string]string{"search_path": cloneSchema}
	config.Fallbacks = nil
	config.TLSConfig = nil
	config.ValidateConnect = validateConnectedClone
	return config, nil
}

func validateConnectedClone(ctx context.Context, pgConn *pgconn.PgConn) error {
	results, err := pgConn.Exec(ctx, "SELECT current_database(), current_schema()").ReadAll()
	if err != nil || len(results) != 1 || len(results[0].Rows) != 1 || len(results[0].Rows[0]) != 2 {
		return errors.New("connected clone identity could not be verified")
	}
	if string(results[0].Rows[0][0]) != cloneDatabase || string(results[0].Rows[0][1]) != cloneSchema {
		return errors.New("connected database identity is not the fixed rehearsal clone")
	}
	return nil
}

func openCloneConnection(ctx context.Context, sourceDSN string) (*pgx.Conn, error) {
	config, err := guardConnectionConfig(sourceDSN)
	if err != nil {
		return nil, err
	}
	return pgx.ConnectConfig(ctx, config)
}

// executeGuardedWrite uses only explicit operator confirmation and the fresh
// process/session proof; no execution mode probes the protected loopback port.
func executeGuardedWrite(ctx context.Context, stopProof string, queryer rowQueryer, verifyProcess processVerifier, report func(stopProofCounts) error, write func() error) error {
	if stopProof != requiredStopProof || queryer == nil || verifyProcess == nil || write == nil {
		return errStopProofRejected
	}
	counts, err := verifyStopProof(ctx, queryer, verifyProcess)
	if err != nil {
		return fmt.Errorf("%w: runtime proof failed", errStopProofRejected)
	}
	if report != nil {
		if err := report(counts); err != nil {
			return fmt.Errorf("%w: safe proof report failed", errStopProofRejected)
		}
	}
	return write()
}

func reportStopProofCounts(writer io.Writer, counts stopProofCounts) error {
	if writer == nil {
		return errors.New("stop proof report writer is unavailable")
	}
	return json.NewEncoder(writer).Encode(counts)
}

func verifyStopProof(ctx context.Context, queryer rowQueryer, verifyProcess processVerifier) (stopProofCounts, error) {
	var counts stopProofCounts
	if queryer == nil || verifyProcess == nil {
		return counts, errors.New("stop proof dependencies are unavailable")
	}
	processCount, err := verifyProcess(ctx)
	if err != nil || processCount < 0 {
		return counts, errors.New("pinned backend process state is unavailable")
	}
	counts.TargetProcesses = processCount
	if processCount != 0 {
		return counts, errors.New("pinned backend process is running")
	}
	var databaseName, schema string
	err = queryer.QueryRow(ctx, `SELECT current_database(), current_schema(),
		COUNT(*) FILTER (WHERE pid <> pg_backend_pid())::bigint
		FROM pg_stat_activity WHERE datname=current_database()`).Scan(&databaseName, &schema, &counts.OtherCloneSessions)
	if err != nil || databaseName != cloneDatabase || schema != cloneSchema || counts.OtherCloneSessions < 0 {
		return counts, errors.New("clone session count is unavailable or outside the fixed clone")
	}
	err = queryer.QueryRow(ctx, `SELECT COUNT(*)::bigint FROM pg_prepared_xacts
		WHERE database=current_database()`).Scan(&counts.PreparedTransactions)
	if err != nil || counts.PreparedTransactions < 0 {
		return counts, errors.New("prepared transaction count is unavailable")
	}
	if counts.OtherCloneSessions != 0 || counts.PreparedTransactions != 0 {
		return counts, errors.New("clone has other sessions or prepared transactions")
	}
	return counts, nil
}

func verifyPinnedBackendWith(ctx context.Context, path, expectedSHA256 string, open fileOpener, enumerate processImageEnumerator) (int64, error) {
	if !filepath.IsAbs(path) || open == nil || enumerate == nil {
		return 0, errors.New("pinned backend verifier configuration is invalid")
	}
	wantHash, err := hex.DecodeString(expectedSHA256)
	if err != nil || len(wantHash) != sha256.Size {
		return 0, errors.New("pinned backend hash is invalid")
	}
	file, err := open(path)
	if err != nil || file == nil {
		return 0, errors.New("pinned backend executable is unavailable")
	}
	hash := sha256.New()
	_, copyErr := io.Copy(hash, file)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), expectedSHA256) {
		return 0, errors.New("pinned backend executable hash does not match")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	images, err := enumerate(ctx, filepath.Base(path))
	if err != nil {
		return 0, errors.New("process image enumeration is incomplete")
	}
	wanted := normalizeProcessImagePath(path)
	var matches int64
	for _, imagePath := range images {
		if !filepath.IsAbs(imagePath) || filepath.Base(imagePath) == "." || filepath.Base(imagePath) == string(filepath.Separator) {
			return 0, errors.New("process image path is unavailable")
		}
		if !strings.EqualFold(filepath.Base(imagePath), filepath.Base(path)) {
			return 0, errors.New("process image enumeration returned an unexpected candidate")
		}
		if strings.EqualFold(normalizeProcessImagePath(imagePath), wanted) {
			matches++
		}
	}
	return matches, nil
}

func normalizeProcessImagePath(path string) string {
	path = strings.TrimPrefix(path, `\\?\`)
	return filepath.Clean(path)
}

func verifyPinnedBackendProcess(ctx context.Context) (int64, error) {
	return verifyPinnedBackendWith(ctx, pinnedBackendPath, pinnedBackendSHA256,
		func(path string) (io.ReadCloser, error) { return os.Open(path) }, enumeratePinnedBackendImages)
}

func helperMaintenanceLockID() (int64, error) {
	lockIDText, err := database.GenerateAdvisoryLockId("/"+cloneDatabase, cloneSchema, "t22_payload_stop_proof")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(lockIDText, 10, 64)
}

func acquireHelperMaintenanceLock(ctx context.Context, conn *pgx.Conn) error {
	if conn == nil {
		return errors.New("helper maintenance lock connection is unavailable")
	}
	lockID, err := helperMaintenanceLockID()
	if err != nil {
		return err
	}
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1::bigint)", lockID).Scan(&acquired); err != nil || !acquired {
		return errors.New("another rehearsal helper holds the maintenance lock")
	}
	return nil
}

func verifyCloneAndLedger(ctx context.Context, queryer queryRower, expectedVersion int64) error {
	if err := verifyCloneIdentity(ctx, queryer); err != nil {
		return err
	}
	version, dirty, err := readExactMigrationLedger(ctx, queryer, false)
	if err != nil {
		return err
	}
	if dirty || version != expectedVersion {
		return errors.New("migration ledger version is not the required clean version")
	}
	return nil
}

func readExactMigrationLedger(ctx context.Context, queryer queryRower, lock bool) (int64, bool, error) {
	query := "SELECT version, dirty FROM public.schema_migrations"
	if lock {
		query += " FOR UPDATE"
	}
	rows, err := queryer.Query(ctx, query)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	count := 0
	var version int64
	var dirty bool
	for rows.Next() {
		if err := rows.Scan(&version, &dirty); err != nil {
			return 0, false, err
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	if count != 1 {
		return 0, false, errors.New("migration ledger must contain exactly one row")
	}
	return version, dirty, nil
}

// upgradeOnly is a restricted forward-up runner for the fixed, SHA-pinned
// 120→121 migration. It never forces, skips, or repairs a dirty ledger.
func upgradeOnly(ctx context.Context, conn *pgx.Conn, stopProofFlag string, reportWriter io.Writer, result *output) error {
	if err := verifyCloneAndLedger(ctx, conn, baseMigration); err != nil {
		return err
	}
	if err := acquireHelperMaintenanceLock(ctx, conn); err != nil {
		return err
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	migrationSQL, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(migrationFile)))
	if err != nil || !migrationHashMatches(migrationSQL) {
		return errors.New("pinned migration file is unavailable or changed")
	}
	lockIDText, err := database.GenerateAdvisoryLockId("/"+cloneDatabase, cloneSchema, "schema_migrations")
	if err != nil {
		return err
	}
	lockID, err := strconv.ParseInt(lockIDText, 10, 64)
	if err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1::bigint)", lockID); err != nil {
		return err
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), maxConnectionTime)
		defer cancel()
		_, _ = conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1::bigint)", lockID)
	}()

	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	commitAttempted := false
	defer func() {
		if !commitAttempted {
			_ = rollbackBounded(tx)
		}
	}()
	if err := configureMigrationTransaction(ctx, tx); err != nil {
		return err
	}
	if err := verifyCloneIdentity(ctx, tx); err != nil {
		return err
	}
	version, dirty, err := readExactMigrationLedger(ctx, tx, true)
	if err != nil || dirty || version != baseMigration {
		return errors.New("locked migration ledger is not exactly one clean base version")
	}
	if err := verifyFixedSourceScope(ctx, tx); err != nil {
		return err
	}
	if err := lockPausedFixedSources(ctx, tx); err != nil {
		return err
	}
	if err := lockQuiescentSourceStates(ctx, tx); err != nil {
		return err
	}
	if err := verifyGlobalQuiescence(ctx, tx); err != nil {
		return err
	}
	if result == nil {
		return errors.New("migration result sink is unavailable")
	}
	err = executeGuardedWrite(ctx, stopProofFlag, tx, verifyPinnedBackendProcess,
		func(counts stopProofCounts) error {
			if err := reportStopProofCounts(reportWriter, counts); err != nil {
				return err
			}
			result.StopProof = &counts
			return nil
		},
		func() error {
			for _, statement := range migrationStatements(string(migrationSQL)) {
				if _, err := tx.Exec(ctx, statement); err != nil {
					return err
				}
			}
			tag, err := tx.Exec(ctx, "UPDATE public.schema_migrations SET version=$1, dirty=FALSE WHERE version=$2 AND dirty=FALSE", targetMigration, baseMigration)
			if err != nil || tag.RowsAffected() != 1 {
				return errors.New("migration ledger update did not match exactly one clean version row")
			}
			return nil
		})
	if err != nil {
		return err
	}
	commitAttempted = true
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%w: migration transaction", errCommitOutcomeUnknown)
	}
	return nil
}

func verifyCloneIdentity(ctx context.Context, queryer queryRower) error {
	var databaseName, schema string
	if err := queryer.QueryRow(ctx, "SELECT current_database(), current_schema()").Scan(&databaseName, &schema); err != nil {
		return err
	}
	if databaseName != cloneDatabase || schema != cloneSchema {
		return errors.New("clone identity mismatch")
	}
	return nil
}

func configureMigrationTransaction(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SET LOCAL statement_timeout = '8s'"); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '3s'")
	return err
}

func runPreflight(ctx context.Context, stdout, stderr io.Writer, conn *pgx.Conn) int {
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: pgx.ReadOnly})
	if err != nil {
		writeFailure(stderr, "preflight_begin_failed")
		return 2
	}
	defer rollbackBounded(tx)
	if err := configureMigrationTransaction(ctx, tx); err != nil {
		var pgErr *pgconn.PgError
		code := "unknown"
		if errors.As(err, &pgErr) {
			code = pgErr.Code
		}
		_ = writeJSON(stderr, map[string]string{"status": "preflight_transaction_settings_failed", "sqlstate": code})
		return 2
	}
	version, dirty, err := readExactMigrationLedger(ctx, tx, false)
	if err != nil || dirty || (version != baseMigration && version != targetMigration) {
		writeFailure(stderr, "preflight_ledger_rejected")
		return 2
	}
	if verifyCloneIdentity(ctx, tx) != nil || verifyFixedSourceScope(ctx, tx) != nil || verifyPausedFixedSources(ctx, tx) != nil || verifyGlobalQuiescence(ctx, tx) != nil {
		writeFailure(stderr, "preflight_scope_or_quiescence_rejected")
		return 2
	}
	var readOnly, statementTimeout, lockTimeout string
	if tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only'), current_setting('statement_timeout'), current_setting('lock_timeout')`).Scan(&readOnly, &statementTimeout, &lockTimeout) != nil || readOnly != "on" {
		writeFailure(stderr, "preflight_readonly_not_confirmed")
		return 2
	}
	proof, err := verifyStopProof(ctx, tx, verifyPinnedBackendProcess)
	if err != nil {
		writeFailure(stderr, "preflight_stop_proof_rejected")
		return 2
	}
	if err := tx.Rollback(ctx); err != nil {
		writeFailure(stderr, "preflight_rollback_unknown")
		return 3
	}
	return writeJSON(stdout, map[string]any{"status": "preflight_complete", "readonly": true, "migration_version": version, "statement_timeout": statementTimeout, "lock_timeout": lockTimeout, "stop_proof": proof})
}

func rollbackBounded(tx pgx.Tx) error {
	return rollbackBoundedUntil(tx, time.Now().Add(maxConnectionTime))
}

func rollbackBoundedUntil(tx pgx.Tx, deadline time.Time) error {
	ctx, cancel := contextUntil(deadline)
	defer cancel()
	return tx.Rollback(ctx)
}

func migrationHashMatches(sql []byte) bool {
	hash := sha256.Sum256(sql)
	return strings.EqualFold(hex.EncodeToString(hash[:]), migrationSHA256)
}

func migrationStatements(sql string) []string {
	parts := strings.Split(sql, ";")
	statements := make([]string, 0, len(parts))
	for _, part := range parts {
		if statement := strings.TrimSpace(part); statement != "" {
			statements = append(statements, statement)
		}
	}
	return statements
}

func repositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(current, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(current, filepath.FromSlash(migrationFile))); err == nil {
				return current, nil
			}
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", errors.New("repository root was not found")
		}
		current = parent
	}
}

type batchDisposition uint8

const (
	batchStop batchDisposition = iota
	batchContinue
	batchCheckRemaining
)

type backfillBatchOutcome struct {
	operationErr      error
	rollbackAttempted bool
	rollbackErr       error
	commitAttempted   bool
	commitErr         error
	parsedRows        int64
	vectorRows        int64
	timeLimit         bool
}

func applyBackfillBatchOutcome(result *output, outcome backfillBatchOutcome) (batchDisposition, int) {
	if outcome.commitAttempted {
		if outcome.operationErr != nil || outcome.rollbackAttempted {
			result.Status = "batch_outcome_invalid"
			return batchStop, 3
		}
		if outcome.commitErr != nil {
			result.Status = "commit_outcome_unknown"
			return batchStop, 3
		}
		if outcome.parsedRows < 0 || outcome.vectorRows < 0 || outcome.parsedRows+outcome.vectorRows > maxRowsPerBatch {
			result.Status = "batch_outcome_invalid"
			return batchStop, 3
		}
		result.ParsedRowsUpdated += outcome.parsedRows
		result.VectorRowsUpdated += outcome.vectorRows
		result.RowsUpdated += outcome.parsedRows + outcome.vectorRows
		if outcome.parsedRows+outcome.vectorRows == 0 {
			return batchCheckRemaining, 3
		}
		return batchContinue, 0
	}
	if outcome.operationErr != nil {
		if !outcome.rollbackAttempted || outcome.rollbackErr != nil {
			result.Status = "abort_outcome_unknown"
			return batchStop, 3
		}
		if outcome.timeLimit {
			result.Status = "partial_time_limit"
			return batchStop, 3
		}
		result.Status = "batch_rejected_rolled_back"
		return batchStop, 2
	}
	result.Status = "batch_outcome_invalid"
	return batchStop, 3
}

func backfillBatchLimit(rowsUpdated int64) int64 {
	if rowsUpdated < 0 || rowsUpdated >= maxRowsPerRun {
		return 0
	}
	return min(maxRowsPerBatch, maxRowsPerRun-rowsUpdated)
}

func applyBackfillRemaining(result *output, remaining int64, partialStatus string) {
	if remaining < 0 {
		result.Status = "partial_remaining_count_invalid"
		result.ResumeAllowed = true
		return
	}
	result.RemainingNullRows = &remaining
	if remaining == 0 {
		result.Status = "complete"
		result.ResumeAllowed = false
		return
	}
	result.Status = partialStatus
	result.ResumeAllowed = true
}

func runBackfill(ctx context.Context, conn *pgx.Conn, timing backfillDeadline, stopProofFlag string, reportWriter io.Writer) (output, int) {
	result := output{Mode: "backfill", Database: cloneDatabase, Status: "running", MigrationVersion: targetMigration, ResumeAllowed: true}
	exitCode := 3
	if err := acquireHelperMaintenanceLock(ctx, conn); err != nil {
		result.Status = "partial_helper_lock_rejected"
		return result, 2
	}
	for {
		if ctx.Err() != nil || !time.Now().Before(timing.workDeadline) {
			result.Status = "partial_time_limit"
			break
		}
		limit := backfillBatchLimit(result.RowsUpdated)
		if limit == 0 {
			result.Status = "partial_row_limit"
			break
		}
		batchCtx, batchCancel := contextWithDeadlineLimit(ctx, timing.workDeadline, maxStatementTime)
		tx, err := conn.Begin(batchCtx)
		if err != nil {
			batchCancel()
			if ctx.Err() != nil || !time.Now().Before(timing.workDeadline) {
				result.Status = "partial_time_limit"
			} else {
				result.Status = "partial_transaction_start_failed"
				exitCode = 2
			}
			break
		}
		if err = verifyCloneAndLedger(batchCtx, tx, targetMigration); err == nil {
			err = verifyFixedSourceScope(batchCtx, tx)
		}
		if err == nil {
			err = lockPausedFixedSources(batchCtx, tx)
		}
		if err == nil {
			err = lockQuiescentSourceStates(batchCtx, tx)
		}
		if err == nil {
			err = verifyGlobalQuiescence(batchCtx, tx)
		}
		parsedRows := int64(0)
		vectorRows := int64(0)
		if err == nil {
			err = executeGuardedWrite(batchCtx, stopProofFlag, tx, verifyPinnedBackendProcess,
				func(counts stopProofCounts) error {
					if err := reportStopProofCounts(reportWriter, counts); err != nil {
						return err
					}
					result.StopProof = &counts
					return nil
				},
				func() error {
					parsedRows, err = updateParsedBatch(batchCtx, tx, limit)
					if err == nil && parsedRows == 0 {
						vectorRows, err = updateVectorBatch(batchCtx, tx, limit)
					}
					return err
				})
		}
		if err != nil {
			batchCancel()
			rollbackErr := rollbackBoundedUntil(tx, timing.totalDeadline)
			if errors.Is(err, errStopProofRejected) && rollbackErr == nil {
				result.Status = "partial_stop_proof_failed"
				exitCode = 2
				break
			}
			disposition, code := applyBackfillBatchOutcome(&result, backfillBatchOutcome{
				operationErr: err, rollbackAttempted: true, rollbackErr: rollbackErr,
				timeLimit: ctx.Err() != nil || !time.Now().Before(timing.workDeadline),
			})
			exitCode = code
			if disposition != batchStop {
				result.Status = "batch_outcome_invalid"
				exitCode = 3
			}
			break
		}
		commitErr := tx.Commit(batchCtx)
		batchCancel()
		disposition, code := applyBackfillBatchOutcome(&result, backfillBatchOutcome{
			commitAttempted: true, commitErr: commitErr, parsedRows: parsedRows, vectorRows: vectorRows,
		})
		if disposition == batchStop {
			exitCode = code
			break
		}
		if disposition == batchCheckRemaining {
			remaining, countErr := countScopedNullRowsBounded(ctx, conn)
			if countErr != nil {
				result.Status = "partial_remaining_count_unavailable"
				exitCode = 3
			} else {
				applyBackfillRemaining(&result, remaining, "partial_rows_locked_or_scope_changed")
				if result.Status == "complete" {
					exitCode = 0
				}
			}
			break
		}
	}
	if result.Status == "running" {
		result.Status = "partial_row_limit"
	}
	if result.Status == "partial_row_limit" && ctx.Err() == nil && time.Now().Before(timing.workDeadline) {
		remaining, err := countScopedNullRowsBounded(ctx, conn)
		if err != nil {
			result.Status = "partial_remaining_count_unavailable"
		} else {
			applyBackfillRemaining(&result, remaining, "partial_row_limit")
			if result.Status == "complete" {
				exitCode = 0
			}
		}
	}
	result.ElapsedMilliseconds = time.Since(timing.startedAt).Milliseconds()
	return result, exitCode
}

func verifyFixedSourceScope(ctx context.Context, queryer queryRower) error {
	var kbCount int64
	if err := queryer.QueryRow(ctx, `SELECT COUNT(*) FROM public.knowledge_bases
		WHERE id=$1 AND tenant_id=$2 AND deleted_at IS NULL`, knowledgeBaseID, tenantID).Scan(&kbCount); err != nil || kbCount != 1 {
		return errors.New("fixed tenant and knowledge-base scope did not verify")
	}
	var sourceCount int64
	query := `SELECT COUNT(*) FROM public.data_sources
		WHERE tenant_id=$1 AND knowledge_base_id=$2 AND deleted_at IS NULL AND id=ANY($3::varchar[])`
	if err := queryer.QueryRow(ctx, query, tenantID, knowledgeBaseID, approvedSourceIDs[:]).Scan(&sourceCount); err != nil || sourceCount != int64(len(approvedSourceIDs)) {
		return errors.New("fixed source scope did not verify exactly three sources")
	}
	return nil
}

func lockPausedFixedSources(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT id, status FROM public.data_sources
		WHERE tenant_id=$1 AND knowledge_base_id=$2 AND deleted_at IS NULL AND id=ANY($3::varchar[])
		ORDER BY id FOR UPDATE`, tenantID, knowledgeBaseID, approvedSourceIDs[:])
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return err
		}
		count++
		if status != "paused" || !containsSourceID(id) {
			return errors.New("fixed source is not paused or is outside approved scope")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(approvedSourceIDs) {
		return errors.New("fixed source row lock count mismatch")
	}
	return nil
}

func verifyPausedFixedSources(ctx context.Context, queryer queryRower) error {
	var count int64
	err := queryer.QueryRow(ctx, `SELECT count(*) FROM public.data_sources
		WHERE tenant_id=$1 AND knowledge_base_id=$2 AND deleted_at IS NULL
		AND id=ANY($3::varchar[]) AND status='paused'`, tenantID, knowledgeBaseID, approvedSourceIDs[:]).Scan(&count)
	if err != nil || count != int64(len(approvedSourceIDs)) {
		return errors.New("fixed sources are not all paused")
	}
	return nil
}

func containsSourceID(sourceID string) bool {
	for _, approved := range approvedSourceIDs {
		if sourceID == approved {
			return true
		}
	}
	return false
}

func lockQuiescentSourceStates(ctx context.Context, tx pgx.Tx) error {
	rows, err := tx.Query(ctx, `SELECT data_source_id, active_sync_log_id, pending_sync_log_id, lease_owner, lease_expires_at
		FROM public.source_sync_states WHERE tenant_id=$1 AND data_source_id=ANY($2::varchar[])
		ORDER BY data_source_id FOR UPDATE`, tenantID, approvedSourceIDs[:])
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		var sourceID string
		var activeID, pendingID, leaseOwner *string
		var leaseExpiresAt *time.Time
		if err := rows.Scan(&sourceID, &activeID, &pendingID, &leaseOwner, &leaseExpiresAt); err != nil {
			return err
		}
		count++
		if activeID != nil || pendingID != nil || leaseOwner != nil || leaseExpiresAt != nil {
			return errors.New("source state is not quiescent")
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(approvedSourceIDs) {
		return errors.New("a fixed source state row is missing")
	}
	return nil
}

func verifyGlobalQuiescence(ctx context.Context, queryer queryRower) error {
	guards := []string{
		`SELECT count(*) FROM public.data_sources WHERE deleted_at IS NULL AND status NOT IN ('paused','deleted')`,
		`SELECT count(*) FROM public.knowledges WHERE deleted_at IS NULL AND parse_status IN ('pending','processing','finalizing')`,
		`SELECT count(*) FROM public.sync_logs WHERE status IN ('queued','running')`,
		`SELECT count(*) FROM public.source_sync_states WHERE pending_sync_log_id IS NOT NULL OR active_sync_log_id IS NOT NULL OR lease_owner IS NOT NULL OR lease_expires_at IS NOT NULL`,
		`SELECT count(*) FROM public.source_sync_states s LEFT JOIN public.data_sources d ON d.id=s.data_source_id AND d.tenant_id=s.tenant_id WHERE d.id IS NULL`,
		`SELECT count(*) FROM public.source_sync_runs r WHERE r.phase NOT IN ('published','failed','canceled','superseded') OR NOT EXISTS (SELECT 1 FROM public.sync_logs l WHERE l.id=r.sync_log_id)`,
		`SELECT count(*) FROM public.task_pending_ops WHERE task_type IN ('wiki:ingest','wiki:finalize','source:wiki:update')`,
		`SELECT count(*) FROM public.source_publication_outbox WHERE status NOT IN ('delivered','superseded')`,
		`SELECT count(*) FROM public.source_cleanup_operations WHERE status IN ('pending','running')`,
		`SELECT count(*) FROM public.source_wiki_attempts WHERE status IN ('queued','running')`,
		`SELECT count(*) FROM public.source_wiki_batches WHERE status IN ('queued','running')`,
		`SELECT count(*) FROM public.source_wiki_update_plans WHERE status IN ('pending','running')`,
		`SELECT count(*) FROM public.source_wiki_update_plan_items WHERE state IN ('pending','running')`,
	}
	for _, query := range guards {
		var count int64
		if err := queryer.QueryRow(ctx, query).Scan(&count); err != nil || count != 0 {
			return errors.New("global background-work quiescence could not be confirmed")
		}
	}
	return nil
}

func updateParsedBatch(ctx context.Context, tx pgx.Tx, limit int64) (int64, error) {
	return executeUpdateBatch(ctx, tx, "source_parsed_artifacts", "octet_length(a.parsed::text)", limit)
}

func updateVectorBatch(ctx context.Context, tx pgx.Tx, limit int64) (int64, error) {
	return executeUpdateBatch(ctx, tx, "source_embedding_artifacts", "jsonb_array_length(a.vector)::bigint*4", limit)
}

func executeUpdateBatch(ctx context.Context, tx pgx.Tx, table, derivedExpression string, limit int64) (int64, error) {
	if (table != "source_parsed_artifacts" && table != "source_embedding_artifacts") || limit < 1 || limit > maxRowsPerBatch {
		return 0, errors.New("batch query parameters are invalid")
	}
	query := fmt.Sprintf(`WITH batch AS MATERIALIZED (
		SELECT tenant_id,data_source_id,artifact_key FROM public.%s
		WHERE tenant_id=$1 AND data_source_id=ANY($2::varchar[]) AND logical_payload_bytes IS NULL
		ORDER BY data_source_id,artifact_key LIMIT $3 FOR UPDATE
	)
	UPDATE public.%s AS a SET logical_payload_bytes=%s FROM batch b
	WHERE a.tenant_id=b.tenant_id AND a.data_source_id=b.data_source_id AND a.artifact_key=b.artifact_key
	AND a.logical_payload_bytes IS NULL RETURNING 1`, table, table, derivedExpression)
	rows, err := tx.Query(ctx, query, tenantID, approvedSourceIDs[:], limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		var marker int
		if err := rows.Scan(&marker); err != nil {
			return 0, err
		}
		count++
	}
	return count, rows.Err()
}

func countScopedNullRows(ctx context.Context, queryer queryRower) (int64, error) {
	var parsed, vectors int64
	if err := queryer.QueryRow(ctx, `SELECT COUNT(*) FROM public.source_parsed_artifacts
		WHERE tenant_id=$1 AND data_source_id=ANY($2::varchar[]) AND logical_payload_bytes IS NULL`, tenantID, approvedSourceIDs[:]).Scan(&parsed); err != nil {
		return 0, err
	}
	if err := queryer.QueryRow(ctx, `SELECT COUNT(*) FROM public.source_embedding_artifacts
		WHERE tenant_id=$1 AND data_source_id=ANY($2::varchar[]) AND logical_payload_bytes IS NULL`, tenantID, approvedSourceIDs[:]).Scan(&vectors); err != nil {
		return 0, err
	}
	return parsed + vectors, nil
}

func countScopedNullRowsBounded(parent context.Context, queryer queryRower) (int64, error) {
	ctx, cancel := context.WithTimeout(parent, maxStatementTime)
	defer cancel()
	return countScopedNullRows(ctx, queryer)
}

func runMeasure(stdout io.Writer, conn *pgx.Conn) int {
	trials := make([]measureTrial, 0, 3)
	oldDurations, newDurations := make([]int64, 0, 3), make([]int64, 0, 3)
	allEqual := true
	for trialNumber := 1; trialNumber <= 3; trialNumber++ {
		ctx, cancel := context.WithTimeout(context.Background(), maxMeasurementTime)
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			cancel()
			return writeMeasureFailure(stdout, "measurement_transaction_failed")
		}
		if err := verifyCloneAndLedger(ctx, tx, targetMigration); err != nil {
			rollbackErr := rollbackBounded(tx)
			cancel()
			if rollbackErr != nil {
				return writeMeasureFailure(stdout, "measurement_abort_outcome_unknown")
			}
			return writeMeasureFailure(stdout, "measurement_clone_guard_failed")
		}
		if err := verifyFixedSourceScope(ctx, tx); err != nil {
			rollbackErr := rollbackBounded(tx)
			cancel()
			if rollbackErr != nil {
				return writeMeasureFailure(stdout, "measurement_abort_outcome_unknown")
			}
			return writeMeasureFailure(stdout, "measurement_source_guard_failed")
		}
		if err := verifyPausedFixedSources(ctx, tx); err != nil {
			rollbackErr := rollbackBounded(tx)
			cancel()
			if rollbackErr != nil {
				return writeMeasureFailure(stdout, "measurement_abort_outcome_unknown")
			}
			return writeMeasureFailure(stdout, "measurement_source_pause_guard_failed")
		}
		if err := verifyGlobalQuiescence(ctx, tx); err != nil {
			rollbackErr := rollbackBounded(tx)
			cancel()
			if rollbackErr != nil {
				return writeMeasureFailure(stdout, "measurement_abort_outcome_unknown")
			}
			return writeMeasureFailure(stdout, "measurement_background_work_guard_failed")
		}
		remaining, countErr := countScopedNullRows(ctx, tx)
		if countErr != nil || remaining != 0 {
			rollbackErr := rollbackBounded(tx)
			cancel()
			if rollbackErr != nil {
				return writeMeasureFailure(stdout, "measurement_abort_outcome_unknown")
			}
			if countErr != nil {
				return writeMeasureFailure(stdout, "measurement_null_count_unavailable")
			}
			return writeMeasureFailure(stdout, "measurement_requires_zero_scoped_null_cache_rows")
		}
		oldFirst := trialNumber%2 == 1
		oldValues, newValues := make([]sourceUsage, len(approvedSourceIDs)), make([]sourceUsage, len(approvedSourceIDs))
		var oldElapsed, newElapsed time.Duration
		if oldFirst {
			oldValues, oldElapsed, err = measureQuerySet(ctx, tx, oldUsageSQL)
			if err == nil {
				newValues, newElapsed, err = measureQuerySet(ctx, tx, newUsageSQL)
			}
		} else {
			newValues, newElapsed, err = measureQuerySet(ctx, tx, newUsageSQL)
			if err == nil {
				oldValues, oldElapsed, err = measureQuerySet(ctx, tx, oldUsageSQL)
			}
		}
		if err != nil {
			rollbackErr := rollbackBounded(tx)
			cancel()
			if rollbackErr != nil {
				return writeMeasureFailure(stdout, "measurement_abort_outcome_unknown")
			}
			return writeMeasureFailure(stdout, "measurement_aggregate_failed")
		}
		if err := tx.Commit(ctx); err != nil {
			cancel()
			return writeMeasureFailure(stdout, "measurement_commit_outcome_unknown")
		}
		cancel()
		equal := equalUsage(oldValues, newValues)
		allEqual = allEqual && equal
		oldMS, newMS := oldElapsed.Milliseconds(), newElapsed.Milliseconds()
		oldDurations, newDurations = append(oldDurations, oldMS), append(newDurations, newMS)
		trials = append(trials, measureTrial{Number: trialNumber, OldMilliseconds: oldMS, NewMilliseconds: newMS, OldValues: oldValues, NewValues: newValues, ValuesEqual: equal, SourcesCompared: len(approvedSourceIDs)})
	}
	result := output{Mode: "measure", Status: "complete", Database: cloneDatabase, MigrationVersion: targetMigration, Trials: trials,
		OldMedianMilliseconds: median(oldDurations), NewMedianMilliseconds: median(newDurations), ValuesEqual: allEqual}
	if !allEqual {
		result.Status = "value_mismatch"
	}
	if writeJSON(stdout, result) != 0 {
		return 2
	}
	if !allEqual {
		return 4
	}
	return 0
}

func writeMeasureFailure(writer io.Writer, status string) int {
	_ = writeJSON(writer, output{Mode: "measure", Status: status, Database: cloneDatabase, MigrationVersion: targetMigration})
	return 2
}

const oldUsageSQL = `SELECT
	COALESCE((SELECT SUM(octet_length(v.content)) FROM public.source_file_versions v JOIN public.source_files f ON f.id=v.source_file_id WHERE f.tenant_id=$1 AND f.data_source_id=$2),0)::bigint AS original_bytes,
	COALESCE((SELECT SUM(octet_length(a.parsed::text)) FROM public.source_parsed_artifacts a WHERE a.tenant_id=$1 AND a.data_source_id=$2),0)::bigint AS parsed_cache_bytes,
	(COALESCE((SELECT SUM(jsonb_array_length(a.vector)::bigint*4) FROM public.source_embedding_artifacts a WHERE a.tenant_id=$1 AND a.data_source_id=$2),0)+
	 COALESCE((SELECT SUM(e.dimension::bigint*4) FROM public.embeddings e JOIN public.source_chunk_references cr ON cr.chunk_id=e.chunk_id JOIN public.source_files f ON f.id=cr.source_file_id WHERE f.tenant_id=$1 AND f.data_source_id=$2),0))::bigint AS vector_bytes`

const newUsageSQL = `SELECT
	COALESCE((SELECT SUM(octet_length(v.content)) FROM public.source_file_versions v JOIN public.source_files f ON f.id=v.source_file_id WHERE f.tenant_id=$1 AND f.data_source_id=$2),0)::bigint AS original_bytes,
	COALESCE((SELECT SUM(COALESCE(a.logical_payload_bytes, octet_length(a.parsed::text))) FROM public.source_parsed_artifacts a WHERE a.tenant_id=$1 AND a.data_source_id=$2),0)::bigint AS parsed_cache_bytes,
	(COALESCE((SELECT SUM(COALESCE(a.logical_payload_bytes, jsonb_array_length(a.vector)::bigint*4)) FROM public.source_embedding_artifacts a WHERE a.tenant_id=$1 AND a.data_source_id=$2),0)+
	 COALESCE((SELECT SUM(e.dimension::bigint*4) FROM public.embeddings e JOIN public.source_chunk_references cr ON cr.chunk_id=e.chunk_id JOIN public.source_files f ON f.id=cr.source_file_id WHERE f.tenant_id=$1 AND f.data_source_id=$2),0))::bigint AS vector_bytes`

func measureQuerySet(ctx context.Context, tx pgx.Tx, query string) ([]sourceUsage, time.Duration, error) {
	values := make([]sourceUsage, len(approvedSourceIDs))
	started := time.Now()
	for i, sourceID := range approvedSourceIDs {
		if err := tx.QueryRow(ctx, query, tenantID, sourceID).Scan(&values[i].OriginalBytes, &values[i].ParsedCacheBytes, &values[i].VectorBytes); err != nil {
			return nil, time.Since(started), err
		}
	}
	return values, time.Since(started), nil
}

func equalUsage(left, right []sourceUsage) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func median(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[len(ordered)/2]
}

func closeConnection(conn *pgx.Conn) {
	ctx, cancel := context.WithTimeout(context.Background(), maxConnectionTime)
	defer cancel()
	_ = conn.Close(ctx)
}

func closeConnectionUntil(conn *pgx.Conn, deadline time.Time) error {
	ctx, cancel := contextUntil(deadline)
	defer cancel()
	return conn.Close(ctx)
}

func writeJSON(writer io.Writer, value any) int {
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		return 2
	}
	return 0
}

func writeFailure(writer io.Writer, status string) {
	_ = json.NewEncoder(writer).Encode(map[string]string{"status": status})
}
