package repository

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestLoadSourceWikiImpactSnapshotClassifiesFactBudgetFailureBeforeLoadingRows(t *testing.T) {
	db, mock := newSourceWikiImpactLoaderMock(t)
	expectSourceWikiImpactSnapshot(mock, true, 1, true, 0)
	expectSourceWikiImpactFactInventory(mock, 1024, types.SourceWikiImpactMaxFacts+1)

	snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
		types.SourceWikiImpactPublishedComplete)
	assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
		SourceWikiImpactLoadFailureBudgetExceeded, SourceWikiImpactReasonFactCountExceeded, ErrSourceWikiImpactLoadBudgetExceeded)
	assertSourceWikiImpactMockExpectations(t, mock)
}

func TestLoadSourceWikiImpactSnapshotClassifiesMemberBudgetsBeforeLoadingRows(t *testing.T) {
	tests := []struct {
		name          string
		metadataBytes int64
		factBytes     int64
		wantReason    SourceWikiImpactLoadReasonCode
	}{
		{name: "member metadata", metadataBytes: types.SourceWikiImpactMaxMemberMetadataBytes + 1, wantReason: SourceWikiImpactReasonMemberMetadataBytesExceeded},
		{name: "facts on excluded members", factBytes: types.SourceWikiImpactMaxFactBytes + 1, wantReason: SourceWikiImpactReasonFactBytesExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newSourceWikiImpactLoaderMock(t)
			expectSourceWikiImpactSnapshot(mock, true, 1, true, 0)
			expectSourceWikiImpactMemberInventory(mock, test.metadataBytes, test.factBytes, 0)

			snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
				types.SourceWikiImpactPublishedComplete)
			assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
				SourceWikiImpactLoadFailureBudgetExceeded, test.wantReason, ErrSourceWikiImpactLoadBudgetExceeded)
			assertSourceWikiImpactMockExpectations(t, mock)
		})
	}
}

func TestLoadSourceWikiImpactSnapshotClassifiesPreflightBudgetFailures(t *testing.T) {
	tests := []struct {
		name          string
		members       int
		relations     int
		factBytes     int64
		factCount     int64
		contextBytes  int64
		relationBytes int64
		wantReason    SourceWikiImpactLoadReasonCode
	}{
		{name: "member count", members: types.SourceWikiSkeletonMaxFiles + 1, wantReason: SourceWikiImpactReasonMemberCountExceeded},
		{name: "relation count", relations: types.SourceWikiSkeletonMaxRelations + 1, wantReason: SourceWikiImpactReasonRelationCountExceeded},
		{name: "fact bytes", factBytes: types.SourceWikiImpactMaxFactBytes + 1, wantReason: SourceWikiImpactReasonFactBytesExceeded},
		{name: "relation context bytes", contextBytes: types.SourceWikiImpactMaxContextBytes + 1, wantReason: SourceWikiImpactReasonRelationBytesExceeded},
		{name: "relation total bytes", relationBytes: types.SourceWikiImpactMaxRelationBytes + 1, wantReason: SourceWikiImpactReasonRelationTotalBytesExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newSourceWikiImpactLoaderMock(t)
			expectSourceWikiImpactSnapshot(mock, true, test.members, true, test.relations)
			if test.members <= types.SourceWikiSkeletonMaxFiles && test.relations <= types.SourceWikiSkeletonMaxRelations {
				expectSourceWikiImpactFactInventory(mock, test.factBytes, test.factCount)
				if test.factBytes <= types.SourceWikiImpactMaxFactBytes && test.factCount <= types.SourceWikiImpactMaxFacts {
					expectSourceWikiImpactRelationBounds(mock, test.contextBytes, test.relationBytes)
				}
			}

			snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
				types.SourceWikiImpactPublishedComplete)
			assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
				SourceWikiImpactLoadFailureBudgetExceeded, test.wantReason, ErrSourceWikiImpactLoadBudgetExceeded)
			assertSourceWikiImpactMockExpectations(t, mock)
		})
	}
}

