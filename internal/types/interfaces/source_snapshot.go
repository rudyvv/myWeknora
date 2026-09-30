package interfaces

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
)

// SourceSnapshotRepository owns immutable versions, staging and the publication
// transaction on the built-in PostgreSQL index database.
type SourceSnapshotRepository interface {
	CheckReady(context.Context) error
	RelaySourcePublicationOutbox(context.Context, int) (int, error)
	GetPublished(context.Context, uint64, string) (*types.SourceRunResult, error)
	GetParsedArtifact(context.Context, uint64, string, string) (*types.ParsedSourceFile, error)
	SaveParsedArtifact(context.Context, uint64, string, string, *types.ParsedSourceFile) error
	GetEmbeddingArtifacts(context.Context, uint64, string, []string) (map[string][]float32, error)
	SaveEmbeddingArtifacts(context.Context, uint64, string, map[string][]float32) error
	Create(context.Context, *types.SourceSnapshot, []types.SourceSnapshotMember) error
	SetState(context.Context, string, string, string) error
	UpdateProgress(context.Context, *types.SourceSnapshot, []types.SourceSnapshotMember) error
	StageFile(context.Context, *types.SourceFile, *types.SourceFileVersion, []*types.Chunk) error
	StageIndexes(context.Context, []*types.IndexInfo, map[string][]float32) error
	Publish(context.Context, *types.SourceSnapshot, *types.DataSource, *types.KnowledgeBase, int) error
	EnsurePublishedSourceWikiUpdate(context.Context, *types.DataSource, *types.SourceSnapshot) error
	GetRun(context.Context, uint64, string, string) (*types.SourceRunResult, error)
}

type SourceFileRepository interface {
	ReadPublishedSourceFile(context.Context, uint64, string, ...string) (*types.SourceFileView, error)
}

// SourceFileInfoRepository projects pinned provenance without loading file bytes.
type SourceFileInfoRepository interface {
	ReadPublishedSourceFileInfo(context.Context, uint64, string) (*types.SourceFileView, error)
}

// SourceReadService pins the published repository versions for one question.
// Callers retain the returned context for every read and release it on completion.
type SourceReadService interface {
	BeginSourceRead(context.Context, types.SearchTargets) (context.Context, func(), error)
}

type SourceReadRepository interface {
	AcquireSourceRead(context.Context, types.SearchTargets) (types.SourceReadLease, func(), error)
	CheckSourceRead(context.Context, string) error
}
