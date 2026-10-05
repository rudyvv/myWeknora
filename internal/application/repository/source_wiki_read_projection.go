package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

const (
	sourceWikiReadProjectionMaxPages        = 1024
	sourceWikiReadProjectionMaxBytes  int64 = 16 << 20
	sourceWikiReadProjectionMaxOwners       = 32768
)

var (
	ErrSourceWikiReadProjectionCapacity = errors.New("source Wiki answer capacity_exceeded: immutable read projection exceeds configured limits")
	ErrSourceWikiReadProjectionMissing  = errors.New("source Wiki read projection is unavailable")
)

// captureSourceWikiReadProjection freezes every source-backed Wiki card that
// passes the normal answer predicate in the same SQL statement that accounts
// for capacity, inserts the projection, and pins its exact raw evidence owners.
func captureSourceWikiReadProjection(tx *gorm.DB, ctx context.Context, leaseID string) error {
	for _, table := range []string{
		"source_read_wiki_scopes", "source_wiki_evidence_refs", "source_read_wiki_evidence_refs",
		"source_wiki_page_contributions", "source_read_wiki_projection_captures", "source_read_wiki_page_projections",
	} {
		if !tx.Migrator().HasTable(table) {
			return fmt.Errorf("source Wiki read projection requires migration 000119 and its evidence schema (%s missing)", table)
		}
	}
	if _, err := uuid.Parse(leaseID); err != nil {
		return fmt.Errorf("invalid source read lease for Wiki projection")
	}

	projectionCtx, release := source.WithReadScope(ctx, types.SourceReadLease{ID: leaseID, HasSources: true}, nil, func() {})
	defer release()
	projectionCtx = source.WithWikiAnswerRead(projectionCtx)

	// Reuse the public answer predicate, but compile it as a dry-run subquery so
	// candidate discovery remains inside the atomic INSERT statement below.
	dry := readWikiPageCaptureDB(projectionCtx, tx.Session(&gorm.Session{DryRun: true}), true, true, "wiki_pages").
		Model(&types.WikiPage{}).
		Select("wiki_pages.id").
		Where("wiki_pages.source_provenance IS NOT NULL")
	var candidates []types.WikiPage
	if err := dry.Find(&candidates).Error; err != nil {
		return fmt.Errorf("build source Wiki read projection query: %w", err)
	}
	if len(dry.Statement.Vars) != 0 {
		return fmt.Errorf("source Wiki read projection query unexpectedly contains unbound values")
	}
	eligibleSQL := strings.TrimSuffix(dry.Statement.SQL.String(), ";")
	if eligibleSQL == "" {
		return fmt.Errorf("source Wiki read projection query is empty")
	}
	lease := "'" + leaseID + "'" // UUID-parsed above; safe to embed in repository SQL.

	query := fmt.Sprintf(`WITH eligible AS MATERIALIZED (
		%s
	), candidates AS MATERIALIZED (
		SELECT wp.id AS page_id, wp.version AS page_version, to_jsonb(wp) AS page_snapshot,
		COALESCE((SELECT jsonb_agg(to_jsonb(c) ORDER BY c.id)
			FROM source_wiki_page_contributions c
			WHERE c.page_id=wp.id AND c.revision_id IS NULL AND c.page_version=wp.version), '[]'::jsonb) AS contributions
		FROM eligible e JOIN wiki_pages wp ON wp.id=e.id
	), owner_candidates AS MATERIALIZED (
		SELECT c.page_id,c.page_version,wr.revision_id,wr.evidence_id,wr.source_file_id,wr.file_version_id,wr.snapshot_id,wr.path,wr.commit_sha
		FROM candidates c JOIN source_wiki_evidence_refs wr
			ON wr.page_id=c.page_id AND wr.revision_id IS NULL AND wr.version=c.page_version
		WHERE EXISTS(SELECT 1 FROM jsonb_array_elements(CASE WHEN jsonb_typeof(c.page_snapshot->'source_provenance'->'evidence')='array'
				THEN c.page_snapshot->'source_provenance'->'evidence' ELSE '[]'::jsonb END) e
			WHERE e->>'id'=wr.evidence_id AND e->>'knowledge_id'=wr.source_file_id AND e->>'file_version_id'=wr.file_version_id
				AND e->>'snapshot_id'=wr.snapshot_id AND e->>'path'=wr.path AND e->>'commit_sha'=wr.commit_sha)
			OR EXISTS(SELECT 1 FROM jsonb_array_elements(c.contributions) contribution
				CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(contribution->'contribution'->'source_provenance'->'evidence')='array'
					THEN contribution->'contribution'->'source_provenance'->'evidence' ELSE '[]'::jsonb END) e
				WHERE e->>'id'=wr.evidence_id AND e->>'knowledge_id'=wr.source_file_id AND e->>'file_version_id'=wr.file_version_id
					AND e->>'snapshot_id'=wr.snapshot_id AND e->>'path'=wr.path AND e->>'commit_sha'=wr.commit_sha)
	), metrics AS MATERIALIZED (
		SELECT count(*)::bigint AS page_count,
			COALESCE(sum(octet_length(convert_to(page_snapshot::text, 'UTF8')) + octet_length(convert_to(contributions::text, 'UTF8'))),0)::bigint AS projection_bytes,
			(SELECT count(*)::bigint FROM owner_candidates) AS evidence_owner_count
		FROM candidates
	), capture_write AS (
		INSERT INTO source_read_wiki_projection_captures(lease_id,status,page_count,projection_bytes,evidence_owner_count)
		SELECT %s,
			CASE WHEN page_count>%d OR projection_bytes>%d OR evidence_owner_count>%d THEN 'capacity_exceeded' ELSE 'captured' END,
			page_count,projection_bytes,evidence_owner_count
		FROM metrics
		ON CONFLICT (lease_id) DO NOTHING
		RETURNING status,page_count,projection_bytes,evidence_owner_count
	), page_write AS (
		INSERT INTO source_read_wiki_page_projections(lease_id,page_id,page_version,page_snapshot,contributions)
		SELECT %s,c.page_id,c.page_version,c.page_snapshot,c.contributions
		FROM candidates c CROSS JOIN capture_write cw
		WHERE cw.status='captured'
		RETURNING page_id,page_version
	), owner_write AS (
		INSERT INTO source_read_wiki_evidence_refs
			(lease_id,page_id,revision_id,version,evidence_id,source_file_id,file_version_id,snapshot_id,path,commit_sha)
		SELECT %s,oc.page_id,oc.revision_id,oc.page_version,oc.evidence_id,oc.source_file_id,oc.file_version_id,oc.snapshot_id,oc.path,oc.commit_sha
		FROM page_write pw JOIN owner_candidates oc ON oc.page_id=pw.page_id AND oc.page_version=pw.page_version
		ON CONFLICT DO NOTHING
		RETURNING 1
	)
	SELECT status,page_count,projection_bytes,evidence_owner_count,
		(SELECT count(*) FROM page_write),(SELECT count(*) FROM owner_write)
	FROM capture_write`, eligibleSQL, lease,
		sourceWikiReadProjectionMaxPages, sourceWikiReadProjectionMaxBytes, sourceWikiReadProjectionMaxOwners,
		lease, lease)

	var status string
	var pageCount, projectionBytes, ownerCount, writtenPages, pinnedOwners int64
	if err := tx.Raw(query).Row().Scan(&status, &pageCount, &projectionBytes, &ownerCount, &writtenPages, &pinnedOwners); err != nil {
		return fmt.Errorf("capture source Wiki read projection: %w", err)
	}
	if status == "captured" && (pageCount != writtenPages || ownerCount != pinnedOwners) {
		return fmt.Errorf("source Wiki read projection owner capture was incomplete")
	}
	return nil
}

