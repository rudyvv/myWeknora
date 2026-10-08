//go:build integration

package container

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Checking multiple simultaneously held connections catches SET jit=off on
// only the startup connection, which would leave later pool connections slow.
func TestPostgresApplicationPoolAvoidsJITOnEveryConnection(t *testing.T) {
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SOURCE_TEST_POSTGRES_DSN is required")
	}
	pg, err := pgx.ParseConfig(dsn)
	require.NoError(t, err)
	t.Setenv("DB_DRIVER", "postgres")
	t.Setenv("DB_HOST", pg.Host)
	t.Setenv("DB_PORT", strconv.Itoa(int(pg.Port)))
	t.Setenv("DB_USER", pg.User)
	t.Setenv("DB_PASSWORD", pg.Password)
	t.Setenv("DB_NAME", pg.Database)
	t.Setenv("AUTO_MIGRATE", "false")
	db, err := initDatabase(&config.Config{})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { _ = pool.Close() })
	pool.SetMaxIdleConns(0)
	for round := 0; round < 2; round++ {
		connections := make([]*sql.Conn, 0, 3)
		control, err := pgx.ConnectConfig(context.Background(), pg)
		require.NoError(t, err)
		t.Cleanup(func() { _ = control.Close(context.Background()) })
		var defaultBefore string
		require.NoError(t, control.QueryRow(context.Background(), "SHOW jit").Scan(&defaultBefore))
		for i := 0; i < 3; i++ {
			conn, err := pool.Conn(context.Background())
			require.NoError(t, err)
			connections = append(connections, conn)
			t.Cleanup(func() { _ = conn.Close() })
			var setting string
			require.NoError(t, conn.QueryRowContext(context.Background(), "SHOW jit").Scan(&setting))
			require.Equal(t, "off", setting, "short source/Wiki reads must not pay JIT compilation on fresh pool connections")
		}
		var defaultAfter string
		require.NoError(t, control.QueryRow(context.Background(), "SHOW jit").Scan(&defaultAfter))
		require.Equal(t, defaultBefore, defaultAfter, "application setup must not change the independent connection")
		for _, conn := range connections {
			_ = conn.Close()
		}
	}
}
