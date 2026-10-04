package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Every technical projection uses the same predicate before ranking/pagination,
// including directory entries, neighbour summaries and historical bodies.
func (r *wikiPageRepository) readDB(ctx context.Context, table string) *gorm.DB {
	return readWikiPageDB(ctx, r.db, r.sourceWiki, r.sourceWikiContributions, table)
}

func readWikiPageDB(ctx context.Context, base *gorm.DB, sourceWiki, sourceWikiContributions bool, table string) *gorm.DB {
	db := base.WithContext(ctx)
	if !sourceWiki {
		return db
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		db.AddError(err)
		return db
	}
	p := table + ".source_provenance"
	evidenceArray := "(CASE WHEN jsonb_typeof(" + p + "->'evidence')='array' THEN " + p + "->'evidence' ELSE '[]'::jsonb END)"
	owner := "wr.page_id=" + table + ".id AND wr.revision_id IS NULL AND wr.version=" + table + ".version"
	if table == "wiki_page_revisions" {
		owner = "wr.revision_id=" + table + ".id AND wr.page_id=" + table + ".page_id AND wr.version=" + table + ".version"
	}
	refs := "(CASE WHEN jsonb_typeof(" + table + ".source_refs::jsonb)='array' THEN " + table + ".source_refs::jsonb ELSE '[]'::jsonb END)"
	mixedSources := `(SELECT count(DISTINCT msf.data_source_id) FROM jsonb_array_elements_text(` + refs + `) mfr
		JOIN source_files msf ON msf.id=split_part(mfr,'|',1))>1`
	mixedContributionGate := `NOT (` + mixedSources + `)`
	if sourceWikiContributions {
		contributionOwner := "mc.page_id=" + table + ".id AND mc.revision_id IS NULL AND mc.page_version=" + table + ".version"
		if table == "wiki_page_revisions" {
			contributionOwner = "mc.page_id=" + table + ".page_id AND mc.revision_id=" + table + ".id AND mc.page_version=" + table + ".version"
		}
		mixedContributionGate = `(NOT (` + mixedSources + `) OR ` + wikiPageMixedContributionOwnerSQL(ctx, table, refs, contributionOwner, p) + `)`
	}
	evidence := `NOT EXISTS (SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) we
 WHERE NOT EXISTS(SELECT 1 FROM source_wiki_evidence_refs wr
 JOIN source_file_versions sv ON sv.id=wr.file_version_id AND sv.source_file_id=wr.source_file_id AND sv.snapshot_id=wr.snapshot_id
 JOIN source_snapshots ss ON ss.id=wr.snapshot_id AND ss.state='published'
 JOIN source_files sf ON sf.id=wr.source_file_id AND sf.tenant_id=` + table + `.tenant_id AND sf.knowledge_base_id=` + table + `.knowledge_base_id AND sf.data_source_id=ss.data_source_id
 WHERE ` + owner + ` AND wr.evidence_id=we->>'id' AND wr.source_file_id=we->>'knowledge_id'
		 AND wr.file_version_id=we->>'file_version_id' AND wr.snapshot_id=we->>'snapshot_id'
	 AND wr.path=we->>'path' AND wr.commit_sha=we->>'commit_sha'
 AND sf.data_source_id=we->>'data_source_id' AND sv.sha256=we->>'sha256' AND ss.commit_sha=we->>'commit_sha'
 AND encode(sha256(sv.content),'hex')=sv.sha256
 AND encode(sha256(substring(sv.content from (we->'range'->>'start_byte')::int+1 for GREATEST(0,(we->'range'->>'end_byte')::int-(we->'range'->>'start_byte')::int))),'hex')=we->>'text_sha256'
 AND (we->'range'->>'start_byte')::int>=0 AND (we->'range'->>'end_byte')::int<=octet_length(sv.content)
 AND (we->'range'->>'end_byte')::int>(we->'range'->>'start_byte')::int
 AND EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(` + refs + `, '[]'::jsonb)) fr WHERE split_part(fr,'|',1)=sf.id)))`
	sourceHasEvidence := `NOT (` + mixedSources + `) AND EXISTS(SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) we WHERE we->>'knowledge_id'=wk.id)`
	if sourceWikiContributions {
		contributionOwner := "sc.page_id=" + table + ".id AND sc.revision_id IS NULL AND sc.page_version=" + table + ".version"
		if table == "wiki_page_revisions" {
			contributionOwner = "sc.page_id=" + table + ".page_id AND sc.revision_id=" + table + ".id AND sc.page_version=" + table + ".version"
		}
		sourceHasEvidence = `(NOT (` + mixedSources + `) AND EXISTS(SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) we WHERE we->>'knowledge_id'=wk.id))
			OR ((` + mixedSources + `) AND EXISTS(SELECT 1 FROM source_wiki_page_contributions sc
				WHERE ` + contributionOwner + ` AND sc.source_id=sf.data_source_id
					AND EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(sc.contribution->'source_provenance'->'evidence')='array'
						THEN sc.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END) sce WHERE sce->>'knowledge_id'=wk.id)))`
	}
	allSources := `NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(` + refs + `) fr
 LEFT JOIN knowledges wk ON wk.id=split_part(fr,'|',1)
 LEFT JOIN source_files sf ON sf.id=wk.id
 LEFT JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.tenant_id=wk.tenant_id AND ds.knowledge_base_id=wk.knowledge_base_id
 WHERE NOT COALESCE(wk.id IS NOT NULL AND wk.tenant_id=` + table + `.tenant_id AND wk.knowledge_base_id=` + table + `.knowledge_base_id AND wk.deleted_at IS NULL
 AND ((wk.type='source' AND sf.id IS NOT NULL AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source'
 AND ` + source.SourcePermissionSQL(ctx, "sf.data_source_id", "wk.id") + `
	 AND (` + sourceHasEvidence + `))
 OR (wk.type IS DISTINCT FROM 'source' AND ` + source.OrdinaryKnowledgeSQL(ctx, "wk.id") + `)),FALSE))`
	condition := `jsonb_typeof(` + evidenceArray + `)='array' AND jsonb_array_length(` + evidenceArray + `)>0
	 AND jsonb_array_length(COALESCE(` + refs + `,'[]'::jsonb))>0 AND ` + evidence + " AND " + allSources + " AND (" + mixedContributionGate + ")"
	if source.IsWikiAnswerRead(ctx) && table == "wiki_pages" {
		condition += " AND " + p + "->>'state'='ready' AND " + table + ".status='published' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(" + evidenceArray + ") ae WHERE NOT (" + source.SnapshotSQL(ctx, p+"->>'applicable_snapshot_id'", p+"->>'source_id'", "ae->>'knowledge_id'") + "))"
		condition += " AND " + wikiPageSourceContributionsAnswerSQL(ctx, table)
	}
	// Ordinary wiki rows that contain a source contribution without complete typed
	// provenance fail closed instead of falling back to the legacy intersect rule.
	ordinary := p + " IS NULL AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(" + refs + ",'[]'::jsonb)) fr JOIN source_files sf ON sf.id=split_part(fr,'|',1))"

	return db.Where("(" + ordinary + ") OR (" + condition + ")")
}

