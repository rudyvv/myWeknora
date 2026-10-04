package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type sourceWikiUpdateTopic struct {
	TopicKey      string
	Kind          string
	ModulePath    string
	SourceOwned   bool
	EvidenceSHA   string
	DependencyIDs []string
}

type sourceWikiUpdateInventoryData struct {
	coverage      []types.SourceWikiCoverageTopic
	contributions []types.SourceWikiPageContribution
	previous      types.SourceWikiImpactTopicInventory
	next          types.SourceWikiImpactTopicInventory
	complete      bool
}

var errSourceWikiUpdateInventoryBudgetExceeded = errors.New("source Wiki update inventory exceeded its adapter work budget")

const (
	sourceWikiUpdateInventoryMaxWork = 1_500_000
	sourceWikiUpdateInventoryMaxRefs = 500_000
)

type sourceWikiUpdateInventoryBudget struct {
	work int
	refs int
}

func (b *sourceWikiUpdateInventoryBudget) consume(work, refs int) error {
	if b == nil {
		return nil
	}
	if work < 0 || refs < 0 || work > sourceWikiUpdateInventoryMaxWork || refs > sourceWikiUpdateInventoryMaxRefs ||
		b.work > sourceWikiUpdateInventoryMaxWork-work || b.refs > sourceWikiUpdateInventoryMaxRefs-refs {
		return errSourceWikiUpdateInventoryBudgetExceeded
	}
	b.work += work
	b.refs += refs
	return nil
}

type sourceWikiNextImpactIndex struct {
	parsedFileIDs map[string]bool
	flowFileIDs   map[string]map[string]bool
}

// ProcessPublishedSourceWikiUpdate consumes an accepted publication only
// after rechecking the database-derived source, snapshot, and configuration
// fences. It plans conservatively: incomplete inputs leave all old answers
// stale rather than guessing that a page is unaffected.
func (s *sourceWikiService) ProcessPublishedSourceWikiUpdate(ctx context.Context, payload types.SourceWikiUpdatePayload) error {
	if s == nil || s.db == nil || payload.SchemaVersion != 1 || payload.EventID == "" || payload.DeliveryID == "" ||
		payload.TenantID == 0 || payload.KnowledgeBaseID == "" || payload.DataSourceID == "" || payload.SnapshotID == "" || payload.ConfigGeneration <= 0 {
		return fmt.Errorf("source Wiki update payload identity is incomplete")
	}
	ctx = types.WithExecutionTenant(ctx, payload.TenantID)
	var durable types.SourceWikiUpdatePlan
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND knowledge_base_id=? AND source_id=? AND snapshot_id=?",
		payload.TenantID, payload.KnowledgeBaseID, payload.DataSourceID, payload.SnapshotID).Take(&durable).Error; err != nil {
		return fmt.Errorf("source Wiki update plan is unavailable: %w", err)
	}
	if durable.ConfigGeneration != payload.ConfigGeneration {
		return s.finishSupersededSourceWikiUpdate(ctx, durable.ID, "configuration_generation_changed")
	}
	if durable.Status == "completed" {
		return s.dispatchPendingSourceWikiRegeneration(ctx, payload)
	}
	if durable.Status == "superseded" {
		return nil
	}
	if durable.Status != "pending" && durable.Status != "running" {
		return fmt.Errorf("source Wiki update plan has unsupported state %q", durable.Status)
	}

	// Both sides are real retained publications by the time the asynchronous
	// outbox is consumed. The planner still requires their exact complete
	// manifests and relation inventories before it can carry any page forward.
	impact := types.SourceWikiImpactPlan{
		TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID,
		SourceID: payload.DataSourceID, PreviousSnapshotID: durable.PreviousSnapshotID,
		NextSnapshotID: payload.SnapshotID, Disposition: types.SourceWikiImpactSourceWideStale,
		FallbackReason: "previous_snapshot_unavailable", RescanSkeleton: true,
		RescanReasons: []string{"source_wide_stale_fallback"},
	}
	inventory := types.SourceWikiImpactTopicInventory{
		TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID,
		SourceID: payload.DataSourceID, SnapshotID: payload.SnapshotID, Complete: false,
	}
	if durable.PreviousSnapshotID != "" && durable.PreviousSnapshotID != payload.SnapshotID {
		previous, previousLoaded, err := repository.LoadSourceWikiImpactSnapshot(s.db.WithContext(ctx), payload.TenantID,
			payload.KnowledgeBaseID, payload.DataSourceID, durable.PreviousSnapshotID, types.SourceWikiImpactPublishedComplete)
		if err != nil {
			if reason, deterministic := sourceWikiImpactLoadFallbackReason(err); deterministic {
				return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, reason)
			}
			return fmt.Errorf("load previous published source Wiki impact snapshot: %w", err)
		}
		next, nextLoaded, err := repository.LoadSourceWikiImpactSnapshot(s.db.WithContext(ctx), payload.TenantID,
			payload.KnowledgeBaseID, payload.DataSourceID, payload.SnapshotID, types.SourceWikiImpactPublishedComplete)
		if err != nil {
			if reason, deterministic := sourceWikiImpactLoadFallbackReason(err); deterministic {
				return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, reason)
			}
			return fmt.Errorf("load next published source Wiki impact snapshot: %w", err)
		}
		previous.Relations, err = s.resolveSourceWikiRelations(ctx, payload.TenantID, payload.KnowledgeBaseID,
			payload.DataSourceID, durable.PreviousSnapshotID, previousLoaded, previous.Relations)
		if err != nil {
			return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, "previous_relation_fact_resolution_failed")
		}
		next.Relations, err = s.resolveSourceWikiRelations(ctx, payload.TenantID, payload.KnowledgeBaseID,
			payload.DataSourceID, payload.SnapshotID, nextLoaded, next.Relations)
		if err != nil {
			return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, "next_relation_fact_resolution_failed")
		}
		previousLoaded.Relations = previous.Relations
		nextLoaded.Relations = next.Relations
		data, err := s.loadSourceWikiUpdateInventories(ctx, payload, durable.PreviousSnapshotID,
			previous, previousLoaded, next, nextLoaded)
		if err != nil {
			if errors.Is(err, errSourceWikiUpdateInventoryBudgetExceeded) {
				return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, "topic_inventory_budget_exceeded")
			}
			return fmt.Errorf("load source Wiki topic inventories: %w", err)
		}
		inventory = data.next
		impact, err = source.PlanSourceWikiImpact(previous, next, []types.SourceWikiImpactTopicInventory{data.previous, data.next})
		if err != nil {
			// Planner rejection is deterministic for these already-loaded bounded
			// inputs. Persist a source-wide stale decision and clear any partial
			// per-topic result instead of retrying or committing partial claims.
			return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, "impact_planner_rejected_input")
		}
		if !data.complete && impact.Disposition != types.SourceWikiImpactSourceWideStale {
			return s.persistSourceWikiImpactFallback(ctx, payload, durable, impact, "topic_inventory_incomplete")
		}
	}
	if err := s.persistSourceWikiImpactPlan(ctx, payload, durable, impact, inventory); err != nil {
		return err
	}
	return s.dispatchPendingSourceWikiRegeneration(ctx, payload)
}

