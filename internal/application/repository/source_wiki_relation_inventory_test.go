package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestLoadSourceWikiRelationInventoryRejectsUnboundedTextBeforeMaterializingRows(t *testing.T) {
	db, mock := newSourceWikiImpactLoaderMock(t)
	mock.ExpectBegin()
	expectSourceWikiRelationInventorySnapshot(mock, 1)
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*OCTET_LENGTH.*context::text.*from_path.*from_key.*to_path.*to_key.*resolution_reason.*from_range.*to_range.*FROM "source_code_relations".*`).
		WillReturnRows(sqlmock.NewRows([]string{"relation_count", "context_bytes", "relation_bytes"}).
			AddRow(1, 0, types.SourceWikiImpactMaxRelationBytes+1))
	mock.ExpectRollback()

	inventory, err := LoadSourceWikiRelationInventory(context.Background(), db, 7, "kb-one", "source-one", "snapshot-one")
	if inventory != nil || !errors.Is(err, ErrSourceWikiDerivationDeferred) {
		t.Fatalf("expected oversized relation text to defer before row loading, inventory=%+v err=%v", inventory, err)
	}
	assertSourceWikiImpactMockExpectations(t, mock)
}

func TestLoadSourceWikiRelationInventoryLoadsNormalBoundedRows(t *testing.T) {
	db, mock := newSourceWikiImpactLoaderMock(t)
	mock.ExpectBegin()
	expectSourceWikiRelationInventorySnapshot(mock, 1)
	relation := types.SourceCodeRelation{
		ID: "relation-one", TenantID: 7, DataSourceID: "source-one", SnapshotID: "snapshot-one",
		Kind: "http_route", FromFileID: "file-one", FromVersionID: "version-one", FromPath: "app/A.vue",
		FromKey: "GET /api/a", FromRange: types.JSON(`{"start":1}`), ToRange: types.JSON(`{}`),
		Determinacy: "uncertain", Quality: "structural", ResolutionReason: "unmatched", Context: types.JSON(`[]`),
		CreatedAt: time.Date(2026, time.October, 7, 12, 0, 0, 0, time.UTC),
	}
	mock.ExpectQuery(`(?s)SELECT COUNT\(\*\).*OCTET_LENGTH.*context::text.*from_path.*from_key.*to_path.*to_key.*resolution_reason.*from_range.*to_range.*FROM "source_code_relations".*`).
		WillReturnRows(sqlmock.NewRows([]string{"relation_count", "context_bytes", "relation_bytes"}).
			AddRow(1, len(relation.Context), sourceWikiRelationVariableBytes(relation)))
	mock.ExpectQuery(`SELECT \* FROM "source_code_relations".*`).WillReturnRows(sourceWikiImpactRelationRows().AddRow(
		relation.ID, relation.TenantID, relation.DataSourceID, relation.SnapshotID, relation.Kind,
		relation.FromFileID, relation.FromVersionID, relation.FromPath, relation.FromKey, []byte(relation.FromRange),
		relation.ToFileID, relation.ToVersionID, relation.ToPath, relation.ToKey, []byte(relation.ToRange),
		relation.Determinacy, relation.Quality, relation.ResolutionReason, []byte(relation.Context), relation.CreatedAt))
	mock.ExpectCommit()

	inventory, err := LoadSourceWikiRelationInventory(context.Background(), db, 7, "kb-one", "source-one", "snapshot-one")
	if err != nil {
		t.Fatalf("load bounded source Wiki relation inventory: %v", err)
	}
	if inventory == nil || !inventory.RelationsComplete || inventory.ExpectedCount != 1 || len(inventory.Relations) != 1 ||
		!sourceWikiRelationEqual(inventory.Relations[0], relation) {
		t.Fatalf("unexpected complete relation inventory: %+v", inventory)
	}
	assertSourceWikiImpactMockExpectations(t, mock)
}

func expectSourceWikiRelationInventorySnapshot(mock sqlmock.Sqlmock, relationCount int) {
	mock.ExpectQuery(`(?s)SELECT ss\.id, ss\.tenant_id, ss\.knowledge_base_id, ss\.data_source_id,.*FROM source_snapshots ss.*JOIN source_publications sp.*`).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "knowledge_base_id", "data_source_id", "manifest_complete", "relations_staged", "wiki_derivation_state", "relation_count",
		}).AddRow("snapshot-one", uint64(7), "kb-one", "source-one", true, true, "complete", relationCount))
}