func wikiPageMixedContributionOwnerSQL(ctx context.Context, table, refs, owner, pageProvenance string) string {
	contributionEvidence := `CASE WHEN jsonb_typeof(mc.contribution->'source_provenance'->'evidence')='array'
		THEN mc.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END`
	ownerEvidence := `EXISTS(SELECT 1 FROM source_wiki_evidence_refs mwr
		JOIN source_file_versions msv ON msv.id=mwr.file_version_id AND msv.source_file_id=mwr.source_file_id AND msv.snapshot_id=mwr.snapshot_id
		JOIN source_snapshots mss ON mss.id=mwr.snapshot_id AND mss.state='published'
		JOIN source_files msf ON msf.id=mwr.source_file_id AND msf.tenant_id=` + table + `.tenant_id
			AND msf.knowledge_base_id=` + table + `.knowledge_base_id AND msf.data_source_id=mss.data_source_id
		WHERE mwr.page_id=mc.page_id AND mwr.revision_id IS NOT DISTINCT FROM mc.revision_id AND mwr.version=mc.page_version
			AND mwr.evidence_id=mce->>'id' AND mwr.source_file_id=mce->>'knowledge_id'
			AND mwr.file_version_id=mce->>'file_version_id' AND mwr.snapshot_id=mce->>'snapshot_id'
			AND mwr.path=mce->>'path' AND mwr.commit_sha=mce->>'commit_sha'
			AND msf.data_source_id=mc.source_id AND mce->>'data_source_id'=mc.source_id
			AND msv.sha256=mce->>'sha256' AND mss.commit_sha=mce->>'commit_sha'
			AND encode(sha256(msv.content),'hex')=msv.sha256
			AND encode(sha256(substring(msv.content from (mce->'range'->>'start_byte')::int+1 for GREATEST(0,(mce->'range'->>'end_byte')::int-(mce->'range'->>'start_byte')::int))),'hex')=mce->>'text_sha256'
			AND (mce->'range'->>'start_byte')::int>=0 AND (mce->'range'->>'end_byte')::int<=octet_length(msv.content)
			AND (mce->'range'->>'end_byte')::int>(mce->'range'->>'start_byte')::int
			AND (` + source.SourcePermissionSQL(ctx, "msf.data_source_id", "msf.id") + `))`
	primaryOwner := strings.ReplaceAll(owner, "mc.", "mp.")
	return `(
		COALESCE(` + pageProvenance + `->>'state','') IN ('ready','stale')
		AND (SELECT count(*) FROM source_wiki_page_contributions mc WHERE ` + owner + `) BETWEEN 1 AND ` + fmt.Sprint(types.SourceWikiContributionMaxCount) + `
		AND NOT EXISTS(SELECT 1 FROM source_wiki_page_contributions mc WHERE ` + owner + ` AND (
			mc.state NOT IN ('ready','stale') OR
			(mc.state='ready' AND (mc.target_snapshot_id IS DISTINCT FROM mc.applicable_snapshot_id OR
				mc.contribution->'source_provenance'->>'state' IS DISTINCT FROM 'ready')) OR
			COALESCE(mc.contribution->>'has_unattributed_body','true')<>'false' OR
			mc.contribution->>'topic_kind' IS DISTINCT FROM mc.topic_kind OR
			mc.contribution->>'topic_key' IS DISTINCT FROM mc.topic_key OR
			jsonb_typeof(mc.contribution->'source_provenance') IS DISTINCT FROM 'object' OR
			mc.contribution->'source_provenance'->>'source_id' IS DISTINCT FROM mc.source_id OR
			mc.contribution->'source_provenance'->>'topic_kind' IS DISTINCT FROM mc.topic_kind OR
			mc.contribution->'source_provenance'->>'topic_key' IS DISTINCT FROM mc.topic_key OR
			mc.contribution->'source_provenance'->>'applicable_snapshot_id' IS DISTINCT FROM mc.applicable_snapshot_id OR
			COALESCE(mc.contribution->'source_provenance'->>'state','') NOT IN ('ready','stale') OR
			jsonb_typeof(mc.contribution->'source_provenance'->'evidence') IS DISTINCT FROM 'array' OR
			jsonb_array_length(` + contributionEvidence + `)=0 OR
			jsonb_array_length(` + contributionEvidence + `)>` + fmt.Sprint(types.SourceWikiContributionMaxEvidencePerItem) + ` OR
			NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(` + refs + `) mfr JOIN source_files mrf
				ON mrf.id=split_part(mfr,'|',1) WHERE mrf.data_source_id=mc.source_id) OR
			EXISTS(SELECT 1 FROM jsonb_array_elements(` + contributionEvidence + `) mce
				WHERE mce->>'id' IS NULL OR mce->>'knowledge_id' IS NULL OR mce->>'file_version_id' IS NULL OR
					mce->>'snapshot_id' IS DISTINCT FROM ((` + contributionEvidence + `)->0->>'snapshot_id') OR NOT (` + ownerEvidence + `) OR
					NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(` + refs + `) mfr
						WHERE split_part(mfr,'|',1)=mce->>'knowledge_id'))
		))
		AND EXISTS(SELECT 1 FROM source_wiki_page_contributions mp WHERE ` + primaryOwner + `
			AND mp.source_id=` + pageProvenance + `->>'source_id'
			AND mp.topic_kind=` + pageProvenance + `->>'topic_kind'
			AND mp.topic_key=` + pageProvenance + `->>'topic_key'
			AND mp.applicable_snapshot_id=` + pageProvenance + `->>'applicable_snapshot_id'
			AND mp.contribution->'source_provenance'->'evidence'=` + pageProvenance + `->'evidence')
	)`
}

