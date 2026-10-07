//go:build windows

// t22-fixture-expiry-extend is a narrowly scoped test-fixture helper. Its default
// mode checks file presence only; --extend is the sole mode that may extend the
// existing approved fixture API key in the isolated rehearsal database.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
	"golang.org/x/sys/windows"
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

	acceptedKBID    = "65658207-a2ec-47fb-bf0f-11e7b685369e"
	keyID           = uint64(1)
	tenantID        = uint64(10000)
	keyName         = "t22-rehearsal-20261006"
	oldKeyExpiryISO = "2026-10-06T18:00:00Z"
	newKeyExpiryISO = "2026-10-07T10:45:00Z"

	dotenvPath = `D:\Project-Weknora\WeKnora\.env`
	localEnv   = `D:\Project-Weknora\WeKnora\.env.local`
	privateDir = `C:\Users\28211\.codex\test-runners\t22-isolated-runtime-logs`
	apiKeyFile = `C:\Users\28211\.codex\test-runners\t22-isolated-runtime-logs\t22-rehearsal-api-key.json`

	maxEnvBytes        = 1 << 20
	maxKeyBytes        = 16 << 10
	transactionTimeout = 20 * time.Second
	maxFutureExpiry    = 5 * time.Hour
)

var wikiCapabilities = []string{
	string(types.APIKeyCapabilityManageDataSources),
	string(types.APIKeyCapabilityRetrieve),
	string(types.APIKeyCapabilityChat),
	string(types.APIKeyCapabilityIngest),
}

type safeResult struct {
	Stage    string `json:"stage"`
	Status   string `json:"status"`
	KeyID    uint64 `json:"key_id,omitempty"`
	TenantID uint64 `json:"tenant_id,omitempty"`
	KBID     string `json:"knowledge_base_id,omitempty"`
}

type pathKind int

const (
	regularFile pathKind = iota
	directory
)

type keyArtifact struct {
	Token     string `json:"token"`
	ID        uint64 `json:"id"`
	TenantID  uint64 `json:"tenant_id"`
	KBID      string `json:"kb_id"`
	ExpiresAt string `json:"expires_at"`
}

type fileIdentity struct {
	VolumeSerialNumber uint32
	FileIndexHigh      uint32
	FileIndexLow       uint32
}

type fileSnapshot struct {
	Identity fileIdentity
	Digest   [32]byte
}

type rawKeyRow struct {
	ID               uint64
	TenantID         sql.NullInt64
	ScopeType        string
	Name             string
	KeyHashDigest    [32]byte
	APICipherDigest  [32]byte
	FullAccess       bool
	KnowledgeBaseIDs []byte
	Capabilities     []byte
	LastUsedAt       sql.NullTime
	ExpiresAt        sql.NullTime
	RevokedAt        sql.NullTime
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		result, code := statOnly()
		writeResult(result)
		return code
	}
	if len(args) != 1 || args[0] != "--extend" {
		writeResult(safeResult{Stage: "arguments", Status: "rejected"})
		return 2
	}

	log.SetOutput(io.Discard)
	logger.SetOutput(io.Discard)
	result, code := extendExpiry()
	writeResult(result)
	return code
}

func writeResult(result safeResult) {
	_ = json.NewEncoder(os.Stdout).Encode(result)
}

func statOnly() (safeResult, int) {
	dirState := pathState(privateDir, directory)
	fileState := "not_checked_parent_unavailable"
	if dirState == "present" {
		fileState = pathPresence(apiKeyFile)
	}
	if dirState != "present" {
		return safeResult{Stage: "default", Status: "private_path_unsafe_or_unavailable"}, 1
	}
	if fileState == "present_contents_not_read" || fileState == "absent" {
		return safeResult{Stage: "default", Status: "stat_only"}, 0
	}
	return safeResult{Stage: "default", Status: "fixture_path_unsafe_or_unavailable"}, 1
}