func sourceWikiImpactLoadFallbackReason(err error) (string, bool) {
	var loadErr *repository.SourceWikiImpactLoadError
	if !errors.As(err, &loadErr) || loadErr == nil {
		return "", false
	}
	switch {
	case errors.Is(err, repository.ErrSourceWikiImpactProofIncomplete):
		return "impact_load_proof_incomplete_" + string(loadErr.ReasonCode), true
	case errors.Is(err, repository.ErrSourceWikiImpactSnapshotInvalid):
		return "impact_load_snapshot_invalid_" + string(loadErr.ReasonCode), true
	case errors.Is(err, repository.ErrSourceWikiImpactLoadBudgetExceeded):
		return "impact_load_budget_exceeded_" + string(loadErr.ReasonCode), true
	default:
		return "", false
	}
}

func (s *sourceWikiService) finishSupersededSourceWikiUpdate(ctx context.Context, planID, reason string) error {
	now := time.Now().UTC()
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan types.SourceWikiUpdatePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", planID).Take(&plan).Error; err != nil {
			return err
		}
		if plan.Status == "completed" || plan.Status == "superseded" {
			return nil
		}
		if err := tx.Model(&types.SourceWikiUpdatePlanItem{}).Where("plan_id=? AND state IN ?", plan.ID, []string{"pending", "running"}).
			Update("state", "superseded").Error; err != nil {
			return err
		}
		return tx.Model(&plan).Updates(map[string]any{
			"status": "superseded", "source_wide_stale": true, "reason_code": reason,
			"reason":       "A newer publication or source configuration superseded this Wiki update.",
			"completed_at": now, "updated_at": now,
		}).Error
	})
}

func (s *sourceWikiService) persistSourceWikiImpactFallback(ctx context.Context, payload types.SourceWikiUpdatePayload,
	durable types.SourceWikiUpdatePlan, impact types.SourceWikiImpactPlan, reason string) error {
	impact.Disposition = types.SourceWikiImpactSourceWideStale
	impact.Affected, impact.Unaffected, impact.Removed = nil, nil, nil
	impact.FallbackReason = reason
	impact.RescanSkeleton = true
	impact.RescanReasons = []string{"source_wide_stale_fallback"}
	if err := s.persistSourceWikiImpactPlan(ctx, payload, durable, impact, types.SourceWikiImpactTopicInventory{
		TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID,
		SourceID: payload.DataSourceID, SnapshotID: payload.SnapshotID, Complete: false,
	}); err != nil {
		return err
	}
	return s.dispatchPendingSourceWikiRegeneration(ctx, payload)
}

func (s *sourceWikiService) loadSourceWikiUpdateInventories(ctx context.Context, payload types.SourceWikiUpdatePayload,
	previousSnapshotID string, previous types.SourceWikiImpactSnapshot, previousLoaded *repository.SourceWikiSkeletonSnapshot,
	next types.SourceWikiImpactSnapshot, nextLoaded *repository.SourceWikiSkeletonSnapshot) (sourceWikiUpdateInventoryData, error) {
	data := sourceWikiUpdateInventoryData{complete: true}
	budget := &sourceWikiUpdateInventoryBudget{}
	if !previousLoaded.Complete || !nextLoaded.Complete {
		data.complete = false
	}
	if err := validateSourceWikiSkeletonInputSnapshot(previousLoaded, payload.TenantID, payload.DataSourceID, previousSnapshotID); err != nil {
		data.complete = false
	}
	if err := validateSourceWikiSkeletonInputSnapshot(nextLoaded, payload.TenantID, payload.DataSourceID, payload.SnapshotID); err != nil {
		data.complete = false
	}
	if err := budget.consume(sourceWikiImpactSnapshotAdapterWork(previous)+sourceWikiImpactSnapshotAdapterWork(next), 0); err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	var coverage []types.SourceWikiCoverageTopic
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND knowledge_base_id=? AND source_id=?", payload.TenantID, payload.KnowledgeBaseID, payload.DataSourceID).
		Order("topic_key ASC").Limit(types.SourceWikiBatchMaxCandidates + 1).Find(&coverage).Error; err != nil {
		return data, err
	}
	if len(coverage) > types.SourceWikiBatchMaxCandidates {
		data.complete = false
		coverage = nil
	}
	data.coverage = coverage
	var contributions []types.SourceWikiPageContribution
	if err := s.db.WithContext(ctx).Model(&types.SourceWikiPageContribution{}).
		Joins("JOIN wiki_pages wp ON wp.id=source_wiki_page_contributions.page_id").
		Where("wp.tenant_id=? AND wp.knowledge_base_id=? AND source_wiki_page_contributions.source_id=? AND source_wiki_page_contributions.revision_id IS NULL",
			payload.TenantID, payload.KnowledgeBaseID, payload.DataSourceID).
		Order("source_wiki_page_contributions.topic_key ASC, source_wiki_page_contributions.page_id ASC").
		Limit(types.SourceWikiBatchMaxCandidates + 1).Find(&contributions).Error; err != nil {
		return data, err
	}
	if len(contributions) > types.SourceWikiBatchMaxCandidates {
		data.complete = false
		contributions = nil
	}
	data.contributions = contributions

	previousSkeleton, err := sourceWikiSkeletonPlanForImpact(previous, previousLoaded)
	if err != nil {
		data.complete = false
	}
	nextSkeleton, err := sourceWikiSkeletonPlanForImpact(next, nextLoaded)
	if err != nil {
		data.complete = false
	}
	if err := budget.consume(len(previousSkeleton)+len(nextSkeleton), 0); err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	nextIndex, err := buildSourceWikiNextImpactIndex(next, budget)
	if err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	removalEvidence, err := sourceWikiBuildRemovalEvidenceBounded(next, nextLoaded, budget)
	if err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	previousTopics := make(map[string]sourceWikiUpdateTopic)
	for _, topic := range previousSkeleton {
		previousTopics[topic.TopicKey] = sourceWikiUpdateTopic{TopicKey: topic.TopicKey, Kind: topic.Kind, ModulePath: topic.ModulePath, SourceOwned: true}
	}
	contributionByTopic := make(map[string]types.SourceWikiPageContribution)
	for _, row := range contributions {
		if row.TopicKind == "" || row.TopicKey == "" {
			data.complete = false
			continue
		}
		if existing, duplicate := contributionByTopic[row.TopicKey]; duplicate &&
			(existing.PageID != row.PageID || existing.TopicKind != row.TopicKind) {
			data.complete = false
			continue
		}
		contributionByTopic[row.TopicKey] = row
		if row.ApplicableSnapshotID != previousSnapshotID {
			continue
		}
		topic := previousTopics[row.TopicKey]
		if topic.TopicKey == "" {
			topic = sourceWikiUpdateTopic{TopicKey: row.TopicKey, Kind: row.TopicKind, ModulePath: sourceWikiModulePathForTopic(row.TopicKind, row.TopicKey)}
		}
		topic.SourceOwned = sourceWikiContributionSourceOwned(row.Contribution)
		topic.EvidenceSHA = row.EvidenceSHA256
		if err := json.Unmarshal(row.DependencyFileIDs, &topic.DependencyIDs); err != nil || topic.DependencyIDs == nil {
			data.complete = false
			topic.DependencyIDs = []string{}
		}
		previousTopics[row.TopicKey] = topic
	}
	for _, row := range coverage {
		if row.SnapshotID != previousSnapshotID && row.LastReadySnapshotID != previousSnapshotID {
			continue
		}
		if row.TopicKey == "" || row.Kind == "" {
			data.complete = false
			continue
		}
		topic, exists := previousTopics[row.TopicKey]
		if !exists {
			topic = sourceWikiUpdateTopic{TopicKey: row.TopicKey, Kind: row.Kind, ModulePath: row.ModulePath, SourceOwned: true}
		}
		if topic.Kind != row.Kind || topic.ModulePath != row.ModulePath && row.Kind == "module" {
			data.complete = false
			continue
		}
		previousTopics[row.TopicKey] = topic
	}
	previousTopicWork, previousTopicRefs := sourceWikiUpdateTopicMapWork(previousTopics)
	if err := budget.consume(previousTopicWork, previousTopicRefs); err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	data.previous = makeSourceWikiImpactTopicInventory(previous, previousTopics, previousLoaded, nil, true)
	nextTopics := make(map[string]sourceWikiUpdateTopic, len(nextSkeleton)+len(previousTopics))
	for _, topic := range nextSkeleton {
		if err := budget.consume(1, 0); err != nil {
			return sourceWikiUpdateInventoryData{}, err
		}
		candidate := sourceWikiUpdateTopic{TopicKey: topic.TopicKey, Kind: topic.Kind, ModulePath: topic.ModulePath, SourceOwned: true}
		if old, exists := previousTopics[topic.TopicKey]; exists {
			candidate.SourceOwned, candidate.EvidenceSHA = old.SourceOwned, old.EvidenceSHA
			var valid bool
			candidate.DependencyIDs, valid, err = sourceWikiImpactDependenciesForNext(topic, old, nextIndex, budget)
			if err != nil {
				return sourceWikiUpdateInventoryData{}, err
			}
			if !valid {
				data.complete = false
			}
			if err := budget.consume(len(candidate.DependencyIDs), len(candidate.DependencyIDs)); err != nil {
				return sourceWikiUpdateInventoryData{}, err
			}
		}
		nextTopics[topic.TopicKey] = candidate
	}
	for key, old := range previousTopics {
		if err := budget.consume(1, 0); err != nil {
			return sourceWikiUpdateInventoryData{}, err
		}
		if _, exists := nextTopics[key]; exists {
			continue
		}
		if sourceWikiTopicMayBeProvenRemoved(old, removalEvidence) {
			continue
		}
		retained := old
		if err := budget.consume(len(old.DependencyIDs), len(old.DependencyIDs)); err != nil {
			return sourceWikiUpdateInventoryData{}, err
		}
		retained.DependencyIDs = intersectSourceWikiIDs(old.DependencyIDs, nextIndex.parsedFileIDs)
		nextTopics[key] = retained
	}
	nextTopicWork, nextTopicRefs := sourceWikiUpdateTopicMapWork(nextTopics)
	if err := budget.consume(nextTopicWork, nextTopicRefs); err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	if err := budget.consume(len(previousLoaded.Members)+len(nextLoaded.Members), 0); err != nil {
		return sourceWikiUpdateInventoryData{}, err
	}
	data.next = makeSourceWikiImpactTopicInventory(next, nextTopics, nextLoaded, nextLoaded, data.complete)
	data.previous.Complete = data.complete
	data.next.Complete = data.complete
	return data, nil
}