func TestLoadSourceWikiImpactSnapshotClassifiesIncompleteProof(t *testing.T) {
	tests := []struct {
		name          string
		manifest      bool
		memberCount   int
		relations     bool
		relationCount int
		memberRows    *sqlmock.Rows
		wantReason    SourceWikiImpactLoadReasonCode
	}{
		{name: "manifest not complete", manifest: false, relations: true, wantReason: SourceWikiImpactReasonManifestIncomplete},
		{name: "relations not staged", manifest: true, relations: false, wantReason: SourceWikiImpactReasonRelationsIncomplete},
		{name: "member count mismatch", manifest: true, memberCount: 1, relations: true,
			memberRows: sqlmock.NewRows(sourceWikiImpactMemberColumns()), wantReason: SourceWikiImpactReasonMemberCountMismatch},
		{name: "unparsed member", manifest: true, memberCount: 1, relations: true,
			memberRows: sourceWikiImpactMemberRows().AddRow("app/A.java", "", "", "", "included", false,
				nil, nil, nil, nil, nil, nil, nil, nil, nil, nil), wantReason: SourceWikiImpactReasonMemberUnparsed},
		{name: "relation count mismatch", manifest: true, relations: true, relationCount: 1,
			memberRows: sqlmock.NewRows(sourceWikiImpactMemberColumns()), wantReason: SourceWikiImpactReasonRelationCountMismatch},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, mock := newSourceWikiImpactLoaderMock(t)
			expectSourceWikiImpactSnapshot(mock, test.manifest, test.memberCount, test.relations, test.relationCount)
			if test.manifest && test.relations && test.memberCount <= types.SourceWikiSkeletonMaxFiles && test.relationCount <= types.SourceWikiSkeletonMaxRelations {
				expectSourceWikiImpactFactInventory(mock, 0, 0)
				expectSourceWikiImpactRelationBounds(mock, 0, 0)
				expectSourceWikiImpactMemberRows(mock, test.memberRows)
				if test.wantReason == SourceWikiImpactReasonRelationCountMismatch {
					expectSourceWikiImpactRelationRows(mock, sqlmock.NewRows([]string{"id"}))
				}
			}

			snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
				types.SourceWikiImpactPublishedComplete)
			assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
				SourceWikiImpactLoadFailureProofIncomplete, test.wantReason, ErrSourceWikiImpactProofIncomplete)
			assertSourceWikiImpactMockExpectations(t, mock)
		})
	}
}

func TestLoadSourceWikiImpactSnapshotClassifiesInvalidMaterializedFacts(t *testing.T) {
	db, mock := newSourceWikiImpactLoaderMock(t)
	expectSourceWikiImpactSnapshot(mock, true, 1, true, 0)
	expectSourceWikiImpactFactInventory(mock, 2, 0)
	expectSourceWikiImpactRelationBounds(mock, 0, 0)
	expectSourceWikiImpactMemberRows(mock, sourceWikiImpactMemberRows().AddRow(
		"app/A.java", "file-one", "version-one", "", "parsed", false,
		"file-one", uint64(7), "kb-one", "source-one", "version-one", "file-one",
		"content-sha", "parser-v1", "structural", []byte("{}")))

	snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
		types.SourceWikiImpactPublishedComplete)
	assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
		SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonParsedFactsInvalid, ErrSourceWikiImpactSnapshotInvalid)
	assertSourceWikiImpactMockExpectations(t, mock)
}

func TestLoadSourceWikiImpactSnapshotKeepsMissingParserMetadataTyped(t *testing.T) {
	db, mock := newSourceWikiImpactLoaderMock(t)
	expectSourceWikiImpactSnapshot(mock, true, 1, true, 0)
	expectSourceWikiImpactFactInventory(mock, 2, 0)
	expectSourceWikiImpactRelationBounds(mock, 0, 0)
	expectSourceWikiImpactMemberRows(mock, sourceWikiImpactMemberRows().AddRow(
		"app/A.java", "file-one", "version-one", "", "parsed", false,
		"file-one", uint64(7), "kb-one", "source-one", "version-one", "file-one",
		"content-sha", "", "structural", []byte("[]")))

	snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
		types.SourceWikiImpactPublishedComplete)
	assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
		SourceWikiImpactLoadFailureInvalid, SourceWikiImpactReasonParsedMemberIdentity, ErrSourceWikiImpactSnapshotInvalid)
	assertSourceWikiImpactMockExpectations(t, mock)
}

