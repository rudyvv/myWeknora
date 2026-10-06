package repository

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type sourceMembershipQueryCounter struct {
	logger.Interface
	count int
}

type sourceRelationFixture struct {
	db       *gorm.DB
	snapshot types.SourceSnapshot
	files    []types.SourceFile
	versions []types.SourceFileVersion
}

func (l *sourceMembershipQueryCounter) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	query, rows := fc()
	l.Interface.Trace(ctx, begin, func() (string, int64) { return query, rows }, err)
	if strings.Contains(strings.ToLower(query), "source_snapshot_members") {
		l.count++
	}
}

func newSourceRelationFixture(t *testing.T, dbLogger logger.Interface) sourceRelationFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared"), &gorm.Config{Logger: dbLogger})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return seedSourceRelationFixture(t, db)
}

func TestSourceRelationFactBoundsQueryBindsExactlyOneArgumentPerPlaceholder(t *testing.T) {
	query, args := sourceRelationFactBoundsQuery(7, "kb-one", "source-one", "snapshot-one")
	if got, want := len(args), strings.Count(query, "?"); got != want {
		t.Fatalf("source fact bounds query has %d placeholders but %d bound arguments", want, got)
	}
	if got, want := len(args), 10; got != want {
		t.Fatalf("source fact bounds query has %d bound arguments, want %d for three scoped identity checks and snapshot", got, want)
	}
}

