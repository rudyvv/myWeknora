package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	sourceCleanupLeaseTTL    = 2 * time.Minute
	sourceCleanupDeleteBatch = 500
	sourceCleanupGCBatch     = 20

	sourceCleanupPhaseCurrentWiki = "current_wiki"
	sourceCleanupPhaseHistoryWiki = "history_wiki"
	sourceCleanupPhaseWikiOwners  = "wiki_owners"
	sourceCleanupPhaseWakeGC      = "wake_gc"
	sourceCleanupPhaseSnapshots   = "snapshots"
	sourceCleanupPhaseComplete    = "complete"
)

type sourceCleanupCandidate struct {
	ID              string `gorm:"column:id"`
	DataSourceID    string `gorm:"column:data_source_id"`
	TenantID        uint64 `gorm:"column:tenant_id"`
	KnowledgeBaseID string `gorm:"column:knowledge_base_id"`
	Phase           string `gorm:"column:phase"`
}

type sourceCleanupCursor struct {
	LastID string `json:"last_id"`
}

type storedSourceWikiContribution struct {
	Title               string                      `json:"title"`
	Summary             string                      `json:"summary"`
	Content             string                      `json:"content"`
	SourceRefs          []string                    `json:"source_refs"`
	SourceProvenance    *types.SourceWikiProvenance `json:"source_provenance"`
	HasUnattributedBody bool                        `json:"has_unattributed_body"`
}

// ProcessSourceCleanupBatch advances durable cleanup operations in bounded
// transactions. Each pass either processes one page/revision or deletes at
// most sourceCleanupDeleteBatch rows from each owner/cache table.
func (r *SyncLogRepository) ProcessSourceCleanupBatch(ctx context.Context, limit int) (int, error) {
	if r.db == nil || r.db.Dialector.Name() != "postgres" {
		return 0, nil
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	processed := 0
	skipped := make(map[string]struct{})
	for processed < limit {
		var candidate sourceCleanupCandidate
		query := r.db.WithContext(ctx).Table("source_cleanup_operations").
			Select("id,data_source_id,tenant_id,knowledge_base_id,phase").
			Where("status='pending' OR (status='running' AND (lease_expires_at IS NULL OR lease_expires_at<=now()))").
			Order("updated_at ASC,id ASC")
		if len(skipped) > 0 {
			ids := make([]string, 0, len(skipped))
			for id := range skipped {
				ids = append(ids, id)
			}
			query = query.Where("id NOT IN ?", ids)
		}
		if err := query.Take(&candidate).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				break
			}
			return processed, err
		}
		owner := uuid.NewString()
		token, claimed, err := r.claimSourceCleanup(ctx, candidate, owner)
		if err != nil {
			return processed, err
		}
		if !claimed {
			skipped[candidate.ID] = struct{}{}
			continue
		}
		processed++
		if candidate.Phase == sourceCleanupPhaseSnapshots {
			if _, err := (&sourceSnapshotRepository{db: r.db}).collectRetiredSourceVersions(ctx, sourceCleanupGCBatch, candidate.DataSourceID); err != nil {
				markErr := r.markSourceCleanupFailed(ctx, candidate, owner, token)
				if markErr != nil {
					return processed, errors.Join(err, markErr)
				}
				return processed, err
			}
		}
		if err := r.processSourceCleanupStep(ctx, candidate, owner, token); err != nil {
			markErr := r.markSourceCleanupFailed(ctx, candidate, owner, token)
			if markErr != nil {
				return processed, errors.Join(err, markErr)
			}
			return processed, err
		}
	}
	return processed, nil
}

func (r *SyncLogRepository) claimSourceCleanup(ctx context.Context, candidate sourceCleanupCandidate, owner string) (int64, bool, error) {
	var fencingToken int64
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockCleanupDataSource(tx, candidate); err != nil {
			return err
		}
		if err := lockCurrentSourcePublication(tx, candidate.DataSourceID); err != nil {
			return err
		}
		var operation types.SourceCleanupOperation
		err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("id=? AND data_source_id=?", candidate.ID, candidate.DataSourceID).Take(&operation).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return errSourceCleanupNotClaimed
		}
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		if operation.Status != types.SourceCleanupPending &&
			(operation.Status != types.SourceCleanupRunning || operation.LeaseExpiresAt != nil && operation.LeaseExpiresAt.After(now)) {
			return errSourceCleanupNotClaimed
		}
		operation.Status = types.SourceCleanupRunning
		operation.LeaseOwner = &owner
		operation.LeaseExpiresAt = timePointer(now.Add(sourceCleanupLeaseTTL))
		operation.FencingToken++
		operation.AttemptCount++
		operation.UpdatedAt = now
		fencingToken = operation.FencingToken
		return tx.Model(&operation).Updates(map[string]any{
			"status": operation.Status, "lease_owner": owner, "lease_expires_at": operation.LeaseExpiresAt,
			"fencing_token": operation.FencingToken, "attempt_count": operation.AttemptCount, "updated_at": now,
		}).Error
	})
	if errors.Is(err, errSourceCleanupNotClaimed) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return fencingToken, true, nil
}

