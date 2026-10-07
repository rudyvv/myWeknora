//go:build windows

// launch is a narrow launcher for the already accepted T22 feature binary.
// Its default mode is filesystem-stat-only. --start is reserved for Root after
// the isolated runtime, database and ACL gates have been independently met.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"golang.org/x/sys/windows"
)

const (
	acceptedExe = `C:/Users/28211/.codex/test-runners/weknora-t22-acceptance-8f1fa40a.exe`
	acceptedSHA = "0724ff7ae19e0fde27a754fef8bfa278875f4d3adec4cb660584fdcf3ba7c5bd"
	workRoot    = `C:\Users\28211\.codex\worktrees\source-integration\WeKnora`
	primaryEnv  = `D:\Project-Weknora\WeKnora\.env`
	localEnv    = `D:\Project-Weknora\WeKnora\.env.local`

	storageRoot = `C:\Users\28211\.codex\test-runners\t22-isolated-files`
	privateLogs = `C:\Users\28211\.codex\test-runners\t22-isolated-runtime-logs`

	databaseName  = "source_t22_live_rehearsal_20261006"
	databaseHost  = "127.0.0.1"
	databasePort  = uint16(57822)
	databaseSeed  = "source_test"
	rehearsalKBID = "65658207-a2ec-47fb-bf0f-11e7b685369e"
	redisAddress  = "127.0.0.1:57824"
	serverAddress = "127.0.0.1:57825"
	parserURL     = "http://127.0.0.1:57823"

	maxDotenvBytes    = 1 << 20
	frozenTLSDeadline = "2026-10-07T16:30:00Z"
)

var knownAsynqQueues = map[string]struct{}{
	"default":         {},
	"chat_attachment": {},
	"postprocess":     {},
	"summary":         {},
	"multimodal":      {},
	"graph":           {},
	"question":        {},
	"sync":            {},
	"low":             {},
	"wiki":            {},
	"memory":          {},
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		return statOnly()
	}
	if len(args) == 1 && (args[0] == "-h" || args[0] == "--help" || args[0] == "-help") {
		usage()
		return 0
	}
	if len(args) != 1 || args[0] != "--start" {
		fmt.Fprintln(os.Stderr, "refused: unsupported arguments")
		return 2
	}
	return startAcceptedApp()
}

func usage() {
	fmt.Println("launch.exe: default mode performs filesystem metadata checks only")
	fmt.Println("  launch.exe --start  Root-only; requires Root ACL attestation and all fixed preflight gates")
	fmt.Println("The launcher never creates directories, changes ACLs, cleans database state, or runs migrations.")
}

func statOnly() int {
	fmt.Println("mode=stat-only")
	app := pathState(acceptedExe, regularFile)
	root := pathState(workRoot, directory)
	storage := pathState(storageRoot, directory)
	logs := pathState(privateLogs, directory)
	fmt.Printf("accepted_app=%s (sha256=not-read)\n", app)
	fmt.Printf("working_root=%s\n", root)
	fmt.Printf("isolated_storage=%s\n", storage)
	fmt.Printf("private_log_root=%s\n", logs)
	fmt.Println("acl_gate=requires_root_host_verification (not inspected)")
	fmt.Println("environment=not-read; database=not-connected; redis=not-contacted; app=not-launched")
	if app != "present" || root != "present" || storage != "present" || logs != "present" {
		return 1
	}
	return 0
}

type pathKind int

const (
	regularFile pathKind = iota
	directory
)

func pathState(path string, kind pathKind) string {
	if _, err := inspectPath(path, kind); err != nil {
		return "missing_or_unsafe"
	}
	return "present"
}