func wikiPageSourceContributionsAnswerSQL(ctx context.Context, table string) string {
	contributionEvidence := `CASE WHEN jsonb_typeof(c.contribution->'source_provenance'->'evidence')='array'
		THEN c.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END`
	permission := source.SourcePermissionSQL(ctx, "c.source_id", "ce->>'knowledge_id'")
	snapshot := source.SnapshotSQL(ctx, "c.applicable_snapshot_id", "c.source_id", "ce->>'knowledge_id'")
	return `EXISTS(SELECT 1 FROM source_wiki_page_contributions c
		WHERE c.page_id=` + table + `.id AND c.revision_id IS NULL AND c.page_version=` + table + `.version)
		AND NOT EXISTS(SELECT 1 FROM source_wiki_page_contributions c
			WHERE c.page_id=` + table + `.id AND c.revision_id IS NULL AND c.page_version=` + table + `.version
			AND (c.state<>'ready' OR c.target_snapshot_id<>c.applicable_snapshot_id OR
				COALESCE(c.contribution->>'has_unattributed_body','true')<>'false' OR
				c.contribution->'source_provenance'->>'source_id'<>c.source_id OR
				c.contribution->'source_provenance'->>'topic_key'<>c.topic_key OR
				c.contribution->'source_provenance'->>'state'<>'ready' OR
				c.contribution->'source_provenance'->>'applicable_snapshot_id'<>c.applicable_snapshot_id OR
				jsonb_typeof(c.contribution->'source_provenance'->'evidence')<>'array' OR
				jsonb_array_length(` + contributionEvidence + `)=0 OR
				NOT EXISTS(SELECT 1 FROM source_publications cp JOIN source_snapshots cs ON cs.id=cp.snapshot_id
					AND cs.data_source_id=cp.data_source_id AND cs.tenant_id=cp.tenant_id AND cs.knowledge_base_id=cp.knowledge_base_id
					WHERE cp.data_source_id=c.source_id AND cp.tenant_id=` + table + `.tenant_id
					AND cp.knowledge_base_id=` + table + `.knowledge_base_id AND cp.snapshot_id=c.applicable_snapshot_id
					AND cs.state='published' AND cs.manifest_complete=TRUE) OR
				EXISTS(SELECT 1 FROM jsonb_array_elements(` + contributionEvidence + `) ce
					WHERE ce->>'data_source_id'<>c.source_id OR ce->>'knowledge_id' IS NULL OR
						NOT (` + permission + `) OR NOT (` + snapshot + `))))`
}

