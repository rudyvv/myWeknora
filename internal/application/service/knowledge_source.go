package service

import (
	"context"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *knowledgeService) GetSourceFile(ctx context.Context, id string, versionID ...string) (*types.SourceFileView, error) {
	knowledge, err := s.repo.GetKnowledgeByID(ctx, types.MustTenantIDFromContext(ctx), id)
	if err != nil {
		return nil, err
	}
	if knowledge.Type != types.KnowledgeTypeSource {
		return nil, apperrors.NewBadRequestError("knowledge is not a repository source file")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, knowledge.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	if _, err := resolveKBReadTenant(ctx, kb, s.kbShareService); err != nil {
		return nil, err
	}
	reader, ok := s.repo.(interfaces.SourceFileRepository)
	if !ok {
		return nil, apperrors.NewServiceUnavailableError("source file reader is unavailable")
	}
	return reader.ReadPublishedSourceFile(ctx, kb.TenantID, id, versionID...)
}
