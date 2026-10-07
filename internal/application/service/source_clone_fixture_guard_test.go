//go:build integration

package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/stretchr/testify/require"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const fixedSourceAcceptanceClone = "source_t22_live_rehearsal_20261006"

type cloneProofDiagnosticError string

func (e cloneProofDiagnosticError) Error() string { return string(e) }

func safeCloneSQLState(err error) string {
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || len(pgError.Code) != 5 {
		return "unknown"
	}
	for _, char := range pgError.Code {
		if !(char >= '0' && char <= '9') && !(char >= 'A' && char <= 'Z') {
			return "unknown"
		}
	}
	return pgError.Code
}

func fixedCloneFixtureConfig(t *testing.T, raw string) *pgx.ConnConfig {
	t.Helper()
	u, err := url.Parse(raw)
	require.True(t, err == nil && u != nil, "fixed fixture URI invalid")
	require.True(t, (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Host == "127.0.0.1:57822" && u.Path == "/source_test" && u.Fragment == "" && u.Opaque == "", "fixed fixture URI endpoint rejected")
	query := u.Query()
	require.True(t, len(query) == 1 && len(query["sslmode"]) == 1 && query.Get("sslmode") == "disable", "fixed fixture URI options rejected")
	config, err := pgx.ParseConfig(raw)
	require.True(t, err == nil, "fixed fixture PostgreSQL config invalid")
	require.True(t, config.Host == "127.0.0.1" && config.Port == 57822 && config.Database == "source_test" && config.TLSConfig == nil && len(config.Fallbacks) == 0, "fixed fixture transport rejected")
	config.Database = fixedSourceAcceptanceClone
	config.RuntimeParams = map[string]string{"search_path": "public"}
	config.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		rows, err := conn.Exec(ctx, "SELECT current_database()").ReadAll()
		if err != nil || len(rows) != 1 || len(rows[0].Rows) != 1 || len(rows[0].Rows[0]) != 1 || string(rows[0].Rows[0][0]) != fixedSourceAcceptanceClone {
			return errors.New("fixed clone connection rejected")
		}
		return nil
	}
	return config
}

func fixedCloneFixtureAdmin(t *testing.T, config *pgx.ConnConfig) *gorm.DB {
	t.Helper()
	pool := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(pgdriver.New(pgdriver.Config{Conn: pool}), &gorm.Config{Logger: quietSourceIntegrationLogger{}})
	require.True(t, err == nil, "fixed clone admin connection failed")
	return db
}

func fixedCloneFixtureDatabase(t *testing.T, admin *gorm.DB, config *pgx.ConnConfig, schema string) *gorm.DB {
	t.Helper()
	role := schema + "_role"
	// UUID-derived identifiers contain only lowercase hex and underscores.
	var database, superuser string
	require.NoError(t, admin.Raw("SELECT current_database(),current_setting('is_superuser')").Row().Scan(&database, &superuser))
	require.Equal(t, fixedSourceAcceptanceClone, database)
	require.Equal(t, "on", superuser, "fixture requires the existing dedicated admin; never grant new admin rights")
	require.NoError(t, admin.Exec("CREATE ROLE "+role+" NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS").Error)
	t.Cleanup(func() {
		// Only this UUID-derived schema and its newly created role are removed.
		require.NoError(t, admin.Exec("DROP SCHEMA IF EXISTS "+schema+" CASCADE").Error)
		require.NoError(t, admin.Exec("DROP ROLE "+role).Error)
	})
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema+" AUTHORIZATION "+role).Error)
	worker := config.Copy()
	worker.RuntimeParams = map[string]string{"search_path": schema + ",public", "role": role}
	worker.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		// Enforce identity and lack of public-table privileges on every pooled
		// connection before any synthetic migration or production query can run.
		sql := `SELECT current_database(),current_schema(),current_user,
   has_schema_privilege(current_user,'public','CREATE'),
   (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','f')
    AND (has_table_privilege(current_user,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
     OR (has_table_privilege(current_user,c.oid,'SELECT') AND NOT EXISTS (
      SELECT 1 FROM pg_depend d WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e'))))::text,
   (SELECT count(*) FROM pg_roles WHERE rolname=current_user AND
    (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolinherit))::text,
   (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relkind='S'
    AND has_sequence_privilege(current_user,c.oid,'USAGE,SELECT,UPDATE'))::text`
		result, err := conn.Exec(ctx, sql).ReadAll()
		if err != nil {
			return cloneProofDiagnosticError("restricted_fixture_proof_query_" + safeCloneSQLState(err))
		}
		if len(result) != 1 || len(result[0].Rows) != 1 || len(result[0].Rows[0]) != 7 {
			return errors.New("restricted fixture identity unavailable")
		}
		values := result[0].Rows[0]
		if string(values[0]) != fixedSourceAcceptanceClone || string(values[1]) != schema || string(values[2]) != role || string(values[3]) != "f" || string(values[4]) != "0" || string(values[5]) != "0" || string(values[6]) != "0" {
			return cloneProofDiagnosticError(fmt.Sprintf("restricted_fixture_proof_database_%t_schema_%t_role_%t_public_create_%t_application_privilege_zero_%t_role_flags_zero_%t_public_sequences_zero_%t",
				string(values[0]) == fixedSourceAcceptanceClone, string(values[1]) == schema, string(values[2]) == role, string(values[3]) != "f", string(values[4]) == "0", string(values[5]) == "0", string(values[6]) == "0"))
		}
		return nil
	}
	pool := stdlib.OpenDB(*worker)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(pgdriver.New(pgdriver.Config{Conn: pool}), &gorm.Config{Logger: quietSourceIntegrationLogger{}})
	if err != nil {
		proof := "no_validation_diagnostic"
		var diagnostic cloneProofDiagnosticError
		if errors.As(err, &diagnostic) {
			proof = string(diagnostic)
		}
		t.Logf("restricted_fixture_connection sqlstate=%s proof=%s", safeCloneSQLState(err), proof)
	}
	require.True(t, err == nil, "restricted fixture session failed")
	return db
}
