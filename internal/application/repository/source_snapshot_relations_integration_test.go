//go:build integration

package repository

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestStageRelationsBatchesEndpointMembershipReadsPostgres(t *testing.T) {
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SOURCE_TEST_POSTGRES_DSN is not configured")
	}
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse isolated PostgreSQL test DSN: %v", err)
	}
	if address.Path != "/source_test" || address.Hostname() != "127.0.0.1" {
		t.Fatal("refusing to run repository integration test outside the isolated local source_test database")
	}
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL test database: %v", err)
	}
	schema := "source_relations_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated test schema: %v", err)
	}
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	counter := &sourceMembershipQueryCounter{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{Logger: counter})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		t.Fatalf("open isolated PostgreSQL schema: %v", err)
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

	fixture := seedSourceRelationFixture(t, db)
	relations := make([]types.SourceCodeRelation, 245)
	for i := range relations {
		relations[i] = relationForEndpoints(fmt.Sprintf("pg-relation-%03d", i), fixture.files[0], fixture.files[1], fixture.versions[0], fixture.versions[1])
	}
	counter.count = 0
	if err := NewSourceSnapshotRepository(db).StageRelations(context.Background(), fixture.snapshot.TenantID, fixture.snapshot.DataSourceID, fixture.snapshot.ID, relations); err != nil {
		t.Fatalf("stage valid relations in PostgreSQL transaction: %v", err)
	}
	if counter.count > 1 {
		t.Fatalf("PostgreSQL endpoint membership reads = %d for 245 relations, want at most 1 batched read", counter.count)
	}
}
