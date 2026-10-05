package interfaces

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
)

// SourceWikiService generates one explicitly selected module independently of
// source publication. Every manual invocation has a fresh bounded budget.
type SourceWikiService interface {
	GenerateModule(context.Context, types.SourceWikiGenerateRequest) (*types.SourceWikiAttempt, error)
	ListAttempts(context.Context, string) ([]*types.SourceWikiAttempt, error)
	ReadEvidence(context.Context, string, string, int, string) (*types.SourceFileView, error)
}

// SourceWikiReadProjectionRepository checks the immutable source-backed Wiki
// projection captured for an in-flight answer lease.
type SourceWikiReadProjectionRepository interface {
	CheckSourceWikiReadProjection(context.Context, string) error
}

// SourceWikiBatchExecutionService is an internal extension used only by the
// durable batch worker; HTTP clients cannot provide its trusted topic fields.
type SourceWikiBatchExecutionService interface {
	GenerateTopic(context.Context, types.SourceWikiGenerateRequest) (*types.SourceWikiAttempt, error)
	ResumeSourceWikiBatch(context.Context, string)
	StopSourceWikiBatches()
	ResumePendingSourceWikiUpdates(context.Context) error
}

// SourceWikiUpdateProcessor consumes one accepted durable publication delivery.
// Implementations must revalidate the source snapshot and configuration
// generation before changing a page or applicability record.
type SourceWikiUpdateProcessor interface {
	ProcessPublishedSourceWikiUpdate(context.Context, types.SourceWikiUpdatePayload) error
}
type WikiReadService interface {
	BeginWikiRead(context.Context, types.SearchTargets) (context.Context, func(), error)
}

type WikiSourceApplicabilityRepository interface {
	WikiSourceApplicable(context.Context, *types.WikiPage) (bool, error)
}

type WikiRevisionIdentityRepository interface {
	GetWikiPageIdentity(context.Context, string, string) (*types.WikiPage, error)
	GetWikiPageIdentityByID(context.Context, string) (*types.WikiPage, error)
}

// WikiFolderPresenceRepository identifies occupied containers without exposing
// any page body, title or count outside the caller's read scope.
type WikiFolderPresenceRepository interface {
	OccupiedWikiFolders(context.Context, string, uint64) (map[string]bool, error)
}