var errSourceCleanupNotClaimed = errors.New("source cleanup operation was not claimable")

func (r *SyncLogRepository) processSourceCleanupStep(ctx context.Context, candidate sourceCleanupCandidate, owner string, token int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ds, err := lockCleanupDataSource(tx, candidate)
		if err != nil {
			return err
		}
		if ds.SourceBindingState != types.SourceBindingUnbound || ds.SourceQueryEnabled {
			return errors.New("source cleanup lost its query revocation fence")
		}
		if err := lockCurrentSourcePublication(tx, candidate.DataSourceID); err != nil {
			return err
		}
		var operation types.SourceCleanupOperation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id=? AND data_source_id=?", candidate.ID, candidate.DataSourceID).Take(&operation).Error; err != nil {
			return err
		}
		if operation.Status != types.SourceCleanupRunning || derefSourceID(operation.LeaseOwner) != owner ||
			operation.FencingToken != token || operation.LeaseExpiresAt == nil || !operation.LeaseExpiresAt.After(time.Now().UTC()) {
			return errSourceCleanupNotClaimed
		}
		if err := advanceSourceCleanupStepTx(tx, &operation); err != nil {
			return err
		}
		now := time.Now().UTC()
		updates := map[string]any{
			"phase": operation.Phase, "cursor": string(operation.Cursor), "updated_at": now,
			"lease_owner": nil, "lease_expires_at": nil,
		}
		if operation.Phase == sourceCleanupPhaseComplete {
			completedAt := now
			operation.Status = types.SourceCleanupCompleted
			operation.CompletedAt = &completedAt
			updates["status"], updates["retryable"], updates["error_code"], updates["completed_at"] = operation.Status, false, "", completedAt
		} else {
			operation.Status = types.SourceCleanupPending
			updates["status"], updates["retryable"] = operation.Status, false
		}
		return tx.Model(&operation).Updates(updates).Error
	})
}

func (r *SyncLogRepository) markSourceCleanupFailed(ctx context.Context, candidate sourceCleanupCandidate, owner string, token int64) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if _, err := lockCleanupDataSource(tx, candidate); err != nil {
			return err
		}
		if err := lockCurrentSourcePublication(tx, candidate.DataSourceID); err != nil {
			return err
		}
		result := tx.Table("source_cleanup_operations").Where(
			"id=? AND data_source_id=? AND status='running' AND lease_owner=? AND fencing_token=?",
			candidate.ID, candidate.DataSourceID, owner, token,
		).Updates(map[string]any{
			"status": types.SourceCleanupFailed, "retryable": true, "error_code": "cleanup_step_failed",
			"lease_owner": nil, "lease_expires_at": nil, "updated_at": time.Now().UTC(),
		})
		return result.Error
	})
}

func lockCleanupDataSource(tx *gorm.DB, candidate sourceCleanupCandidate) (*types.DataSource, error) {
	var ds types.DataSource
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND tenant_id=?", candidate.DataSourceID, candidate.TenantID).Take(&ds).Error; err != nil {
		return nil, err
	}
	if ds.KnowledgeBaseID != candidate.KnowledgeBaseID {
		return nil, errors.New("source cleanup identity no longer matches its data source")
	}
	return &ds, nil
}

