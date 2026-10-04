package repository

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// stageSourceWikiUpdateFence invalidates the current-source answer projection
// and records the publication/configuration fence in the same transaction as
// the snapshot flip and durable outbox event. A later bounded planner may
// safely promote unchanged contributions; until then they fail closed.
func stageSourceWikiUpdateFence(
	tx *gorm.DB,
	tenantID uint64,
	knowledgeBaseID, sourceID, previousSnapshotID, snapshotID string,
	configGeneration int64,
	now time.Time,
) error {
	if tx == nil || tenantID == 0 || knowledgeBaseID == "" || sourceID == "" || snapshotID == "" || configGeneration <= 0 || now.IsZero() {
		return fmt.Errorf("source Wiki update fence identity is incomplete")
	}
	plan := map[string]any{
		"schema_version":       1,
		"state":                "awaiting_bounded_impact_plan",
		"previous_snapshot_id": previousSnapshotID,
		"snapshot_id":          snapshotID,
		"config_generation":    configGeneration,
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return err
	}
	inventoryJSON, err := json.Marshal(map[string]any{
		"schema_version":    1,
		"snapshot_id":       snapshotID,
		"config_generation": configGeneration,
		"complete":          false,
	})
	if err != nil {
		return err
	}
	digest := sha256.Sum256(planJSON)
	result := tx.Exec(`INSERT INTO source_wiki_update_plans
		(id,tenant_id,knowledge_base_id,source_id,previous_snapshot_id,snapshot_id,config_generation,
		 plan_digest,status,source_wide_stale,reason_code,reason,plan,next_inventory,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,'pending',TRUE,'impact_plan_pending',?,?::jsonb,?::jsonb,?,?)
		ON CONFLICT(source_id,snapshot_id) DO UPDATE SET
		 config_generation=EXCLUDED.config_generation,plan_digest=EXCLUDED.plan_digest,status='pending',
		 source_wide_stale=TRUE,reason_code='impact_plan_pending',reason=EXCLUDED.reason,
			 plan=EXCLUDED.plan,next_inventory=EXCLUDED.next_inventory,completed_at=NULL,updated_at=EXCLUDED.updated_at
		WHERE source_wiki_update_plans.config_generation<>EXCLUDED.config_generation`,
		uuid.NewString(), tenantID, knowledgeBaseID, sourceID, previousSnapshotID, snapshotID,
		configGeneration, hex.EncodeToString(digest[:]),
		"A bounded impact plan has not yet been materialized; current-source Wiki reads remain fail-closed.",
		string(planJSON), string(inventoryJSON), now, now)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		var existing struct {
			TenantID           uint64
			KnowledgeBaseID    string
			PreviousSnapshotID string
			ConfigGeneration   int64
		}
		if err := tx.Table("source_wiki_update_plans").
			Select("tenant_id,knowledge_base_id,previous_snapshot_id,config_generation").
			Where("source_id=? AND snapshot_id=?", sourceID, snapshotID).Take(&existing).Error; err != nil {
			return err
		}
		if existing.TenantID != tenantID || existing.KnowledgeBaseID != knowledgeBaseID || existing.PreviousSnapshotID != previousSnapshotID || existing.ConfigGeneration != configGeneration {
			return fmt.Errorf("source Wiki update plan already exists with a different publication fence")
		}
	} else {
		if err := tx.Exec(`UPDATE source_wiki_update_plans SET status='superseded',source_wide_stale=TRUE,
			reason_code='newer_publication_superseded',reason='A newer source publication superseded this unprocessed Wiki plan.',completed_at=?,updated_at=?
			WHERE source_id=? AND snapshot_id<>? AND status IN ('pending','running')`,
			now, now, sourceID, snapshotID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE source_wiki_update_plan_items SET state='superseded',updated_at=?
			WHERE plan_id IN (SELECT id FROM source_wiki_update_plans WHERE source_id=? AND status='superseded') AND state IN ('pending','running')`,
			now, sourceID).Error; err != nil {
			return err
		}
		if err := tx.Exec(`UPDATE source_wiki_update_plan_items SET state='superseded',updated_at=?
			WHERE plan_id=(SELECT id FROM source_wiki_update_plans WHERE source_id=? AND snapshot_id=?) AND state IN ('pending','running')`,
			now, sourceID, snapshotID).Error; err != nil {
			return err
		}
	}
	if result.RowsAffected == 1 {
		if err := tx.Exec(`UPDATE source_wiki_page_contributions
			SET target_snapshot_id=?, state='stale', reason_code='impact_plan_pending',
				reason='Awaiting bounded source impact planning.', updated_at=?
			WHERE source_id=? AND revision_id IS NULL AND state<>'removed'`, snapshotID, now, sourceID).Error; err != nil {
			return err
		}
		return tx.Exec(`UPDATE wiki_pages
			SET source_provenance=jsonb_set(source_provenance::jsonb,'{state}','"stale"'::jsonb,true), updated_at=?
			WHERE tenant_id=? AND knowledge_base_id=? AND source_provenance IS NOT NULL
			  AND source_provenance->>'source_id'=? AND source_provenance->>'state'='ready'`,
			now, tenantID, knowledgeBaseID, sourceID).Error
	}
	return nil
}

// PersistSourceWikiPageContributionInTx records the page's single-source
// contribution only after the fenced Wiki page write has succeeded. Callers
// provide a complete module-member inventory when one is available; an empty
// inventory remains explicit and cannot be mistaken for proof of no
// dependencies.
func PersistSourceWikiPageContributionInTx(tx *gorm.DB, page *types.WikiPage, topicKind, topicKey string, dependencyFileIDs, moduleMemberFileIDs []string, inventoryComplete bool, now time.Time) error {
	if tx == nil || page == nil || page.ID == "" || page.TenantID == 0 || page.KnowledgeBaseID == "" || page.Version <= 0 || page.SourceProvenance == nil {
		return fmt.Errorf("source Wiki page contribution identity is incomplete")
	}
	p := page.SourceProvenance
	if p.SourceID == "" || p.ApplicableSnapshotID == "" || (topicKey == "" && p.TopicKey == "") || now.IsZero() {
		return fmt.Errorf("source Wiki page contribution has no source, topic, or snapshot binding")
	}
	if topicKind == "" {
		topicKind = p.TopicKind
	}
	if topicKey == "" {
		topicKey = p.TopicKey
	}
	dependencyIDs := make([]string, 0, len(p.Evidence)+len(dependencyFileIDs))
	evidence := make([]map[string]any, 0, len(p.Evidence))
	seenEvidence := make(map[string]struct{}, len(p.Evidence))
	for _, item := range p.Evidence {
		if item.KnowledgeID == "" || item.Path == "" || item.SHA256 == "" || item.TextSHA256 == "" || item.Range.EndByte <= item.Range.StartByte {
			return fmt.Errorf("source Wiki page contribution contains incomplete evidence")
		}
		if _, ok := seenEvidence[item.KnowledgeID]; !ok {
			seenEvidence[item.KnowledgeID] = struct{}{}
			dependencyIDs = append(dependencyIDs, item.KnowledgeID)
		}
		evidence = append(evidence, map[string]any{
			"source_file_id": item.KnowledgeID, "path": item.Path, "content_sha256": item.SHA256,
			"text_sha256": item.TextSHA256, "range": item.Range, "quality": item.Quality,
		})
	}
	dependencyIDs = append(dependencyIDs, dependencyFileIDs...)
	dependencyIDs = sortedUniqueIDs(dependencyIDs)
	sort.Strings(dependencyIDs)
	moduleMemberFileIDs = sortedUniqueIDs(moduleMemberFileIDs)
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	evidenceDigest := sha256.Sum256(evidenceJSON)
	dependencyJSON, err := json.Marshal(dependencyIDs)
	if err != nil {
		return err
	}
	membersJSON, err := json.Marshal(moduleMemberFileIDs)
	if err != nil {
		return err
	}
	contributionJSON, err := json.Marshal(map[string]any{
		"topic_kind": topicKind, "topic_key": topicKey, "source_provenance": p,
		"title": page.Title, "summary": page.Summary, "content": page.Content,
		"source_refs": page.SourceRefs, "has_unattributed_body": false,
	})
	if err != nil {
		return err
	}
	state := p.State
	reasonCode, reason := "", ""
	if state != "ready" {
		state = "stale"
	}
	if !inventoryComplete {
		state, reasonCode, reason = "stale", "dependency_manifest_incomplete", "The source contribution dependency inventory is incomplete and cannot be carried forward."
	}
	result := tx.Exec(`INSERT INTO source_wiki_page_contributions
		(page_id,revision_id,source_id,page_version,topic_kind,topic_key,applicable_snapshot_id,target_snapshot_id,
		 state,reason_code,reason,evidence_sha256,dependency_file_ids,module_member_file_ids,contribution,updated_at)
		VALUES(?,NULL,?,?,?,?,?,?,?,?,?,?,?::jsonb,?::jsonb,?::jsonb,?)
		ON CONFLICT (page_id,source_id,topic_kind,topic_key) WHERE revision_id IS NULL DO UPDATE SET
		 page_version=EXCLUDED.page_version,topic_kind=EXCLUDED.topic_kind,topic_key=EXCLUDED.topic_key,
		 applicable_snapshot_id=EXCLUDED.applicable_snapshot_id,target_snapshot_id=EXCLUDED.target_snapshot_id,
		 state=EXCLUDED.state,reason_code=EXCLUDED.reason_code,reason=EXCLUDED.reason,evidence_sha256=EXCLUDED.evidence_sha256,
		 dependency_fingerprint='',module_fingerprint='',dependency_file_ids=EXCLUDED.dependency_file_ids,
		 module_member_file_ids=EXCLUDED.module_member_file_ids,contribution=EXCLUDED.contribution,updated_at=EXCLUDED.updated_at`,
		page.ID, p.SourceID, page.Version, topicKind, topicKey, p.ApplicableSnapshotID, p.ApplicableSnapshotID,
		state, reasonCode, reason, hex.EncodeToString(evidenceDigest[:]), string(dependencyJSON), string(membersJSON), string(contributionJSON), now)
	return result.Error
}

func sortedUniqueIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	copyIDs := append([]string(nil), ids...)
	sort.Strings(copyIDs)
	result := copyIDs[:0]
	last := ""
	for _, id := range copyIDs {
		if id == "" || id == last {
			continue
		}
		result = append(result, id)
		last = id
	}
	return result
}

func archiveSourceWikiContributionsInTx(tx *gorm.DB, revision *types.WikiPageRevision) error {
	if tx == nil || revision == nil || revision.PageID == "" || revision.ID == "" || revision.Version <= 0 || !tx.Migrator().HasTable(&types.SourceWikiPageContribution{}) {
		return nil
	}
	var rows []types.SourceWikiPageContribution
	if err := tx.Where("page_id=? AND revision_id IS NULL", revision.PageID).Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		row.ID = 0
		row.RevisionID = &revision.ID
		row.PageVersion = revision.Version
		row.UpdatedAt = revision.EditedAt
		if err := tx.Exec(`INSERT INTO source_wiki_page_contributions
			(page_id,revision_id,source_id,page_version,topic_kind,topic_key,applicable_snapshot_id,target_snapshot_id,
			 state,reason_code,reason,evidence_sha256,dependency_fingerprint,module_fingerprint,
			 dependency_file_ids,module_member_file_ids,contribution,updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?::jsonb,?) ON CONFLICT (revision_id,source_id,topic_kind,topic_key) WHERE revision_id IS NOT NULL DO NOTHING`,
			row.PageID, row.RevisionID, row.SourceID, row.PageVersion, row.TopicKind, row.TopicKey,
			row.ApplicableSnapshotID, row.TargetSnapshotID, row.State, row.ReasonCode, row.Reason,
			row.EvidenceSHA256, row.DependencyFingerprint, row.ModuleFingerprint,
			string(row.DependencyFileIDs), string(row.ModuleMemberFileIDs), string(row.Contribution), row.UpdatedAt).Error; err != nil {
			return err
		}
	}
	return nil
}

func markSourceWikiContributionsUnverifiedInTx(tx *gorm.DB, pageID string, pageVersion int, now time.Time) error {
	if tx == nil || pageID == "" || pageVersion <= 0 || !tx.Migrator().HasTable(&types.SourceWikiPageContribution{}) {
		return nil
	}
	return tx.Exec(`UPDATE source_wiki_page_contributions SET page_version=?,state='unverified',
		reason_code='wiki_page_edited',reason='Page content changed outside a source revalidation plan.',updated_at=?
		WHERE page_id=? AND revision_id IS NULL`, pageVersion, now, pageID).Error
}
