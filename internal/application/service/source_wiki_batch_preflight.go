package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"gorm.io/gorm"
)

const sourceWikiBatchPreflightTimeout = 3 * time.Minute

// PreflightSourceWikiBatch validates the server-selected source publication
// and model, then returns a bounded plan preview. It has no persistence or
// provider side effects; the accepted batch runner will own actual creation.
func (s *sourceWikiService) PreflightSourceWikiBatch(
	ctx context.Context,
	knowledgeBaseID string,
	sourceID string,
	req types.SourceWikiBatchPreflightRequest,
) (*types.SourceWikiBatchPreflight, error) {
	if s == nil || s.db == nil || s.kb == nil || s.models == nil || knowledgeBaseID == "" || sourceID == "" {
		return nil, apperrors.NewBadRequestError("knowledge base, source and model services are required")
	}
	restartBatchID := strings.TrimSpace(req.RestartOfBatchID)
	if len(restartBatchID) > 36 {
		return nil, apperrors.NewBadRequestError("restart_of_batch_id is invalid")
	}
	req.RestartOfBatchID = restartBatchID
	kb, err := s.kb.GetKnowledgeBaseByID(ctx, knowledgeBaseID)
	if err != nil {
		return nil, err
	}
	ctx, err = requireKBWrite(ctx, kb)
	if err != nil {
		return nil, err
	}
	if !kb.IsWikiEnabled() {
		return nil, apperrors.NewBadRequestError("Wiki feature is disabled")
	}
	if s.db.Dialector.Name() != "postgres" {
		return nil, apperrors.NewBadRequestError("source Wiki requires PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(ctx, sourceWikiBatchPreflightTimeout)
	defer cancel()
	ctx, release, err := beginSourceRead(ctx, s.kb, types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: kb.ID, SourceIDs: []string{sourceID},
	}})
	if err != nil {
		return nil, err
	}
	defer release()
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}

	var dataSource types.DataSource
	sourcePermission := source.SourcePermissionSQL(ctx, "data_sources.id", "NULL")
	if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND type = ? AND deleted_at IS NULL",
		sourceID, kb.TenantID, kb.ID, types.ConnectorTypeGitLab).Where(sourcePermission).Take(&dataSource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrSourceWikiBatchNotFound
		}
		return nil, err
	}

	publication, snapshot, err := loadCurrentSourceWikiPublication(ctx, s.db, kb, sourceID)
	if err != nil {
		return nil, err
	}
	if err := rejectActiveSourceWikiBatch(ctx, s.db, kb, sourceID); err != nil {
		return nil, err
	}
	restartFrom, err := loadSourceWikiRestartReference(ctx, s.db, kb, sourceID, req.RestartOfBatchID)
	if err != nil {
		return nil, err
	}

	modelID := sourceWikiBatchModelID(kb)
	if modelID == "" {
		return nil, apperrors.NewBadRequestError("Wiki generation model is not configured")
	}
	model, err := s.models.GetModelByID(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if model == nil || model.Type != types.ModelTypeKnowledgeQA || model.Status != types.ModelStatusActive || !model.DeletedAt.Time.IsZero() {
		return nil, apperrors.NewBadRequestError("Wiki generation model is unavailable or is not an active chat model")
	}
	completionTokens := types.SourceWikiBatchMaxCompletionTokens
	if model.Parameters.MaxOutputTokens > 0 && model.Parameters.MaxOutputTokens < completionTokens {
		completionTokens = model.Parameters.MaxOutputTokens
	}
	if model.Parameters.ContextWindow > 0 && model.Parameters.ContextWindow < completionTokens+types.SourceWikiBatchMinInputTokens {
		return nil, apperrors.NewBadRequestError("Wiki model context window must fit the bounded prompt and completion budgets")
	}

	projection, err := repository.LoadSourceWikiModuleProjection(ctx, s.db, kb.TenantID, kb.ID, sourceID, snapshot.ID)
	if err != nil {
		if errors.Is(err, repository.ErrSourceWikiModuleProjectionBudgetExceeded) {
			return nil, fmt.Errorf("%w: source Wiki module inventory exceeded a hard bound: %w", repository.ErrSourceWikiDerivationDeferred, err)
		}
		return nil, fmt.Errorf("%w: source Wiki module inventory is unavailable: %w", repository.ErrSourceWikiDerivationUnavailable, err)
	}
	if projection == nil || projection.TenantID != kb.TenantID || projection.KnowledgeBaseID != kb.ID ||
		projection.DataSourceID != sourceID || projection.SnapshotID != snapshot.ID || !projection.InventoryComplete ||
		projection.RelationsComplete || !projection.RelationsDeferred || len(projection.Members) != projection.MemberCount {
		return nil, fmt.Errorf("%w: source Wiki module projection is incomplete or belongs to another publication", repository.ErrSourceWikiDerivationUnavailable)
	}
	moduleSeedFiles := make([]sourceWikiSkeletonFile, 0, projection.ParsedFileCount)
	for _, member := range projection.Members {
		file := sourceWikiSkeletonFile{Path: member.Path, Generated: member.Generated, Facts: []types.ParsedSourceFact{}}
		if member.Seed != nil {
			file.Facts = append(file.Facts, types.ParsedSourceFact{Kind: member.Seed.Kind, Name: member.Seed.Name, Quality: member.Seed.Quality})
		}
		moduleSeedFiles = append(moduleSeedFiles, file)
	}

	relationInventory, err := repository.LoadSourceWikiRelationInventory(ctx, s.db, kb.TenantID, kb.ID, sourceID, snapshot.ID)
	if err != nil {
		if errors.Is(err, repository.ErrSourceWikiDerivationDeferred) || errors.Is(err, repository.ErrSourceWikiDerivationUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: source Wiki relation inventory is unavailable: %w", repository.ErrSourceWikiDerivationUnavailable, err)
	}
	if relationInventory == nil || relationInventory.TenantID != kb.TenantID || relationInventory.KnowledgeBaseID != kb.ID ||
		relationInventory.DataSourceID != sourceID || relationInventory.SnapshotID != snapshot.ID ||
		!relationInventory.RelationsComplete || len(relationInventory.Relations) != relationInventory.ExpectedCount {
		return nil, fmt.Errorf("%w: source Wiki relation inventory is incomplete or belongs to another publication", repository.ErrSourceWikiDerivationUnavailable)
	}
	var skeletonFiles []sourceWikiSkeletonFile
	var factSnapshot *repository.SourceWikiSkeletonSnapshot
	if sourceWikiHasHTTPRoute(relationInventory.Relations) {
		impact, loaded, loadErr := repository.LoadSourceWikiImpactSnapshotWithRelationInventory(
			s.db.WithContext(ctx), kb.TenantID, kb.ID, sourceID, snapshot.ID,
			types.SourceWikiImpactPublishedComplete, relationInventory)
		if loadErr != nil {
			switch {
			case errors.Is(loadErr, repository.ErrSourceWikiImpactLoadBudgetExceeded):
				return nil, fmt.Errorf("%w: complete source facts for HTTP route resolution exceed a hard bound: %w", repository.ErrSourceWikiDerivationDeferred, loadErr)
			case errors.Is(loadErr, repository.ErrSourceWikiImpactProofIncomplete), errors.Is(loadErr, repository.ErrSourceWikiImpactSnapshotInvalid):
				return nil, fmt.Errorf("%w: complete source facts for HTTP route resolution are unavailable: %w", repository.ErrSourceWikiDerivationUnavailable, loadErr)
			default:
				return nil, fmt.Errorf("%w: complete source facts for HTTP route resolution are unavailable: %w", repository.ErrSourceWikiDerivationUnavailable, loadErr)
			}
		}
		if impact.TenantID != kb.TenantID || impact.KnowledgeBaseID != kb.ID || impact.SourceID != sourceID || impact.SnapshotID != snapshot.ID ||
			impact.Stage != types.SourceWikiImpactPublishedComplete || !impact.ManifestComplete || !impact.RelationsComplete ||
			impact.ExpectedMemberCount != projection.MemberCount || impact.ExpectedRelationCount != relationInventory.ExpectedCount ||
			loaded == nil || !loaded.Complete || loaded.TenantID != kb.TenantID || loaded.DataSourceID != sourceID || loaded.SnapshotID != snapshot.ID ||
			!reflect.DeepEqual(relationInventory.Relations, impact.Relations) || !reflect.DeepEqual(relationInventory.Relations, loaded.Relations) {
			return nil, fmt.Errorf("%w: complete HTTP route facts do not match the current published relation inventory", repository.ErrSourceWikiDerivationUnavailable)
		}
		factSnapshot = loaded
		skeletonFiles = make([]sourceWikiSkeletonFile, len(loaded.Files))
		for i, file := range loaded.Files {
			skeletonFiles[i] = sourceWikiSkeletonFile{Path: file.Path, Generated: file.Generated, Facts: file.Facts}
		}
	} else {
		// Without route anchors, the planner does not need full parser facts for
		// flow correlation; projection seeds still preserve module candidates.
		skeletonFiles = moduleSeedFiles
	}
	relations, err := s.resolveSourceWikiRelations(ctx, kb.TenantID, kb.ID, sourceID, snapshot.ID, factSnapshot, relationInventory.Relations)
	if err != nil {
		return nil, fmt.Errorf("%w: HTTP route relation references could not be verified: %w", repository.ErrSourceWikiDerivationUnavailable, err)
	}
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: sourceID, SnapshotID: snapshot.ID, Files: skeletonFiles, ModuleSeedFiles: moduleSeedFiles, Relations: relations,
	}, types.SourceWikiBatchMaxInitialTopics)
	if len(plan.Topics) > types.SourceWikiBatchMaxCandidates {
		return nil, fmt.Errorf("%w: candidate inventory exceeds the %d-topic preflight bound", repository.ErrSourceWikiDerivationDeferred, types.SourceWikiBatchMaxCandidates)
	}
	if err := validateSourceWikiSkeletonPlan(plan); err != nil {
		return nil, fmt.Errorf("%w: source Wiki candidate plan is invalid: %w", repository.ErrSourceWikiDerivationUnavailable, err)
	}
	if plan.ModuleCount == 0 && plan.FlowCount == 0 {
		return nil, fmt.Errorf("%w: the published source has no structural module or HTTP flow candidates for Wiki coverage", repository.ErrSourceWikiDerivationUnavailable)
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}

	// Reject a preflight that went stale while the bounded inventory was read.
	var currentSource types.DataSource
	if err := s.db.WithContext(ctx).Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND deleted_at IS NULL", sourceID, kb.TenantID, kb.ID).
		Where(sourcePermission).Take(&currentSource).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrSourceWikiBatchInvalidState
		}
		return nil, err
	}
	if !currentSource.UpdatedAt.Equal(dataSource.UpdatedAt) {
		return nil, repository.ErrSourceWikiBatchInvalidState
	}
	currentPublication, currentSnapshot, err := loadCurrentSourceWikiPublication(ctx, s.db, kb, sourceID)
	if err != nil {
		return nil, err
	}
	if currentPublication.SnapshotID != publication.SnapshotID || currentSnapshot.CommitSHA != snapshot.CommitSHA {
		return nil, repository.ErrSourceWikiBatchInvalidState
	}
	currentModel, err := s.models.GetModelByID(ctx, modelID)
	if err != nil {
		return nil, err
	}
	if !sourceWikiSameModel(model, currentModel) {
		return nil, repository.ErrSourceWikiBatchInvalidState
	}
	currentKB, err := s.kb.GetKnowledgeBaseByID(ctx, kb.ID)
	if err != nil {
		return nil, err
	}
	if currentKB == nil || currentKB.TenantID != kb.TenantID || !currentKB.IsWikiEnabled() ||
		!currentKB.UpdatedAt.Equal(kb.UpdatedAt) || sourceWikiBatchModelID(currentKB) != modelID {
		return nil, repository.ErrSourceWikiBatchInvalidState
	}

	modelFingerprint, err := sourceWikiModelSettingsFingerprint(model)
	if err != nil {
		return nil, err
	}
	initialTopics := append([]types.SourceWikiTopic(nil), plan.Topics[:plan.InitialCount]...)
	warnings := []string{}
	if model.Parameters.ContextWindow <= 0 {
		warnings = append(warnings, "The model context window is unknown; runtime calls must enforce their own input bounds.")
	}
	return &types.SourceWikiBatchPreflight{
		PreflightPassed: true, StartAvailable: model.Parameters.ContextWindow > 0,
		DispatchReason: func() string {
			if model.Parameters.ContextWindow <= 0 {
				return "The model context window is unknown; batch generation cannot be started safely."
			}
			return ""
		}(),
		SourceID: sourceID, SnapshotID: snapshot.ID, CommitSHA: snapshot.CommitSHA,
		PublishedAt: snapshot.PublishedAt, SourceUpdatedAt: dataSource.UpdatedAt,
		ModelID: model.ID, ModelUpdatedAt: model.UpdatedAt, ModelContextWindow: model.Parameters.ContextWindow,
		ModelContextKnown: model.Parameters.ContextWindow > 0, MaxCompletionTokens: completionTokens,
		CandidateCount: len(plan.Topics), InitialCount: plan.InitialCount, ExpansionCount: plan.ExpansionCount,
		ModuleCount: plan.ModuleCount, FlowCount: plan.FlowCount, InitialTopics: initialTopics,
		RestartFrom: restartFrom, Warnings: warnings,
		MaxCalls: types.SourceWikiBatchMaxCalls, MaxTokens: types.SourceWikiBatchMaxTokens,
		MaxElapsedMS: types.SourceWikiBatchMaxElapsed.Milliseconds(), MaxInitialTopics: types.SourceWikiBatchMaxInitialTopics,
		SkeletonMaxCalls: types.SourceWikiBatchSkeletonMaxCalls, SkeletonMaxTokens: types.SourceWikiBatchSkeletonMaxTokens,
		QAMaxCalls: types.SourceWikiBatchQAMaxCalls, QAMaxTokens: types.SourceWikiBatchQAMaxTokens,
		SourceConfigFingerprint:  sourceWikiSourceFingerprint(&dataSource),
		ModelSettingsFingerprint: modelFingerprint, PlannedTopics: plan.Topics,
	}, nil
}

