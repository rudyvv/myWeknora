//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sourceWikiModuleProjectionSQLCapture struct {
	logger.Interface
	mu      sync.Mutex
	queries []string
}

func (c *sourceWikiModuleProjectionSQLCapture) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	query, rows := fc()
	c.mu.Lock()
	c.queries = append(c.queries, query)
	c.mu.Unlock()
	c.Interface.Trace(ctx, begin, func() (string, int64) { return query, rows }, err)
}

func (c *sourceWikiModuleProjectionSQLCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queries = nil
}

func (c *sourceWikiModuleProjectionSQLCapture) snapshot() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.queries...)
}

type sourceWikiModuleProjectionPG struct {
	db         *gorm.DB
	ctx        context.Context
	capture    *sourceWikiModuleProjectionSQLCapture
	tenantID   uint64
	kbID       string
	sourceID   string
	snapshotID string
	leaseID    string
	digest     string
}

func newSourceWikiModuleProjectionPostgres(t *testing.T) *sourceWikiModuleProjectionPG {
	t.Helper()
	dsn := os.Getenv("SOURCE_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("SOURCE_TEST_POSTGRES_DSN is not configured")
	}
	address, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse isolated PostgreSQL test DSN: %v", err)
	}
	if address.Path != "/source_test" || address.Hostname() != "127.0.0.1" {
		t.Fatal("refusing to run module-projection integration test outside the isolated local source_test database")
	}
	admin, err := gorm.Open(pgdriver.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("open isolated PostgreSQL test database: %v", err)
	}
	schema := "source_wiki_module_projection_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated module-projection schema: %v", err)
	}
	query := address.Query()
	query.Set("search_path", schema+",public")
	address.RawQuery = query.Encode()
	capture := &sourceWikiModuleProjectionSQLCapture{Interface: logger.Default.LogMode(logger.Silent)}
	db, err := gorm.Open(pgdriver.Open(address.String()), &gorm.Config{Logger: capture})
	if err != nil {
		_ = admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error
		t.Fatalf("open isolated module-projection schema: %v", err)
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
	baseSchema := `
CREATE TABLE source_snapshots (
	id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
	data_source_id VARCHAR(36) NOT NULL, state TEXT NOT NULL, manifest_complete BOOLEAN NOT NULL,
	manifest_digest TEXT NOT NULL, member_count INTEGER NOT NULL, file_count INTEGER NOT NULL,
	relations_staged BOOLEAN NOT NULL DEFAULT FALSE, wiki_derivation_state VARCHAR(32) NOT NULL DEFAULT 'pending'
);
CREATE TABLE source_publications (
	data_source_id VARCHAR(36) PRIMARY KEY, snapshot_id VARCHAR(36) NOT NULL,
	tenant_id BIGINT NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL
);
CREATE TABLE source_files (
	id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
	data_source_id VARCHAR(36) NOT NULL, path TEXT NOT NULL
);
CREATE TABLE source_file_versions (
	id VARCHAR(36) PRIMARY KEY, source_file_id VARCHAR(36) NOT NULL, snapshot_id VARCHAR(36) NOT NULL,
	blob_sha TEXT NOT NULL, sha256 TEXT NOT NULL, content BYTEA NOT NULL, parser_version TEXT NOT NULL,
	quality TEXT NOT NULL, symbols JSONB NOT NULL, facts JSONB NOT NULL
);
-- Deliberately omit uniqueness constraints on manifest identity columns so the
-- reader's fail-closed checks are exercised even for corrupted legacy rows.
CREATE TABLE source_snapshot_members (
	snapshot_id VARCHAR(36) NOT NULL, path TEXT NOT NULL, source_file_id VARCHAR(36) NOT NULL DEFAULT '',
	file_version_id VARCHAR(36) NOT NULL DEFAULT '', blob_sha TEXT NOT NULL, status TEXT NOT NULL,
	generated BOOLEAN NOT NULL DEFAULT FALSE
);
CREATE TABLE source_read_leases (id VARCHAR(36) PRIMARY KEY, expires_at TIMESTAMPTZ NOT NULL);
CREATE TABLE source_read_scopes (
	lease_id VARCHAR(36) NOT NULL, snapshot_id VARCHAR(36) NOT NULL, data_source_id VARCHAR(36) NOT NULL,
	knowledge_base_id VARCHAR(36) NOT NULL, tenant_id BIGINT NOT NULL, knowledge_ids JSONB NOT NULL, tag_ids JSONB NOT NULL
);
CREATE TABLE source_read_wiki_scopes (
	lease_id VARCHAR(36) NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL, tenant_id BIGINT NOT NULL,
	source_ids JSONB NOT NULL, knowledge_ids JSONB NOT NULL, tag_ids JSONB NOT NULL
);
CREATE TABLE data_sources (
	id VARCHAR(36) PRIMARY KEY, tenant_id BIGINT NOT NULL, knowledge_base_id VARCHAR(36) NOT NULL,
	config JSONB NOT NULL, source_query_enabled BOOLEAN NOT NULL, deleted_at TIMESTAMPTZ
);
CREATE TABLE knowledge_tag_relations (knowledge_id VARCHAR(36) NOT NULL, tag_id VARCHAR(36) NOT NULL);
`
	if err := db.Exec(baseSchema).Error; err != nil {
		t.Fatalf("create module-projection integration schema: %v", err)
	}
	fixture := &sourceWikiModuleProjectionPG{
		db: db, capture: capture, tenantID: 7, kbID: "kb-one", sourceID: "source-one",
		snapshotID: uuid.NewString(), leaseID: uuid.NewString(), digest: strings.Repeat("a", 64),
	}
	if err := db.Exec(`INSERT INTO source_snapshots
		(id,tenant_id,knowledge_base_id,data_source_id,state,manifest_complete,manifest_digest,member_count,file_count,relations_staged,wiki_derivation_state)
		VALUES (?,?,?,?, 'published',true,?,0,0,false,'deferred_capacity')`,
		fixture.snapshotID, fixture.tenantID, fixture.kbID, fixture.sourceID, fixture.digest).Error; err != nil {
		t.Fatalf("seed published source snapshot: %v", err)
	}
	if err := db.Exec(`INSERT INTO source_publications(data_source_id,snapshot_id,tenant_id,knowledge_base_id) VALUES (?,?,?,?)`,
		fixture.sourceID, fixture.snapshotID, fixture.tenantID, fixture.kbID).Error; err != nil {
		t.Fatalf("seed current source publication: %v", err)
	}
	if err := db.Exec(`INSERT INTO data_sources(id,tenant_id,knowledge_base_id,config,source_query_enabled)
		VALUES (?,?,?,'{"settings":{"content_mode":"source"}}',true)`,
		fixture.sourceID, fixture.tenantID, fixture.kbID).Error; err != nil {
		t.Fatalf("seed source permission row: %v", err)
	}
	if err := db.Exec(`INSERT INTO source_read_leases(id,expires_at) VALUES (?,?)`, fixture.leaseID, time.Now().Add(time.Hour)).Error; err != nil {
		t.Fatalf("seed source read lease: %v", err)
	}
	if err := db.Exec(`INSERT INTO source_read_scopes
		(lease_id,snapshot_id,data_source_id,knowledge_base_id,tenant_id,knowledge_ids,tag_ids)
		VALUES (?,?,?,?,?,'[]','[]')`,
		fixture.leaseID, fixture.snapshotID, fixture.sourceID, fixture.kbID, fixture.tenantID).Error; err != nil {
		t.Fatalf("seed pinned source read scope: %v", err)
	}
	if err := db.Exec(`INSERT INTO source_read_wiki_scopes
		(lease_id,knowledge_base_id,tenant_id,source_ids,knowledge_ids,tag_ids)
		VALUES (?,?,?,?::jsonb,'[]','[]')`, fixture.leaseID, fixture.kbID, fixture.tenantID,
		`["`+fixture.sourceID+`"]`).Error; err != nil {
		t.Fatalf("seed source Wiki permission scope: %v", err)
	}
	ctx, release := source.WithReadScope(context.Background(), types.SourceReadLease{ID: fixture.leaseID, HasSources: true}, nil, func() {})
	t.Cleanup(release)
	fixture.ctx = ctx
	return fixture
}