func advanceSourceCleanupStepTx(tx *gorm.DB, operation *types.SourceCleanupOperation) error {
	var cursor sourceCleanupCursor
	if len(operation.Cursor) > 0 {
		if err := json.Unmarshal(operation.Cursor, &cursor); err != nil {
			return fmt.Errorf("decode source cleanup cursor: %w", err)
		}
	}
	switch operation.Phase {
	case "", sourceCleanupPhaseCurrentWiki:
		pageID, err := nextSourceCleanupCurrentPage(tx, operation, cursor.LastID)
		if err != nil {
			return err
		}
		if pageID == "" {
			operation.Phase, cursor.LastID = sourceCleanupPhaseHistoryWiki, ""
			break
		}
		if err := withdrawSourceCleanupCurrentPageTx(tx, operation, pageID); err != nil {
			return err
		}
		cursor.LastID = pageID
	case sourceCleanupPhaseHistoryWiki:
		revisionID, err := nextSourceCleanupHistoryRevision(tx, operation, cursor.LastID)
		if err != nil {
			return err
		}
		if revisionID == "" {
			operation.Phase, cursor.LastID = sourceCleanupPhaseWikiOwners, ""
			break
		}
		if err := withdrawSourceCleanupHistoryRevisionTx(tx, operation, revisionID); err != nil {
			return err
		}
		cursor.LastID = revisionID
	case sourceCleanupPhaseWikiOwners:
		remaining, err := releaseSourceCleanupWikiOwnersTx(tx, operation)
		if err != nil {
			return err
		}
		if !remaining {
			operation.Phase, cursor.LastID = sourceCleanupPhaseWakeGC, ""
		}
	case sourceCleanupPhaseWakeGC:
		var snapshotIDs []string
		if err := tx.Table("source_snapshots ss").Distinct("ss.id").
			Joins("JOIN source_snapshot_gc_candidates gc ON gc.snapshot_id=ss.id").
			Where("ss.data_source_id=? AND ss.state='retired' AND ss.id>?", operation.DataSourceID, cursor.LastID).
			Order("ss.id ASC").Limit(sourceCleanupDeleteBatch).Pluck("ss.id", &snapshotIDs).Error; err != nil {
			return err
		}
		for _, snapshotID := range snapshotIDs {
			if err := enqueueSourceSnapshotGCCandidate(tx, snapshotID); err != nil {
				return err
			}
		}
		if len(snapshotIDs) == 0 {
			operation.Phase, cursor.LastID = sourceCleanupPhaseSnapshots, ""
		} else {
			cursor.LastID = snapshotIDs[len(snapshotIDs)-1]
		}
	case sourceCleanupPhaseSnapshots:
		done, err := retireSourceCleanupSnapshotsTx(tx, operation)
		if err != nil {
			return err
		}
		if done {
			operation.Phase, cursor.LastID = sourceCleanupPhaseComplete, ""
		}
	case sourceCleanupPhaseComplete:
		return nil
	default:
		return fmt.Errorf("source cleanup phase %q is unsupported", operation.Phase)
	}
	cursorJSON, err := json.Marshal(cursor)
	if err != nil {
		return err
	}
	operation.Cursor = types.JSON(cursorJSON)
	return nil
}

func nextSourceCleanupCurrentPage(tx *gorm.DB, operation *types.SourceCleanupOperation, afterID string) (string, error) {
	var id string
	err := tx.Raw(`SELECT wp.id FROM wiki_pages wp
		WHERE wp.tenant_id=? AND wp.knowledge_base_id=? AND wp.deleted_at IS NULL AND wp.id>?
		AND (
			EXISTS(SELECT 1 FROM source_wiki_page_contributions c WHERE c.page_id=wp.id AND c.revision_id IS NULL AND c.source_id=?) OR
			EXISTS(SELECT 1 FROM source_wiki_evidence_refs wr JOIN source_files sf ON sf.id=wr.source_file_id
				WHERE wr.page_id=wp.id AND wr.revision_id IS NULL AND sf.data_source_id=?) OR
			EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(wp.source_refs::jsonb)='array'
				THEN wp.source_refs::jsonb ELSE '[]'::jsonb END) AS refs(ref)
				JOIN source_files sf ON sf.id=split_part(refs.ref,'|',1) WHERE sf.data_source_id=?) OR
			wp.source_provenance->>'source_id'=?
		)
		ORDER BY wp.id ASC LIMIT 1`, operation.TenantID, operation.KnowledgeBaseID, afterID,
		operation.DataSourceID, operation.DataSourceID, operation.DataSourceID, operation.DataSourceID).Scan(&id).Error
	return id, err
}