func validateSourceWikiSkeletonInputSnapshot(snapshot *repository.SourceWikiSkeletonSnapshot, tenantID uint64, sourceID, snapshotID string) error {
	if snapshot == nil || snapshot.TenantID != tenantID || snapshot.DataSourceID != sourceID || snapshot.SnapshotID != snapshotID || !snapshot.Complete {
		return fmt.Errorf("source Wiki skeleton input is incomplete or crosses snapshot identity")
	}
	return nil
}

func sourceWikiSkeletonPlanForImpact(snapshot types.SourceWikiImpactSnapshot, loaded *repository.SourceWikiSkeletonSnapshot) ([]types.SourceWikiTopic, error) {
	if loaded == nil {
		return nil, fmt.Errorf("source Wiki skeleton snapshot is missing")
	}
	files := make([]sourceWikiSkeletonFile, 0, len(loaded.Files))
	for _, file := range loaded.Files {
		files = append(files, sourceWikiSkeletonFile{Path: file.Path, Generated: file.Generated, Facts: file.Facts})
	}
	plan := buildSourceWikiSkeleton(sourceWikiSkeletonInput{
		SourceID: snapshot.SourceID, SnapshotID: snapshot.SnapshotID, Files: files, Relations: loaded.Relations,
	}, sourceWikiMaxInitialTopics)
	if len(plan.Topics) > types.SourceWikiBatchMaxCandidates {
		return nil, fmt.Errorf("source Wiki topic inventory exceeds the bounded candidate count")
	}
	if err := validateSourceWikiSkeletonPlan(plan); err != nil {
		return nil, err
	}
	return plan.Topics, nil
}

func makeSourceWikiImpactTopicInventory(snapshot types.SourceWikiImpactSnapshot, topics map[string]sourceWikiUpdateTopic,
	loaded *repository.SourceWikiSkeletonSnapshot, _ *repository.SourceWikiSkeletonSnapshot, complete bool) types.SourceWikiImpactTopicInventory {
	result := types.SourceWikiImpactTopicInventory{
		TenantID: snapshot.TenantID, KnowledgeBaseID: snapshot.KnowledgeBaseID, SourceID: snapshot.SourceID,
		SnapshotID: snapshot.SnapshotID, Complete: complete,
	}
	for _, topic := range topics {
		if topic.TopicKey == "" || topic.Kind == "" {
			result.Complete = false
			continue
		}
		deps := sourceWikiSortedUniqueIDs(topic.DependencyIDs)
		result.Topics = append(result.Topics, types.SourceWikiImpactTopicDependencies{
			TopicKey: topic.TopicKey, Kind: topic.Kind, ModulePath: topic.ModulePath, SourceOwned: topic.SourceOwned,
			EvidenceSHA: topic.EvidenceSHA, ExpectedDependencyFileCount: len(deps), DependencyFileIDs: deps,
		})
	}
	sort.Slice(result.Topics, func(i, j int) bool { return result.Topics[i].TopicKey < result.Topics[j].TopicKey })
	moduleIDs := map[string][]string{}
	if loaded != nil {
		for _, member := range loaded.Members {
			if member.FileID == "" || member.Path == "" {
				result.Complete = false
				continue
			}
			moduleIDs[path.Dir(member.Path)] = append(moduleIDs[path.Dir(member.Path)], member.FileID)
		}
	}
	modulePaths := make([]string, 0, len(moduleIDs))
	for modulePath := range moduleIDs {
		modulePaths = append(modulePaths, modulePath)
	}
	sort.Strings(modulePaths)
	for _, modulePath := range modulePaths {
		fileIDs := sourceWikiSortedUniqueIDs(moduleIDs[modulePath])
		result.ModuleInventory = append(result.ModuleInventory, types.SourceWikiImpactModule{
			Path: modulePath, ExpectedFileCount: len(fileIDs), FileIDs: fileIDs,
		})
	}
	result.ExpectedTopicCount = len(result.Topics)
	result.ExpectedModuleCount = len(result.ModuleInventory)
	return result
}

