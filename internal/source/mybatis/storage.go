package mybatis

import (
	"encoding/json"
	"fmt"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// ToStoredRelation binds a parser edge to the immutable source file versions
// known by the snapshot pipeline. It refuses an empty snapshot identity so a
// caller cannot accidentally persist cross-version relationships.
func (relation Relation) ToStoredRelation(snapshotID, dataSourceID, fromFileID, fromVersionID, toFileID, toVersionID string) (types.SourceCodeRelation, error) {
	if snapshotID == "" || dataSourceID == "" {
		return types.SourceCodeRelation{}, fmt.Errorf("snapshot and data source IDs are required")
	}
	if fromFileID == "" || fromVersionID == "" {
		return types.SourceCodeRelation{}, fmt.Errorf("source relation requires an immutable from file and version")
	}
	if relation.Kind != RelationTableAccess && (toFileID == "" || toVersionID == "") {
		return types.SourceCodeRelation{}, fmt.Errorf("non-table source relation requires an immutable target file and version")
	}
	fromRange, err := json.Marshal(relation.FromRange)
	if err != nil {
		return types.SourceCodeRelation{}, err
	}
	toRange, err := json.Marshal(relation.ToRange)
	if err != nil {
		return types.SourceCodeRelation{}, err
	}
	context, err := json.Marshal(relation.Context)
	if err != nil {
		return types.SourceCodeRelation{}, err
	}
	return types.SourceCodeRelation{ID: uuid.NewString(), DataSourceID: dataSourceID, SnapshotID: snapshotID,
		Kind: relation.Kind, FromFileID: fromFileID, FromVersionID: fromVersionID, FromPath: relation.FromPath,
		FromKey: relation.FromKey, FromRange: types.JSON(fromRange), ToFileID: toFileID, ToVersionID: toVersionID,
		ToPath: relation.ToPath, ToKey: relation.ToKey, ToRange: types.JSON(toRange), Determinacy: relation.Determinacy,
		Quality: relation.Quality, Context: types.JSON(context)}, nil
}