func nextSourceCleanupHistoryRevision(tx *gorm.DB, operation *types.SourceCleanupOperation, afterID string) (string, error) {
	var id string
	err := tx.Raw(`SELECT rev.id FROM wiki_page_revisions rev
		WHERE rev.tenant_id=? AND rev.knowledge_base_id=? AND rev.id>?
		AND (
			EXISTS(SELECT 1 FROM source_wiki_page_contributions c WHERE c.revision_id=rev.id AND c.source_id=?) OR
			EXISTS(SELECT 1 FROM source_wiki_evidence_refs wr JOIN source_files sf ON sf.id=wr.source_file_id
				WHERE wr.revision_id=rev.id AND sf.data_source_id=?) OR
			EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(rev.source_refs::jsonb)='array'
				THEN rev.source_refs::jsonb ELSE '[]'::jsonb END) AS refs(ref)
				JOIN source_files sf ON sf.id=split_part(refs.ref,'|',1) WHERE sf.data_source_id=?) OR
			rev.source_provenance->>'source_id'=?
		)
		ORDER BY rev.id ASC LIMIT 1`, operation.TenantID, operation.KnowledgeBaseID, afterID,
		operation.DataSourceID, operation.DataSourceID, operation.DataSourceID, operation.DataSourceID).Scan(&id).Error
	return id, err
}

func withdrawSourceCleanupCurrentPageTx(tx *gorm.DB, operation *types.SourceCleanupOperation, pageID string) error {
	var page types.WikiPage
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND tenant_id=? AND knowledge_base_id=? AND deleted_at IS NULL", pageID, operation.TenantID, operation.KnowledgeBaseID).
		Take(&page).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	input, err := sourceCleanupWithdrawalInputTx(tx, operation, page.ID, nil, page.Version,
		types.SourceWikiProjectionCurrent, page.Title, page.Summary, page.Content,
		[]string(page.SourceRefs), page.SourceProvenance)
	if err != nil {
		return err
	}
	result, err := source.WithdrawSourceWikiContributions(input)
	if err != nil {
		return err
	}
	switch result.Disposition {
	case types.SourceWikiWithdrawalUnchanged:
		return nil
	case types.SourceWikiWithdrawalWithdrawPage:
		if err := enqueueWikiEvidenceGCCandidates(tx, tx.Table("source_wiki_evidence_refs").Where("page_id=? AND revision_id IS NULL", page.ID)); err != nil {
			return err
		}
		if err := tx.Table("wiki_pages").Where("id=? AND version=?", page.ID, page.Version).
			Update("deleted_at", time.Now().UTC()).Error; err != nil {
			return err
		}
		if err := tx.Where("page_id=? AND revision_id IS NULL", page.ID).Delete(&types.SourceWikiPageContribution{}).Error; err != nil {
			return err
		}
		if err := tx.Where("page_id=? AND revision_id IS NULL", page.ID).Delete(&types.SourceWikiEvidenceRef{}).Error; err != nil {
			return err
		}
		return nil
	case types.SourceWikiWithdrawalReproject:
		if result.Page == nil {
			return errors.New("source Wiki withdrawal returned an empty reprojected page")
		}
		chunkRefs, err := filterSourceWikiChunkRefsTx(tx, operation.DataSourceID, page.ChunkRefs)
		if err != nil {
			return err
		}
		updatedAt := time.Now().UTC()
		wikiPath := cleanupWikiPath(page.PageType, page.CategoryPath, result.Page.Title, page.Slug)
		updates := map[string]any{
			"title": result.Page.Title, "summary": result.Page.Summary, "content": result.Page.Content,
			"source_refs": types.StringArray(result.Page.SourceRefs), "source_provenance": result.Page.Provenance,
			"chunk_refs": chunkRefs, "aliases": types.StringArray{}, "page_metadata": types.JSON(`{}`),
			"wiki_path": wikiPath, "updated_at": updatedAt,
		}
		changed := tx.Model(&types.WikiPage{}).Where("id=? AND tenant_id=? AND knowledge_base_id=? AND version=? AND deleted_at IS NULL",
			page.ID, page.TenantID, page.KnowledgeBaseID, page.Version).Updates(updates)
		if changed.Error != nil {
			return changed.Error
		}
		if changed.RowsAffected != 1 {
			return fmt.Errorf("source Wiki page changed during source withdrawal")
		}
		if err := enqueueWikiEvidenceGCCandidates(tx, tx.Table("source_wiki_evidence_refs").Where(
			"page_id=? AND revision_id IS NULL AND version=? AND source_file_id IN (SELECT id FROM source_files WHERE data_source_id=?)",
			page.ID, page.Version, operation.DataSourceID)); err != nil {
			return err
		}
		if err := tx.Exec(`DELETE FROM source_wiki_evidence_refs wr USING source_files sf
			WHERE wr.source_file_id=sf.id AND wr.page_id=? AND wr.revision_id IS NULL AND wr.version=? AND sf.data_source_id=?`,
			page.ID, page.Version, operation.DataSourceID).Error; err != nil {
			return err
		}
		if err := tx.Where("page_id=? AND revision_id IS NULL AND source_id=?", page.ID, operation.DataSourceID).
			Delete(&types.SourceWikiPageContribution{}).Error; err != nil {
			return err
		}
		return nil
	default:
		return fmt.Errorf("source Wiki withdrawal disposition %q is unsupported", result.Disposition)
	}
}