func pinSourceWikiEvidenceOwner(tx *gorm.DB, ctx context.Context, leaseID, pageID string, revisionID *string, version int) error {
	if leaseID == "" || pageID == "" || version <= 0 {
		return nil
	}
	owner := "wr.page_id=? AND wr.revision_id IS NULL"
	args := []any{leaseID, leaseID, pageID}
	if revisionID != nil {
		owner = "wr.page_id=? AND wr.revision_id=?"
		args = append(args, *revisionID)
	}
	args = append(args, version)
	return tx.Exec(`INSERT INTO source_read_wiki_evidence_refs
		(lease_id,page_id,revision_id,version,evidence_id,source_file_id,file_version_id,snapshot_id,path,commit_sha)
		SELECT ?,wr.page_id,wr.revision_id,wr.version,wr.evidence_id,wr.source_file_id,wr.file_version_id,wr.snapshot_id,wr.path,wr.commit_sha
		FROM source_wiki_evidence_refs wr
		JOIN source_file_versions sv ON sv.id=wr.file_version_id AND sv.source_file_id=wr.source_file_id AND sv.snapshot_id=wr.snapshot_id
		JOIN source_files sf ON sf.id=wr.source_file_id
		JOIN source_read_leases rl ON rl.id=? AND rl.expires_at>now()
		WHERE `+owner+` AND wr.version=? AND `+source.SourcePermissionSQL(ctx, "sf.data_source_id", "sf.id")+`
		ON CONFLICT DO NOTHING`,
		args...).Error
}

func enqueueSourceSnapshotGCCandidate(tx *gorm.DB, snapshotID string) error {
	if snapshotID == "" {
		return nil
	}
	return tx.Exec(`INSERT INTO source_snapshot_gc_candidates(snapshot_id)
		VALUES (?) ON CONFLICT(snapshot_id) DO UPDATE
		SET next_attempt_at=LEAST(source_snapshot_gc_candidates.next_attempt_at,now()),
			enqueue_generation=source_snapshot_gc_candidates.enqueue_generation+1,
			last_error=''`, snapshotID).Error
}

func enqueueWikiEvidenceGCCandidates(tx *gorm.DB, refs *gorm.DB) error {
	var snapshotIDs []string
	if err := refs.Distinct().Pluck("snapshot_id", &snapshotIDs).Error; err != nil {
		return err
	}
	for _, snapshotID := range snapshotIDs {
		if err := enqueueSourceSnapshotGCCandidate(tx, snapshotID); err != nil {
			return err
		}
	}
	return nil
}

