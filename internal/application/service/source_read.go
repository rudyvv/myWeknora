package service

import (
	"context"
	"strconv"

	"github.com/Tencent/WeKnora/internal/application/access"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *knowledgeBaseService) BeginSourceRead(ctx context.Context, targets types.SearchTargets) (context.Context, func(), error) {
	noop := func() {}
	if source.HasReadScope(ctx) {
		return ctx, noop, source.ValidateReadScope(ctx)
	}
	repo, ok := s.kgRepo.(interfaces.SourceReadRepository)
	if !ok || len(targets) == 0 {
		return ctx, noop, nil
	}
	normalized := make(types.SearchTargets, 0, len(targets))
	for _, target := range targets {
		if target == nil || target.KnowledgeBaseID == "" {
			continue
		}
		kb, err := s.repo.GetKnowledgeBaseByID(ctx, target.KnowledgeBaseID)
		if err != nil {
			return ctx, noop, err
		}
		if _, err = resolveKBReadTenant(ctx, kb, s.kbShareService); err != nil {
			return ctx, noop, err
		}
		copy := *target
		copy.TenantID = kb.TenantID
		normalized = append(normalized, &copy)
	}
	lease, release, err := repo.AcquireSourceRead(ctx, normalized)
	if err != nil {
		return ctx, noop, err
	}
	if lease.ID == "" {
		return ctx, release, nil
	}
	agentID, agentTenant := access.SharedAgentIdentity(ctx)
	validate := func(current context.Context) error {
		if err := repo.CheckSourceRead(current, lease.ID); err != nil {
			return err
		}
		// Previously issued grants cannot preserve a revoked organization share.
		fresh := context.WithValue(current, types.KBGrantsContextKey, nil)
		fresh = context.WithValue(fresh, types.SharedAgentGrantContextKey, nil)
		for _, target := range normalized {
			kb, err := s.repo.GetKnowledgeBaseByID(fresh, target.KnowledgeBaseID)
			if err != nil {
				return err
			}
			if kb.TenantID != target.TenantID {
				return apperrors.NewForbiddenError("source knowledge base ownership changed")
			}
			shares := s.kbShareService
			if types.CallerFromContext(fresh).UserID == "" {
				shares = nil
			}
			_, err = access.ResolveKB(fresh, access.KBRequest{Caller: types.CallerFromContext(fresh), AgentID: agentID, AgentSourceTenantID: strconv.FormatUint(agentTenant, 10)}, kb, types.OrgRoleViewer, shares, s.agentShareService)
			if err != nil {
				return err
			}
		}
		return nil
	}
	pinned, releaseReader := source.WithReadScope(ctx, lease, validate, release)
	return pinned, releaseReader, nil
}

func beginSourceRead(ctx context.Context, service interfaces.KnowledgeBaseService, targets types.SearchTargets) (context.Context, func(), error) {
	if reader, ok := service.(interfaces.SourceReadService); ok {
		return reader.BeginSourceRead(ctx, targets)
	}
	return ctx, func() {}, nil
}