func withdrawSourceCleanupHistoryRevisionTx(tx *gorm.DB, operation *types.SourceCleanupOperation, revisionID string) error {
	var revision types.WikiPageRevision
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("id=? AND tenant_id=? AND knowledge_base_id=?", revisionID, operation.TenantID, operation.KnowledgeBaseID).
		Take(&revision).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	input, err := sourceCleanupWithdrawalInputTx(tx, operation, revision.PageID, &revision.ID, revision.Version,
		types.SourceWikiProjectionRevision, revision.Title, revision.Summary, revision.Content,
		[]string(revision.SourceRefs), revision.SourceProvenance)
	if err != nil {
		return err
	}
	result, err := source.WithdrawSourceWikiContributions(input)
	if err != nil {
		return err
	}
	if result.Disposition == types.SourceWikiWithdrawalUnchanged {
		return nil
	}
	if err := enqueueWikiEvidenceGCCandidates(tx, tx.Table("source_wiki_evidence_refs").Where("revision_id=?", revision.ID)); err != nil {
		return err
	}
	return tx.Unscoped().Where("id=?", revision.ID).Delete(&types.WikiPageRevision{}).Error
}

func sourceCleanupWithdrawalInputTx(tx *gorm.DB, operation *types.SourceCleanupOperation, pageID string, revisionID *string, pageVersion int,
	kind types.SourceWikiProjectionKind, title, summary, content string, sourceRefs []string, provenance *types.SourceWikiProvenance,
) (types.SourceWikiWithdrawalInput, error) {
	input := types.SourceWikiWithdrawalInput{
		TenantID: operation.TenantID, KnowledgeBaseID: operation.KnowledgeBaseID, PageID: pageID,
		SourceID: operation.DataSourceID, ProjectionKind: kind,
		Page: types.SourceWikiWithdrawalProjection{Title: title, Summary: summary, Content: content,
			SourceRefs: sourceRefs, Provenance: provenance},
		Entries: make([]types.SourceWikiWithdrawalEntry, 0),
	}
	var rows []types.SourceWikiPageContribution
	q := tx.Where("page_id=? AND page_version=?", pageID, pageVersion)
	if revisionID == nil {
		q = q.Where("revision_id IS NULL")
	} else {
		q = q.Where("revision_id=?", *revisionID)
	}
	if err := q.Order("source_id ASC,topic_kind ASC,topic_key ASC").Limit(types.SourceWikiContributionMaxCount + 1).
		Find(&rows).Error; err != nil {
		return input, err
	}
	if len(rows) > types.SourceWikiContributionMaxCount {
		input.HasUnattributedBody = true
		return input, nil
	}
	entryRefs := make(map[string]struct{})
	entryOwners := make(map[string]struct{})
	for _, row := range rows {
		var stored storedSourceWikiContribution
		if len(row.Contribution) == 0 || json.Unmarshal(row.Contribution, &stored) != nil || stored.SourceProvenance == nil {
			input.HasUnattributedBody = true
			continue
		}
		provenance := *stored.SourceProvenance
		originSnapshot := ""
		if len(provenance.Evidence) > 0 {
			originSnapshot = provenance.Evidence[0].SnapshotID
		}
		state := types.SourceWikiContributionCurrent
		if row.State == "ready" && provenance.State == "ready" && row.TargetSnapshotID == row.ApplicableSnapshotID {
			state = types.SourceWikiContributionCurrent
		} else if row.State == "stale" && provenance.State == "stale" {
			state = types.SourceWikiContributionStale
		} else {
			input.HasUnattributedBody = true
			continue
		}
		contribution := types.SourceWikiContribution{
			SourceID: row.SourceID, TopicKind: row.TopicKind, TopicKey: row.TopicKey,
			OriginSnapshotID: originSnapshot, ApplicableSnapshotID: row.ApplicableSnapshotID,
			Body: stored.Content, Evidence: append([]types.SourceWikiEvidence(nil), provenance.Evidence...),
			State: state,
		}
		if state == types.SourceWikiContributionStale {
			contribution.FailureReason = row.Reason
		}
		refs := append([]string(nil), stored.SourceRefs...)
		input.Entries = append(input.Entries, types.SourceWikiWithdrawalEntry{
			Contribution: contribution, Title: stored.Title, Summary: stored.Summary,
			SourceRefs: refs, Provenance: provenance,
		})
		for _, ref := range refs {
			entryRefs[ref] = struct{}{}
		}
		for _, evidence := range provenance.Evidence {
			entryOwners[sourceCleanupEvidenceOwnerKey(evidence.ID, evidence.KnowledgeID, evidence.FileVersionID, evidence.SnapshotID)] = struct{}{}
		}
		input.HasUnattributedBody = input.HasUnattributedBody || stored.HasUnattributedBody || row.PageVersion != pageVersion
	}
	for _, ref := range sourceRefs {
		if _, ok := entryRefs[ref]; !ok {
			input.HasUnattributedBody = true
		}
	}
	for ref := range entryRefs {
		found := false
		for _, pageRef := range sourceRefs {
			if pageRef == ref {
				found = true
				break
			}
		}
		if !found {
			input.HasUnattributedBody = true
		}
	}
	if provenance != nil {
		found := false
		for _, entry := range input.Entries {
			if entry.Contribution.SourceID == provenance.SourceID && entry.Contribution.TopicKind == provenance.TopicKind && entry.Contribution.TopicKey == provenance.TopicKey {
				found = true
				break
			}
		}
		if !found {
			input.HasUnattributedBody = true
		}
	}
	var owners []struct {
		EvidenceID    string `gorm:"column:evidence_id"`
		SourceFileID  string `gorm:"column:source_file_id"`
		FileVersionID string `gorm:"column:file_version_id"`
		SnapshotID    string `gorm:"column:snapshot_id"`
	}
	ownerQuery := tx.Table("source_wiki_evidence_refs").
		Select("evidence_id,source_file_id,file_version_id,snapshot_id").Where("page_id=? AND version=?", pageID, pageVersion)
	if revisionID == nil {
		ownerQuery = ownerQuery.Where("revision_id IS NULL")
	} else {
		ownerQuery = ownerQuery.Where("revision_id=?", *revisionID)
	}
	if err := ownerQuery.Find(&owners).Error; err != nil {
		return input, err
	}
	for _, owner := range owners {
		key := sourceCleanupEvidenceOwnerKey(owner.EvidenceID, owner.SourceFileID, owner.FileVersionID, owner.SnapshotID)
		if _, ok := entryOwners[key]; !ok {
			input.HasUnattributedBody = true
		}
		delete(entryOwners, key)
	}
	if len(entryOwners) != 0 {
		input.HasUnattributedBody = true
	}
	return input, nil
}