func (f *sourceWikiModuleProjectionPG) addParsed(t *testing.T, path string, generated bool, facts []types.ParsedSourceFact, content []byte) (string, string) {
	t.Helper()
	if content == nil {
		content = []byte{}
	}
	fileID, versionID := uuid.NewString(), uuid.NewString()
	blobSHA, contentSHA := strings.Repeat("b", 40), strings.Repeat("c", 64)
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		t.Fatalf("marshal fixture parser facts: %v", err)
	}
	if err := f.db.Exec(`INSERT INTO source_files(id,tenant_id,knowledge_base_id,data_source_id,path) VALUES (?,?,?,?,?)`,
		fileID, f.tenantID, f.kbID, f.sourceID, path).Error; err != nil {
		t.Fatalf("seed source file %q: %v", path, err)
	}
	if err := f.db.Exec(`INSERT INTO source_file_versions
		(id,source_file_id,snapshot_id,blob_sha,sha256,content,parser_version,quality,symbols,facts)
		VALUES (?,?,?,?,?,?,?,'structural','[]',?::jsonb)`,
		versionID, fileID, f.snapshotID, blobSHA, contentSHA, content, "fixture-parser", string(factsJSON)).Error; err != nil {
		t.Fatalf("seed source file version %q: %v", path, err)
	}
	if err := f.db.Exec(`INSERT INTO source_snapshot_members
		(snapshot_id,path,source_file_id,file_version_id,blob_sha,status,generated) VALUES (?,?,?,?,?,'parsed',?)`,
		f.snapshotID, path, fileID, versionID, blobSHA, generated).Error; err != nil {
		t.Fatalf("seed parsed manifest member %q: %v", path, err)
	}
	return fileID, versionID
}

