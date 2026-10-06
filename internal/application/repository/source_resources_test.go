package repository

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestSourceResourceUsageTxKeepsCanonicalNullFallbackAndScopeSQL(t *testing.T) {
	sqlDB, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)

	query := `(?s)SELECT.*SUM\(octet_length\(v\.content\)\).*FROM source_file_versions v JOIN source_files f ON f\.id=v\.source_file_id
		WHERE f\.tenant_id=\$[0-9]+ AND f\.data_source_id=\$[0-9]+\).*AS original_bytes,
		.*SUM\(COALESCE\(a\.logical_payload_bytes, octet_length\(a\.parsed::text\)\)\).*FROM source_parsed_artifacts a WHERE a\.tenant_id=\$[0-9]+ AND a\.data_source_id=\$[0-9]+\).*AS parsed_cache_bytes,
		.*SUM\(COALESCE\(a\.logical_payload_bytes, jsonb_array_length\(a\.vector\)::bigint\*4\)\).*FROM source_embedding_artifacts a WHERE a\.tenant_id=\$[0-9]+ AND a\.data_source_id=\$[0-9]+\).*\+
		.*SUM\(e\.dimension::bigint\*4\).*FROM embeddings e JOIN source_chunk_references cr ON cr\.chunk_id=e\.chunk_id
		JOIN source_files f ON f\.id=cr\.source_file_id WHERE f\.tenant_id=\$[0-9]+ AND f\.data_source_id=\$[0-9]+\).*AS vector_bytes`
	mock.ExpectQuery(query).
		WithArgs(int64(10001), "source-a", int64(10001), "source-a", int64(10001), "source-a", int64(10001), "source-a").
		WillReturnRows(sqlmock.NewRows([]string{"original_bytes", "parsed_cache_bytes", "vector_bytes"}).AddRow(17, 121, 36))

	usage, err := sourceResourceUsageTx(db.WithContext(context.Background()), 10001, "source-a")
	require.NoError(t, err)
	require.Equal(t, types.SourceResourceUsage{OriginalBytes: 17, ParsedCacheBytes: 121, VectorBytes: 36}, usage)
	require.NoError(t, mock.ExpectationsWereMet())
}
