package service

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *wikiPageService) BeginWikiRead(ctx context.Context, targets types.SearchTargets) (context.Context, func(), error) {
	return beginSourceRead(ctx, s.kbService, targets)
}

func (s *wikiPageService) revisionOwnerIdentity(ctx context.Context, kbID, slug string) (*types.WikiPage, error) {
	if repo, ok := s.repo.(interfaces.WikiRevisionIdentityRepository); ok {
		return repo.GetWikiPageIdentity(ctx, kbID, slug)
	}
	return s.repo.GetBySlug(ctx, kbID, slug)
}
