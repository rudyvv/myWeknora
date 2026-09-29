package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

type SourceRelationQuery struct {
	Kind  string
	Key   string
	Limit int
}

// SourceCodeRelationRepository reads only immutable relations for a fixed
// source snapshot. The writer replaces one snapshot's set atomically after all
// participating file versions have been staged.
type SourceCodeRelationRepository interface {
	ReplaceSnapshotRelations(context.Context, uint64, string, string, []types.SourceCodeRelation) error
	ListSnapshotRelations(context.Context, uint64, string, string, SourceRelationQuery) ([]types.SourceCodeRelation, error)
}