func (f *sourceWikiModuleProjectionPG) addExcluded(t *testing.T, path string) {
	t.Helper()
	if err := f.db.Exec(`INSERT INTO source_snapshot_members
		(snapshot_id,path,source_file_id,file_version_id,blob_sha,status,generated) VALUES (?,?,'','',?,'excluded',false)`,
		f.snapshotID, path, strings.Repeat("e", 40)).Error; err != nil {
		t.Fatalf("seed excluded manifest member %q: %v", path, err)
	}
}

func (f *sourceWikiModuleProjectionPG) setCounts(t *testing.T, members, parsed int) {
	t.Helper()
	if err := f.db.Exec(`UPDATE source_snapshots SET member_count=?,file_count=? WHERE id=?`, members, parsed, f.snapshotID).Error; err != nil {
		t.Fatalf("set source snapshot manifest counts: %v", err)
	}
}

func (f *sourceWikiModuleProjectionPG) addMaxPathMembers(t *testing.T, count int) {
	t.Helper()
	const pathSQL = `WITH paths AS (
		SELECT i, 'p'||lpad(i::text,6,'0')||repeat('x',4089) AS path
		FROM generate_series(1,?) AS series(i)
	)`
	if err := f.db.Exec(pathSQL+` INSERT INTO source_files(id,tenant_id,knowledge_base_id,data_source_id,path)
		SELECT md5('projection-file-'||i::text), ?, ?, ?, path FROM paths`, count, f.tenantID, f.kbID, f.sourceID).Error; err != nil {
		t.Fatalf("seed max-path source files: %v", err)
	}
	if err := f.db.Exec(pathSQL+` INSERT INTO source_file_versions
		(id,source_file_id,snapshot_id,blob_sha,sha256,content,parser_version,quality,symbols,facts)
		SELECT md5('projection-version-'||i::text),md5('projection-file-'||i::text),?,?,?,''::bytea,
			'fixture-parser','structural','[]','[]' FROM paths`,
		count, f.snapshotID, strings.Repeat("b", 40), strings.Repeat("c", 64)).Error; err != nil {
		t.Fatalf("seed max-path source versions: %v", err)
	}
	if err := f.db.Exec(pathSQL+` INSERT INTO source_snapshot_members
		(snapshot_id,path,source_file_id,file_version_id,blob_sha,status,generated)
		SELECT ?,path,md5('projection-file-'||i::text),md5('projection-version-'||i::text),?,'parsed',false FROM paths`,
		count, f.snapshotID, strings.Repeat("b", 40)).Error; err != nil {
		t.Fatalf("seed max-path manifest members: %v", err)
	}
	f.setCounts(t, count, count)
}

