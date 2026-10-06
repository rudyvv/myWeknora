package postgres

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type diversityCandidateFixture struct {
	ID              string `gorm:"primaryKey"`
	ChunkID         string `gorm:"uniqueIndex"`
	Content         string
	SourceID        string
	SourceType      int
	KnowledgeID     string
	KnowledgeBaseID string
	TagID           string
	Score           float64
	IsPublished     bool
	InReadScope     bool
	Dimension       int
	IsEnabled       bool
}

func (diversityCandidateFixture) TableName() string { return "embeddings" }

type diversityReferenceFixture struct {
	ChunkID       string `gorm:"primaryKey"`
	SnapshotID    string
	SourceFileID  string
	FileVersionID string
	Path          string
}

func (diversityReferenceFixture) TableName() string { return "source_chunk_references" }

type diversityResultFixture struct {
	ID      string
	ChunkID string
	Score   float64
}

func TestSourceCandidateDiversityActivationAndPoolBounds(t *testing.T) {
	tests := []struct {
		name       string
		sourceView bool
		params     types.RetrieveParams
		want       bool
	}{
		{
			name:       "natural source query with explicit sources",
			sourceView: true,
			params:     types.RetrieveParams{Query: "追踪确认接口和服务层更新路径", TopK: 50, SourceIDs: []string{"source-a"}},
			want:       true,
		},
		{
			name:       "ordinary document retrieval",
			sourceView: false,
			params:     types.RetrieveParams{Query: "追踪确认接口和服务层更新路径", TopK: 50, SourceIDs: []string{"source-a"}},
		},
		{
			name:       "source scope absent",
			sourceView: true,
			params:     types.RetrieveParams{Query: "追踪确认接口和服务层更新路径", TopK: 50},
		},
		{
			name:       "explicit identifier query",
			sourceView: true,
			params:     types.RetrieveParams{Query: "SignupServiceImpl", TopK: 50, SourceIDs: []string{"source-a"}},
		},
		{
			name:       "explicit path query",
			sourceView: true,
			params:     types.RetrieveParams{Query: "src/main/java/demo/SignupService.java", TopK: 50, SourceIDs: []string{"source-a"}},
		},
		{
			name:       "blank precomputed-vector query",
			sourceView: true,
			params:     types.RetrieveParams{TopK: 50, SourceIDs: []string{"source-a"}},
		},
		{
			name:       "topk above bounded pool",
			sourceView: true,
			params:     types.RetrieveParams{Query: "自然语言查询", TopK: 201, SourceIDs: []string{"source-a"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldDiversifySourceCandidates(tt.sourceView, tt.params); got != tt.want {
				t.Fatalf("shouldDiversifySourceCandidates() = %t, want %t", got, tt.want)
			}
		})
	}

	for _, tt := range []struct{ topK, want int }{{1, 100}, {50, 100}, {100, 200}, {150, 200}, {200, 200}} {
		if got := sourceCandidatePoolSize(tt.topK); got != tt.want {
			t.Fatalf("sourceCandidatePoolSize(%d) = %d, want %d", tt.topK, got, tt.want)
		}
	}
}

func TestKeywordSourcePrioritySQLFiltersBoundedPoolBeforePartition(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	gormDB, err := gorm.Open(pgdriver.New(pgdriver.Config{Conn: db, PreferSimpleProtocol: true}), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	scopedCtx, release := source.WithReadScope(context.Background(), types.SourceReadLease{
		ID: "ae79c267-78dd-4c22-9c68-30f952adb38b", HasSources: true,
	}, nil, func() {})
	defer release()
	conditions := []clause.Expression{
		clause.Expr{SQL: source.PublishedChunkSQL(scopedCtx, "embeddings.chunk_id", "embeddings.knowledge_id")},
		clause.Expr{SQL: sourceRepositoryFilterSQL("?"), Vars: []interface{}{"source-a"}},
		clause.Eq{Column: clause.Column{Table: "embeddings", Name: "knowledge_base_id"}, Value: "kb-a"},
		clause.IN{Column: clause.Column{Table: "embeddings", Name: "knowledge_id"}, Values: []interface{}{"file-a"}},
		clause.Expr{SQL: sourceTagFilterSQL("?,?"), Vars: []interface{}{"tag-a", "tag-b", "tag-a", "tag-b"}},
		clause.Expr{SQL: "dimension = ?", Vars: []interface{}{1536}},
		clause.Expr{SQL: "(is_enabled IS NULL OR is_enabled = ?)", Vars: []interface{}{true}},
		clause.Expr{SQL: "content ||| ?", Vars: []interface{}{"追踪确认接口"}},
		clause.OrderBy{Columns: []clause.OrderByColumn{{Column: clause.Column{Name: "score"}, Desc: true}}},
	}
	bounded := buildKeywordCandidateQuery(gormDB, conditions, sourceCandidatePoolSize(50), "paradedb.score(id)")
	query := applySourceFileVersionPriority(gormDB, bounded, 50)
	result := query.Find(&[]pgVectorWithScore{})
	if result.Error != nil {
		t.Fatal(result.Error)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	sql := result.Statement.SQL.String()
	for _, required := range []string{
		"paradedb.score(id) as score",
		"source_read_scopes rs",
		"knowledge_tag_relations",
		"\"embeddings\".\"knowledge_id\" = $3",
		"source_chunk_references quota_refs",
		"PARTITION BY quota_refs.snapshot_id, quota_refs.source_file_id, quota_refs.file_version_id",
		"ORDER BY source_bounded_candidates.score DESC, source_bounded_candidates.chunk_id ASC",
		"CASE WHEN source_ranked_candidates.source_file_rank <= 4 THEN 0 ELSE 1 END ASC",
		"LIMIT $11",
		"LIMIT $12",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("generated keyword SQL missing %q:\n%s", required, sql)
		}
	}
	poolLimit := strings.Index(sql, "LIMIT $11")
	quotaJoin := strings.Index(sql, "JOIN source_chunk_references quota_refs")
	priorityOrder := strings.Index(sql, "CASE WHEN source_ranked_candidates.source_file_rank <= 4 THEN 0 ELSE 1 END ASC")
	if poolLimit < 0 || quotaJoin < poolLimit || priorityOrder < quotaJoin {
		t.Fatalf("keyword SQL must scope/bound candidates before prioritizing per-file heads:\n%s", sql)
	}
	if strings.Contains(sql, "WHERE source_ranked_candidates.source_file_rank") {
		t.Fatalf("keyword SQL must keep overflow candidates eligible for TopK backfill:\n%s", sql)
	}
	for _, filter := range []string{
		"rl.id='ae79c267-78dd-4c22-9c68-30f952adb38b'",
		"sf.data_source_id IN ($1)",
		"\"embeddings\".\"knowledge_base_id\" = $2",
		"\"embeddings\".\"knowledge_id\" = $3",
		"knowledge_tag_relations",
		"dimension = $8",
		"is_enabled = $9",
	} {
		position := strings.Index(sql, filter)
		if position < 0 || position > poolLimit {
			t.Errorf("authorization/filter %q must appear before the bounded candidate pool", filter)
		}
	}
}

func TestSourceFileVersionPrioritySQLDiversifiesAfterScopedCandidatePool(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&diversityCandidateFixture{}, &diversityReferenceFixture{}); err != nil {
		t.Fatal(err)
	}

	var candidates []diversityCandidateFixture
	var references []diversityReferenceFixture
	add := func(chunkID, sourceID, knowledgeBaseID, snapshotID, fileID, versionID, path string, score float64, published, inScope bool) {
		candidates = append(candidates, diversityCandidateFixture{
			ID: chunkID, ChunkID: chunkID, Content: "confirm update", SourceID: sourceID, SourceType: 1,
			KnowledgeID: fileID, KnowledgeBaseID: knowledgeBaseID, Score: score, IsPublished: published,
			InReadScope: inScope, Dimension: 1536, IsEnabled: true,
		})
		references = append(references, diversityReferenceFixture{
			ChunkID: chunkID, SnapshotID: snapshotID, SourceFileID: fileID, FileVersionID: versionID, Path: path,
		})
	}

	// High-ranked unauthorized or stale rows must be removed by the candidate query
	// before its 100-row limit; otherwise they would consume the diversity pool.
	for i := 0; i < 140; i++ {
		add(fmt.Sprintf("stale-%03d", i), "source-other", "kb-a", "stale-snapshot", "stale-file", "stale-version", "src/shared/SignupService.java", 2-float64(i)/1000, false, false)
	}
	for i := 0; i < 50; i++ {
		add(fmt.Sprintf("a-%03d", i), "source-a", "kb-a", "snapshot-a", "file-a", "version-a", "src/shared/SignupService.java", 1-float64(i)/1000, true, true)
	}
	add("b-001", "source-a", "kb-a", "snapshot-a", "file-b", "version-b", "src/service/ConfirmService.java", .70, true, true)
	add("c-001", "source-a", "kb-a", "snapshot-a", "file-c", "version-c", "src/api/ConfirmController.java", .69, true, true)
	if err := db.Create(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}

	conditions := []clause.Expression{
		clause.Expr{SQL: "source_id = ?", Vars: []interface{}{"source-a"}},
		clause.Expr{SQL: "knowledge_base_id = ?", Vars: []interface{}{"kb-a"}},
		clause.Expr{SQL: "is_published = ?", Vars: []interface{}{true}},
		clause.Expr{SQL: "in_read_scope = ?", Vars: []interface{}{true}},
		clause.Expr{SQL: "dimension = ?", Vars: []interface{}{1536}},
		clause.Expr{SQL: "is_enabled = ?", Vars: []interface{}{true}},
		clause.Expr{SQL: "content LIKE ?", Vars: []interface{}{"%confirm%"}},
		clause.OrderBy{Columns: []clause.OrderByColumn{{Column: clause.Column{Name: "score"}, Desc: true}}},
	}
	bounded := buildKeywordCandidateQuery(db, conditions, sourceCandidatePoolSize(50), "score")
	var got []diversityResultFixture
	if err := applySourceFileVersionPriority(db, bounded, 50).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	seen := make(map[string]bool)
	for _, hit := range got {
		seen[hit.ChunkID] = true
		var ref diversityReferenceFixture
		if err := db.First(&ref, "chunk_id = ?", hit.ChunkID).Error; err != nil {
			t.Fatal(err)
		}
		counts[ref.SourceFileID+"/"+ref.FileVersionID]++
	}
	if !seen["b-001"] || !seen["c-001"] {
		t.Fatalf("lower-ranked files inside the bounded pool must survive the dominant file: got %v", seen)
	}
	if counts["file-a/version-a"] != 48 {
		t.Fatalf("dominant file count = %d, want 48 after two alternate files get priority and backfill fills TopK", counts["file-a/version-a"])
	}
	if counts["stale-file/stale-version"] != 0 || len(got) != 50 {
		t.Fatalf("scope-filtered candidates must not consume the pool; expected 50 in-scope hits after backfill, got %d (%v)", len(got), counts)
	}
}

func TestSourceFileVersionPriorityReturnsTopKForSingleDominantFile(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&diversityCandidateFixture{}, &diversityReferenceFixture{}); err != nil {
		t.Fatal(err)
	}
	var candidates []diversityCandidateFixture
	var references []diversityReferenceFixture
	for i := 0; i < 60; i++ {
		chunkID := fmt.Sprintf("only-file-%02d", i)
		candidates = append(candidates, diversityCandidateFixture{
			ID: chunkID, ChunkID: chunkID, SourceID: "source-a", KnowledgeBaseID: "kb-a", Score: 1 - float64(i)/100,
		})
		references = append(references, diversityReferenceFixture{
			ChunkID: chunkID, SnapshotID: "snapshot-a", SourceFileID: "file-a", FileVersionID: "version-a",
		})
	}
	if err := db.Create(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}
	conditions := []clause.Expression{
		clause.Expr{SQL: "source_id = ? AND knowledge_base_id = ?", Vars: []interface{}{"source-a", "kb-a"}},
		clause.OrderBy{Columns: []clause.OrderByColumn{{Column: clause.Column{Name: "score"}, Desc: true}}},
	}
	bounded := buildKeywordCandidateQuery(db, conditions, sourceCandidatePoolSize(50), "score")
	var got []diversityResultFixture
	if err := applySourceFileVersionPriority(db, bounded, 50).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	if len(got) != 50 {
		t.Fatalf("single-file retrieval returned %d candidates, want TopK 50 after bounded-pool backfill", len(got))
	}
	for i, hit := range got {
		if hit.Score != 1-float64(i)/100 {
			t.Fatalf("single-file hit rank %d score = %g, want original relevance %g", i+1, hit.Score, 1-float64(i)/100)
		}
	}
}