// CheckSourceWikiReadProjection reports capacity as a Wiki-only failure. The
// caller's source RAG lease remains valid and can continue independently.
func (r *wikiPageRepository) CheckSourceWikiReadProjection(ctx context.Context, leaseID string) error {
	if err := source.ValidateReadScope(ctx); err != nil {
		return err
	}
	ctxLease, ok := source.ReadLeaseID(ctx)
	if !ok || ctxLease != leaseID {
		return ErrSourceWikiReadProjectionMissing
	}
	if !r.sourceWiki {
		return nil
	}
	var row struct{ Status string }
	err := r.db.WithContext(ctx).Table("source_read_wiki_projection_captures AS capture").
		Select("capture.status").
		Joins("JOIN source_read_leases lease ON lease.id=capture.lease_id AND lease.expires_at>now()").
		Where("capture.lease_id=?", leaseID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrSourceWikiReadProjectionMissing
	}
	if err != nil {
		return err
	}
	if row.Status == "capacity_exceeded" {
		return ErrSourceWikiReadProjectionCapacity
	}
	if row.Status != "captured" {
		return ErrSourceWikiReadProjectionMissing
	}
	return nil
}

// sourceWikiAnswerProjectionDB serves fixed source-backed card versions while
// preserving the live ordinary-Wiki branch. The projection branch rechecks
// current grants, source usability, scope and immutable raw evidence owners.
func sourceWikiAnswerProjectionDB(ctx context.Context, base *gorm.DB) *gorm.DB {
	if err := source.ValidateReadScope(ctx); err != nil {
		base.AddError(err)
		return base
	}
	leaseID, ok := source.ReadLeaseID(ctx)
	if !ok {
		base.AddError(ErrSourceWikiReadProjectionMissing)
		return base
	}
	if _, err := uuid.Parse(leaseID); err != nil {
		base.AddError(ErrSourceWikiReadProjectionMissing)
		return base
	}
	lease := "'" + leaseID + "'"
	ordinary := wikiPageOrdinaryReadCondition("wiki_pages")
	ordinaryDB := base.Session(&gorm.Session{DryRun: true}).
		Table("wiki_pages").
		Where(ordinary).
		Select("wiki_pages.*")
	var ordinaryRows []types.WikiPage
	if err := ordinaryDB.Find(&ordinaryRows).Error; err != nil {
		base.AddError(err)
		return base
	}
	if len(ordinaryDB.Statement.Vars) != 0 || ordinaryDB.Statement.SQL.String() == "" {
		base.AddError(ErrSourceWikiReadProjectionMissing)
		return base
	}
	ordinarySQL := strings.TrimSuffix(ordinaryDB.Statement.SQL.String(), ";")
	usable := sourceWikiAnswerProjectionUsableSQL(ctx, "projection")
	query := fmt.Sprintf(`(
		SELECT (jsonb_populate_record(NULL::wiki_pages, projection.page_snapshot)).*
		FROM source_read_wiki_page_projections projection
		JOIN source_read_wiki_projection_captures capture ON capture.lease_id=projection.lease_id AND capture.status='captured'
		JOIN source_read_leases lease ON lease.id=projection.lease_id AND lease.expires_at>now()
		WHERE projection.lease_id=%s AND %s
		UNION ALL
		%s
	) AS wiki_pages`, lease, usable, ordinarySQL)
	return base.WithContext(ctx).Table(query)
}