func extendExpiry() (safeResult, int) {
	debugSetting := strings.TrimSpace(os.Getenv("LLM_DEBUG_LOG"))
	if (debugSetting != "false" && debugSetting != "0") || logger.LLMDebugEnabled() {
		return reject("logging")
	}
	if os.Getenv(aclGateEnv) != "verified" {
		return reject("root_acl_gate_required")
	}
	if _, err := inspectPath(privateDir, directory); err != nil {
		return reject("private_directory_unsafe")
	}

	pgConfig, err := parseSourceDSN(os.Getenv(sourceDSNEnv))
	if err != nil {
		return reject("source_database_dsn_invalid")
	}
	artifact, fixtureSnapshot, err := readArtifact()
	if err != nil {
		return reject("protected_fixture_file_unavailable")
	}
	defer wipeString(&artifact.Token)
	oldExpiry, err := validateArtifact(artifact)
	if err != nil {
		return reject("fixture_metadata_invalid")
	}
	newExpiry, err := time.Parse(time.RFC3339, newKeyExpiryISO)
	if err != nil || !newExpiry.After(oldExpiry) ||
		validateArtifactExpiry(newExpiry, time.Now().UTC()) != nil {
		return reject("target_expiry_invalid")
	}
	newExpiry = newExpiry.UTC()

	aesKey, err := readSystemAESKey()
	if err != nil {
		return reject("original_dotenv_aes_key_unavailable")
	}
	defer wipeString(&aesKey)
	restoreEnv, err := setTemporaryEnvironment(map[string]string{
		"SYSTEM_AES_KEY": aesKey,
		"LLM_DEBUG_LOG":  "false",
	})
	if err != nil {
		return reject("private_runtime_environment_setup_failed")
	}
	defer restoreEnv()
	if utils.GetAESKey() == nil {
		return reject("original_dotenv_aes_key_invalid")
	}

	sqlDB := stdlib.OpenDB(*pgConfig)
	defer sqlDB.Close()
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(0)
	ctx, cancel := context.WithTimeout(context.Background(), transactionTimeout)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return fail("database_connect")
	}
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB}), &gorm.Config{
		Logger:                 gormlogger.Default.LogMode(gormlogger.Silent),
		DisableAutomaticPing:   true,
		SkipDefaultTransaction: false,
	})
	if err != nil {
		return fail("database_adapter")
	}
	tx := db.Begin(&sql.TxOptions{Isolation: sql.LevelSerializable})
	if tx.Error != nil {
		return fail("transaction_begin")
	}
	tx = tx.WithContext(ctx)
	defer tx.Rollback()

	if err := verifyTransaction(tx, pgConfig.User); err != nil {
		return reject("database_target_or_migration_invalid")
	}
	kbTenant, err := readKnowledgeBaseTenant(tx)
	if err != nil || kbTenant != tenantID {
		return reject("approved_knowledge_base_invalid")
	}

	artifactHash := tokenHash(artifact.Token)
	defer wipeString(&artifactHash)
	row, err := readFixtureKeyRow(tx, true)
	if err != nil || validateKeyRow(row, artifactHash, oldExpiry) != nil {
		return reject("approved_key_row_mismatch")
	}
	var knowledgeBaseIDs, capabilities []string
	if json.Unmarshal(row.KnowledgeBaseIDs, &knowledgeBaseIDs) != nil ||
		json.Unmarshal(row.Capabilities, &capabilities) != nil {
		return reject("approved_key_row_mismatch")
	}

	apiKeyService := service.NewTenantAPIKeyService(apprepo.NewTenantAPIKeyRepository(tx))
	updated, updateErr := apiKeyService.UpdateAPIKey(ctx, interfaces.TenantAPIKeyUpdateRequest{
		TenantID:         tenantID,
		APIKeyID:         keyID,
		Name:             keyName,
		FullAccess:       false,
		KnowledgeBaseIDs: knowledgeBaseIDs,
		Capabilities:     capabilities,
		ExpiresAt:        &newExpiry,
	})
	if updated != nil {
		wipeString(&updated.APIKey)
	}
	if updateErr != nil || updated == nil || updated.ID != keyID {
		return fail("key_update")
	}

	postRow, err := readFixtureKeyRow(tx, false)
	if err != nil || validateKeyRow(postRow, artifactHash, newExpiry) != nil ||
		!sameKeyExceptExpiry(row, postRow) {
		return fail("key_postcheck")
	}
	postKBtenant, err := readKnowledgeBaseTenant(tx)
	if err != nil || postKBtenant != tenantID {
		return fail("knowledge_base_postcheck")
	}

	if err := tx.Commit().Error; err != nil {
		return safeResult{Stage: "commit", Status: "outcome_unknown"}, 3
	}
	artifact.ExpiresAt = newKeyExpiryISO
	if err := writeFixtureAtomically(fixtureSnapshot, artifact); err != nil {
		return safeResult{Stage: "post_commit_fixture_replace", Status: "outcome_unknown"}, 3
	}
	return safeResult{Stage: "fixture_expiry", Status: "extended"}, 0
}