func inspectPath(path string, kind pathKind) (os.FileInfo, error) {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" {
		return nil, errors.New("absolute drive path required")
	}
	rest := strings.TrimLeft(strings.TrimPrefix(clean, volume), `\/`)
	components := strings.FieldsFunc(rest, func(r rune) bool { return r == '\\' || r == '/' })
	if len(components) == 0 {
		return nil, errors.New("path leaf required")
	}
	current := volume + `\`
	for _, component := range components {
		current = filepath.Join(current, component)
		ptr, err := windows.UTF16PtrFromString(current)
		if err != nil {
			return nil, errors.New("invalid path")
		}
		attrs, err := windows.GetFileAttributes(ptr)
		if err != nil {
			return nil, err
		}
		if attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return nil, errors.New("reparse point rejected")
		}
	}
	info, err := os.Stat(clean)
	if err != nil {
		return nil, err
	}
	switch kind {
	case directory:
		if !info.IsDir() {
			return nil, errors.New("expected directory")
		}
	case regularFile:
		if !info.Mode().IsRegular() {
			return nil, errors.New("expected regular file")
		}
	}
	return info, nil
}

func startAcceptedApp() int {
	// Paths are fixed. Root separately verifies strict DACLs immediately before
	// invocation and attests that gate via this non-secret process variable.
	if os.Getenv("T22_RUNTIME_ACL_GATE") != "verified" {
		return refuse("root_acl_gate_required")
	}
	if acceptedSHA == "" {
		return refuse("accepted_binary_hash_not_approved")
	}
	if _, err := inspectPath(acceptedExe, regularFile); err != nil {
		return refuse("accepted_binary_missing_or_unsafe")
	}
	if _, err := inspectPath(workRoot, directory); err != nil {
		return refuse("working_root_missing_or_unsafe")
	}
	if _, err := inspectPath(storageRoot, directory); err != nil {
		return refuse("storage_root_missing_or_unsafe")
	}
	if _, err := inspectPath(privateLogs, directory); err != nil {
		return refuse("private_log_root_missing_or_unsafe")
	}
	if !acceptedBinaryMatches() {
		return refuse("accepted_binary_hash_mismatch")
	}

	kbID := strings.TrimSpace(os.Getenv("T22_REHEARSAL_KB_ID"))
	if kbID != rehearsalKBID {
		return refuse("known_rehearsal_kb_id_required")
	}
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	pgConfig, err := parseRootDSN(dsn)
	if err != nil {
		return refuse("root_database_dsn_invalid")
	}
	pgConfig.Database = databaseName // The only field changed from the approved source_test DSN.

	dotenvSecrets, err := readAppSecrets()
	if err != nil {
		return refuse("protected_app_config_unavailable")
	}
	defer clearStringMap(dotenvSecrets)
	if dotenvSecrets["SYSTEM_AES_KEY"] == "" || dotenvSecrets["JWT_SECRET"] == "" {
		return refuse("required_app_secrets_unavailable")
	}

	tlsEnv, err := rootGitLabTLS()
	if err != nil {
		return refuse("root_gitlab_tls_policy_invalid")
	}

	if err := verifyDatabaseReadOnly(pgConfig, kbID); err != nil {
		return refuse("database_preflight_failed_or_unknown")
	}
	redisPassword := os.Getenv("SOURCE_TEST_REDIS_PASSWORD")
	if strings.TrimSpace(redisPassword) == "" {
		return refuse("isolated_redis_password_required")
	}
	if err := verifyIsolatedRedis(redisPassword); err != nil {
		return refuse("isolated_redis_not_empty_or_unavailable")
	}
	if !loopbackPortAvailable(serverAddress) {
		return refuse("backend_port_unavailable")
	}

	logFile, logPath, err := createPrivateLog()
	if err != nil {
		return refuse("private_log_create_failed")
	}

	cmd := exec.Command(acceptedExe)
	cmd.Dir = workRoot
	cmd.Env = childEnvironment(pgConfig, dotenvSecrets, tlsEnv, redisPassword, logPath)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return refuse("accepted_app_start_failed")
	}
	pid := cmd.Process.Pid
	_ = cmd.Process.Release()
	_ = logFile.Close()
	fmt.Printf("mode=start status=started pid=%d log=protected\n", pid)
	return 0
}

func refuse(category string) int {
	fmt.Fprintf(os.Stderr, "refused: %s\n", category)
	return 2
}

func acceptedBinaryMatches() bool {
	if acceptedSHA == "" {
		return false
	}
	f, err := os.Open(acceptedExe)
	if err != nil {
		return false
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false
	}
	return hex.EncodeToString(h.Sum(nil)) == acceptedSHA
}

func parseRootDSN(raw string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.User == nil {
		return nil, errors.New("invalid PostgreSQL URI")
	}
	if u.Hostname() != databaseHost || u.Port() != "57822" || strings.TrimPrefix(u.Path, "/") != databaseSeed {
		return nil, errors.New("source DSN is not the approved loopback database")
	}
	if _, ok := u.User.Password(); !ok || u.User.Username() == "" {
		return nil, errors.New("admin credentials are incomplete")
	}
	query := u.Query()
	for _, key := range []string{"host", "hostaddr", "port", "dbname", "user", "password", "service"} {
		if query.Has(key) {
			return nil, errors.New("DSN query may not redirect the connection")
		}
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil || cfg == nil || cfg.Host != databaseHost || cfg.Port != databasePort || cfg.Database != databaseSeed || cfg.User == "" || cfg.Password == "" {
		return nil, errors.New("parsed DSN is not the approved loopback database")
	}
	return cfg, nil
}

func readAppSecrets() (map[string]string, error) {
	primary, err := readDotenv(primaryEnv, false)
	if err != nil {
		return nil, err
	}
	merged := map[string]string{}
	mergeSelected(merged, primary)
	clearStringMap(primary)
	local, err := readDotenv(localEnv, true)
	if err != nil {
		clearStringMap(merged)
		return nil, err
	}
	mergeSelected(merged, local)
	clearStringMap(local)
	return merged, nil
}

func readDotenv(path string, optional bool) (map[string]string, error) {
	info, err := inspectPath(path, regularFile)
	if err != nil {
		if optional && isNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if info.Size() > maxDotenvBytes {
		return nil, errors.New("dotenv file too large")
	}
	values, err := godotenv.Read(path)
	if err != nil {
		return nil, errors.New("dotenv parse failed")
	}
	return values, nil
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

func mergeSelected(dst, src map[string]string) {
	for _, key := range []string{"SYSTEM_AES_KEY", "JWT_SECRET"} {
		if value, ok := src[key]; ok {
			dst[key] = value
		}
	}
}

func clearStringMap(values map[string]string) {
	for key := range values {
		values[key] = ""
	}
}

func rootGitLabTLS() (map[string]string, error) {
	origin, originSet := os.LookupEnv("GITLAB_TLS_INSECURE_ORIGIN")
	until, untilSet := os.LookupEnv("GITLAB_TLS_INSECURE_UNTIL")
	if originSet != untilSet {
		return nil, errors.New("incomplete TLS exception")
	}
	if !originSet || (origin == "" && until == "") {
		return map[string]string{}, nil
	}
	if origin != "https://gitlab.p.it" || until != frozenTLSDeadline || origin != strings.TrimSpace(origin) || until != strings.TrimSpace(until) {
		return nil, errors.New("invalid TLS exception")
	}
	u, err := url.Parse(origin)
	if err != nil || u == nil || !strings.EqualFold(u.Scheme, "https") || u.Host == "" || u.User != nil || u.Opaque != "" ||
		u.Path != "" || u.RawPath != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return nil, errors.New("invalid TLS origin")
	}
	deadline, err := time.Parse(time.RFC3339, until)
	if err != nil || !deadline.After(time.Now()) || deadline.After(time.Now().Add(24*time.Hour)) {
		return nil, errors.New("invalid TLS deadline")
	}
	return map[string]string{
		"GITLAB_TLS_INSECURE_ORIGIN": origin,
		"GITLAB_TLS_INSECURE_UNTIL":  until,
	}, nil
}

func verifyDatabaseReadOnly(cfg *pgx.ConnConfig, kbID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var transactionReadOnly string
	if err := tx.QueryRow(ctx, `SHOW transaction_read_only`).Scan(&transactionReadOnly); err != nil || transactionReadOnly != "on" {
		return errors.New("database transaction is not read-only")
	}

	rows, err := tx.Query(ctx, `SELECT version, dirty FROM public.schema_migrations`)
	if err != nil {
		return err
	}
	rowCount := 0
	var version int64
	var dirty bool
	for rows.Next() {
		if err := rows.Scan(&version, &dirty); err != nil {
			rows.Close()
			return err
		}
		rowCount++
	}
	rowsErr := rows.Err()
	rows.Close()
	if rowsErr != nil || rowCount != 1 || version != 121 || dirty {
		return errors.New("migration ledger is not exactly 121 clean")
	}

	var knownKB bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM public.knowledge_bases
		WHERE id::text=$1 AND tenant_id=10000 AND deleted_at IS NULL AND vector_store_id IS NULL
	)`, kbID).Scan(&knownKB); err != nil || !knownKB {
		return errors.New("known T22 knowledge base missing or externally indexed")
	}

	guards := []string{
		`SELECT count(*) FROM public.knowledges WHERE deleted_at IS NULL AND parse_status IN ('pending','processing','finalizing')`,
		`SELECT count(*) FROM public.data_sources WHERE deleted_at IS NULL AND status NOT IN ('paused','deleted')`,
		`SELECT count(*) FROM public.sync_logs WHERE status IN ('queued','running')`,
		`SELECT count(*) FROM public.source_sync_states WHERE pending_sync_log_id IS NOT NULL OR active_sync_log_id IS NOT NULL OR lease_owner IS NOT NULL OR lease_expires_at IS NOT NULL`,
		`SELECT count(*) FROM public.source_sync_runs WHERE phase IN ('queued','running','waiting_for_catch_up','retry_wait')`,
		`SELECT count(*) FROM public.task_pending_ops WHERE task_type IN ('wiki:ingest','wiki:finalize','source:wiki:update')`,
		`SELECT count(*) FROM public.source_publication_outbox WHERE status IN ('pending','running')`,
		`SELECT count(*) FROM public.source_cleanup_operations WHERE status IN ('pending','running')`,
		`SELECT count(*) FROM public.source_wiki_attempts WHERE status IN ('queued','running')`,
		`SELECT count(*) FROM public.source_wiki_batches WHERE status IN ('queued','running')`,
		`SELECT count(*) FROM public.source_wiki_update_plans WHERE status IN ('pending','running')`,
		`SELECT count(*) FROM public.source_wiki_update_plan_items WHERE state IN ('pending','running')`,
		`SELECT count(*) FROM public.storage_backends WHERE deleted_at IS NULL AND status='active' AND lower(provider)<>'local'`,
		`SELECT count(*) FROM public.tenants t JOIN public.storage_backends b ON b.id=t.default_storage_backend_id WHERE lower(coalesce(b.provider,''))<>'local'`,
		`SELECT count(*) FROM public.knowledge_bases k JOIN public.storage_backends b ON b.id=k.storage_backend_id WHERE lower(coalesce(b.provider,''))<>'local'`,
		`SELECT count(*) FROM public.tenants WHERE storage_engine_config IS NOT NULL AND nullif(trim(storage_engine_config->>'default_provider'),'') IS NOT NULL AND lower(trim(storage_engine_config->>'default_provider'))<>'local'`,
		`SELECT count(*) FROM public.knowledge_bases WHERE nullif(trim(storage_provider_config->>'provider'),'') IS NOT NULL AND lower(trim(storage_provider_config->>'provider'))<>'local'`,
		`SELECT count(*) FROM public.knowledge_bases WHERE nullif(trim(cos_config->>'provider'),'') IS NOT NULL AND lower(trim(cos_config->>'provider'))<>'local'`,
	}
	for _, query := range guards {
		var count int64
		if err := tx.QueryRow(ctx, query).Scan(&count); err != nil || count != 0 {
			return errors.New("runtime safety guard is nonzero or schema is unknown")
		}
	}
	return nil
}