func TestLoadSourceWikiImpactSnapshotKeepsOperationalErrorsUntyped(t *testing.T) {
	t.Run("scope-filtered not found", func(t *testing.T) {
		db, mock := newSourceWikiImpactLoaderMock(t)
		mock.ExpectQuery(`SELECT \* FROM "source_snapshots".*`).
			WillReturnRows(sqlmock.NewRows([]string{"id"}))
		snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 99, "other-kb", "other-source", "snapshot-one",
			types.SourceWikiImpactPublishedComplete)
		assertSourceWikiImpactOperationalError(t, snapshot, loaded, err, gorm.ErrRecordNotFound)
		assertSourceWikiImpactMockExpectations(t, mock)
	})
	t.Run("cancelled database read", func(t *testing.T) {
		db, mock := newSourceWikiImpactLoaderMock(t)
		expectSourceWikiImpactSnapshot(mock, true, 0, true, 0)
		mock.ExpectQuery(`(?s)SELECT.*OCTET_LENGTH.*sv\.facts::text.*FROM source_snapshot_members sm.*`).
			WillReturnError(context.Canceled)
		snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
			types.SourceWikiImpactPublishedComplete)
		assertSourceWikiImpactOperationalError(t, snapshot, loaded, err, context.Canceled)
		assertSourceWikiImpactMockExpectations(t, mock)
	})
	t.Run("invalid requested identity", func(t *testing.T) {
		snapshot, loaded, err := LoadSourceWikiImpactSnapshot(nil, 7, "kb-one", "source-one", "snapshot-one",
			types.SourceWikiImpactPublishedComplete)
		assertSourceWikiImpactOperationalError(t, snapshot, loaded, err, nil)
	})
}

func TestLoadSourceWikiImpactSnapshotPreservesParsedAndExcludedMembers(t *testing.T) {
	db, mock := newSourceWikiImpactLoaderMock(t)
	expectSourceWikiImpactSnapshot(mock, true, 2, true, 0)
	expectSourceWikiImpactMemberInventory(mock, 175, 2, 0)
	expectSourceWikiImpactRelationBounds(mock, 0, 0)
	expectSourceWikiImpactMemberRows(mock, sourceWikiImpactMemberRows().
		AddRow("app/A.java", "file-one", "version-one", "", "parsed", false,
			"file-one", uint64(7), "kb-one", "source-one", "version-one", "file-one",
			"content-sha", "parser-v1", "structural", []byte("[]")).
		AddRow("assets/data.bin", "file-two", "", "excluded-sha", "excluded", false,
			"file-two", uint64(7), "kb-one", "source-one", nil, nil, nil, nil, nil, nil))
	expectSourceWikiImpactRelationRows(mock, sqlmock.NewRows([]string{"id"}))

	snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
		types.SourceWikiImpactPublishedComplete)
	if err != nil {
		t.Fatalf("load complete parsed/excluded snapshot: %v", err)
	}
	if loaded == nil || !loaded.Complete || !snapshot.ManifestComplete || !snapshot.RelationsComplete ||
		len(snapshot.Members) != 2 || len(loaded.Members) != 1 || len(loaded.Files) != 1 || len(snapshot.Relations) != 0 {
		t.Fatalf("unexpected loaded source Wiki snapshot: snapshot=%+v loaded=%+v", snapshot, loaded)
	}
	if snapshot.Members[0].Status != "parsed" || !snapshot.Members[0].FactsComplete || snapshot.Members[1].Status != "excluded" || !snapshot.Members[1].FactsComplete {
		t.Fatalf("parsed/excluded member semantics changed: %+v", snapshot.Members)
	}
	assertSourceWikiImpactMockExpectations(t, mock)
}