func registerSourceWikiAttemptEvidence(tx *gorm.DB, attemptID string, evidence []types.SourceWikiEvidence) error {
	if attemptID == "" || len(evidence) == 0 {
		return fmt.Errorf("source Wiki attempt evidence owner is incomplete")
	}
	for _, item := range evidence {
		result := tx.Exec(`INSERT INTO source_wiki_attempt_evidence_refs
			(attempt_id,source_file_id,file_version_id,snapshot_id)
			SELECT ?,sv.source_file_id,sv.id,sv.snapshot_id
			FROM source_file_versions sv
			JOIN source_files sf ON sf.id=sv.source_file_id
			JOIN source_snapshots ss ON ss.id=sv.snapshot_id AND ss.state='published'
			JOIN source_snapshot_members sm ON sm.snapshot_id=ss.id AND sm.source_file_id=sf.id AND sm.file_version_id=sv.id AND sm.status='parsed'
			WHERE sv.id=? AND sv.source_file_id=? AND sv.snapshot_id=? AND sv.sha256=?
			AND sf.data_source_id=? AND ss.commit_sha=? AND sm.path=?
			ON CONFLICT (attempt_id,file_version_id) DO NOTHING`,
			attemptID, item.FileVersionID, item.KnowledgeID, item.SnapshotID, item.SHA256, item.DataSourceID, item.CommitSHA, item.Path)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			var exists int64
			if err := tx.Table("source_wiki_attempt_evidence_refs").Where("attempt_id=? AND file_version_id=? AND source_file_id=? AND snapshot_id=?", attemptID, item.FileVersionID, item.KnowledgeID, item.SnapshotID).Count(&exists).Error; err != nil {
				return err
			}
			if exists != 1 {
				return fmt.Errorf("source Wiki attempt evidence version is no longer a published exact match")
			}
		}
	}
	return nil
}

// RegisterSourceWikiAttemptEvidence pins the exact published file versions
// selected for a running generation attempt. Call inside the attempt update
// transaction before any model call.
func RegisterSourceWikiAttemptEvidence(tx *gorm.DB, attemptID string, evidence []types.SourceWikiEvidence) error {
	return registerSourceWikiAttemptEvidence(tx, attemptID, evidence)
}

func releaseSourceWikiAttemptEvidence(tx *gorm.DB, attemptID string) error {
	if attemptID == "" {
		return nil
	}
	return tx.Exec("DELETE FROM source_wiki_attempt_evidence_refs WHERE attempt_id=?", attemptID).Error
}

// ReleaseSourceWikiAttemptEvidence releases exact raw-version owners after a
// terminal attempt transition, in the same transaction as that transition.
func ReleaseSourceWikiAttemptEvidence(tx *gorm.DB, attemptID string) error {
	return releaseSourceWikiAttemptEvidence(tx, attemptID)
}

