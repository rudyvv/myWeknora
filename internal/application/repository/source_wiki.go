package repository

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Every technical projection uses the same predicate before ranking/pagination,
// including directory entries, neighbour summaries and historical bodies.
func (r *wikiPageRepository) readDB(ctx context.Context, table string) *gorm.DB {
	db := r.db.WithContext(ctx)
	if !r.sourceWiki {
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
	evidence := `NOT EXISTS (SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) we
 WHERE NOT EXISTS(SELECT 1 FROM source_wiki_evidence_refs wr
 JOIN source_file_versions sv ON sv.id=wr.file_version_id AND sv.source_file_id=wr.source_file_id AND sv.snapshot_id=wr.snapshot_id
 JOIN source_snapshots ss ON ss.id=wr.snapshot_id AND ss.state='published'
 JOIN source_files sf ON sf.id=wr.source_file_id AND sf.tenant_id=` + table + `.tenant_id AND sf.knowledge_base_id=` + table + `.knowledge_base_id AND sf.data_source_id=ss.data_source_id
 WHERE ` + owner + ` AND wr.evidence_id=we->>'id' AND wr.source_file_id=we->>'knowledge_id'
 AND wr.file_version_id=we->>'file_version_id' AND wr.snapshot_id=we->>'snapshot_id'
 AND sf.data_source_id=we->>'data_source_id' AND sv.sha256=we->>'sha256' AND ss.commit_sha=we->>'commit_sha'
 AND encode(sha256(sv.content),'hex')=sv.sha256
 AND encode(sha256(substring(sv.content from (we->'range'->>'start_byte')::int+1 for GREATEST(0,(we->'range'->>'end_byte')::int-(we->'range'->>'start_byte')::int))),'hex')=we->>'text_sha256'
 AND (we->'range'->>'start_byte')::int>=0 AND (we->'range'->>'end_byte')::int<=octet_length(sv.content)
 AND (we->'range'->>'end_byte')::int>(we->'range'->>'start_byte')::int
 AND EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(` + refs + `, '[]'::jsonb)) fr WHERE split_part(fr,'|',1)=sf.id)))`
	allSources := `NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(` + refs + `) fr
 LEFT JOIN knowledges wk ON wk.id=split_part(fr,'|',1)
 LEFT JOIN source_files sf ON sf.id=wk.id
 LEFT JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.tenant_id=wk.tenant_id AND ds.knowledge_base_id=wk.knowledge_base_id
 WHERE NOT COALESCE(wk.id IS NOT NULL AND wk.tenant_id=` + table + `.tenant_id AND wk.knowledge_base_id=` + table + `.knowledge_base_id AND wk.deleted_at IS NULL
 AND ((wk.type='source' AND sf.id IS NOT NULL AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source'
 AND ` + source.SourcePermissionSQL(ctx, "sf.data_source_id", "wk.id") + `
 AND EXISTS(SELECT 1 FROM jsonb_array_elements(` + evidenceArray + `) we WHERE we->>'knowledge_id'=wk.id))
 OR (wk.type IS DISTINCT FROM 'source' AND ` + source.OrdinaryKnowledgeSQL(ctx, "wk.id") + `)),FALSE))`
	condition := `jsonb_typeof(` + evidenceArray + `)='array' AND jsonb_array_length(` + evidenceArray + `)>0
 AND jsonb_array_length(COALESCE(` + refs + `,'[]'::jsonb))>0 AND ` + evidence + " AND " + allSources
	if source.IsWikiAnswerRead(ctx) && table == "wiki_pages" {
		condition += " AND " + p + "->>'state'='ready' AND " + table + ".status='published' AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(" + evidenceArray + ") ae WHERE NOT (" + source.SnapshotSQL(ctx, p+"->>'applicable_snapshot_id'", p+"->>'source_id'", "ae->>'knowledge_id'") + "))"
	}
	// Ordinary wiki rows that contain a source contribution without complete typed
	// provenance fail closed instead of falling back to the legacy intersect rule.
	ordinary := p + " IS NULL AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(COALESCE(" + refs + ",'[]'::jsonb)) fr JOIN source_files sf ON sf.id=split_part(fr,'|',1))"

	return db.Where("(" + ordinary + ") OR (" + condition + ")")
}

func registerSourceWikiEvidence(db *gorm.DB, page *types.WikiPage, revision *types.WikiPageRevision) error {
	if page.SourceProvenance == nil {
		return nil
	}
	var revisionID *string
	if revision != nil {
		revisionID = &revision.ID
	}
	for _, e := range page.SourceProvenance.Evidence {
		ref := types.SourceWikiEvidenceRef{ID: uuid.NewString(), PageID: page.ID, RevisionID: revisionID, Version: page.Version, EvidenceID: e.ID, SourceFileID: e.KnowledgeID, FileVersionID: e.FileVersionID, SnapshotID: e.SnapshotID}
		if err := db.Create(&ref).Error; err != nil {
			return err
		}
	}
	return nil
}

// ReadSourceWikiEvidence requires a registered body owner and exact immutable
// version. Callers first load that complete page/revision through readDB.
func ReadSourceWikiEvidence(ctx context.Context, db *gorm.DB, pageID string, revisionID *string, version int, e types.SourceWikiEvidence) (*types.SourceFileView, error) {
	if err := source.ValidateReadScope(ctx); err != nil {
		return nil, err
	}
	q := db.WithContext(ctx).Table("source_wiki_evidence_refs wr").
		Select("sf.id AS knowledge_id, sf.data_source_id, ss.id AS snapshot_id, ss.project_id, ss.commit_sha, ss.repository_url, sv.id AS file_version_id, sv.sha256, sv.encoding, sv.quality, sv.parser_version, sv.content AS raw_content, sv.symbols, octet_length(sv.content) AS file_size, sm.path").
		Joins("JOIN source_file_versions sv ON sv.id=wr.file_version_id AND sv.source_file_id=wr.source_file_id AND sv.snapshot_id=wr.snapshot_id").
		Joins("JOIN source_files sf ON sf.id=wr.source_file_id").
		Joins("JOIN source_snapshots ss ON ss.id=wr.snapshot_id AND ss.data_source_id=sf.data_source_id AND ss.tenant_id=sf.tenant_id AND ss.knowledge_base_id=sf.knowledge_base_id AND ss.state='published'").
		Joins("JOIN source_snapshot_members sm ON sm.snapshot_id=ss.id AND sm.source_file_id=sf.id AND sm.file_version_id=sv.id AND sm.status='parsed'").
		Joins("JOIN data_sources ds ON ds.id=sf.data_source_id AND ds.deleted_at IS NULL AND ds.config->'settings'->>'content_mode'='source'").
		Where("wr.page_id=? AND wr.version=? AND wr.evidence_id=? AND sv.id=? AND sf.id=?", pageID, version, e.ID, e.FileVersionID, e.KnowledgeID).
		Where(source.SourcePermissionSQL(ctx, "sf.data_source_id", "sf.id"))
	if revisionID == nil {
		q = q.Where("wr.revision_id IS NULL")
	} else {
		q = q.Where("wr.revision_id=?", *revisionID)
	}
	var file types.SourceFileView
	if err := q.Take(&file).Error; err != nil {
		return nil, err
	}
	hash := sha256.Sum256(file.RawContent)
	if hex.EncodeToString(hash[:]) != file.SHA256 || file.SHA256 != e.SHA256 || file.CommitSHA != e.CommitSHA || file.SnapshotID != e.SnapshotID || file.Path != e.Path || file.DataSourceID != e.DataSourceID {
		return nil, fmt.Errorf("registered evidence provenance mismatch")
	}
	if e.Range.StartByte < 0 || e.Range.EndByte > len(file.RawContent) || e.Range.EndByte <= e.Range.StartByte {
		return nil, fmt.Errorf("registered evidence range mismatch")
	}
	raw := file.RawContent[e.Range.StartByte:e.Range.EndByte]
	hash = sha256.Sum256(raw)
	if hex.EncodeToString(hash[:]) != e.TextSHA256 || e.Range.StartLine != 1+strings.Count(string(file.RawContent[:e.Range.StartByte]), "\n") || e.Range.EndLine != 1+strings.Count(string(file.RawContent[:e.Range.EndByte-1]), "\n") {
		return nil, fmt.Errorf("registered evidence coordinates mismatch")
	}
	file.Content = string(file.RawContent)
	return &file, nil
}

func (r *wikiPageRepository) WikiSourceApplicable(ctx context.Context, page *types.WikiPage) (bool, error) {
	if page.SourceProvenance == nil {
		return true, nil
	}
	if err := source.ValidateReadScope(ctx); err != nil {
		return false, err
	}
	var applicable bool
	predicate := source.SnapshotSQL(ctx, "ss.id", "ss.data_source_id", "sm.source_file_id")
	err := r.db.WithContext(ctx).Raw("SELECT EXISTS(SELECT 1 FROM source_snapshots ss JOIN source_snapshot_members sm ON sm.snapshot_id=ss.id WHERE ss.id=? AND ss.data_source_id=? AND sm.status='parsed' AND "+predicate+")", page.SourceProvenance.ApplicableSnapshotID, page.SourceProvenance.SourceID).Scan(&applicable).Error
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