func TestLoadSourceWikiImpactSnapshotWithRelationInventoryReusesAndVerifiesRows(t *testing.T) {
	relationTime := time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC)
	relation := types.SourceCodeRelation{
		ID: "relation-one", TenantID: 7, DataSourceID: "source-one", SnapshotID: "snapshot-one",
		Kind: "http_route", FromFileID: "file-one", FromVersionID: "version-one", FromPath: "app/A.vue",
		FromKey: "GET /api/a", FromRange: types.JSON(`{"start":1}`), ToFileID: "", ToVersionID: "",
		ToPath: "", ToKey: "", ToRange: types.JSON(`{}`), Determinacy: "uncertain", Quality: "structural",
		ResolutionReason: "unmatched", Context: types.JSON(`[]`), CreatedAt: relationTime,
	}
	for _, mismatch := range []bool{false, true} {
		name := "matches"
		inventoryRelation := relation
		if mismatch {
			name = "mismatch fails closed"
			inventoryRelation.FromPath = "other.vue"
		}
		t.Run(name, func(t *testing.T) {
			db, mock := newSourceWikiImpactLoaderMock(t)
			expectSourceWikiImpactSnapshot(mock, true, 0, true, 1)
			expectSourceWikiImpactFactInventory(mock, 0, 0)
			expectSourceWikiImpactRelationBounds(mock, int64(len(relation.Context)), sourceWikiRelationVariableBytes(relation))
			expectSourceWikiImpactMemberRows(mock, sourceWikiImpactMemberRows())
			expectSourceWikiImpactRelationRows(mock, sourceWikiImpactRelationRows().AddRow(
				relation.ID, relation.TenantID, relation.DataSourceID, relation.SnapshotID, relation.Kind,
				relation.FromFileID, relation.FromVersionID, relation.FromPath, relation.FromKey, []byte(relation.FromRange),
				relation.ToFileID, relation.ToVersionID, relation.ToPath, relation.ToKey, []byte(relation.ToRange),
				relation.Determinacy, relation.Quality, relation.ResolutionReason, []byte(relation.Context), relation.CreatedAt))
			inventory := &SourceWikiRelationInventory{
				TenantID: 7, KnowledgeBaseID: "kb-one", DataSourceID: "source-one", SnapshotID: "snapshot-one",
				ExpectedCount: 1, RelationsComplete: true, Relations: []types.SourceCodeRelation{inventoryRelation},
			}

			snapshot, loaded, err := LoadSourceWikiImpactSnapshotWithRelationInventory(db, 7, "kb-one", "source-one", "snapshot-one",
				types.SourceWikiImpactPublishedComplete, inventory)
			if mismatch {
				assertSourceWikiImpactLoadFailure(t, snapshot, loaded, err,
					SourceWikiImpactLoadFailureProofIncomplete, SourceWikiImpactReasonRelationInventoryChanged, ErrSourceWikiImpactProofIncomplete)
			} else {
				if err != nil {
					t.Fatalf("load exact bounded relation inventory: %v", err)
				}
				if len(snapshot.Relations) != 1 || loaded == nil || len(loaded.Relations) != 1 ||
					&snapshot.Relations[0] != &inventory.Relations[0] || &loaded.Relations[0] != &inventory.Relations[0] {
					t.Fatal("impact snapshot must reuse the single verified relation inventory")
				}
			}
			assertSourceWikiImpactMockExpectations(t, mock)
		})
	}
}

func TestLoadSourceWikiImpactSnapshotKeepsForeignFileScopeOperational(t *testing.T) {
	for _, status := range []string{"parsed", "excluded"} {
		for _, scope := range []string{"tenant", "kb", "source"} {
			t.Run(status+"_"+scope, func(t *testing.T) {
				db, mock := newSourceWikiImpactLoaderMock(t)
				expectSourceWikiImpactSnapshot(mock, true, 1, true, 0)
				var factBytes int64
				var versionSourceFileID any
				var facts []byte
				fileVersionID, contentSHA, parserVersion, quality := "version-one", "content-sha", "parser-v1", "structural"
				if status == "parsed" {
					factBytes = 2
					versionSourceFileID = "file-one"
					facts = []byte("[]")
				} else {
					fileVersionID, contentSHA, parserVersion, quality = "", "", "", ""
				}
				expectSourceWikiImpactFactInventory(mock, factBytes, 0)
				expectSourceWikiImpactRelationBounds(mock, 0, 0)
				fileTenantID, fileKBID, fileSourceID := uint64(7), "kb-one", "source-one"
				switch scope {
				case "tenant":
					fileTenantID = 8
				case "kb":
					fileKBID = "kb-two"
				case "source":
					fileSourceID = "source-two"
				}
				expectSourceWikiImpactMemberRows(mock, sourceWikiImpactMemberRows().AddRow(
					"app/A.java", "file-one", fileVersionID, "", status, false,
					"file-one", fileTenantID, fileKBID, fileSourceID,
					fileVersionID, versionSourceFileID, contentSHA, parserVersion, quality, facts))

				snapshot, loaded, err := LoadSourceWikiImpactSnapshot(db, 7, "kb-one", "source-one", "snapshot-one",
					types.SourceWikiImpactPublishedComplete)
				assertSourceWikiImpactOperationalError(t, snapshot, loaded, err, nil)
				assertSourceWikiImpactMockExpectations(t, mock)
			})
		}
	}
}

func expectSourceWikiImpactSnapshot(mock sqlmock.Sqlmock, manifestComplete bool, memberCount int, relationsStaged bool, relationCount int) {
	mock.ExpectQuery(`SELECT \* FROM "source_snapshots".*`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "knowledge_base_id", "data_source_id", "state", "manifest_complete", "member_count", "relations_staged", "relation_count",
		}).AddRow("snapshot-one", uint64(7), "kb-one", "source-one", "published", manifestComplete, memberCount, relationsStaged, relationCount))
}

func expectSourceWikiImpactFactInventory(mock sqlmock.Sqlmock, factBytes, factCount int64) {
	expectSourceWikiImpactMemberInventory(mock, 0, factBytes, factCount)
}

