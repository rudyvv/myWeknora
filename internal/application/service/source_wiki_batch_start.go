package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// StartSourceWikiBatch revalidates the preflight, then atomically persists the
// fixed parent and the full server-computed candidate plan before waking the
// durable background runner.
func (s *sourceWikiService) StartSourceWikiBatch(ctx context.Context, knowledgeBaseID, sourceID string, req types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error) {
	preflight, err := s.PreflightSourceWikiBatch(ctx, knowledgeBaseID, sourceID, req)
	if err != nil {
		return nil, err
	}
	if !preflight.PreflightPassed || !preflight.StartAvailable || !preflight.ModelContextKnown || len(preflight.PlannedTopics) == 0 {
		return nil, fmt.Errorf("%w: batch start requires a complete plan and a confirmed model context window", repository.ErrSourceWikiBatchInvalidState)
	}
	hasExpectedBinding := req.ExpectedSnapshotID != "" || !req.ExpectedSourceUpdatedAt.IsZero() ||
		req.ExpectedModelID != "" || !req.ExpectedModelUpdatedAt.IsZero()
	if hasExpectedBinding {
		if req.ExpectedSnapshotID == "" || req.ExpectedSourceUpdatedAt.IsZero() || req.ExpectedModelID == "" || req.ExpectedModelUpdatedAt.IsZero() {
			return nil, fmt.Errorf("%w: preview binding is incomplete; run preflight again", repository.ErrSourceWikiBatchInvalidState)
		}
		if req.ExpectedSnapshotID != preflight.SnapshotID || !req.ExpectedSourceUpdatedAt.Equal(preflight.SourceUpdatedAt) ||
			req.ExpectedModelID != preflight.ModelID || !req.ExpectedModelUpdatedAt.Equal(preflight.ModelUpdatedAt) {
			return nil, fmt.Errorf("%w: source or model changed after preview; run preflight again", repository.ErrSourceWikiBatchInvalidState)
		}
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	batch := &types.SourceWikiBatch{
		ID: uuid.NewString(), TenantID: 0, KnowledgeBaseID: knowledgeBaseID, SourceID: sourceID,
		SnapshotID: preflight.SnapshotID, SourceConfigFingerprint: preflight.SourceConfigFingerprint,
		SourceUpdatedAt: preflight.SourceUpdatedAt, ModelID: preflight.ModelID,
		ModelSettingsFingerprint: preflight.ModelSettingsFingerprint, ModelContextWindow: preflight.ModelContextWindow,
		MaxCompletionTokens: preflight.MaxCompletionTokens, Status: "running", Phase: "skeleton",
		MaxCalls: preflight.MaxCalls, MaxTokens: preflight.MaxTokens, MaxElapsedMS: preflight.MaxElapsedMS,
		MaxInitialTopics: preflight.MaxInitialTopics, SkeletonMaxCalls: preflight.SkeletonMaxCalls,
		SkeletonMaxTokens: preflight.SkeletonMaxTokens, QAMaxCalls: preflight.QAMaxCalls,
		QAMaxTokens: preflight.QAMaxTokens, CreatedAt: now, UpdatedAt: now,
		DeadlineAt: now.Add(time.Duration(preflight.MaxElapsedMS) * time.Millisecond),
	}
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	batch.TenantID = kb.TenantID
	if err := repository.NewSourceWikiBatchLedger(s.db).CreateWithPlan(ctx, batch, preflight.PlannedTopics, now); err != nil {
		return nil, err
	}
	batch, err = repository.NewSourceWikiBatchLedger(s.db).Get(ctx, knowledgeBaseID, batch.ID)
	if err != nil {
		return nil, err
	}
	s.ResumeSourceWikiBatch(ctx, batch.ID)
	return batch, nil
}