func verifyIsolatedRedis(password string) error {
	conn, err := net.DialTimeout("tcp4", redisAddress, 2*time.Second)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	r := bufio.NewReader(conn)
	if err := writeRedisAuth(conn, password); err != nil {
		return err
	}
	line, err := r.ReadString('\n')
	if err != nil || line != "+OK\r\n" {
		return errors.New("isolated Redis AUTH failed")
	}
	if err := writeRedisCommand(conn, "PING"); err != nil {
		return err
	}
	line, err = r.ReadString('\n')
	if err != nil || line != "+PONG\r\n" {
		return errors.New("isolated Redis PING failed")
	}

	inspector := asynq.NewInspector(asynq.RedisClientOpt{
		Network:      "tcp",
		Addr:         redisAddress,
		Password:     password,
		DB:           0,
		DialTimeout:  2 * time.Second,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 2 * time.Second,
		PoolSize:     2,
	})
	defer inspector.Close()
	queues, err := inspector.Queues()
	if err != nil || len(queues) > len(knownAsynqQueues) {
		return errors.New("isolated Redis queue list unavailable or unbounded")
	}
	seen := make(map[string]struct{}, len(queues))
	for _, queue := range queues {
		if _, known := knownAsynqQueues[queue]; !known {
			return errors.New("isolated Redis contains an unknown queue")
		}
		if _, duplicate := seen[queue]; duplicate {
			return errors.New("isolated Redis queue list is invalid")
		}
		seen[queue] = struct{}{}
		info, err := inspector.GetQueueInfo(queue)
		if err != nil || info == nil || info.Queue != queue {
			return errors.New("isolated Redis queue info unavailable")
		}
		counts := []int{info.Size, info.Groups, info.Pending, info.Active, info.Scheduled, info.Retry,
			info.Archived, info.Completed, info.Aggregating, info.Processed, info.Failed,
			info.ProcessedTotal, info.FailedTotal}
		for _, count := range counts {
			if count < 0 {
				return errors.New("isolated Redis queue info is invalid")
			}
		}
		if info.MemoryUsage < 0 || info.Latency < 0 ||
			info.Size != info.Pending+info.Active+info.Scheduled+info.Retry+info.Aggregating+info.Archived+info.Completed {
			return errors.New("isolated Redis queue info is inconsistent")
		}
		if info.Active != 0 || info.Pending != 0 || info.Scheduled != 0 || info.Retry != 0 || info.Aggregating != 0 {
			return errors.New("isolated Redis has non-idle queued work")
		}
	}
	return nil
}