func TestLoadSourceWikiModuleProjectionPagesManifestAndSelectsOnlyBestSeed(t *testing.T) {
	f := newSourceWikiModuleProjectionPostgres(t)
	payload := strings.Repeat("irrelevant-fact-payload-", 90_000)
	parityFacts := []types.ParsedSourceFact{
		{Kind: "mybatis_mapper", Name: "OrderMapper", Quality: "structural"},
		{Kind: "java_type", Name: "OrderController", Quality: "structural"},
		{Kind: "spring_mapping", Name: "first mapping", Quality: ""},
		{Kind: "spring_mapping", Name: "second mapping", Quality: "structural"},
		{Kind: "java_method", Name: "irrelevant", Quality: "structural", Text: payload},
	}
	for i := 0; i < 129; i++ {
		path := fmt.Sprintf("src/file-%03d.java", i)
		facts := []types.ParsedSourceFact{}
		generated := i == 1
		switch i {
		case 0:
			facts = parityFacts
		case 1:
			facts = []types.ParsedSourceFact{{Kind: "spring_mapping", Name: "GeneratedController", Quality: "structural"}}
		case 2:
			facts = []types.ParsedSourceFact{
				{Kind: "spring_mapping", Name: "untrusted", Quality: "approximate"},
				{Kind: "java_type", Name: "OrderServiceImpl", Quality: ""},
			}
		case 3:
			facts = []types.ParsedSourceFact{{Kind: "mybatis_result_map", Name: "OrderResult", Quality: "structural"}}
		case 128:
			facts = []types.ParsedSourceFact{{Kind: "spring_mapping", Name: "LastPageSeed", Quality: "structural"}}
		}
		content := []byte(nil)
		if i == 4 {
			content = []byte(strings.Repeat("source-content-not-returned-", 50_000))
		}
		f.addParsed(t, path, generated, facts, content)
	}
	f.addExcluded(t, "src/zz-excluded.txt")
	f.setCounts(t, 130, 129)
	f.capture.reset()

	projection, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID)
	if err != nil {
		t.Fatalf("load bounded module projection: %v", err)
	}
	if !projection.InventoryComplete || projection.RelationsComplete || !projection.RelationsDeferred {
		t.Fatalf("projection proof states = inventory:%t relations-complete:%t relations-deferred:%t, want true/false/true",
			projection.InventoryComplete, projection.RelationsComplete, projection.RelationsDeferred)
	}
	if projection.ManifestDigest != f.digest || projection.MemberCount != 130 || projection.ParsedFileCount != 129 || len(projection.Members) != 130 {
		t.Fatalf("projection identity/counts = digest:%q members:%d parsed:%d returned:%d", projection.ManifestDigest,
			projection.MemberCount, projection.ParsedFileCount, len(projection.Members))
	}
	if got := projection.Members[0].Seed; got == nil || got.Kind != "spring_mapping" || got.Name != "first mapping" || got.Ordinal != 3 || got.Priority != 110 {
		t.Fatalf("priority/ordinal seed = %+v, want first spring mapping at ordinal 3 with priority 110", got)
	}
	bestPriority, bestOrdinal := 0, 0
	for i, fact := range parityFacts {
		if priority := source.SourceWikiModuleFactPriority(fact); priority > bestPriority {
			bestPriority, bestOrdinal = priority, i+1
		}
	}
	if bestPriority != projection.Members[0].Seed.Priority || int64(bestOrdinal) != projection.Members[0].Seed.Ordinal {
		t.Fatalf("SQL seed priority/ordinal = %d/%d, shared Go selector = %d/%d", projection.Members[0].Seed.Priority,
			projection.Members[0].Seed.Ordinal, bestPriority, bestOrdinal)
	}
	if projection.Members[1].Seed != nil {
		t.Fatalf("generated file received a module seed: %+v", projection.Members[1].Seed)
	}
	if got := projection.Members[2].Seed; got == nil || got.Kind != "java_type" || got.Priority != 90 {
		t.Fatalf("Java architectural-role priority seed = %+v, want java_type/90", got)
	}
	if got := projection.Members[3].Seed; got == nil || got.Kind != "mybatis_result_map" || got.Priority != 75 {
		t.Fatalf("MyBatis priority seed = %+v, want mybatis_result_map/75", got)
	}
	if got := projection.Members[128].Seed; got == nil || got.Name != "LastPageSeed" {
		t.Fatalf("second keyset page seed = %+v, want LastPageSeed", got)
	}
	if got := projection.Members[129]; got.Status != "excluded" || got.Seed != nil {
		t.Fatalf("excluded inventory member = %+v, want counted excluded member without seed", got)
	}
	if projection.Members[0].Seed.Name == payload || strings.Contains(projection.Members[0].Seed.Name, "irrelevant-fact-payload") {
		t.Fatal("large irrelevant parser fact payload escaped into the module projection")
	}

	pageQueries := 0
	for _, query := range f.capture.snapshot() {
		lower := strings.ToLower(query)
		if strings.Contains(lower, "jsonb_array_elements") && strings.Contains(lower, "limit 128") {
			pageQueries++
			selectEnd := strings.Index(lower, " from ")
			if selectEnd < 0 {
				t.Fatalf("cannot identify projection select list in page SQL: %s", query)
			}
			selectList := lower[:selectEnd]
			for _, forbidden := range []string{"sv.facts", "sv.content", "sv.symbols", "context"} {
				if strings.Contains(selectList, forbidden) {
					t.Fatalf("projection page selected full payload field %q: %s", forbidden, query)
				}
			}
		}
	}
	if pageQueries != 2 {
		t.Fatalf("manifest page queries = %d, want 2 pages for 130 keyset members at 128/page", pageQueries)
	}
	var stored struct {
		RelationsStaged bool
		WikiState       string `gorm:"column:wiki_derivation_state"`
	}
	if err := f.db.Table("source_snapshots").Select("relations_staged,wiki_derivation_state").Where("id=?", f.snapshotID).Take(&stored).Error; err != nil {
		t.Fatalf("verify loader did not mutate relation proof: %v", err)
	}
	if stored.RelationsStaged || stored.WikiState != "deferred_capacity" {
		t.Fatalf("module projection mutated persisted relation state: staged=%t state=%q", stored.RelationsStaged, stored.WikiState)
	}
}