func sourceCleanupEvidenceOwnerKey(evidenceID, sourceFileID, fileVersionID, snapshotID string) string {
	return evidenceID + "\x00" + sourceFileID + "\x00" + fileVersionID + "\x00" + snapshotID
}

func filterSourceWikiChunkRefsTx(tx *gorm.DB, dataSourceID string, existing types.StringArray) (types.StringArray, error) {
	if len(existing) == 0 {
		return types.StringArray{}, nil
	}
	var sourceChunkIDs []string
	if err := tx.Table("source_chunk_references cr").Distinct("cr.chunk_id").
		Joins("JOIN source_snapshots ss ON ss.id=cr.snapshot_id").
		Where("ss.data_source_id=? AND cr.chunk_id IN ?", dataSourceID, []string(existing)).Pluck("cr.chunk_id", &sourceChunkIDs).Error; err != nil {
		return nil, err
	}
	remove := make(map[string]struct{}, len(sourceChunkIDs))
	for _, id := range sourceChunkIDs {
		remove[id] = struct{}{}
	}
	filtered := make(types.StringArray, 0, len(existing))
	for _, id := range existing {
		if _, ok := remove[id]; !ok {
			filtered = append(filtered, id)
		}
	}
	return filtered, nil
}

func cleanupWikiPath(pageType string, categoryPath types.StringArray, title, slug string) string {
	display := strings.TrimSpace(title)
	if display == "" {
		display = strings.TrimSpace(slug)
	}
	parts := make([]string, 0, len(categoryPath)+2)
	if value := strings.TrimSpace(pageType); value != "" {
		parts = append(parts, value)
	}
	for _, category := range categoryPath {
		if category = strings.TrimSpace(category); category != "" {
			parts = append(parts, category)
		}
	}
	if display != "" {
		parts = append(parts, display)
	}
	return strings.Join(parts, "/")
}