func reject(stage string) (safeResult, int) {
	return safeResult{Stage: stage, Status: "rejected"}, 2
}

func fail(stage string) (safeResult, int) {
	return safeResult{Stage: stage, Status: "failed"}, 1
}

func parseSourceDSN(raw string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(raw)
	if err != nil || u == nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") ||
		u.Opaque != "" || u.Host != fmt.Sprintf("%s:%d", acceptedHost, acceptedPort) ||
		u.EscapedPath() != "/"+seedDatabase || u.Fragment != "" || u.RawFragment != "" ||
		u.ForceQuery || u.User == nil {
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
	cfg.RuntimeParams = map[string]string{"application_name": "t22-fixture-expiry-extend"}
	return cfg, nil
}

func verifyTransaction(tx *gorm.DB, expectedUser string) error {
	var databaseName, currentUser, isolation, readOnly string
	if err := tx.Raw("SELECT current_database(), current_user").Row().Scan(&databaseName, &currentUser); err != nil ||
		databaseName != testDatabase || currentUser != expectedUser {
		return errors.New("wrong connected database identity")
	}
	if err := tx.Raw("SHOW transaction_isolation").Row().Scan(&isolation); err != nil || strings.ToLower(strings.TrimSpace(isolation)) != "serializable" {
		return errors.New("transaction is not serializable")
	}
	if err := tx.Raw("SHOW transaction_read_only").Row().Scan(&readOnly); err != nil || strings.ToLower(strings.TrimSpace(readOnly)) != "off" {
		return errors.New("transaction is read only")
	}
	rows, err := tx.Raw("SELECT version, dirty FROM public.schema_migrations").Rows()
	if err != nil {
		return errors.New("migration ledger unavailable")
	}
	defer rows.Close()
	count := 0
	var version int64
	var dirty bool
	for rows.Next() {
		count++
		if count > 1 || rows.Scan(&version, &dirty) != nil {
			return errors.New("migration ledger invalid")
		}
	}
	if rows.Err() != nil || count != 1 || version != 121 || dirty {
		return errors.New("migration ledger is not exactly 121 clean")
	}
	return nil
}

func readKnowledgeBaseTenant(tx *gorm.DB) (uint64, error) {
	var actualTenant uint64
	err := tx.Raw(
		`SELECT tenant_id FROM public.knowledge_bases
		 WHERE id::text = ? AND deleted_at IS NULL`, acceptedKBID,
	).Row().Scan(&actualTenant)
	return actualTenant, err
}

func readFixtureKeyRow(tx *gorm.DB, lock bool) (rawKeyRow, error) {
	var row rawKeyRow
	var keyHash, apiCipher []byte
	query := `SELECT id, tenant_id, scope_type, name, key_hash, api_key, full_access,
		knowledge_base_ids, capabilities, last_used_at, expires_at, revoked_at,
		created_at, updated_at
		FROM public.tenant_api_keys WHERE id = ?`
	if lock {
		query += " FOR UPDATE"
	}
	err := tx.Raw(query, keyID).Row().Scan(
		&row.ID, &row.TenantID, &row.ScopeType, &row.Name, &keyHash, &apiCipher,
		&row.FullAccess, &row.KnowledgeBaseIDs, &row.Capabilities, &row.LastUsedAt,
		&row.ExpiresAt, &row.RevokedAt, &row.CreatedAt, &row.UpdatedAt,
	)
	defer clear(keyHash)
	defer clear(apiCipher)
	if err != nil {
		return rawKeyRow{}, err
	}
	row.KeyHashDigest = sha256.Sum256(keyHash)
	row.APICipherDigest = sha256.Sum256(apiCipher)
	return row, nil
}

func validateKeyRow(row rawKeyRow, expectedHash string, expectedExpiry time.Time) error {
	expectedHashBytes := []byte(expectedHash)
	expectedHashDigest := sha256.Sum256(expectedHashBytes)
	clear(expectedHashBytes)
	if row.ID != keyID || !row.TenantID.Valid || row.TenantID.Int64 != int64(tenantID) ||
		row.ScopeType != string(types.APIKeyScopeTenant) || row.Name != keyName || row.FullAccess ||
		row.KeyHashDigest != expectedHashDigest || !row.ExpiresAt.Valid || row.RevokedAt.Valid ||
		!row.ExpiresAt.Time.UTC().Equal(expectedExpiry.UTC()) {
		return errors.New("fixture key row does not match its protected artifact")
	}
	var knowledgeBaseIDs, capabilities []string
	if json.Unmarshal(row.KnowledgeBaseIDs, &knowledgeBaseIDs) != nil ||
		json.Unmarshal(row.Capabilities, &capabilities) != nil ||
		!exactSet(knowledgeBaseIDs, []string{acceptedKBID}) ||
		!exactSet(capabilities, wikiCapabilities) {
		return errors.New("fixture key scope is invalid")
	}
	return nil
}

func sameKeyExceptExpiry(before, after rawKeyRow) bool {
	return before.ID == after.ID && before.TenantID == after.TenantID &&
		before.ScopeType == after.ScopeType && before.Name == after.Name &&
		before.KeyHashDigest == after.KeyHashDigest &&
		before.APICipherDigest == after.APICipherDigest &&
		before.FullAccess == after.FullAccess &&
		bytes.Equal(before.KnowledgeBaseIDs, after.KnowledgeBaseIDs) &&
		bytes.Equal(before.Capabilities, after.Capabilities) &&
		sameNullTime(before.LastUsedAt, after.LastUsedAt) &&
		sameNullTime(before.RevokedAt, after.RevokedAt) &&
		before.CreatedAt.Equal(after.CreatedAt)
}

func sameNullTime(before, after sql.NullTime) bool {
	if before.Valid != after.Valid {
		return false
	}
	return !before.Valid || before.Time.Equal(after.Time)
}

func exactSet(values, expected []string) bool {
	if len(values) != len(expected) {
		return false
	}
	set := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		set[value] = struct{}{}
	}
	for _, value := range values {
		if _, ok := set[value]; !ok {
			return false
		}
		delete(set, value)
	}
	return len(set) == 0
}