func registerSourceWikiEvidence(db *gorm.DB, page *types.WikiPage, revision *types.WikiPageRevision) error {
	if db == nil || page == nil || page.ID == "" || page.Version <= 0 {
		return fmt.Errorf("source Wiki evidence owner is incomplete")
	}
	var revisionID *string
	if revision != nil {
		revisionID = &revision.ID
	}
	owners := make(map[sourceWikiEvidenceOwnerKey]types.SourceWikiEvidence)
	addEvidence := func(e types.SourceWikiEvidence) error {
		if e.ID == "" || e.KnowledgeID == "" || e.FileVersionID == "" || e.SnapshotID == "" || e.Path == "" || e.CommitSHA == "" {
			return fmt.Errorf("source Wiki evidence owner contains incomplete raw identity")
		}
		key := sourceWikiEvidenceOwnerKey{EvidenceID: e.ID, SourceFileID: e.KnowledgeID, FileVersionID: e.FileVersionID, SnapshotID: e.SnapshotID}
		if prior, exists := owners[key]; exists {
			if prior.Path != e.Path || prior.CommitSHA != e.CommitSHA {
				return fmt.Errorf("source Wiki evidence identity has conflicting immutable coordinates")
			}
			return nil
		}
		owners[key] = e
		return nil
	}
	if page.SourceProvenance != nil {
		for _, e := range page.SourceProvenance.Evidence {
			if err := addEvidence(e); err != nil {
				return err
			}
		}
	}
	if db.Migrator().HasTable(&types.SourceWikiPageContribution{}) {
		var rows []types.SourceWikiPageContribution
		if err := db.Where("page_id=? AND revision_id IS NULL AND page_version=?", page.ID, page.Version).
			Order("id ASC").Limit(types.SourceWikiContributionMaxCount + 1).Find(&rows).Error; err != nil {
			return err
		}
		if len(rows) > types.SourceWikiContributionMaxCount {
			return fmt.Errorf("source Wiki evidence owners exceed the bounded mixed-page contribution count")
		}
		for _, row := range rows {
			var stored struct {
				HasUnattributedBody *bool                       `json:"has_unattributed_body"`
				TopicKind           string                      `json:"topic_kind"`
				TopicKey            string                      `json:"topic_key"`
				SourceProvenance    *types.SourceWikiProvenance `json:"source_provenance"`
			}
			if len(row.Contribution) == 0 || json.Unmarshal(row.Contribution, &stored) != nil ||
				stored.HasUnattributedBody == nil || *stored.HasUnattributedBody || stored.SourceProvenance == nil {
				continue
			}
			provenance := stored.SourceProvenance
			if stored.TopicKind != row.TopicKind || stored.TopicKey != row.TopicKey ||
				provenance.SourceID != row.SourceID || provenance.TopicKind != row.TopicKind || provenance.TopicKey != row.TopicKey ||
				provenance.ApplicableSnapshotID != row.ApplicableSnapshotID ||
				(provenance.State != "ready" && provenance.State != "stale") ||
				(row.State != "ready" && row.State != "stale") ||
				(row.State == "ready" && (row.TargetSnapshotID != row.ApplicableSnapshotID || provenance.State != "ready")) ||
				len(provenance.Evidence) == 0 ||
				len(provenance.Evidence) > types.SourceWikiContributionMaxEvidencePerItem {
				continue
			}
			originSnapshot := provenance.Evidence[0].SnapshotID
			valid := originSnapshot != ""
			for _, e := range provenance.Evidence {
				if e.DataSourceID != row.SourceID || e.SnapshotID != originSnapshot {
					valid = false
					break
				}
			}
			if !valid {
				continue
			}
			for _, e := range provenance.Evidence {
				if err := addEvidence(e); err != nil {
					return err
				}
			}
		}
	}
	for key, e := range owners {
		ref := types.SourceWikiEvidenceRef{
			ID: uuid.NewString(), PageID: page.ID, RevisionID: revisionID, Version: page.Version,
			EvidenceID: key.EvidenceID, SourceFileID: key.SourceFileID, FileVersionID: key.FileVersionID,
			SnapshotID: key.SnapshotID, Path: e.Path, CommitSHA: e.CommitSHA,
		}
		result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ref)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			query := db.Table("source_wiki_evidence_refs").Where(
				"page_id=? AND version=? AND evidence_id=? AND source_file_id=? AND file_version_id=? AND snapshot_id=?",
				page.ID, page.Version, key.EvidenceID, key.SourceFileID, key.FileVersionID, key.SnapshotID)
			if revisionID == nil {
				query = query.Where("revision_id IS NULL")
			} else {
				query = query.Where("revision_id=?", *revisionID)
			}
			var count int64
			if err := query.Count(&count).Error; err != nil {
				return err
			}
			if count != 1 {
				return fmt.Errorf("source Wiki evidence owner conflicts with a different registered raw identity")
			}
		}
	}
	return nil
}

type sourceWikiEvidenceOwnerKey struct {
	EvidenceID    string
	SourceFileID  string
	FileVersionID string
	SnapshotID    string
}

