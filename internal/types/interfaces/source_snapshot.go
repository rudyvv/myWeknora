package interfaces

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
)

// SourceSnapshotRepository owns immutable versions, staging and the publication
// transaction on the built-in PostgreSQL index database.
type SourceSnapshotRepository interface {
	CheckReady(context.Context) error
	Create(context.Context, *types.SourceSnapshot, []types.SourceSnapshotMember) error
	SetState(context.Context, string, string, string) error
	StageFile(context.Context, *types.SourceFile, *types.SourceFileVersion, []*types.Chunk) error
	StageIndexes(context.Context, []*types.IndexInfo, map[string][]float32) error
	Publish(context.Context, *types.SourceSnapshot, *types.DataSource, *types.KnowledgeBase, int) error
	GetRun(context.Context, uint64, string, string) (*types.SourceRunResult, error)
}

type SourceFileRepository interface {
	ReadPublishedSourceFile(context.Context, uint64, string, ...string) (*types.SourceFileView, error)
}