func validateArtifact(artifact keyArtifact) (time.Time, error) {
	if artifact.Token == "" || len(artifact.Token) > 512 || !strings.HasPrefix(artifact.Token, "sk-") ||
		artifact.ID != keyID || artifact.TenantID != tenantID || artifact.KBID != acceptedKBID ||
		artifact.ExpiresAt != oldKeyExpiryISO {
		return time.Time{}, errors.New("fixture metadata does not match the approved key")
	}
	expiresAt, err := time.Parse(time.RFC3339, artifact.ExpiresAt)
	if err != nil {
		return time.Time{}, errors.New("fixture expiry is invalid")
	}
	return expiresAt.UTC(), nil
}

func validateArtifactExpiry(expiresAt, now time.Time) error {
	remaining := expiresAt.Sub(now)
	if !expiresAt.After(now) || remaining > maxFutureExpiry {
		return errors.New("fixture expiry is not future within the maximum lifetime")
	}
	return nil
}

func tokenHash(token string) string {
	material := []byte(token)
	digest := sha256.Sum256(material)
	clear(material)
	return hex.EncodeToString(digest[:])
}

func readArtifact() (keyArtifact, fileSnapshot, error) {
	var artifact keyArtifact
	f, identity, err := openArtifactFile()
	if err != nil {
		return artifact, fileSnapshot{}, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxKeyBytes+1))
	if err != nil || len(data) > maxKeyBytes {
		clear(data)
		return artifact, fileSnapshot{}, errors.New("fixture file is too large or unreadable")
	}
	defer clear(data)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		wipeString(&artifact.Token)
		return keyArtifact{}, fileSnapshot{}, errors.New("fixture artifact schema is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		wipeString(&artifact.Token)
		return keyArtifact{}, fileSnapshot{}, errors.New("fixture artifact has trailing data")
	}
	return artifact, fileSnapshot{Identity: identity, Digest: sha256.Sum256(data)}, nil
}

