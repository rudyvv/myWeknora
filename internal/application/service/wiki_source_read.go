package service

import (
	"context"
	"fmt"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *wikiPageService) BeginWikiRead(ctx context.Context, targets types.SearchTargets) (context.Context, func(), error) {
	pinned, release, err := beginSourceRead(ctx, s.kbService, targets)
	if err != nil {
		return nil, nil, err
	}
	if source.IsWikiAnswerRead(pinned) {
		if leaseID, ok := source.ReadLeaseID(pinned); ok {
			projection, supported := s.repo.(interfaces.SourceWikiReadProjectionRepository)
			if !supported {
				release()
				return nil, nil, fmt.Errorf("source Wiki answer projection is unavailable")
			}
			if err := projection.CheckSourceWikiReadProjection(pinned, leaseID); err != nil {
				release()
				return nil, nil, err
			}
		}
	}
	return pinned, release, nil
}

// isPinnedWikiAnswerRead identifies the only Wiki read that must preserve the
// answer projection's captured applicability state. BeginWikiRead has already
// checked the lease/capacity and repository reads still revalidate permissions
// and raw evidence before this service-level live applicability check is skipped.
func isPinnedWikiAnswerRead(ctx context.Context) bool {
	if !source.IsWikiAnswerRead(ctx) {
		return false
	}
	_, ok := source.ReadLeaseID(ctx)
	return ok
}

func (s *wikiPageService) revisionOwnerIdentity(ctx context.Context, kbID, slug string) (*types.WikiPage, error) {
	if repo, ok := s.repo.(interfaces.WikiRevisionIdentityRepository); ok {
		return repo.GetWikiPageIdentity(ctx, kbID, slug)
	}
	return s.repo.GetBySlug(ctx, kbID, slug)
}