func sourceWikiAnswerProjectionUsableSQL(ctx context.Context, alias string) string {
	page := alias + ".page_snapshot"
	refs := "(CASE WHEN jsonb_typeof(" + page + "->'source_refs')='array' THEN " + page + "->'source_refs' ELSE '[]'::jsonb END)"
	provenance := page + "->'source_provenance'"
	primaryEvidence := "(CASE WHEN jsonb_typeof(" + provenance + "->'evidence')='array' THEN " + provenance + "->'evidence' ELSE '[]'::jsonb END)"
	contributions := alias + ".contributions"
	leaseID, _ := source.ReadLeaseID(ctx)
	lease := "'" + leaseID + "'"
	primaryOwner := sourceWikiProjectionEvidenceOwnerSQL(ctx, alias, "primary_evidence", provenance+"->>'source_id'", provenance+"->>'applicable_snapshot_id'")
	primaryValid := `jsonb_typeof(` + primaryEvidence + `)='array' AND jsonb_array_length(` + primaryEvidence + `)>0
		AND ` + provenance + `->>'state'='ready'
		AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(` + primaryEvidence + `) primary_evidence
			WHERE primary_evidence->>'data_source_id' IS DISTINCT FROM ` + provenance + `->>'source_id'
			OR NOT (` + primaryOwner + `))`
	contributionOwner := sourceWikiProjectionEvidenceOwnerSQL(ctx, alias, "contribution_evidence", "contribution->>'source_id'", "contribution->>'applicable_snapshot_id'")
	contributionsValid := `jsonb_typeof(` + contributions + `)='array' AND jsonb_array_length(` + contributions + `)>0
		AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(` + contributions + `) contribution
			CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(contribution->'contribution'->'source_provenance'->'evidence')='array'
				THEN contribution->'contribution'->'source_provenance'->'evidence' ELSE '[]'::jsonb END) contribution_evidence
			WHERE contribution->>'source_id' IS DISTINCT FROM contribution_evidence->>'data_source_id'
			OR NOT (` + contributionOwner + `))` +
		` AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(` + contributions + `) contribution
			WHERE contribution->>'state' IS DISTINCT FROM 'ready'
			OR contribution->>'target_snapshot_id' IS DISTINCT FROM contribution->>'applicable_snapshot_id'
			OR contribution->'contribution'->>'has_unattributed_body' IS DISTINCT FROM 'false'
			OR contribution->'contribution'->>'topic_kind' IS DISTINCT FROM contribution->>'topic_kind'
			OR contribution->'contribution'->>'topic_key' IS DISTINCT FROM contribution->>'topic_key'
			OR contribution->'contribution'->'source_provenance'->>'source_id' IS DISTINCT FROM contribution->>'source_id'
			OR contribution->'contribution'->'source_provenance'->>'topic_kind' IS DISTINCT FROM contribution->>'topic_kind'
			OR contribution->'contribution'->'source_provenance'->>'topic_key' IS DISTINCT FROM contribution->>'topic_key'
			OR contribution->'contribution'->'source_provenance'->>'applicable_snapshot_id' IS DISTINCT FROM contribution->>'applicable_snapshot_id'
			OR contribution->'contribution'->'source_provenance'->>'state' IS DISTINCT FROM 'ready'
			OR jsonb_typeof(contribution->'contribution'->'source_provenance'->'evidence') IS DISTINCT FROM 'array'
			OR jsonb_array_length(CASE WHEN jsonb_typeof(contribution->'contribution'->'source_provenance'->'evidence')='array'
				THEN contribution->'contribution'->'source_provenance'->'evidence' ELSE '[]'::jsonb END)=0)`
	allRefs := `NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(` + refs + `) ref
		LEFT JOIN knowledges wk ON wk.id=split_part(ref,'|',1)
		LEFT JOIN source_files sf ON sf.id=wk.id
		LEFT JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.tenant_id=sf.tenant_id AND ds.knowledge_base_id=sf.knowledge_base_id
		WHERE NOT COALESCE(wk.id IS NOT NULL AND wk.tenant_id=(` + page + `->>'tenant_id')::bigint
			AND wk.knowledge_base_id=` + page + `->>'knowledge_base_id' AND wk.deleted_at IS NULL
			AND ((wk.type='source' AND sf.id IS NOT NULL AND ds.deleted_at IS NULL AND ds.source_query_enabled IS TRUE
				AND ds.config->'settings'->>'content_mode'='source'
				AND ` + source.SourcePermissionSQL(ctx, "sf.data_source_id", "sf.id") + `
				AND (EXISTS(SELECT 1 FROM jsonb_array_elements(` + primaryEvidence + `) se WHERE se->>'knowledge_id'=wk.id)
					OR EXISTS(SELECT 1 FROM jsonb_array_elements(` + contributions + `) sc
						CROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(sc->'contribution'->'source_provenance'->'evidence')='array'
							THEN sc->'contribution'->'source_provenance'->'evidence' ELSE '[]'::jsonb END) sce
						WHERE sc->>'source_id'=sf.data_source_id AND sce->>'knowledge_id'=wk.id)))
			OR (wk.type IS DISTINCT FROM 'source' AND ` + source.OrdinaryKnowledgeSQL(ctx, "wk.id") + `)),FALSE))`
	return alias + `.lease_id=` + lease + ` AND ` + alias + `.page_id=` + page + `->>'id'
		AND ` + alias + `.page_version=(` + page + `->>'version')::integer
		AND ` + page + `->>'status'='published' AND ` + primaryValid + ` AND ` + contributionsValid + ` AND ` + allRefs
}