func loadCurrentSourceWikiPublication(ctx context.Context, db *gorm.DB, kb *types.KnowledgeBase, sourceID string) (*types.SourcePublication, *types.SourceSnapshot, error) {
	var publication types.SourcePublication
	if err := db.WithContext(ctx).Where("data_source_id = ? AND tenant_id = ? AND knowledge_base_id = ?", sourceID, kb.TenantID, kb.ID).Take(&publication).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, repository.ErrSourceWikiBatchInvalidState
		}
		return nil, nil, err
	}
	var snapshot types.SourceSnapshot
	if err := db.WithContext(ctx).Where("id = ? AND data_source_id = ? AND tenant_id = ? AND knowledge_base_id = ? AND state = 'published' AND manifest_complete = TRUE",
		publication.SnapshotID, sourceID, kb.TenantID, kb.ID).Take(&snapshot).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, repository.ErrSourceWikiBatchInvalidState
		}
		return nil, nil, err
	}
	if snapshot.WikiDerivationState == "deferred_capacity" {
		return nil, nil, fmt.Errorf("%w: this source snapshot remains searchable, but Wiki generation was deferred by capacity", repository.ErrSourceWikiDerivationDeferred)
	}
	if snapshot.WikiDerivationState != "complete" || !snapshot.RelationsStaged {
		return nil, nil, fmt.Errorf("%w: the published source has no complete Wiki derivation", repository.ErrSourceWikiDerivationUnavailable)
	}
	return &publication, &snapshot, nil
}