func TestSourceFileVersionPrioritySeparatesSamePathAcrossSourcesAndVersions(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&diversityCandidateFixture{}, &diversityReferenceFixture{}); err != nil {
		t.Fatal(err)
	}
	var candidates []diversityCandidateFixture
	var references []diversityReferenceFixture
	groups := []struct {
		source, snapshot, file, version string
	}{
		{"source-a", "snapshot-a", "file-a", "version-a"},
		{"source-b", "snapshot-b", "file-b", "version-b"},     // Same path, different source.
		{"source-a", "snapshot-new", "file-a", "version-new"}, // Same source file, new immutable version.
	}
	for groupIndex, group := range groups {
		for i := 0; i < 6; i++ {
			chunkID := fmt.Sprintf("g%d-%d", groupIndex, i)
			candidates = append(candidates, diversityCandidateFixture{
				ID: chunkID, ChunkID: chunkID, Content: "confirm", SourceID: group.source, SourceType: 1,
				KnowledgeID: group.file, KnowledgeBaseID: "kb-a", Score: 1 - float64(groupIndex)/10 - float64(i)/100,
				IsPublished: true, InReadScope: true, Dimension: 1536, IsEnabled: true,
			})
			references = append(references, diversityReferenceFixture{
				ChunkID: chunkID, SnapshotID: group.snapshot, SourceFileID: group.file,
				FileVersionID: group.version, Path: "src/shared/SignupService.java",
			})
		}
	}
	if err := db.Create(&candidates).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&references).Error; err != nil {
		t.Fatal(err)
	}
	conditions := []clause.Expression{
		clause.Expr{SQL: "source_id IN (?, ?)", Vars: []interface{}{"source-a", "source-b"}},
		clause.Expr{SQL: "knowledge_base_id = ? AND is_published = ? AND in_read_scope = ?", Vars: []interface{}{"kb-a", true, true}},
		clause.Expr{SQL: "dimension = ? AND is_enabled = ?", Vars: []interface{}{1536, true}},
		clause.OrderBy{Columns: []clause.OrderByColumn{{Column: clause.Column{Name: "score"}, Desc: true}}},
	}
	bounded := buildKeywordCandidateQuery(db, conditions, sourceCandidatePoolSize(50), "score")
	var got []diversityResultFixture
	if err := applySourceFileVersionPriority(db, bounded, 50).Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, hit := range got {
		var ref diversityReferenceFixture
		if err := db.First(&ref, "chunk_id = ?", hit.ChunkID).Error; err != nil {
			t.Fatal(err)
		}
		counts[ref.SnapshotID+"/"+ref.SourceFileID+"/"+ref.FileVersionID]++
	}
	if len(got) != len(groups)*6 {
		t.Fatalf("same-path source/version groups must each backfill to their six available candidates: got %d rows, want %d", len(got), len(groups)*6)
	}
	for _, group := range groups {
		key := group.snapshot + "/" + group.file + "/" + group.version
		if counts[key] != 6 {
			t.Errorf("group %s count = %d, want all six candidates after priority/backfill", key, counts[key])
		}
	}
}