// ReadSourceWikiEvidence requires a registered body owner and exact immutable
// version. Callers first load that complete page/revision through readDB.
func ReadSourceWikiEvidence(ctx context.Context, db *gorm.DB, pageID string, revisionID *string, version int, e types.SourceWikiEvidence) (*types.SourceFileView, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	var file types.SourceFileView
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		leaseID, ok := source.ReadLeaseID(ctx)
		if !ok {
			return fmt.Errorf("source evidence read requires a durable lease")
		}
		if err := pinSourceWikiEvidenceOwner(tx, ctx, leaseID, pageID, revisionID, version); err != nil {
			return err
		}
		q := tx.Table("source_read_wiki_evidence_refs wr").
			Select("sf.id AS knowledge_id, sf.data_source_id, ss.id AS snapshot_id, ss.project_id, ss.commit_sha, ss.repository_url, sv.id AS file_version_id, sv.sha256, sv.encoding, sv.quality, sv.parser_version, sv.content AS raw_content, sv.symbols, octet_length(sv.content) AS file_size, wr.path AS path").
			Joins("JOIN source_file_versions sv ON sv.id=wr.file_version_id AND sv.source_file_id=wr.source_file_id AND sv.snapshot_id=wr.snapshot_id").
			Joins("JOIN source_read_leases rl ON rl.id=wr.lease_id AND rl.expires_at>now()").
			Joins("JOIN source_files sf ON sf.id=wr.source_file_id").
			Joins("JOIN source_snapshots ss ON ss.id=wr.snapshot_id AND ss.data_source_id=sf.data_source_id AND ss.tenant_id=sf.tenant_id AND ss.knowledge_base_id=sf.knowledge_base_id AND ss.state='published'").
			Joins("JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source'").
			Where("wr.lease_id=? AND wr.page_id=? AND wr.version=? AND wr.evidence_id=? AND sv.id=? AND sf.id=? AND wr.snapshot_id=? AND wr.path=? AND wr.commit_sha=? AND sf.data_source_id=?",
				leaseID, pageID, version, e.ID, e.FileVersionID, e.KnowledgeID, e.SnapshotID, e.Path, e.CommitSHA, e.DataSourceID).
			Where(source.SourcePermissionSQL(ctx, "sf.data_source_id", "sf.id"))
		if revisionID == nil {
			q = q.Where("wr.revision_id IS NULL")
		} else {
			q = q.Where("wr.revision_id=?", *revisionID)
		}
		if err := q.Clauses(clause.Locking{Strength: "SHARE", Table: clause.Table{Name: "sv"}}).Take(&file).Error; err != nil {
			return err
		}
		hash := sha256.Sum256(file.RawContent)
		if hex.EncodeToString(hash[:]) != file.SHA256 || file.SHA256 != e.SHA256 || file.CommitSHA != e.CommitSHA || file.SnapshotID != e.SnapshotID || file.Path != e.Path || file.DataSourceID != e.DataSourceID {
			return fmt.Errorf("registered evidence provenance mismatch")
		}
		if e.Range.StartByte < 0 || e.Range.EndByte > len(file.RawContent) || e.Range.EndByte <= e.Range.StartByte {
			return fmt.Errorf("registered evidence range mismatch")
		}
		raw := file.RawContent[e.Range.StartByte:e.Range.EndByte]
		hash = sha256.Sum256(raw)
		if hex.EncodeToString(hash[:]) != e.TextSHA256 || e.Range.StartLine != 1+strings.Count(string(file.RawContent[:e.Range.StartByte]), "\n") || e.Range.EndLine != 1+strings.Count(string(file.RawContent[:e.Range.EndByte-1]), "\n") {
			return fmt.Errorf("registered evidence coordinates mismatch")
		}
		if err := enrichSFCReferences(ctx, tx, &file); err != nil {
			return err
		}
		file.Content = string(file.RawContent)
		return nil
	}); err != nil {
		return nil, err
	}
	return &file, nil
}