func sourceWikiBatchModelID(kb *types.KnowledgeBase) string {
	if kb == nil {
		return ""
	}
	if kb.WikiConfig != nil && kb.WikiConfig.SynthesisModelID != "" {
		return kb.WikiConfig.SynthesisModelID
	}
	return kb.SummaryModelID
}

func sourceWikiHasHTTPRoute(relations []types.SourceCodeRelation) bool {
	for _, relation := range relations {
		if relation.Kind == "http_route" {
			return true
		}
	}
	return false
}

func rejectActiveSourceWikiBatch(ctx context.Context, db *gorm.DB, kb *types.KnowledgeBase, sourceID string) error {
	permission := source.SourcePermissionSQL(ctx, "source_wiki_batches.source_id", "NULL")
	var active types.SourceWikiBatch
	err := db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND status IN ?",
		kb.TenantID, kb.ID, sourceID, []string{"queued", "running"}).Where(permission).Take(&active).Error
	if err == nil {
		return repository.ErrSourceWikiBatchAlreadyActive
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return nil
}

func loadSourceWikiRestartReference(ctx context.Context, db *gorm.DB, kb *types.KnowledgeBase, sourceID, batchID string) (*types.SourceWikiBatchRestartReference, error) {
	if batchID == "" {
		return nil, nil
	}
	permission := source.SourcePermissionSQL(ctx, "source_wiki_batches.source_id", "NULL")
	var batch types.SourceWikiBatch
	if err := db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ? AND source_id = ? AND id = ?",
		kb.TenantID, kb.ID, sourceID, batchID).Where(permission).Take(&batch).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, repository.ErrSourceWikiBatchNotFound
		}
		return nil, err
	}
	if batch.Status != "completed" && batch.Status != "failed" && batch.Status != "expired" {
		return nil, repository.ErrSourceWikiBatchInvalidState
	}
	return &types.SourceWikiBatchRestartReference{
		ID: batch.ID, Status: batch.Status, SnapshotID: batch.SnapshotID, Phase: batch.Phase,
		Cursor: batch.Cursor, CandidateCount: batch.CandidateCount, InitialCount: batch.InitialCount,
		CallsReserved: batch.CallsReserved, TokensReserved: batch.TokensReserved,
	}, nil
}

func sourceWikiModelSettingsFingerprint(model *types.Model) (string, error) {
	if model == nil {
		return "", fmt.Errorf("Wiki generation model is unavailable")
	}
	return sourceWikiModelFingerprint(model), nil
}