func openArtifactFile() (*os.File, fileIdentity, error) {
	if _, err := inspectPath(apiKeyFile, regularFile); err != nil {
		return nil, fileIdentity{}, err
	}
	f, err := os.Open(apiKeyFile)
	if err != nil {
		return nil, fileIdentity{}, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 ||
		info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		_ = f.Close()
		return nil, fileIdentity{}, errors.New("fixture file handle is unsafe")
	}
	openedInfo, err := f.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || openedInfo.Size() < 0 || openedInfo.Size() > maxKeyBytes {
		_ = f.Close()
		return nil, fileIdentity{}, errors.New("fixture file size or type is invalid")
	}
	return f, fileIdentity{
		VolumeSerialNumber: info.VolumeSerialNumber,
		FileIndexHigh:      info.FileIndexHigh,
		FileIndexLow:       info.FileIndexLow,
	}, nil
}

func fixtureMatches(snapshot fileSnapshot) bool {
	artifact, current, err := readArtifact()
	if err != nil {
		return false
	}
	wipeString(&artifact.Token)
	return current == snapshot
}

func writeFixtureAtomically(snapshot fileSnapshot, artifact keyArtifact) error {
	if _, err := inspectPath(privateDir, directory); err != nil || !fixtureMatches(snapshot) {
		return errors.New("protected fixture changed before replacement")
	}
	data, err := json.Marshal(artifact)
	if err != nil || len(data) > maxKeyBytes {
		clear(data)
		return errors.New("updated fixture serialization failed")
	}
	defer clear(data)

	var tempPath string
	var tempFile *os.File
	for attempt := 0; attempt < 8; attempt++ {
		if _, err := inspectPath(privateDir, directory); err != nil {
			return errors.New("protected fixture directory changed")
		}
		random := make([]byte, 12)
		if _, err := rand.Read(random); err != nil {
			clear(random)
			return errors.New("temporary fixture name unavailable")
		}
		name := "t22-rehearsal-api-key.json." + hex.EncodeToString(random) + ".tmp"
		clear(random)
		tempPath = filepath.Join(privateDir, name)
		tempFile, err = os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			tempPath = ""
			continue
		}
		if err != nil {
			return errors.New("temporary fixture create failed")
		}
		break
	}
	if tempFile == nil {
		return errors.New("unique temporary fixture unavailable")
	}
	removeTemp := func() {
		_ = tempFile.Close()
		if tempPath != "" {
			_ = os.Remove(tempPath)
		}
	}
	if err := tempFile.Chmod(0600); err != nil {
		removeTemp()
		return errors.New("temporary fixture permissions failed")
	}
	written, err := tempFile.Write(data)
	if err != nil || written != len(data) {
		removeTemp()
		return errors.New("temporary fixture write failed")
	}
	if err := tempFile.Sync(); err != nil {
		removeTemp()
		return errors.New("temporary fixture flush failed")
	}
	if err := tempFile.Close(); err != nil {
		_ = os.Remove(tempPath)
		return errors.New("temporary fixture close failed")
	}
	tempFile = nil
	if !fixtureMatches(snapshot) {
		_ = os.Remove(tempPath)
		return errors.New("protected fixture changed before atomic replacement")
	}
	tempName, err := windows.UTF16PtrFromString(tempPath)
	if err != nil {
		_ = os.Remove(tempPath)
		return errors.New("temporary fixture path invalid")
	}
	fixtureName, err := windows.UTF16PtrFromString(apiKeyFile)
	if err != nil {
		_ = os.Remove(tempPath)
		return errors.New("protected fixture path invalid")
	}
	flags := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	if err := windows.MoveFileEx(tempName, fixtureName, flags); err != nil {
		_ = os.Remove(tempPath)
		return errors.New("atomic fixture replacement failed")
	}
	tempPath = ""
	writtenArtifact, _, err := readArtifact()
	if err != nil {
		return errors.New("replacement fixture verification failed")
	}
	defer wipeString(&writtenArtifact.Token)
	if writtenArtifact.Token != artifact.Token || writtenArtifact.ID != artifact.ID ||
		writtenArtifact.TenantID != artifact.TenantID || writtenArtifact.KBID != artifact.KBID ||
		writtenArtifact.ExpiresAt != artifact.ExpiresAt {
		return errors.New("replacement fixture content mismatch")
	}
	return nil
}