func expectSourceWikiImpactMemberInventory(mock sqlmock.Sqlmock, metadataBytes, factBytes, factCount int64) {
	mock.ExpectQuery(`(?s)SELECT.*OCTET_LENGTH.*sm\.path.*sv\.facts::text.*jsonb_typeof\(sv\.facts\).*FROM source_snapshot_members sm.*`).
		WillReturnRows(sqlmock.NewRows([]string{"metadata_bytes", "fact_bytes", "fact_count"}).AddRow(metadataBytes, factBytes, factCount))
}

func expectSourceWikiImpactRelationBounds(mock sqlmock.Sqlmock, contextBytes, relationBytes int64) {
	mock.ExpectQuery(`(?s)SELECT.*OCTET_LENGTH.*context::text.*from_path.*from_key.*to_path.*to_key.*resolution_reason.*from_range.*to_range.*FROM "source_code_relations".*`).
		WillReturnRows(sqlmock.NewRows([]string{"context_bytes", "relation_bytes"}).AddRow(contextBytes, relationBytes))
}

func expectSourceWikiImpactMemberRows(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`(?s)SELECT sm\.path,.*FROM source_snapshot_members sm.*`).WillReturnRows(rows)
}

func expectSourceWikiImpactRelationRows(mock sqlmock.Sqlmock, rows *sqlmock.Rows) {
	mock.ExpectQuery(`SELECT \* FROM "source_code_relations".*`).WillReturnRows(rows)
}

func sourceWikiImpactRelationRows() *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "tenant_id", "data_source_id", "snapshot_id", "kind", "from_file_id", "from_version_id",
		"from_path", "from_key", "from_range", "to_file_id", "to_version_id", "to_path", "to_key",
		"to_range", "determinacy", "quality", "resolution_reason", "context", "created_at",
	})
}

func sourceWikiImpactMemberColumns() []string {
	return []string{
		"path", "source_file_id", "file_version_id", "blob_sha", "status", "generated",
		"file_id", "file_tenant_id", "file_kb_id", "file_source_id", "version_id", "version_source_file_id",
		"content_sha", "parser_version", "quality", "facts",
	}
}

func sourceWikiImpactMemberRows() *sqlmock.Rows {
	return sqlmock.NewRows(sourceWikiImpactMemberColumns())
}

func assertSourceWikiImpactLoadFailure(t *testing.T, snapshot types.SourceWikiImpactSnapshot, loaded *SourceWikiSkeletonSnapshot,
	err error, failure SourceWikiImpactLoadFailure, reason SourceWikiImpactLoadReasonCode, sentinel error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected deterministic source Wiki impact load failure")
	}
	var classified *SourceWikiImpactLoadError
	if !errors.As(err, &classified) {
		t.Fatalf("expected typed source Wiki load failure, got %T: %v", err, err)
	}
	if classified.Failure != failure || classified.ReasonCode != reason || !errors.Is(err, sentinel) {
		t.Fatalf("unexpected source Wiki load failure: failure=%q reason=%q err=%v", classified.Failure, classified.ReasonCode, err)
	}
	if !reflect.DeepEqual(snapshot, types.SourceWikiImpactSnapshot{}) || loaded != nil {
		t.Fatalf("load error returned partial proof: members=%d loaded=%v", len(snapshot.Members), loaded != nil)
	}
}

func assertSourceWikiImpactOperationalError(t *testing.T, snapshot types.SourceWikiImpactSnapshot, loaded *SourceWikiSkeletonSnapshot,
	err error, want error) {
	t.Helper()
	if err == nil {
		t.Fatal("expected operational source Wiki impact load error")
	}
	var classified *SourceWikiImpactLoadError
	if errors.As(err, &classified) || errors.Is(err, ErrSourceWikiImpactProofIncomplete) ||
		errors.Is(err, ErrSourceWikiImpactSnapshotInvalid) || errors.Is(err, ErrSourceWikiImpactLoadBudgetExceeded) {
		t.Fatalf("operational error was misclassified as a fallback: %v", err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("operational error lost its identity: got %v, want %v", err, want)
	}
	if !reflect.DeepEqual(snapshot, types.SourceWikiImpactSnapshot{}) || loaded != nil {
		t.Fatalf("operational error returned partial proof: members=%d loaded=%v", len(snapshot.Members), loaded != nil)
	}
}

func assertSourceWikiImpactMockExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func newSourceWikiImpactLoaderMock(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	t.Helper()
	sqlDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("create sql mock: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := gorm.Open(postgres.New(postgres.Config{Conn: sqlDB, PreferSimpleProtocol: true}), &gorm.Config{
		DisableAutomaticPing: true,
		Logger:               logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open mocked postgres gorm: %v", err)
	}
	return db, mock
}