func sourceWikiContributionSourceOwned(raw types.JSON) bool {
	var value struct {
		HasUnattributedBody bool                        `json:"has_unattributed_body"`
		SourceProvenance    *types.SourceWikiProvenance `json:"source_provenance"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil || value.HasUnattributedBody || value.SourceProvenance == nil {
		return false
	}
	return value.SourceProvenance.State == "ready" || value.SourceProvenance.State == "stale"
}

func sourceWikiModulePathForTopic(kind, key string) string {
	if kind != "module" || !strings.HasPrefix(key, "module/") {
		return ""
	}
	return strings.TrimPrefix(key, "module/")
}

func sourceWikiImpactSnapshotAdapterWork(snapshot types.SourceWikiImpactSnapshot) int {
	work := len(snapshot.Members) + len(snapshot.Relations)
	for _, member := range snapshot.Members {
		work += len(member.Facts)
	}
	return work
}

func sourceWikiUpdateTopicMapWork(topics map[string]sourceWikiUpdateTopic) (work, refs int) {
	work = len(topics)
	for _, topic := range topics {
		work += len(topic.DependencyIDs)
		refs += len(topic.DependencyIDs)
	}
	return work, refs
}

func buildSourceWikiNextImpactIndex(snapshot types.SourceWikiImpactSnapshot, budget *sourceWikiUpdateInventoryBudget) (*sourceWikiNextImpactIndex, error) {
	index := &sourceWikiNextImpactIndex{parsedFileIDs: make(map[string]bool), flowFileIDs: make(map[string]map[string]bool)}
	for _, member := range snapshot.Members {
		work := 1 + len(member.Facts)
		if err := budget.consume(work, 0); err != nil {
			return nil, err
		}
		if member.Status != "parsed" {
			continue
		}
		if member.SourceFileID != "" {
			index.parsedFileIDs[member.SourceFileID] = true
		}
		if member.Generated {
			continue
		}
		for _, fact := range member.Facts {
			if fact.Kind != "api_request" || member.SourceFileID == "" {
				continue
			}
			route := sourceWikiCanonicalRoute(fact.HTTPMethod + " " + fact.RoutePath)
			if route == "" {
				continue
			}
			if index.flowFileIDs[route] == nil {
				index.flowFileIDs[route] = make(map[string]bool)
			}
			index.flowFileIDs[route][member.SourceFileID] = true
		}
	}
	return index, nil
}

func intersectSourceWikiIDs(ids []string, allowed map[string]bool) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[id] {
			result = append(result, id)
		}
	}
	return sourceWikiSortedUniqueIDs(result)
}

func sourceWikiImpactDependenciesForNext(topic types.SourceWikiTopic, old sourceWikiUpdateTopic, next *sourceWikiNextImpactIndex,
	budget *sourceWikiUpdateInventoryBudget) ([]string, bool, error) {
	if topic.Kind != "flow" {
		if err := budget.consume(len(old.DependencyIDs), len(old.DependencyIDs)); err != nil {
			return nil, false, err
		}
		return intersectSourceWikiIDs(old.DependencyIDs, next.parsedFileIDs), true, nil
	}
	ids := map[string]bool{}
	for _, relation := range topic.Relations {
		if err := budget.consume(1, 0); err != nil {
			return nil, false, err
		}
		if relation.DataSourceID != topic.SourceID || relation.SnapshotID != topic.SnapshotID || relation.TenantID == 0 {
			return nil, false, nil
		}
		for _, fileID := range []string{relation.FromFileID, relation.ToFileID} {
			if fileID != "" {
				ids[fileID] = true
			}
		}
		if relation.Kind == "http_route" {
			var refs []types.SourceRelationFactRef
			if len(relation.Context) == 0 || json.Unmarshal(relation.Context, &refs) != nil || refs == nil {
				return nil, false, nil
			}
			if err := budget.consume(len(refs), len(refs)); err != nil {
				return nil, false, err
			}
			for _, ref := range refs {
				if ref.DataSourceID != topic.SourceID || ref.SnapshotID != topic.SnapshotID || ref.FileID == "" || ref.FileVersionID == "" || ref.Path == "" {
					return nil, false, nil
				}
				ids[ref.FileID] = true
			}
		}
	}
	if len(topic.Relations) == 0 {
		route := strings.TrimPrefix(topic.TopicKey, "flow/")
		for id := range next.flowFileIDs[sourceWikiCanonicalRoute(route)] {
			ids[id] = true
		}
	}
	result := make([]string, 0, len(ids))
	for id := range ids {
		result = append(result, id)
	}
	if err := budget.consume(len(result), len(result)); err != nil {
		return nil, false, err
	}
	return intersectSourceWikiIDs(result, next.parsedFileIDs), true, nil
}

type sourceWikiRemovalEvidence struct {
	manifestComplete bool
	flowComplete     bool
	modulePaths      map[string]bool
	routes           map[string]bool
}

func sourceWikiBuildRemovalEvidence(snapshot types.SourceWikiImpactSnapshot, loaded *repository.SourceWikiSkeletonSnapshot) sourceWikiRemovalEvidence {
	evidence, _ := sourceWikiBuildRemovalEvidenceBounded(snapshot, loaded, nil)
	return evidence
}

func sourceWikiBuildRemovalEvidenceBounded(snapshot types.SourceWikiImpactSnapshot, loaded *repository.SourceWikiSkeletonSnapshot,
	budget *sourceWikiUpdateInventoryBudget) (sourceWikiRemovalEvidence, error) {
	evidence := sourceWikiRemovalEvidence{modulePaths: map[string]bool{}, routes: map[string]bool{}}
	if loaded == nil || !loaded.Complete || !snapshot.ManifestComplete || len(snapshot.Members) != snapshot.ExpectedMemberCount {
		return evidence, nil
	}
	evidence.manifestComplete = true
	evidence.flowComplete = snapshot.RelationsComplete && snapshot.ExpectedRelationCount >= 0 &&
		len(snapshot.Relations) == snapshot.ExpectedRelationCount && len(snapshot.Relations) <= types.SourceWikiSkeletonMaxRelations
	factCount, factBytes := 0, 0
	for _, member := range snapshot.Members {
		if err := budget.consume(1+len(member.Facts), 0); err != nil {
			return sourceWikiRemovalEvidence{}, err
		}
		if member.Path == "" {
			evidence.manifestComplete = false
			evidence.flowComplete = false
			continue
		}
		evidence.modulePaths[path.Dir(member.Path)] = true
		if member.Status != "parsed" || member.Quality != "structural" || !member.FactsComplete || member.ExpectedFactCount != len(member.Facts) {
			evidence.flowComplete = false
			continue
		}
		factCount += len(member.Facts)
		encodedFacts, err := json.Marshal(member.Facts)
		if err != nil {
			evidence.flowComplete = false
		} else {
			factBytes += len(encodedFacts)
		}
		if factCount > types.SourceWikiImpactMaxFacts || factBytes > types.SourceWikiImpactMaxFactBytes {
			evidence.flowComplete = false
		}
		for _, fact := range member.Facts {
			switch fact.Kind {
			case "api_request":
				if route := sourceWikiCanonicalRoute(fact.HTTPMethod + " " + fact.RoutePath); route != "" {
					evidence.routes[route] = true
				}
				if fact.Quality != "structural" || fact.Dynamic || fact.Certainty == "uncertain" {
					evidence.flowComplete = false
				}
			case "api_prefix", "api_proxy", "spring_mapping":
				if fact.Quality != "structural" || fact.Dynamic || fact.Certainty == "uncertain" {
					evidence.flowComplete = false
				}
			}
		}
	}
	for _, relation := range snapshot.Relations {
		if err := budget.consume(1, 0); err != nil {
			return sourceWikiRemovalEvidence{}, err
		}
		if relation.TenantID != snapshot.TenantID || relation.DataSourceID != snapshot.SourceID || relation.SnapshotID != snapshot.SnapshotID {
			evidence.flowComplete = false
			continue
		}
		if relation.Kind == "http_route" {
			if route := sourceWikiCanonicalRoute(relation.FromKey); route != "" {
				evidence.routes[route] = true
			} else {
				evidence.flowComplete = false
			}
		}
	}
	return evidence, nil
}

func sourceWikiTopicMayBeProvenRemoved(topic sourceWikiUpdateTopic, evidence sourceWikiRemovalEvidence) bool {
	if !topic.SourceOwned {
		return false
	}
	switch topic.Kind {
	case "module":
		return evidence.manifestComplete && topic.ModulePath != "" && !evidence.modulePaths[topic.ModulePath]
	case "flow":
		route := sourceWikiCanonicalRoute(strings.TrimPrefix(topic.TopicKey, "flow/"))
		return evidence.flowComplete && route != "" && !evidence.routes[route]
	default:
		return false
	}
}

func (s *sourceWikiService) persistSourceWikiImpactPlan(ctx context.Context, payload types.SourceWikiUpdatePayload,
	durable types.SourceWikiUpdatePlan, impact types.SourceWikiImpactPlan, inventory types.SourceWikiImpactTopicInventory) error {
	planJSON, err := json.Marshal(impact)
	if err != nil {
		return fmt.Errorf("encode source Wiki impact plan: %w", err)
	}
	inventoryJSON, err := json.Marshal(inventory)
	if err != nil {
		return fmt.Errorf("encode source Wiki next inventory: %w", err)
	}
	digest := sha256.Sum256(planJSON)
	digestHex := hex.EncodeToString(digest[:])
	now := time.Now().UTC()
	items := make([]types.SourceWikiUpdatePlanItem, 0, len(impact.Affected)+len(impact.Unaffected)+len(impact.Removed)+1)
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var plan types.SourceWikiUpdatePlan
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", durable.ID).Take(&plan).Error; err != nil {
			return err
		}
		if plan.Status == "completed" || plan.Status == "superseded" {
			return nil
		}
		if plan.TenantID != payload.TenantID || plan.KnowledgeBaseID != payload.KnowledgeBaseID ||
			plan.SourceID != payload.DataSourceID || plan.SnapshotID != payload.SnapshotID || plan.ConfigGeneration != payload.ConfigGeneration ||
			plan.PreviousSnapshotID != durable.PreviousSnapshotID {
			return fmt.Errorf("source Wiki update plan changed while impact was being computed")
		}
		var sourceState struct {
			TenantID                 uint64 `gorm:"column:tenant_id"`
			ConfigGeneration         int64  `gorm:"column:config_generation"`
			LastSuccessfulSnapshotID string `gorm:"column:last_successful_snapshot_id"`
		}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Table("source_sync_states").
			Select("tenant_id,config_generation,last_successful_snapshot_id").
			Where("data_source_id=? AND tenant_id=?", payload.DataSourceID, payload.TenantID).Take(&sourceState).Error; err != nil {
			return err
		}
		var publication types.SourcePublication
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).Where("data_source_id=? AND tenant_id=? AND knowledge_base_id=?",
			payload.DataSourceID, payload.TenantID, payload.KnowledgeBaseID).Take(&publication).Error; err != nil {
			return err
		}
		var nextSnapshot types.SourceSnapshot
		if err := tx.Where("id=? AND data_source_id=? AND tenant_id=? AND knowledge_base_id=? AND state='published' AND manifest_complete=TRUE",
			payload.SnapshotID, payload.DataSourceID, payload.TenantID, payload.KnowledgeBaseID).Take(&nextSnapshot).Error; err != nil {
			return err
		}
		var outbox struct {
			ID               string     `gorm:"column:id"`
			TenantID         uint64     `gorm:"column:tenant_id"`
			KnowledgeBaseID  string     `gorm:"column:knowledge_base_id"`
			DataSourceID     string     `gorm:"column:data_source_id"`
			SnapshotID       string     `gorm:"column:snapshot_id"`
			EventType        string     `gorm:"column:event_type"`
			ConfigGeneration int64      `gorm:"column:config_generation"`
			Payload          types.JSON `gorm:"column:payload"`
		}
		if err := tx.Table("source_publication_outbox").Where("id=?", payload.EventID).Take(&outbox).Error; err != nil {
			return err
		}
		var accepted types.SourceWikiUpdatePayload
		if json.Unmarshal(outbox.Payload, &accepted) != nil || accepted != payload || outbox.ID != payload.EventID ||
			outbox.TenantID != payload.TenantID || outbox.KnowledgeBaseID != payload.KnowledgeBaseID ||
			outbox.DataSourceID != payload.DataSourceID || outbox.SnapshotID != payload.SnapshotID ||
			outbox.EventType != "source.wiki.update" || outbox.ConfigGeneration != payload.ConfigGeneration {
			return fmt.Errorf("source Wiki update no longer matches its accepted publication event")
		}
		if sourceState.TenantID != payload.TenantID || sourceState.ConfigGeneration != payload.ConfigGeneration ||
			sourceState.LastSuccessfulSnapshotID != payload.SnapshotID || publication.SnapshotID != payload.SnapshotID ||
			payload.CommitSHA != "" && nextSnapshot.CommitSHA != payload.CommitSHA {
			return markSourceWikiUpdateSupersededInTx(tx, &plan, "publication_or_configuration_changed", now)
		}
		var activeSource int64
		if err := tx.Model(&types.DataSource{}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND deleted_at IS NULL",
			payload.DataSourceID, payload.TenantID, payload.KnowledgeBaseID).Count(&activeSource).Error; err != nil {
			return err
		}
		if activeSource != 1 {
			return markSourceWikiUpdateSupersededInTx(tx, &plan, "source_unavailable", now)
		}

		if err := tx.Where("plan_id=?", plan.ID).Delete(&types.SourceWikiUpdatePlanItem{}).Error; err != nil {
			return err
		}
		if impact.Disposition == types.SourceWikiImpactSourceWideStale {
			var rows []types.SourceWikiPageContribution
			if err := tx.Model(&types.SourceWikiPageContribution{}).
				Joins("JOIN wiki_pages wp ON wp.id=source_wiki_page_contributions.page_id").
				Where("wp.tenant_id=? AND wp.knowledge_base_id=? AND source_wiki_page_contributions.source_id=? AND source_wiki_page_contributions.revision_id IS NULL",
					payload.TenantID, payload.KnowledgeBaseID, payload.DataSourceID).
				Order("source_wiki_page_contributions.topic_key ASC").Limit(types.SourceWikiBatchMaxCandidates + 1).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) <= types.SourceWikiBatchMaxCandidates {
				alreadyCurrent := make(map[int64]bool, len(rows))
				for _, row := range rows {
					if sourceWikiContributionReadyForSnapshot(row, payload) {
						alreadyCurrent[row.ID] = true
						continue
					}
					if err := markSourceWikiContributionStaleInTx(tx, payload, row, impact.FallbackReason,
						"The bounded source impact proof was unavailable; this source contribution remains stale.", now); err != nil {
						return err
					}
				}
				byTopic := make(map[string][]types.SourceWikiPageContribution, len(rows))
				for _, row := range rows {
					byTopic[row.TopicKey] = append(byTopic[row.TopicKey], row)
				}
				topics := make([]string, 0, len(byTopic))
				for topicKey := range byTopic {
					topics = append(topics, topicKey)
				}
				sort.Strings(topics)
				for _, topicKey := range topics {
					group := byTopic[topicKey]
					var pageID *string
					pageVersion := 0
					if len(group) == 1 {
						pageID, pageVersion = &group[0].PageID, group[0].PageVersion
					}
					if len(group) == 1 && alreadyCurrent[group[0].ID] {
						items = append(items, sourceWikiUpdateItem(plan.ID, topicKey, pageID, "carry_forward",
							"same_snapshot_already_ready", "This exact source contribution is already ready and applicable to the accepted snapshot.",
							"completed", pageVersion, group[0].EvidenceSHA256, group[0].DependencyFingerprint,
							map[string]any{"snapshot_id": payload.SnapshotID}, now))
						continue
					}
					items = append(items, sourceWikiUpdateItem(plan.ID, topicKey, pageID, "regenerate",
						"source_wide_stale_fallback", impact.FallbackReason, "pending", pageVersion, "", "",
						map[string]any{"fallback": impact.FallbackReason, "page_count": len(group)}, now))
				}
			}
		} else {
			removalProofs := map[string]bool{}
			if len(impact.Removed) > 0 {
				var err error
				removalProofs, err = verifySourceWikiRemovalsInTx(tx, payload, impact.Removed)
				if err != nil {
					return err
				}
			}
			for _, topic := range impact.Unaffected {
				row, found, err := sourceWikiCurrentContributionInTx(tx, payload, topic.TopicKey)
				if err != nil {
					return err
				}
				if found {
					carried, carryErr := carrySourceWikiContributionInTx(tx, payload, row, topic.ApplicabilityFingerprint, now)
					if carryErr != nil {
						return carryErr
					}
					if carried {
						items = append(items, sourceWikiUpdateItem(plan.ID, topic.TopicKey, &row.PageID, "carry_forward",
							"impact_unchanged", "Complete old/new impact fingerprints match; original evidence provenance was retained.",
							"completed", row.PageVersion, topic.EvidenceSHA, topic.ApplicabilityFingerprint,
							map[string]any{"snapshot_id": payload.SnapshotID}, now))
						continue
					}
				}
				reason := "The old page contribution is missing, mixed, or failed its carry-forward provenance validation."
				items = append(items, sourceWikiUpdateItem(plan.ID, topic.TopicKey, sourceWikiOptionalPageID(row, found), "regenerate",
					"carry_forward_rejected", reason, "pending", sourceWikiPageVersion(row, found), topic.EvidenceSHA,
					topic.ApplicabilityFingerprint, map[string]any{"snapshot_id": payload.SnapshotID}, now))
			}
			for _, topic := range impact.Affected {
				row, found, err := sourceWikiCurrentContributionInTx(tx, payload, topic.TopicKey)
				if err != nil {
					return err
				}
				if found {
					if err := markSourceWikiContributionStaleInTx(tx, payload, row, "source_dependency_changed", strings.Join(topic.Reasons, ", "), now); err != nil {
						return err
					}
				}
				items = append(items, sourceWikiUpdateItem(plan.ID, topic.TopicKey, sourceWikiOptionalPageID(row, found), "regenerate",
					"source_dependency_changed", strings.Join(topic.Reasons, ", "), "pending", sourceWikiPageVersion(row, found),
					"", "", map[string]any{"reasons": topic.Reasons}, now))
			}
			for _, topic := range impact.Removed {
				row, found, err := sourceWikiCurrentContributionInTx(tx, payload, topic.TopicKey)
				if err != nil {
					return err
				}
				removed := removalProofs[topic.TopicKey]
				if removed && found {
					removed, err = s.removeSourceWikiContributionInTx(tx, payload, row, payload.SnapshotID, now)
					if err != nil {
						return err
					}
				}
				state, action, reasonCode, reason := "completed", "remove", "source_contribution_confirmed_absent", topic.Reason
				if !removed {
					state, action, reasonCode, reason = "failed", "remove", "removal_not_proven", "The independent bounded snapshot proof did not confirm absence; the source contribution was preserved."
				}
				items = append(items, sourceWikiUpdateItem(plan.ID, topic.TopicKey, sourceWikiOptionalPageID(row, found), action,
					reasonCode, reason, state, sourceWikiPageVersion(row, found), "", "",
					map[string]any{"complete_snapshot_id": payload.SnapshotID}, now))
			}
		}
		if impact.RescanSkeleton {
			items = append(items, sourceWikiUpdateItem(plan.ID, "source-skeleton", nil, "skeleton", "skeleton_rescan_required",
				strings.Join(impact.RescanReasons, ", "), "pending", 0, "", "", map[string]any{"reasons": impact.RescanReasons}, now))
		}
		if len(items) > 0 {
			if err := tx.Create(&items).Error; err != nil {
				return err
			}
		}
		sourceWide := impact.Disposition == types.SourceWikiImpactSourceWideStale
		reasonCode, reason := "incremental_impact_planned", "The complete source impact plan was persisted; affected topics remain stale until regenerated."
		if sourceWide {
			reasonCode, reason = impact.FallbackReason, "The bounded impact scan was incomplete; all source Wiki answers remain stale."
		}
		result := tx.Model(&plan).Where("id=? AND status IN ?", plan.ID, []string{"pending", "running"}).Updates(map[string]any{
			"plan_digest": digestHex, "status": "completed", "source_wide_stale": sourceWide,
			"reason_code": reasonCode, "reason": reason, "plan": types.JSON(planJSON), "next_inventory": types.JSON(inventoryJSON),
			"completed_at": now, "updated_at": now,
		})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("source Wiki update plan changed before impact commit")
		}
		return nil
	})
}

func markSourceWikiUpdateSupersededInTx(tx *gorm.DB, plan *types.SourceWikiUpdatePlan, reason string, now time.Time) error {
	if tx == nil || plan == nil {
		return fmt.Errorf("source Wiki update supersession target is incomplete")
	}
	if err := tx.Model(&types.SourceWikiUpdatePlanItem{}).Where("plan_id=? AND state IN ?", plan.ID, []string{"pending", "running"}).
		Update("state", "superseded").Error; err != nil {
		return err
	}
	return tx.Model(plan).Updates(map[string]any{
		"status": "superseded", "source_wide_stale": true, "reason_code": reason,
		"reason":       "A newer publication or source configuration superseded this Wiki update.",
		"completed_at": now, "updated_at": now,
	}).Error
}

func sourceWikiCurrentContributionInTx(tx *gorm.DB, payload types.SourceWikiUpdatePayload, topicKey string) (types.SourceWikiPageContribution, bool, error) {
	var row types.SourceWikiPageContribution
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Model(&types.SourceWikiPageContribution{}).
		Joins("JOIN wiki_pages wp ON wp.id=source_wiki_page_contributions.page_id").
		Where("wp.tenant_id=? AND wp.knowledge_base_id=? AND source_wiki_page_contributions.source_id=? AND source_wiki_page_contributions.topic_key=? AND source_wiki_page_contributions.revision_id IS NULL",
			payload.TenantID, payload.KnowledgeBaseID, payload.DataSourceID, topicKey).Take(&row).Error
	if err == gorm.ErrRecordNotFound {
		return row, false, nil
	}
	return row, err == nil, err
}

func carrySourceWikiContributionInTx(tx *gorm.DB, payload types.SourceWikiUpdatePayload, row types.SourceWikiPageContribution,
	fingerprint string, now time.Time) (bool, error) {
	var page types.WikiPage
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=?",
		row.PageID, payload.TenantID, payload.KnowledgeBaseID, row.PageVersion).Take(&page).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	var otherCount int64
	if err := tx.Model(&types.SourceWikiPageContribution{}).Where("page_id=? AND revision_id IS NULL", row.PageID).Count(&otherCount).Error; err != nil {
		return false, err
	}
	if otherCount != 1 || page.SourceProvenance == nil || page.SourceProvenance.SourceID != payload.DataSourceID ||
		page.SourceProvenance.TopicKind != row.TopicKind || page.SourceProvenance.TopicKey != row.TopicKey ||
		page.SourceProvenance.State != "stale" || page.Version != row.PageVersion || row.TargetSnapshotID != payload.SnapshotID {
		return false, nil
	}
	var fields map[string]json.RawMessage
	if len(row.Contribution) == 0 || json.Unmarshal(row.Contribution, &fields) != nil {
		return false, nil
	}
	var contributionData struct {
		Content             string                      `json:"content"`
		HasUnattributedBody *bool                       `json:"has_unattributed_body"`
		SourceProvenance    *types.SourceWikiProvenance `json:"source_provenance"`
	}
	if json.Unmarshal(row.Contribution, &contributionData) != nil || contributionData.HasUnattributedBody == nil ||
		*contributionData.HasUnattributedBody || contributionData.SourceProvenance == nil {
		return false, nil
	}
	provenance := *contributionData.SourceProvenance
	if provenance.SourceID != payload.DataSourceID || provenance.TopicKind != row.TopicKind || provenance.TopicKey != row.TopicKey ||
		provenance.ApplicableSnapshotID != row.ApplicableSnapshotID || len(provenance.Evidence) == 0 {
		return false, nil
	}
	originSnapshot := provenance.Evidence[0].SnapshotID
	for _, evidence := range provenance.Evidence {
		if evidence.SnapshotID != originSnapshot || evidence.DataSourceID != payload.DataSourceID {
			return false, nil
		}
	}
	contributionState := types.SourceWikiContributionStale
	if provenance.State == "ready" {
		contributionState = types.SourceWikiContributionCurrent
	} else if provenance.State != "stale" {
		return false, nil
	}
	set := types.SourceWikiContributionSet{
		TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID,
		Contributions: []types.SourceWikiContribution{{
			SourceID: payload.DataSourceID, TopicKind: row.TopicKind, TopicKey: row.TopicKey,
			OriginSnapshotID: originSnapshot, ApplicableSnapshotID: provenance.ApplicableSnapshotID,
			Body: contributionData.Content, Evidence: provenance.Evidence, State: contributionState,
		}},
	}
	updated, err := source.ReplaceSourceWikiContribution(set, types.SourceWikiContributionTarget{
		TenantID: payload.TenantID, KnowledgeBaseID: payload.KnowledgeBaseID, SourceID: payload.DataSourceID,
		TopicKind: row.TopicKind, TopicKey: row.TopicKey, SnapshotID: payload.SnapshotID,
	}, types.SourceWikiContributionOutcome{Kind: types.SourceWikiContributionOutcomeUnchanged})
	if err != nil || len(updated.Contributions) != 1 || updated.Contributions[0].ApplicableSnapshotID != payload.SnapshotID ||
		updated.Contributions[0].OriginSnapshotID != originSnapshot {
		return false, nil
	}
	provenance.State, provenance.ApplicableSnapshotID = "ready", payload.SnapshotID
	encodedProvenance, err := json.Marshal(provenance)
	if err != nil {
		return false, err
	}
	fields["source_provenance"] = encodedProvenance
	encodedContribution, err := json.Marshal(fields)
	if err != nil {
		return false, err
	}
	updatedRow := tx.Model(&types.SourceWikiPageContribution{}).Where("id=? AND page_version=? AND target_snapshot_id=? AND state='stale'",
		row.ID, page.Version, payload.SnapshotID).Updates(map[string]any{
		"applicable_snapshot_id": payload.SnapshotID, "state": "ready", "reason_code": "", "reason": "",
		"dependency_fingerprint": fingerprint, "contribution": types.JSON(encodedContribution), "updated_at": now,
	})
	if updatedRow.Error != nil {
		return false, updatedRow.Error
	}
	if updatedRow.RowsAffected != 1 {
		return false, nil
	}
	pageProvenance := *page.SourceProvenance
	pageProvenance.State, pageProvenance.ApplicableSnapshotID = "ready", payload.SnapshotID
	updatedPage := tx.Model(&types.WikiPage{}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=? AND source_provenance->>'source_id'=? AND source_provenance->>'topic_key'=?",
		page.ID, payload.TenantID, payload.KnowledgeBaseID, page.Version, payload.DataSourceID, row.TopicKey).
		Update("source_provenance", pageProvenance)
	if updatedPage.Error != nil {
		return false, updatedPage.Error
	}
	if updatedPage.RowsAffected != 1 {
		return false, fmt.Errorf("source Wiki page provenance changed while carrying applicability forward")
	}
	return true, nil
}

func markSourceWikiContributionStaleInTx(tx *gorm.DB, payload types.SourceWikiUpdatePayload, row types.SourceWikiPageContribution,
	reasonCode, reason string, now time.Time) error {
	var page types.WikiPage
	if err := tx.Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=?",
		row.PageID, payload.TenantID, payload.KnowledgeBaseID, row.PageVersion).Take(&page).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil
		}
		return err
	}
	var fields map[string]json.RawMessage
	if len(row.Contribution) > 0 && json.Unmarshal(row.Contribution, &fields) == nil {
		var provenance types.SourceWikiProvenance
		if raw := fields["source_provenance"]; len(raw) > 0 && json.Unmarshal(raw, &provenance) == nil {
			provenance.State = "stale"
			if encoded, err := json.Marshal(provenance); err == nil {
				fields["source_provenance"] = encoded
				if encodedContribution, err := json.Marshal(fields); err == nil {
					row.Contribution = types.JSON(encodedContribution)
				}
			}
		}
	}
	updates := map[string]any{"state": "stale", "reason_code": reasonCode, "reason": reason, "updated_at": now}
	if len(row.Contribution) > 0 {
		updates["contribution"] = row.Contribution
	}
	updated := tx.Model(&types.SourceWikiPageContribution{}).Where(
		"id=? AND page_id=? AND source_id=? AND topic_kind=? AND topic_key=? AND revision_id IS NULL AND page_version=? AND applicable_snapshot_id=? AND target_snapshot_id=? AND state=?",
		row.ID, row.PageID, row.SourceID, row.TopicKind, row.TopicKey, row.PageVersion, row.ApplicableSnapshotID, row.TargetSnapshotID, row.State,
	).Updates(updates)
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != 1 {
		return nil
	}
	if page.SourceProvenance != nil && page.SourceProvenance.SourceID == payload.DataSourceID && page.SourceProvenance.TopicKey == row.TopicKey {
		page.SourceProvenance.State = "stale"
		return tx.Model(&types.WikiPage{}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=?",
			page.ID, payload.TenantID, payload.KnowledgeBaseID, row.PageVersion).Update("source_provenance", page.SourceProvenance).Error
	}
	return nil
}

func sourceWikiContributionReadyForSnapshot(row types.SourceWikiPageContribution, payload types.SourceWikiUpdatePayload) bool {
	if row.ID == 0 || row.SourceID != payload.DataSourceID || row.State != "ready" ||
		row.ApplicableSnapshotID != payload.SnapshotID || row.TargetSnapshotID != payload.SnapshotID || len(row.Contribution) == 0 {
		return false
	}
	var stored sourceWikiStoredContribution
	if json.Unmarshal(row.Contribution, &stored) != nil || stored.HasUnattributedBody == nil || *stored.HasUnattributedBody ||
		stored.SourceProvenance == nil || stored.Content == "" || stored.SourceRefs == nil ||
		stored.TopicKind != row.TopicKind || stored.TopicKey != row.TopicKey {
		return false
	}
	provenance := stored.SourceProvenance
	if provenance.SourceID != row.SourceID || provenance.TopicKind != row.TopicKind || provenance.TopicKey != row.TopicKey ||
		provenance.State != "ready" || provenance.ApplicableSnapshotID != payload.SnapshotID || len(provenance.Evidence) == 0 {
		return false
	}
	originSnapshot := provenance.Evidence[0].SnapshotID
	if originSnapshot == "" {
		return false
	}
	refs := make([]string, 0, len(provenance.Evidence))
	for _, evidence := range provenance.Evidence {
		if evidence.DataSourceID != row.SourceID || evidence.SnapshotID != originSnapshot || evidence.ID == "" ||
			evidence.KnowledgeID == "" || evidence.FileVersionID == "" || evidence.Path == "" {
			return false
		}
		refs = append(refs, evidence.KnowledgeID+"|"+evidence.Path)
	}
	return equalSortedSourceWikiIDs(stored.SourceRefs, refs)
}

// verifySourceWikiRemovalsInTx performs one independent last-mile absence proof
// for the complete removal set. The bounded loader verifies exact membership,
// facts, and relation provenance before any source contribution is deleted.
func verifySourceWikiRemovalsInTx(tx *gorm.DB, payload types.SourceWikiUpdatePayload,
	removedTopics []types.SourceWikiRemovedTopic) (map[string]bool, error) {
	proofs := make(map[string]bool, len(removedTopics))
	if len(removedTopics) == 0 || len(removedTopics) > types.SourceWikiBatchMaxCandidates {
		return proofs, nil
	}
	snapshot, loaded, err := repository.LoadSourceWikiImpactSnapshot(tx, payload.TenantID, payload.KnowledgeBaseID,
		payload.DataSourceID, payload.SnapshotID, types.SourceWikiImpactPublishedComplete)
	if err != nil {
		// Validation and availability failures are both a denial of deletion.
		// Query errors will still surface when the surrounding transaction next
		// attempts to write; malformed snapshot evidence must not retry forever.
		return proofs, nil
	}
	evidence := sourceWikiBuildRemovalEvidence(snapshot, loaded)
	for _, topic := range removedTopics {
		proofs[topic.TopicKey] = sourceWikiTopicMayBeProvenRemoved(sourceWikiUpdateTopic{
			TopicKey: topic.TopicKey, Kind: sourceWikiModuleOrFlowKind(topic.TopicKey),
			ModulePath: sourceWikiModulePathForTopic(sourceWikiModuleOrFlowKind(topic.TopicKey), topic.TopicKey), SourceOwned: true,
		}, evidence)
	}
	return proofs, nil
}

func sourceWikiModuleOrFlowKind(topicKey string) string {
	switch {
	case strings.HasPrefix(topicKey, "module/"):
		return "module"
	case strings.HasPrefix(topicKey, "flow/"):
		return "flow"
	default:
		return ""
	}
}

func sourceWikiUpdateItem(planID, topicKey string, pageID *string, action, reasonCode, reason, state string,
	pageVersion int, evidenceSHA, dependencyFingerprint string, details map[string]any, now time.Time) types.SourceWikiUpdatePlanItem {
	encoded, _ := json.Marshal(details)
	return types.SourceWikiUpdatePlanItem{
		PlanID: planID, TopicKey: topicKey, PageID: pageID, Action: action, ReasonCode: reasonCode, Reason: reason,
		State: state, ExpectedPageVersion: pageVersion, EvidenceSHA256: evidenceSHA,
		DependencyFingerprint: dependencyFingerprint, Details: types.JSON(encoded), UpdatedAt: now,
	}
}

func sourceWikiOptionalPageID(row types.SourceWikiPageContribution, found bool) *string {
	if !found || row.PageID == "" {
		return nil
	}
	pageID := row.PageID
	return &pageID
}

func sourceWikiPageVersion(row types.SourceWikiPageContribution, found bool) int {
	if !found {
		return 0
	}
	return row.PageVersion
}

var _ interfaces.SourceWikiUpdateProcessor = (*sourceWikiService)(nil)
