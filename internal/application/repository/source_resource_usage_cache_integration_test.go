//go:build integration

package repository

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSourceArtifactPayloadCachePreservesLogicalQuotaPostgres(t *testing.T) {
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SOURCE_TEST_POSTGRES_DSN is not configured")
	}
	address, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Equal(t, "/source_test", address.Path, "refusing to use a non-dedicated database")
	require.Equal(t, "127.0.0.1", address.Hostname(), "refusing to use a non-local database")
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	schema := "source_payload_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		if sqlDB, dbErr := admin.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	t.Cleanup(func() {
		if sqlDB, dbErr := db.DB(); dbErr == nil {
			_ = sqlDB.Close()
		}
	})

	require.NoError(t, db.Exec(`
		CREATE TABLE source_parsed_artifacts (
			tenant_id BIGINT NOT NULL, data_source_id VARCHAR(36) NOT NULL,
			artifact_key TEXT NOT NULL, parsed JSONB NOT NULL,
			PRIMARY KEY (tenant_id,data_source_id,artifact_key)
		);
		CREATE TABLE source_embedding_artifacts (
			tenant_id BIGINT NOT NULL, data_source_id VARCHAR(36) NOT NULL,
			artifact_key TEXT NOT NULL, vector JSONB NOT NULL,
			PRIMARY KEY (tenant_id,data_source_id,artifact_key)
		);
		CREATE TABLE source_files (
			id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, data_source_id VARCHAR(36) NOT NULL
		);
		CREATE TABLE source_file_versions (
			id VARCHAR(36) PRIMARY KEY, source_file_id VARCHAR(36) NOT NULL, content BYTEA NOT NULL
		);
		CREATE TABLE source_chunk_references (chunk_id TEXT NOT NULL, source_file_id VARCHAR(36) NOT NULL);
		CREATE TABLE embeddings (chunk_id TEXT NOT NULL, dimension INTEGER NOT NULL);
	`).Error)

	tenantA, tenantB := uint64(10001), uint64(10002)
	sharedSourceID, otherSourceID := uuid.NewString(), uuid.NewString()
	type seededSource struct {
		tenantID                 uint64
		sourceID, fileID         string
		versionID, chunkID       string
		original, parsed, vector string
		dimension                int
	}
	seeded := []seededSource{
		{tenantID: tenantA, sourceID: sharedSourceID, fileID: uuid.NewString(), versionID: uuid.NewString(), chunkID: uuid.NewString(), original: "source-a-original", parsed: `{"fixture":"tenant-a-shared","items":[1,2]}`, vector: `[1,0,0]`, dimension: 3},
		{tenantID: tenantB, sourceID: sharedSourceID, fileID: uuid.NewString(), versionID: uuid.NewString(), chunkID: uuid.NewString(), original: "source-b-original-longer", parsed: `{"fixture":"tenant-b-shared","items":[1,2,3,4]}`, vector: `[1,0,0,0,0,0]`, dimension: 6},
		{tenantID: tenantA, sourceID: otherSourceID, fileID: uuid.NewString(), versionID: uuid.NewString(), chunkID: uuid.NewString(), original: "source-c-original-longest", parsed: `{"fixture":"tenant-a-other","items":[1,2,3,4,5,6]}`, vector: `[1,0,0,0,0,0,0,0,0]`, dimension: 9},
	}
	for _, source := range seeded {
		require.NoError(t, db.Exec("INSERT INTO source_files(id,tenant_id,data_source_id) VALUES (?,?,?)", source.fileID, source.tenantID, source.sourceID).Error)
		require.NoError(t, db.Exec("INSERT INTO source_file_versions(id,source_file_id,content) VALUES (?,?,?)", source.versionID, source.fileID, []byte(source.original)).Error)
		require.NoError(t, db.Exec("INSERT INTO source_chunk_references(chunk_id,source_file_id) VALUES (?,?)", source.chunkID, source.fileID).Error)
		require.NoError(t, db.Exec("INSERT INTO embeddings(chunk_id,dimension) VALUES (?,?)", source.chunkID, source.dimension).Error)
		require.NoError(t, db.Exec("INSERT INTO source_parsed_artifacts(tenant_id,data_source_id,artifact_key,parsed) VALUES (?,?,?,?::jsonb)", source.tenantID, source.sourceID, "legacy", source.parsed).Error)
		require.NoError(t, db.Exec("INSERT INTO source_embedding_artifacts(tenant_id,data_source_id,artifact_key,vector) VALUES (?,?,?,?::jsonb)", source.tenantID, source.sourceID, "legacy", source.vector).Error)
	}

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	migration, err := os.ReadFile(filepath.Join(root, "migrations", "versioned", "000121_source_artifact_logical_payload_bytes.up.sql"))
	require.NoError(t, err)
	require.NoError(t, db.Exec(string(migration)).Error)
	var cachedLegacyRows int64
	require.NoError(t, db.Raw(`SELECT
		(SELECT COUNT(*) FROM source_parsed_artifacts WHERE artifact_key='legacy' AND logical_payload_bytes IS NOT NULL)+
		(SELECT COUNT(*) FROM source_embedding_artifacts WHERE artifact_key='legacy' AND logical_payload_bytes IS NOT NULL)`).Scan(&cachedLegacyRows).Error)
	require.Zero(t, cachedLegacyRows, "migration leaves historical artifact metadata NULL for bounded follow-up backfill")

	var constraintStates []struct {
		Name      string `gorm:"column:conname"`
		Validated bool   `gorm:"column:convalidated"`
	}
	require.NoError(t, db.Raw(`SELECT conname, convalidated FROM pg_constraint
		WHERE conrelid IN ('source_parsed_artifacts'::regclass,'source_embedding_artifacts'::regclass)
		AND conname IN ('source_parsed_artifacts_logical_payload_bytes_check','source_embedding_artifacts_logical_payload_bytes_check')
		ORDER BY conname`).Scan(&constraintStates).Error)
	require.Len(t, constraintStates, 2)
	for _, constraint := range constraintStates {
		require.False(t, constraint.Validated, "migration adds the new-write CHECK without scanning legacy rows")
	}

	legacyUsage := func(tenant uint64, sourceID string) types.SourceResourceUsage {
		t.Helper()
		var usage types.SourceResourceUsage
		err := db.Raw(`SELECT
			COALESCE((SELECT SUM(octet_length(v.content)) FROM source_file_versions v JOIN source_files f ON f.id=v.source_file_id WHERE f.tenant_id=? AND f.data_source_id=?),0)::bigint AS original_bytes,
			COALESCE((SELECT SUM(octet_length(a.parsed::text)) FROM source_parsed_artifacts a WHERE a.tenant_id=? AND a.data_source_id=?),0)::bigint AS parsed_cache_bytes,
			(COALESCE((SELECT SUM(jsonb_array_length(a.vector)::bigint*4) FROM source_embedding_artifacts a WHERE a.tenant_id=? AND a.data_source_id=?),0)+
			 COALESCE((SELECT SUM(e.dimension::bigint*4) FROM embeddings e JOIN source_chunk_references cr ON cr.chunk_id=e.chunk_id JOIN source_files f ON f.id=cr.source_file_id WHERE f.tenant_id=? AND f.data_source_id=?),0))::bigint AS vector_bytes`,
			tenant, sourceID, tenant, sourceID, tenant, sourceID, tenant, sourceID).Scan(&usage).Error
		require.NoError(t, err)
		return usage
	}
	repository := NewSourceSnapshotRepository(db)
	for _, scope := range []struct {
		tenant   uint64
		sourceID string
	}{{tenantA, sharedSourceID}, {tenantB, sharedSourceID}, {tenantA, otherSourceID}} {
		usage, usageErr := repository.GetSourceResourceUsage(context.Background(), scope.tenant, scope.sourceID)
		require.NoError(t, usageErr)
		require.Equal(t, legacyUsage(scope.tenant, scope.sourceID), usage, "NULL cache metadata must preserve the original scoped formula")
	}

	parsed := &types.ParsedSourceFile{
		ParserVersion: "payload-cache-test", SHA256: strings.Repeat("a", 64), ByteLength: 12,
		Encoding: "utf-8", Quality: "structural", Symbols: []types.SourceSymbol{},
		Chunks: []types.ParsedSourceChunk{}, Facts: []types.ParsedSourceFact{}, Diagnostics: []types.ParsedSourceDiagnostic{},
	}
	require.NoError(t, repository.SaveParsedArtifact(context.Background(), tenantA, sharedSourceID, "fresh", parsed))
	require.NoError(t, repository.SaveEmbeddingArtifacts(context.Background(), tenantA, sharedSourceID, map[string][]float32{"fresh": []float32{1, 0, 0}}))
	var parsedBytes, parsedCanonicalBytes, vectorBytes int64
	require.NoError(t, db.Raw("SELECT logical_payload_bytes FROM source_parsed_artifacts WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Scan(&parsedBytes).Error)
	require.NoError(t, db.Raw("SELECT octet_length(parsed::text) FROM source_parsed_artifacts WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Scan(&parsedCanonicalBytes).Error)
	require.Equal(t, parsedCanonicalBytes, parsedBytes, "new parsed bytes use PostgreSQL's canonical JSONB text length")
	require.NoError(t, db.Raw("SELECT logical_payload_bytes FROM source_embedding_artifacts WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Scan(&vectorBytes).Error)
	require.EqualValues(t, 12, vectorBytes, "new vector artifacts use float32 dimension times four")
	usageAfterFresh, err := repository.GetSourceResourceUsage(context.Background(), tenantA, sharedSourceID)
	require.NoError(t, err)
	require.Equal(t, legacyUsage(tenantA, sharedSourceID), usageAfterFresh)

	duplicateParsed := *parsed
	duplicateParsed.ParserVersion = "duplicate-payload-must-not-replace"
	duplicateParsed.Facts = []types.ParsedSourceFact{{Kind: "duplicate", Name: strings.Repeat("x", 4096)}}
	require.NoError(t, repository.SaveParsedArtifact(context.Background(), tenantA, sharedSourceID, "fresh", &duplicateParsed))
	require.NoError(t, repository.SaveEmbeddingArtifacts(context.Background(), tenantA, sharedSourceID, map[string][]float32{"fresh": []float32{1, 0, 0, 0, 0}}))
	var storedParserVersion string
	require.NoError(t, db.Raw("SELECT parsed->>'parser_version' FROM source_parsed_artifacts WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Scan(&storedParserVersion).Error)
	require.Equal(t, parsed.ParserVersion, storedParserVersion, "ON CONFLICT DO NOTHING keeps the prior artifact and its measured bytes")
	var storedVectorBytes int64
	require.NoError(t, db.Raw("SELECT logical_payload_bytes FROM source_embedding_artifacts WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Scan(&storedVectorBytes).Error)
	require.EqualValues(t, 12, storedVectorBytes, "duplicate vector artifacts do not replace the original dimension metadata")
	usageAfterDuplicate, err := repository.GetSourceResourceUsage(context.Background(), tenantA, sharedSourceID)
	require.NoError(t, err)
	require.Equal(t, usageAfterFresh, usageAfterDuplicate)

	parsedCheckErr := db.Exec("UPDATE source_parsed_artifacts SET logical_payload_bytes=0 WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Error
	assertPayloadCheckViolation := func(err error, constraint string) {
		t.Helper()
		var pgErr *pgconn.PgError
		require.ErrorAs(t, err, &pgErr)
		require.Equal(t, "23514", pgErr.Code)
		require.Equal(t, constraint, pgErr.ConstraintName)
	}
	assertPayloadCheckViolation(parsedCheckErr, "source_parsed_artifacts_logical_payload_bytes_check")
	vectorCheckErr := db.Exec("UPDATE source_embedding_artifacts SET logical_payload_bytes=0 WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Error
	assertPayloadCheckViolation(vectorCheckErr, "source_embedding_artifacts_logical_payload_bytes_check")
	require.NoError(t, db.Exec("UPDATE source_parsed_artifacts SET logical_payload_bytes=NULL WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Error)
	require.NoError(t, db.Exec("UPDATE source_embedding_artifacts SET logical_payload_bytes=NULL WHERE tenant_id=? AND data_source_id=? AND artifact_key='fresh'", tenantA, sharedSourceID).Error)
	usageAfterNull, err := repository.GetSourceResourceUsage(context.Background(), tenantA, sharedSourceID)
	require.NoError(t, err)
	require.Equal(t, legacyUsage(tenantA, sharedSourceID), usageAfterNull, "cleared metadata falls back to the exact old JSONB formulas")
}