func sourceWikiProjectionEvidenceOwnerSQL(ctx context.Context, projection, evidence, sourceID, applicableSnapshotID string) string {
	leaseID, _ := source.ReadLeaseID(ctx)
	lease := "'" + leaseID + "'"
	item := evidence
	start := "(" + item + "->'range'->>'start_byte')::int"
	end := "(" + item + "->'range'->>'end_byte')::int"
	return `EXISTS(SELECT 1 FROM source_read_wiki_evidence_refs wr
		JOIN source_file_versions sv ON sv.id=wr.file_version_id AND sv.source_file_id=wr.source_file_id AND sv.snapshot_id=wr.snapshot_id
		JOIN source_files sf ON sf.id=wr.source_file_id
		JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.tenant_id=sf.tenant_id AND ds.knowledge_base_id=sf.knowledge_base_id
		WHERE wr.lease_id=` + lease + ` AND wr.page_id=` + projection + `.page_id AND wr.revision_id IS NULL AND wr.version=` + projection + `.page_version
			AND wr.evidence_id=` + item + `->>'id' AND wr.source_file_id=` + item + `->>'knowledge_id'
			AND wr.file_version_id=` + item + `->>'file_version_id' AND wr.snapshot_id=` + item + `->>'snapshot_id'
			AND wr.path=` + item + `->>'path' AND wr.commit_sha=` + item + `->>'commit_sha'
			AND sf.deleted_at IS NULL AND sf.tenant_id=(` + projection + `.page_snapshot->>'tenant_id')::bigint
			AND sf.knowledge_base_id=` + projection + `.page_snapshot->>'knowledge_base_id'
			AND sf.data_source_id=` + sourceID + ` AND ds.deleted_at IS NULL AND ds.source_query_enabled IS TRUE
			AND ds.config->'settings'->>'content_mode'='source'
			AND sv.sha256=` + item + `->>'sha256' AND encode(sha256(sv.content),'hex')=sv.sha256
			AND ` + start + `>=0 AND ` + end + `<=octet_length(sv.content) AND ` + end + `>` + start + `
			AND encode(sha256(substring(sv.content FROM ` + start + `+1 FOR GREATEST(0,` + end + `-` + start + `))),'hex')=` + item + `->>'text_sha256'
			AND ` + source.SourcePermissionSQL(ctx, "sf.data_source_id", "sf.id") + `
			AND ` + source.SnapshotSQL(ctx, applicableSnapshotID, sourceID, item+"->>'knowledge_id'") + `)`
}
