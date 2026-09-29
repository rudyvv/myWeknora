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