func TestVectorSourcePrioritySQLKeepsAuthorizationFiltersAheadOfCandidateLimit(t *testing.T) {
	query := buildSourceDiverseVectorQuery(
		"WITH source_candidates AS MATERIALIZED (SELECT * FROM embeddings WHERE published_scope AND source_id_scope AND knowledge_base_scope AND tag_scope AND dimension = $2 AND is_enabled = $3)",
		"source_candidates", "", 1536, 4, 5, 6)
	scopeEnd := strings.Index(query, ", source_nearest_candidates AS MATERIALIZED")
	poolLimit := strings.Index(query, "LIMIT $4")
	quotaJoin := strings.Index(query, "JOIN source_chunk_references quota_refs")
	priorityOrder := strings.Index(query, "CASE WHEN source_file_rank <= 4 THEN 0 ELSE 1 END ASC")
	if scopeEnd < 0 || poolLimit < 0 || quotaJoin < poolLimit || priorityOrder < quotaJoin {
		t.Fatalf("vector SQL must apply authorized scopes, then bounded candidate retrieval, then per-file priority:\n%s", query)
	}
	if strings.Contains(query, "WHERE source_file_rank <=") {
		t.Fatalf("vector SQL must keep overflow candidates eligible for TopK backfill:\n%s", query)
	}
	for _, required := range []string{
		"published_scope AND source_id_scope AND knowledge_base_scope AND tag_scope AND dimension = $2 AND is_enabled = $3",
		"PARTITION BY quota_refs.snapshot_id, quota_refs.source_file_id, quota_refs.file_version_id",
		"ORDER BY source_nearest_candidates.distance ASC, source_nearest_candidates.chunk_id ASC",
		"distance <= $5",
		"LIMIT $6",
	} {
		if !strings.Contains(query, required) {
			t.Errorf("generated vector SQL missing %q:\n%s", required, query)
		}
	}
	if scopeEnd < 0 || strings.Index(query, "published_scope") > scopeEnd {
		t.Fatal("authorization filters must be inside the source candidate CTE before file ranking")
	}
}