func TestLoadSourceWikiModuleProjectionRejectsInvalidManifestIdentity(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(*testing.T, *sourceWikiModuleProjectionPG, string, string)
	}{
		{
			name: "wrong owner",
			corrupt: func(t *testing.T, f *sourceWikiModuleProjectionPG, fileID, _ string) {
				if err := f.db.Exec("UPDATE source_files SET tenant_id=tenant_id+1 WHERE id=?", fileID).Error; err != nil {
					t.Fatalf("corrupt source file owner: %v", err)
				}
			},
		},
		{
			name: "manifest blob differs from version",
			corrupt: func(t *testing.T, f *sourceWikiModuleProjectionPG, _, versionID string) {
				if err := f.db.Exec("UPDATE source_file_versions SET blob_sha=? WHERE id=?", strings.Repeat("d", 40), versionID).Error; err != nil {
					t.Fatalf("corrupt source version blob identity: %v", err)
				}
			},
		},
		{
			name: "parsed count mismatch",
			corrupt: func(t *testing.T, f *sourceWikiModuleProjectionPG, _, _ string) {
				if err := f.db.Exec("UPDATE source_snapshots SET file_count=file_count+1 WHERE id=?", f.snapshotID).Error; err != nil {
					t.Fatalf("corrupt parsed member count: %v", err)
				}
			},
		},
		{
			name: "duplicate path",
			corrupt: func(t *testing.T, f *sourceWikiModuleProjectionPG, _, _ string) {
				if err := f.db.Exec(`INSERT INTO source_snapshot_members(snapshot_id,path,source_file_id,file_version_id,blob_sha,status,generated)
					SELECT snapshot_id,path,source_file_id,file_version_id,blob_sha,status,generated FROM source_snapshot_members WHERE snapshot_id=?`, f.snapshotID).Error; err != nil {
					t.Fatalf("duplicate source manifest member: %v", err)
				}
				if err := f.db.Exec("UPDATE source_snapshots SET member_count=2,file_count=2 WHERE id=?", f.snapshotID).Error; err != nil {
					t.Fatalf("update duplicated manifest counts: %v", err)
				}
			},
		},
		{
			name: "empty path",
			corrupt: func(t *testing.T, f *sourceWikiModuleProjectionPG, _, _ string) {
				if err := f.db.Exec("UPDATE source_snapshot_members SET path='' WHERE snapshot_id=?", f.snapshotID).Error; err != nil {
					t.Fatalf("empty source manifest path: %v", err)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSourceWikiModuleProjectionPostgres(t)
			fileID, versionID := f.addParsed(t, "src/Handler.java", false,
				[]types.ParsedSourceFact{{Kind: "java_type", Name: "HandlerService", Quality: "structural"}}, nil)
			f.setCounts(t, 1, 1)
			tt.corrupt(t, f, fileID, versionID)
			if _, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID); err == nil {
				t.Fatal("loader accepted a corrupted published manifest identity")
			}
		})
	}
}