func (r *wikiPageRepository) WikiSourceApplicable(ctx context.Context, page *types.WikiPage) (bool, error) {
	if page.SourceProvenance == nil {
		return true, nil
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return false, err
	}
	provenance := page.SourceProvenance
	if provenance.SourceID == "" || provenance.TopicKind == "" || provenance.TopicKey == "" ||
		provenance.ApplicableSnapshotID == "" || page.ID == "" || page.Version <= 0 ||
		!r.db.Migrator().HasTable("source_wiki_page_contributions") {
		return false, nil
	}
	permission := source.SourcePermissionSQL(ctx, "c.source_id", "e->>'knowledge_id'")
	snapshotPermission := source.SnapshotSQL(ctx, "c.applicable_snapshot_id", "c.source_id", "e->>'knowledge_id'")
	allContributionsValid := `NOT EXISTS (
		SELECT 1 FROM source_wiki_page_contributions x
		WHERE x.page_id=? AND x.page_version=? AND (
			x.state<>'ready' OR x.applicable_snapshot_id='' OR x.target_snapshot_id<>x.applicable_snapshot_id OR
			COALESCE(x.contribution->>'has_unattributed_body','true')<>'false' OR
			jsonb_typeof(x.contribution->'source_provenance'->'evidence')<>'array' OR
			jsonb_array_length(CASE WHEN jsonb_typeof(x.contribution->'source_provenance'->'evidence')='array'
				THEN x.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END)=0 OR
			NOT EXISTS(SELECT 1 FROM source_publications xp JOIN source_snapshots xs ON xs.id=xp.snapshot_id
				AND xs.data_source_id=xp.data_source_id AND xs.tenant_id=xp.tenant_id AND xs.knowledge_base_id=xp.knowledge_base_id
				WHERE xp.data_source_id=x.source_id AND xp.tenant_id=? AND xp.knowledge_base_id=?
				AND xp.snapshot_id=x.applicable_snapshot_id AND xs.state='published' AND xs.manifest_complete=TRUE) OR
			EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(x.contribution->'source_provenance'->'evidence')='array'
				THEN x.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END) xe
				WHERE xe->>'data_source_id'<>x.source_id OR xe->>'knowledge_id' IS NULL OR
				NOT (` + source.SourcePermissionSQL(ctx, "x.source_id", "xe->>'knowledge_id'") + `) OR
				NOT (` + source.SnapshotSQL(ctx, "x.applicable_snapshot_id", "x.source_id", "xe->>'knowledge_id'") + `))
		)
	)`
	var applicable bool
	err := r.db.WithContext(ctx).Raw(`SELECT EXISTS(
		SELECT 1 FROM source_wiki_page_contributions c
		WHERE c.page_id=? AND c.page_version=?
		AND c.source_id=? AND c.topic_kind=? AND c.topic_key=?
		AND c.state='ready' AND c.applicable_snapshot_id=? AND c.target_snapshot_id=?
		AND c.contribution->>'has_unattributed_body'='false'
		AND `+allContributionsValid+`
		AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(c.contribution->'source_provenance'->'evidence')='array'
			THEN c.contribution->'source_provenance'->'evidence' ELSE '[]'::jsonb END) e
			WHERE e->>'data_source_id'<>c.source_id OR e->>'knowledge_id' IS NULL OR NOT (`+permission+`) OR NOT (`+snapshotPermission+`))
		AND EXISTS(SELECT 1 FROM source_publications sp JOIN source_snapshots ss ON ss.id=sp.snapshot_id
			AND ss.data_source_id=sp.data_source_id AND ss.tenant_id=sp.tenant_id AND ss.knowledge_base_id=sp.knowledge_base_id
			WHERE sp.data_source_id=c.source_id AND sp.tenant_id=? AND sp.knowledge_base_id=?
			AND sp.snapshot_id=c.applicable_snapshot_id AND ss.state='published' AND ss.manifest_complete=TRUE)
	)`, page.ID, page.Version, provenance.SourceID, provenance.TopicKind, provenance.TopicKey,
		provenance.ApplicableSnapshotID, provenance.ApplicableSnapshotID, page.ID, page.Version,
		page.TenantID, page.KnowledgeBaseID, page.TenantID, page.KnowledgeBaseID).Scan(&applicable).Error
	return applicable, err
}

func (r *wikiPageRepository) GetWikiPageIdentity(ctx context.Context, kbID, slug string) (*types.WikiPage, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	var page types.WikiPage
	if err := r.db.WithContext(ctx).Select("id,tenant_id,knowledge_base_id,slug,version").Where("knowledge_base_id=? AND slug=?", kbID, slug).First(&page).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, ErrWikiPageNotFound
		}
		return nil, err
	}
	return &page, nil
}
func (r *wikiPageRepository) GetWikiPageIdentityByID(ctx context.Context, id string) (*types.WikiPage, error) {
	var page types.WikiPage
	if err := r.db.WithContext(ctx).Select("id,tenant_id,knowledge_base_id,slug,version").Where("id=?", id).First(&page).Error; err != nil {
		return nil, err
	}
	return &page, nil
}

// Recheck after raw loading as explicit clear can remove a publication even
// when the old file did not contribute to the generic pinned question.
func ValidateSourceWikiEvidencePermission(ctx context.Context, db *gorm.DB, e types.SourceWikiEvidence) error {
	if err := source.ValidateReadScope(ctx); err != nil {
		return err
	}
	var allowed bool
	err := db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM source_files sf WHERE sf.id=? AND sf.data_source_id=? AND "+source.SourcePermissionSQL(ctx, "sf.data_source_id", "sf.id")+")", e.KnowledgeID, e.DataSourceID).Scan(&allowed).Error
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("registered Wiki evidence permission revoked or source cleared")
	}
	return nil
}

// OccupiedWikiFolders is an internal presence projection. The service supplies
// the authorized owner KB's tenant; archived and deleted pages do not occupy a
// container, matching CountPagesByFolder's ordinary Wiki behavior.
func (r *wikiPageRepository) OccupiedWikiFolders(ctx context.Context, kbID string, tenantID uint64) (map[string]bool, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	var folderIDs []string
	if err := r.db.WithContext(ctx).Model(&types.WikiPage{}).
		Where("knowledge_base_id=? AND tenant_id=? AND status<>?", kbID, tenantID, types.WikiPageStatusArchived).
		Distinct("folder_id").Pluck("folder_id", &folderIDs).Error; err != nil {
		return nil, err
	}
	occupied := make(map[string]bool, len(folderIDs))
	for _, id := range folderIDs {
		occupied[id] = true
	}
	return occupied, nil
}