func writeRedisAuth(w io.Writer, password string) error {
	_, err := fmt.Fprintf(w, "*2\r\n$4\r\nAUTH\r\n$%d\r\n%s\r\n", len(password), password)
	return err
}

func writeRedisCommand(w io.Writer, command string) error {
	_, err := fmt.Fprintf(w, "*1\r\n$%d\r\n%s\r\n", len(command), command)
	return err
}

func loopbackPortAvailable(address string) bool {
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

func createPrivateLog() (*os.File, string, error) {
	if _, err := inspectPath(privateLogs, directory); err != nil {
		return nil, "", err
	}
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return nil, "", err
		}
		name := "accepted-app-" + time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(random) + ".log"
		path := filepath.Join(privateLogs, name)
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			return f, path, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("unique private log name unavailable")
}

func childEnvironment(cfg *pgx.ConnConfig, secrets, tlsEnv map[string]string, redisPassword, logPath string) []string {
	env := make([]string, 0, 32)
	for _, key := range []string{"PATH", "SYSTEMROOT", "WINDIR", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH", "TEMP", "TMP"} {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	values := map[string]string{
		"DB_DRIVER":                    "postgres",
		"DB_HOST":                      databaseHost,
		"DB_PORT":                      "57822",
		"DB_USER":                      cfg.User,
		"DB_PASSWORD":                  cfg.Password,
		"DB_NAME":                      databaseName,
		"AUTO_MIGRATE":                 "false",
		"AUTO_RECOVER_DIRTY":           "false",
		"SERVER_HOST":                  "127.0.0.1",
		"SERVER_PORT":                  "57825",
		"REDIS_ADDR":                   redisAddress,
		"REDIS_PASSWORD":               redisPassword,
		"REDIS_DB":                     "0",
		"LOCAL_STORAGE_BASE_DIR":       storageRoot,
		"STORAGE_TYPE":                 "local",
		"STORAGE_ALLOW_LIST":           "local",
		"SOURCE_PARSER_URL":            parserURL,
		"RETRIEVE_DRIVER":              "postgres",
		"WEKNORA_HOUSEKEEPING_ENABLED": "false",
		"GIN_MODE":                     "release",
		"LLM_DEBUG_LOG":                "false",
		"LOG_PATH":                     logPath,
		"SOURCE_RESOURCES_RUN_TIMEOUT": "2h",
		"SYSTEM_AES_KEY":               secrets["SYSTEM_AES_KEY"],
		"JWT_SECRET":                   secrets["JWT_SECRET"],
	}
	for key, value := range tlsEnv {
		values[key] = value
	}
	for key, value := range values {
		if value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}
