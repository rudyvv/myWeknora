package repository

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestRegisterSourceWikiEvidenceKeepsMixedLocalIDsAndRevisionOwners(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.SourceWikiEvidenceRef{}, &types.SourceWikiPageContribution{}))
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX test_wiki_owner_current
		ON source_wiki_evidence_refs(page_id,version,evidence_id,source_file_id,file_version_id,snapshot_id)
		WHERE revision_id IS NULL`).Error)
	require.NoError(t, db.Exec(`CREATE UNIQUE INDEX test_wiki_owner_revision
		ON source_wiki_evidence_refs(revision_id,evidence_id,source_file_id,file_version_id,snapshot_id)
		WHERE revision_id IS NOT NULL`).Error)

	pageID := uuid.NewString()
	evidenceA := mixedWikiEvidence("e001", "source-a", "file-a", "version-a", "snapshot-a")
	evidenceB := mixedWikiEvidence("e001", "source-b", "file-b", "version-b", "snapshot-b")
	provenanceA := mixedWikiProvenance(evidenceA, "source-a", "topic-a")
	provenanceB := mixedWikiProvenance(evidenceB, "source-b", "topic-b")
	page := &types.WikiPage{ID: pageID, Version: 7, SourceProvenance: &provenanceA}
	for _, provenance := range []types.SourceWikiProvenance{provenanceA, provenanceB} {
		contribution, err := json.Marshal(map[string]any{
			"has_unattributed_body": false,
			"topic_kind":            provenance.TopicKind,
			"topic_key":             provenance.TopicKey,
			"source_provenance":     provenance,
		})
		require.NoError(t, err)
		require.NoError(t, db.Create(&types.SourceWikiPageContribution{
			PageID: pageID, SourceID: provenance.SourceID, PageVersion: page.Version,
			TopicKind: provenance.TopicKind, TopicKey: provenance.TopicKey,
			ApplicableSnapshotID: provenance.ApplicableSnapshotID, TargetSnapshotID: provenance.ApplicableSnapshotID, State: "ready",
			Contribution: types.JSON(contribution),
		}).Error)
	}

	var before []types.SourceWikiPageContribution
	require.NoError(t, db.Where("page_id=?", pageID).Order("id").Find(&before).Error)
	require.Len(t, before, 2)
	require.NoError(t, registerSourceWikiEvidence(db, page, nil))
	assertMixedWikiOwnerRows(t, db, pageID, nil, []types.SourceWikiEvidence{evidenceA, evidenceB})

	// The primary A evidence and A's typed contribution are one exact owner;
	// B's equal local evidence ID remains a separate raw owner.
	var current []types.SourceWikiEvidenceRef
	require.NoError(t, db.Where("page_id=? AND revision_id IS NULL", pageID).Order("source_file_id").Find(&current).Error)
	require.Len(t, current, 2)
	require.Equal(t, "file-a", current[0].SourceFileID)
	require.Equal(t, "file-b", current[1].SourceFileID)
	require.Equal(t, "e001", current[0].EvidenceID)
	require.Equal(t, "e001", current[1].EvidenceID)

	// Registration is idempotent and leaves the original typed JSON untouched.
	require.NoError(t, registerSourceWikiEvidence(db, page, nil))
	var after []types.SourceWikiPageContribution
	require.NoError(t, db.Where("page_id=?", pageID).Order("id").Find(&after).Error)
	require.Equal(t, before, after)

	revisionID := uuid.NewString()
	revision := &types.WikiPageRevision{ID: revisionID, PageID: pageID, Version: page.Version, SourceProvenance: &provenanceA}
	old := &types.WikiPage{ID: pageID, Version: page.Version, SourceProvenance: &provenanceA}
	require.NoError(t, registerSourceWikiEvidence(db, old, revision))
	assertMixedWikiOwnerRows(t, db, pageID, &revisionID, []types.SourceWikiEvidence{evidenceA, evidenceB})
	var currentCount int64
	require.NoError(t, db.Model(&types.SourceWikiEvidenceRef{}).Where("page_id=? AND revision_id IS NULL", pageID).Count(&currentCount).Error)
	require.EqualValues(t, 2, currentCount)
}

func mixedWikiEvidence(id, sourceID, fileID, versionID, snapshotID string) types.SourceWikiEvidence {
	return types.SourceWikiEvidence{
		ID: id, KnowledgeID: fileID,
		SourceEvidence: types.SourceEvidence{
			DataSourceID: sourceID, SnapshotID: snapshotID, FileVersionID: versionID,
			CommitSHA: "commit-" + sourceID, Path: "README.md",
			Range: types.SourceRange{StartByte: 0, EndByte: 4, StartLine: 1, EndLine: 1},
		},
		SHA256: "file-hash-" + sourceID, TextSHA256: "text-hash-" + sourceID,
	}
}

func mixedWikiProvenance(evidence types.SourceWikiEvidence, sourceID, topicKey string) types.SourceWikiProvenance {
	return types.SourceWikiProvenance{
		SourceID: sourceID, TopicKind: "module", TopicKey: topicKey, State: "ready",
		ApplicableSnapshotID: evidence.SnapshotID, Evidence: []types.SourceWikiEvidence{evidence},
	}
}

func assertMixedWikiOwnerRows(t *testing.T, db *gorm.DB, pageID string, revisionID *string, expected []types.SourceWikiEvidence) {
	t.Helper()
	query := db.Where("page_id=? AND version=?", pageID, 7)
	if revisionID == nil {
		query = query.Where("revision_id IS NULL")
	} else {
		query = query.Where("revision_id=?", *revisionID)
	}
	var refs []types.SourceWikiEvidenceRef
	require.NoError(t, query.Order("source_file_id").Find(&refs).Error)
	require.Len(t, refs, len(expected))
	for i, evidence := range expected {
		require.Equal(t, evidence.ID, refs[i].EvidenceID)
		require.Equal(t, evidence.KnowledgeID, refs[i].SourceFileID)
		require.Equal(t, evidence.FileVersionID, refs[i].FileVersionID)
		require.Equal(t, evidence.SnapshotID, refs[i].SnapshotID)
		require.Equal(t, evidence.Path, refs[i].Path)
		require.Equal(t, evidence.CommitSHA, refs[i].CommitSHA)
	}
}

func TestReadWikiPageDBMixedEvidenceOwnerScopes(t *testing.T) {
	db, _ := newSourceWikiImpactLoaderMock(t)
	ctx := context.Background()
	buildSQL := func(table string, contributions bool) string {
		t.Helper()
		var result []struct{ ID string }
		query := readWikiPageDB(ctx, db.Session(&gorm.Session{DryRun: true}), true, contributions, table).
			Table(table).Select(table + ".id").Find(&result)
		require.NoError(t, query.Error)
		return query.Statement.SQL.String()
	}

	currentSQL := buildSQL("wiki_pages", true)
	require.Contains(t, currentSQL, "sc.page_id=wiki_pages.id AND sc.revision_id IS NULL AND sc.page_version=wiki_pages.version")
	require.Contains(t, currentSQL, "mc.page_id=wiki_pages.id AND mc.revision_id IS NULL AND mc.page_version=wiki_pages.version")
	require.Contains(t, currentSQL, "mwr.revision_id IS NOT DISTINCT FROM mc.revision_id")
	require.Contains(t, currentSQL, "mwr.evidence_id=mce->>'id'")
	require.Contains(t, currentSQL, "mwr.file_version_id=mce->>'file_version_id'")
	require.Contains(t, currentSQL, "mwr.snapshot_id=mce->>'snapshot_id'")
	require.Contains(t, currentSQL, "sc.source_id=sf.data_source_id")
	require.Contains(t, currentSQL, "sce->>'knowledge_id'=wk.id")

	revisionSQL := buildSQL("wiki_page_revisions", true)
	require.Contains(t, revisionSQL, "sc.page_id=wiki_page_revisions.page_id AND sc.revision_id=wiki_page_revisions.id AND sc.page_version=wiki_page_revisions.version")
	require.Contains(t, revisionSQL, "mc.page_id=wiki_page_revisions.page_id AND mc.revision_id=wiki_page_revisions.id AND mc.page_version=wiki_page_revisions.version")

	legacySQL := buildSQL("wiki_pages", false)
	require.NotContains(t, legacySQL, "source_wiki_page_contributions")
	require.Contains(t, legacySQL, "NOT ((SELECT count(DISTINCT")
	require.True(t, strings.Contains(legacySQL, "we->>'knowledge_id'=wk.id"))
}