func releaseSourceCleanupWikiOwnersTx(tx *gorm.DB, operation *types.SourceCleanupOperation) (bool, error) {
	more := false
	deleteBatch := func(table, predicate string, args ...any) error {
		if !tx.Migrator().HasTable(table) {
			return nil
		}
		statement := fmt.Sprintf("DELETE FROM %s WHERE ctid IN (SELECT ctid FROM %s WHERE %s LIMIT ?)", table, table, predicate)
		result := tx.Exec(statement, append(args, sourceCleanupDeleteBatch)...)
		if result.Error != nil {
			return result.Error
		}
		more = more || result.RowsAffected > 0
		return nil
	}
	if tx.Migrator().HasTable("source_wiki_attempts") {
		result := tx.Exec(`UPDATE source_wiki_attempts SET status=CASE WHEN status IN ('queued','running') THEN 'failed' ELSE status END,
			reason='Source content was cleared.',draft=NULL,checkpoint=NULL,updated_at=now()
			WHERE ctid IN (SELECT ctid FROM source_wiki_attempts WHERE source_id=? AND (draft IS NOT NULL OR checkpoint IS NOT NULL OR status IN ('queued','running')) LIMIT ?)`,
			operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		more = more || result.RowsAffected > 0
	}
	if err := deleteBatch("source_wiki_page_contributions", "source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if tx.Migrator().HasTable("source_wiki_evidence_refs") {
		result := tx.Exec(`DELETE FROM source_wiki_evidence_refs wr WHERE wr.ctid IN (
			SELECT wr.ctid FROM source_wiki_evidence_refs wr JOIN source_files sf ON sf.id=wr.source_file_id
			WHERE sf.data_source_id=? LIMIT ?)`, operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		more = more || result.RowsAffected > 0
	}
	if tx.Migrator().HasTable("source_read_wiki_evidence_refs") {
		result := tx.Exec(`DELETE FROM source_read_wiki_evidence_refs rr WHERE rr.ctid IN (
			SELECT rr.ctid FROM source_read_wiki_evidence_refs rr JOIN source_files sf ON sf.id=rr.source_file_id
			WHERE sf.data_source_id=? LIMIT ?)`, operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		more = more || result.RowsAffected > 0
	}
	if tx.Migrator().HasTable("source_wiki_attempt_evidence_refs") {
		result := tx.Exec(`DELETE FROM source_wiki_attempt_evidence_refs ar WHERE ar.ctid IN (
			SELECT ar.ctid FROM source_wiki_attempt_evidence_refs ar JOIN source_wiki_attempts a ON a.id=ar.attempt_id
			WHERE a.source_id=? LIMIT ?)`, operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		more = more || result.RowsAffected > 0
	}
	if err := deleteBatch("source_read_scopes", "snapshot_id IN (SELECT id FROM source_snapshots WHERE data_source_id=?)", operation.DataSourceID); err != nil {
		return false, err
	}
	if tx.Migrator().HasTable("source_read_wiki_scopes") {
		result := tx.Exec(`DELETE FROM source_read_wiki_scopes WHERE ctid IN (
			SELECT ctid FROM source_read_wiki_scopes
			WHERE jsonb_array_length(source_ids)>0 AND jsonb_array_length(source_ids)=1
			AND jsonb_exists(source_ids,?) LIMIT ?)`, operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		more = more || result.RowsAffected > 0
		result = tx.Exec(`UPDATE source_read_wiki_scopes SET source_ids=source_ids-?
			WHERE ctid IN (SELECT ctid FROM source_read_wiki_scopes
				WHERE jsonb_array_length(source_ids)>1 AND jsonb_exists(source_ids,?) LIMIT ?)`, operation.DataSourceID, operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		more = more || result.RowsAffected > 0
	}
	if err := deleteBatch("source_wiki_update_plans", "source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_wiki_topics", "source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_wiki_batches", "source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_wiki_attempts", "source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_publication_outbox", "data_source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("task_pending_ops", "task_type=? AND payload->>'data_source_id'=?", types.TypeSourceWikiUpdate, operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_parsed_artifacts", "data_source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_embedding_artifacts", "data_source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if err := deleteBatch("source_gitlab_webhook_deliveries", "data_source_id=?", operation.DataSourceID); err != nil {
		return false, err
	}
	if tx.Migrator().HasTable("source_gitlab_webhook_configs") {
		if err := tx.Exec("DELETE FROM source_gitlab_webhook_configs WHERE data_source_id=?", operation.DataSourceID).Error; err != nil {
			return false, err
		}
	}
	return more, nil
}

func retireSourceCleanupSnapshotsTx(tx *gorm.DB, operation *types.SourceCleanupOperation) (bool, error) {
	if err := tx.Exec("DELETE FROM source_publications WHERE data_source_id=?", operation.DataSourceID).Error; err != nil {
		return false, err
	}
	var snapshotIDs []string
	if err := tx.Table("source_snapshots").Select("id").
		Where("data_source_id=? AND state<>'retired'", operation.DataSourceID).
		Order("id ASC").Limit(sourceCleanupDeleteBatch).Pluck("id", &snapshotIDs).Error; err != nil {
		return false, err
	}
	if len(snapshotIDs) > 0 {
		if err := tx.Table("source_snapshots").Where("id IN ?", snapshotIDs).Update("state", "retired").Error; err != nil {
			return false, err
		}
		for _, id := range snapshotIDs {
			if err := enqueueSourceSnapshotGCCandidate(tx, id); err != nil {
				return false, err
			}
		}
		return false, nil
	}
	var missingCandidates []string
	if err := tx.Raw(`SELECT ss.id FROM source_snapshots ss
		WHERE ss.data_source_id=? AND ss.state='retired'
		AND NOT EXISTS(SELECT 1 FROM source_snapshot_gc_candidates gc WHERE gc.snapshot_id=ss.id)
		ORDER BY ss.id ASC LIMIT ?`, operation.DataSourceID, sourceCleanupDeleteBatch).Scan(&missingCandidates).Error; err != nil {
		return false, err
	}
	for _, id := range missingCandidates {
		if err := enqueueSourceSnapshotGCCandidate(tx, id); err != nil {
			return false, err
		}
	}
	if len(missingCandidates) > 0 {
		return false, nil
	}
	var remaining int64
	if err := tx.Table("source_files sf").Where(`sf.data_source_id=? AND NOT EXISTS(
		SELECT 1 FROM source_file_versions sv WHERE sv.source_file_id=sf.id)`, operation.DataSourceID).
		Count(&remaining).Error; err != nil {
		return false, err
	}
	if remaining > 0 {
		result := tx.Exec(`DELETE FROM source_files sf WHERE sf.ctid IN (
			SELECT sf.ctid FROM source_files sf WHERE sf.data_source_id=? AND NOT EXISTS(
				SELECT 1 FROM source_file_versions sv WHERE sv.source_file_id=sf.id)
			LIMIT ?)`, operation.DataSourceID, sourceCleanupDeleteBatch)
		if result.Error != nil {
			return false, result.Error
		}
		return false, nil
	}
	var remainingSnapshots int64
	if err := tx.Table("source_snapshots").Where("data_source_id=?", operation.DataSourceID).Count(&remainingSnapshots).Error; err != nil {
		return false, err
	}
	var remainingFiles int64
	if err := tx.Table("source_files").Where("data_source_id=?", operation.DataSourceID).Count(&remainingFiles).Error; err != nil {
		return false, err
	}
	if remainingSnapshots > 0 || remainingFiles > 0 {
		return false, nil
	}
	var remainingRelations int64
	if tx.Migrator().HasTable("source_code_relations") {
		if err := tx.Table("source_code_relations").Where("data_source_id=?", operation.DataSourceID).Count(&remainingRelations).Error; err != nil {
			return false, err
		}
	}
	if remainingRelations > 0 {
		return false, fmt.Errorf("source cleanup reached snapshot phase with retained source code relations")
	}
	return true, nil
}

func timePointer(value time.Time) *time.Time { return &value }