func TestLoadSourceWikiModuleProjectionRequiresCurrentAuthorizedReadScope(t *testing.T) {
	f := newSourceWikiModuleProjectionPostgres(t)
	f.addParsed(t, "src/Handler.java", false, []types.ParsedSourceFact{{Kind: "java_type", Name: "HandlerService", Quality: "structural"}}, nil)
	f.setCounts(t, 1, 1)
	if _, err := LoadSourceWikiModuleProjection(context.Background(), f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID); err == nil {
		t.Fatal("loader accepted a source projection without a pinned source Wiki read grant")
	}
	if err := f.db.Exec("UPDATE source_publications SET snapshot_id=? WHERE data_source_id=?", uuid.NewString(), f.sourceID).Error; err != nil {
		t.Fatalf("replace current source publication: %v", err)
	}
	if _, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID); err == nil {
		t.Fatal("loader accepted a snapshot that is no longer the current publication")
	}
}

func TestLoadSourceWikiModuleProjectionRejectsManifestOverBound(t *testing.T) {
	f := newSourceWikiModuleProjectionPostgres(t)
	if err := f.db.Exec("UPDATE source_snapshots SET member_count=?,file_count=? WHERE id=?",
		types.SourceWikiSkeletonMaxFiles+1, 0, f.snapshotID).Error; err != nil {
		t.Fatalf("set over-bound source manifest count: %v", err)
	}
	if _, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID); err == nil {
		t.Fatal("loader accepted a manifest beyond the existing 50,000-member bound")
	}
}

func TestLoadSourceWikiModuleProjectionRejectsOversizedSeedWithoutTruncation(t *testing.T) {
	f := newSourceWikiModuleProjectionPostgres(t)
	name := strings.Repeat("x", sourceWikiModuleProjectionMaxSeedStringSize+1)
	f.addParsed(t, "src/Handler.java", false,
		[]types.ParsedSourceFact{{Kind: "spring_mapping", Name: name, Quality: "structural"}}, nil)
	f.setCounts(t, 1, 1)
	if _, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID); err == nil {
		t.Fatal("loader accepted or truncated a module seed beyond its per-string hard bound")
	}
}

func TestLoadSourceWikiModuleProjectionAcceptsExactPathByteBudgets(t *testing.T) {
	f := newSourceWikiModuleProjectionPostgres(t)
	f.addMaxPathMembers(t, (8<<20)/4096)
	projection, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID)
	if err != nil {
		t.Fatalf("load projection at exact aggregate path-byte limit: %v", err)
	}
	if !projection.InventoryComplete || len(projection.Members) != (8<<20)/4096 || len(projection.Members[0].Path) != 4096 {
		t.Fatalf("exact-boundary projection is incomplete: complete=%t members=%d first-path-bytes=%d",
			projection.InventoryComplete, len(projection.Members), len(projection.Members[0].Path))
	}
}

func TestLoadSourceWikiModuleProjectionRejectsPathByteOveragesWithoutPartialResult(t *testing.T) {
	tests := []struct {
		name string
		seed func(*testing.T, *sourceWikiModuleProjectionPG)
	}{
		{
			name: "single path exceeds 4096 bytes",
			seed: func(t *testing.T, f *sourceWikiModuleProjectionPG) {
				f.addParsed(t, strings.Repeat("p", 4097), false, nil, nil)
				f.setCounts(t, 1, 1)
			},
		},
		{
			name: "aggregate paths exceed 8 MiB",
			seed: func(t *testing.T, f *sourceWikiModuleProjectionPG) {
				f.addMaxPathMembers(t, (8<<20)/4096+1)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newSourceWikiModuleProjectionPostgres(t)
			tt.seed(t, f)
			projection, err := LoadSourceWikiModuleProjection(f.ctx, f.db, f.tenantID, f.kbID, f.sourceID, f.snapshotID)
			if err == nil || projection != nil {
				t.Fatalf("over-budget path inventory returned projection=%v, err=%v; want nil projection and error", projection, err)
			}
		})
	}
}
