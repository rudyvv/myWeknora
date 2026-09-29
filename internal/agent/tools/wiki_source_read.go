package tools

import (
	"context"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func beginWikiToolRead(ctx context.Context, wiki interfaces.WikiPageService, scopes []WikiScope) (context.Context, func(), error) {
	reader, ok := wiki.(interfaces.WikiReadService)
	if !ok {
		return ctx, func() {}, nil
	}
	targets := types.SearchTargets{}
	for _, sc := range scopes {
		if len(sc.targets) > 0 {
			targets = append(targets, sc.targets...)
			continue
		}
		targets = append(targets, &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: sc.KnowledgeBaseID, KnowledgeIDs: sc.KnowledgeIDs, TagIDs: sc.TagIDs, SourceIDs: sc.SourceIDs})
	}
	return reader.BeginWikiRead(ctx, targets)
}