func seedSourceRelationFixture(t *testing.T, db *gorm.DB) sourceRelationFixture {
	t.Helper()
	if err := db.AutoMigrate(&types.SourceSnapshot{}, &types.SourceSnapshotMember{}, &types.SourceFile{}, &types.SourceFileVersion{}, &types.SourceCodeRelation{}); err != nil {
		t.Fatalf("migrate source snapshot tables: %v", err)
	}
	fixture := sourceRelationFixture{
		db:       db,
		snapshot: types.SourceSnapshot{ID: "snapshot-one", TenantID: 7, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", State: "staging", ManifestComplete: true},
		files: []types.SourceFile{
			{ID: "file-origin", TenantID: 7, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", Path: "src/Origin.java"},
			{ID: "file-target", TenantID: 7, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", Path: "src/Target.xml"},
		},
		versions: []types.SourceFileVersion{
			{ID: "version-origin", SourceFileID: "file-origin", SnapshotID: "snapshot-one"},
			{ID: "version-target", SourceFileID: "file-target", SnapshotID: "snapshot-one"},
		},
	}
	members := []types.SourceSnapshotMember{
		{SnapshotID: fixture.snapshot.ID, Path: fixture.files[0].Path, SourceFileID: fixture.files[0].ID, FileVersionID: fixture.versions[0].ID, Status: "parsed"},
		{SnapshotID: fixture.snapshot.ID, Path: fixture.files[1].Path, SourceFileID: fixture.files[1].ID, FileVersionID: fixture.versions[1].ID, Status: "parsed"},
	}
	for _, value := range []any{&fixture.snapshot, &fixture.files, &fixture.versions, &members} {
		if err := db.Create(value).Error; err != nil {
			t.Fatalf("create fixture %T: %v", value, err)
		}
	}
	return fixture
}

func relationForEndpoints(id string, from, to types.SourceFile, fromVersion, toVersion types.SourceFileVersion) types.SourceCodeRelation {
	return types.SourceCodeRelation{
		ID: id, Kind: "mapper_statement",
		FromFileID: from.ID, FromVersionID: fromVersion.ID, FromPath: from.Path,
		ToFileID: to.ID, ToVersionID: toVersion.ID, ToPath: to.Path,
		Determinacy: "certain", Quality: "structural",
	}
}

func TestStageRelationsBatchesEndpointMembershipReads(t *testing.T) {
	counter := &sourceMembershipQueryCounter{Interface: logger.Default.LogMode(logger.Silent)}
	fixture := newSourceRelationFixture(t, counter)

	relations := make([]types.SourceCodeRelation, 245)
	for i := range relations {
		relations[i] = relationForEndpoints(fmt.Sprintf("relation-%03d", i), fixture.files[0], fixture.files[1], fixture.versions[0], fixture.versions[1])
	}
	counter.count = 0
	if err := NewSourceSnapshotRepository(fixture.db).StageRelations(context.Background(), fixture.snapshot.TenantID, fixture.snapshot.DataSourceID, fixture.snapshot.ID, relations); err != nil {
		t.Fatalf("stage valid relations: %v", err)
	}
	if counter.count > 1 {
		t.Fatalf("endpoint membership reads = %d for 245 relations, want at most 1 batched read", counter.count)
	}
}

func TestStageRelationsRejectsUnverifiedEndpoints(t *testing.T) {
	tests := []struct {
		name string
		add  func(t *testing.T, fixture sourceRelationFixture) (types.SourceFile, types.SourceFileVersion)
	}{
		{
			name: "unknown endpoint IDs",
			add: func(_ *testing.T, _ sourceRelationFixture) (types.SourceFile, types.SourceFileVersion) {
				file := types.SourceFile{ID: "file-unknown", Path: "src/Unknown.xml"}
				version := types.SourceFileVersion{ID: "version-unknown", SourceFileID: file.ID}
				return file, version
			},
		},
		{
			name: "cross-tenant member",
			add: func(t *testing.T, fixture sourceRelationFixture) (types.SourceFile, types.SourceFileVersion) {
				file := types.SourceFile{ID: "file-other-tenant", TenantID: 8, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", Path: "src/OtherTenant.xml"}
				version := types.SourceFileVersion{ID: "version-other-tenant", SourceFileID: file.ID, SnapshotID: fixture.snapshot.ID}
				member := types.SourceSnapshotMember{SnapshotID: fixture.snapshot.ID, Path: file.Path, SourceFileID: file.ID, FileVersionID: version.ID, Status: "parsed"}
				for _, value := range []any{&file, &version, &member} {
					if err := fixture.db.Create(value).Error; err != nil {
						t.Fatalf("create cross-tenant endpoint: %v", err)
					}
				}
				return file, version
			},
		},
		{
			name: "file is not a snapshot member",
			add: func(t *testing.T, fixture sourceRelationFixture) (types.SourceFile, types.SourceFileVersion) {
				file := types.SourceFile{ID: "file-unlisted", TenantID: 7, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", Path: "src/Unlisted.xml"}
				version := types.SourceFileVersion{ID: "version-unlisted", SourceFileID: file.ID, SnapshotID: fixture.snapshot.ID}
				for _, value := range []any{&file, &version} {
					if err := fixture.db.Create(value).Error; err != nil {
						t.Fatalf("create non-member endpoint: %v", err)
					}
				}
				return file, version
			},
		},
		{
			name: "member is not parsed",
			add: func(t *testing.T, fixture sourceRelationFixture) (types.SourceFile, types.SourceFileVersion) {
				file := types.SourceFile{ID: "file-unparsed", TenantID: 7, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", Path: "src/Unparsed.xml"}
				version := types.SourceFileVersion{ID: "version-unparsed", SourceFileID: file.ID, SnapshotID: fixture.snapshot.ID}
				member := types.SourceSnapshotMember{SnapshotID: fixture.snapshot.ID, Path: file.Path, SourceFileID: file.ID, FileVersionID: version.ID, Status: "included"}
				for _, value := range []any{&file, &version, &member} {
					if err := fixture.db.Create(value).Error; err != nil {
						t.Fatalf("create unparsed endpoint: %v", err)
					}
				}
				return file, version
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newSourceRelationFixture(t, logger.Default.LogMode(logger.Silent))
			file, version := tt.add(t, fixture)
			existing := relationForEndpoints("existing-relation", fixture.files[0], fixture.files[1], fixture.versions[0], fixture.versions[1])
			if err := fixture.db.Create(&existing).Error; err != nil {
				t.Fatalf("seed prior relation: %v", err)
			}
			invalid := relationForEndpoints("invalid-relation", fixture.files[0], file, fixture.versions[0], version)
			err := NewSourceSnapshotRepository(fixture.db).StageRelations(context.Background(), fixture.snapshot.TenantID, fixture.snapshot.DataSourceID, fixture.snapshot.ID, []types.SourceCodeRelation{invalid})
			if err == nil {
				t.Fatal("StageRelations accepted a target outside the tenant's parsed snapshot")
			}
			var count int64
			if err := fixture.db.Model(&types.SourceCodeRelation{}).Where("id=?", existing.ID).Count(&count).Error; err != nil {
				t.Fatalf("count prior relation after rejection: %v", err)
			}
			if count != 1 {
				t.Fatalf("prior relation count after rejected replacement = %d, want 1", count)
			}
		})
	}
}