func readSystemAESKey() (string, error) {
	primary, err := readDotenv(dotenvPath, false)
	if err != nil {
		return "", errors.New("original dotenv unavailable or invalid")
	}
	key := primary["SYSTEM_AES_KEY"]
	clearStringMap(primary)
	local, err := readDotenv(localEnv, true)
	if err != nil {
		wipeString(&key)
		return "", errors.New("optional dotenv unavailable or invalid")
	}
	if override, ok := local["SYSTEM_AES_KEY"]; ok {
		wipeString(&key)
		key = override
	}
	clearStringMap(local)
	if len(key) != 32 {
		wipeString(&key)
		return "", errors.New("SYSTEM_AES_KEY must be the original raw 32-byte value")
	}
	return key, nil
}

func readDotenv(path string, optional bool) (map[string]string, error) {
	info, err := inspectPath(path, regularFile)
	if err != nil {
		if optional && isNotExist(err) {
			return map[string]string{}, nil
		}
		return nil, err
	}
	if info.Size() < 0 || info.Size() > maxEnvBytes {
		return nil, errors.New("dotenv file too large")
	}
	values, err := godotenv.Read(path)
	if err != nil {
		return nil, errors.New("dotenv parse failed")
	}
	return values, nil
}

func clearStringMap(values map[string]string) {
	for name := range values {
		values[name] = ""
		delete(values, name)
	}
}

func isNotExist(err error) bool {
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, windows.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, windows.ERROR_PATH_NOT_FOUND)
}

type priorEnvValue struct {
	value   string
	present bool
}

func setTemporaryEnvironment(values map[string]string) (func(), error) {
	prior := make(map[string]priorEnvValue, len(values))
	restore := func() {
		for name, old := range prior {
			if old.present {
				_ = os.Setenv(name, old.value)
			} else {
				_ = os.Unsetenv(name)
			}
		}
	}
	for name, value := range values {
		old, present := os.LookupEnv(name)
		prior[name] = priorEnvValue{value: old, present: present}
		if err := os.Setenv(name, value); err != nil {
			restore()
			return nil, errors.New("environment setup failed")
		}
	}
	return restore, nil
}

func wipeString(value *string) {
	if value != nil {
		*value = ""
	}
}

func inspectPath(path string, kind pathKind) (os.FileInfo, error) {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	if volume == "" || !filepath.IsAbs(clean) {
		return nil, errors.New("absolute drive path required")
	}
	rest := strings.TrimLeft(strings.TrimPrefix(clean, volume), `\ /`)
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
		attributes, err := windows.GetFileAttributes(ptr)
		if err != nil {
			return nil, err
		}
		if attributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
			return nil, errors.New("reparse path component rejected")
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
	default:
		return nil, errors.New("unknown path kind")
	}
	return info, nil
}

func pathState(path string, kind pathKind) string {
	if _, err := inspectPath(path, kind); err != nil {
		return "missing_or_unsafe"
	}
	return "present"
}

func pathPresence(path string) string {
	if _, err := os.Lstat(path); err != nil {
		if isNotExist(err) {
			return "absent"
		}
		return "unsafe_or_unavailable"
	}
	if _, err := inspectPath(path, regularFile); err != nil {
		return "unsafe_or_unavailable"
	}
	return "present_contents_not_read"
}
