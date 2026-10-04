//go:build integration

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestSourceCleanupWorkerRetriesAndWithdrawsOnlySelectedSource(t *testing.T) {
	f := newJavaSourceFixture(t)
	ensureT19LifecycleSchema(t, f)
	syncSourceFixture(t, f)

	targetFile, targetSnapshot, targetVersion := sourceCleanupFixtureEvidenceRows(t, f.db, f.ds.ID)
	var targetChunkIDs []string
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", targetSnapshot.ID).Pluck("chunk_id", &targetChunkIDs).Error)
	otherSourceID := "00000000-0000-4000-8000-000000000002"
	otherSnapshotID, otherFileID, otherVersionID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	otherCommit := strings.Repeat("b", 40)
	otherContent := []byte("class Other { String otherSource() { return \"B\"; } }\n")
	otherDigest := sourceCleanupSHA256(otherContent)
	otherSource := &types.DataSource{ID: otherSourceID, TenantID: f.ds.TenantID, KnowledgeBaseID: f.ds.KnowledgeBaseID,
		Name: "Other source", Type: "gitlab", Status: types.DataSourceStatusActive,
		Config: types.JSON(`{"type":"gitlab","settings":{"content_mode":"source"}}`)}
	require.NoError(t, f.db.Create(otherSource).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO source_snapshots
		(id,tenant_id,knowledge_base_id,data_source_id,sync_log_id,project_id,commit_sha,repository_url,rules_version,
		 state,manifest_complete,manifest_digest,member_count,published_at)
		VALUES(?,?,?,?,?,?,?,?,?,'published',TRUE,?,1,?)`, otherSnapshotID, f.ds.TenantID, f.ds.KnowledgeBaseID,
		otherSourceID, uuid.NewString(), "other-project", otherCommit, "https://example.invalid/other.git", "rules-v1",
		otherDigest, time.Now().UTC()).Error)
	require.NoError(t, f.db.Create(&types.SourceFile{ID: otherFileID, TenantID: f.ds.TenantID,
		KnowledgeBaseID: f.ds.KnowledgeBaseID, DataSourceID: otherSourceID, Path: "src/Other.java"}).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO source_file_versions
		(id,source_file_id,snapshot_id,blob_sha,sha256,content,encoding,parser_version,quality,symbols,facts,diagnostics)
		VALUES(?,?,?,?,?,?,'UTF-8','fixture-parser','accepted','[]'::jsonb,'[]'::jsonb,'[]'::jsonb)`,
		otherVersionID, otherFileID, otherSnapshotID, otherDigest, otherDigest, otherContent).Error)
	require.NoError(t, f.db.Create(&types.SourcePublication{DataSourceID: otherSourceID, SnapshotID: otherSnapshotID,
		TenantID: f.ds.TenantID, KnowledgeBaseID: f.ds.KnowledgeBaseID}).Error)

	targetEntry := sourceCleanupFixtureEntry(f.ds.ID, targetSnapshot.ID, targetVersion.ID, targetFile.ID,
		targetFile.Path, "Source A", "A summary", "Source A body.", "a")
	otherEntry := sourceCleanupFixtureEntry(otherSourceID, otherSnapshotID, otherVersionID, otherFileID,
		"src/Other.java", "Source B", "B summary", "Source B body.", "b")
	mixedPageID := "00000000-0000-4000-8000-000000000001"
	insertSourceCleanupFixtureWiki(t, f.db, mixedPageID, 2, []types.SourceWikiWithdrawalEntry{targetEntry, otherEntry}, true)
	aOnlyPageID := "ffffffff-ffff-4fff-8fff-ffffffffffff"
	insertSourceCleanupFixtureWiki(t, f.db, aOnlyPageID, 1, []types.SourceWikiWithdrawalEntry{targetEntry}, false)
	bOnlyPageID := "eeeeeeee-eeee-4eee-8eee-eeeeeeeeeeee"
	insertSourceCleanupFixtureWiki(t, f.db, bOnlyPageID, 1, []types.SourceWikiWithdrawalEntry{otherEntry}, false)

	leaseID := uuid.NewString()
	require.NoError(t, f.db.Exec("INSERT INTO source_read_leases(id,expires_at) VALUES(?,?)", leaseID, time.Now().UTC().Add(time.Hour)).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO source_read_scopes(lease_id,snapshot_id,data_source_id,knowledge_base_id,tenant_id,knowledge_ids,tag_ids)
		VALUES(?,?,?,?,?,'[]'::jsonb,'[]'::jsonb),(?,?,?,?,?,'[]'::jsonb,'[]'::jsonb)`,
		leaseID, targetSnapshot.ID, f.ds.ID, f.ds.KnowledgeBaseID, f.ds.TenantID,
		leaseID, otherSnapshotID, otherSourceID, f.ds.KnowledgeBaseID, f.ds.TenantID).Error)
	sourceIDsJSON, err := json.Marshal([]string{f.ds.ID, otherSourceID})
	require.NoError(t, err)
	require.NoError(t, f.db.Exec(`INSERT INTO source_read_wiki_scopes(lease_id,knowledge_base_id,tenant_id,source_ids,knowledge_ids,tag_ids)
		VALUES(?,?,?,?::jsonb,'[]'::jsonb,'[]'::jsonb)`,
		leaseID, f.ds.KnowledgeBaseID, f.ds.TenantID, string(sourceIDsJSON)).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO source_gitlab_webhook_configs
		(data_source_id,tenant_id,enabled,secret_ciphertext,last_event_id,last_received_at)
		VALUES(?,?,TRUE,'encrypted-hook-secret','event-before-cleanup',now())`, f.ds.ID, f.ds.TenantID).Error)
	require.NoError(t, f.db.Exec(`INSERT INTO source_gitlab_webhook_deliveries
		(data_source_id,tenant_id,delivery_id,event_id,ref,before_sha,after_sha)
		VALUES(?,?, 'delivery-before-cleanup','event-before-cleanup','refs/heads/main',?,?)`,
		f.ds.ID, f.ds.TenantID, strings.Repeat("0", 40), strings.Repeat("1", 40)).Error)

	lifecycleRepo := repository.NewSyncLogRepository(f.db).(interfaces.SourceLifecycleRepository)
	operation, err := lifecycleRepo.AcceptSourceCleanup(f.ctx, f.ds, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)
	require.NotNil(t, operation)
	require.False(t, dataSourceQueryEnabledForCleanupTest(t, f.db, f.ds.ID))
	var persistedConfigRow struct {
		Config types.JSON `gorm:"column:config"`
	}
	require.NoError(t, f.db.Table("data_sources").Where("id=?", f.ds.ID).Select("config").Take(&persistedConfigRow).Error)
	var persistedConfig map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(persistedConfigRow.Config, &persistedConfig))
	require.NotContains(t, persistedConfig, "credentials", "clear must remove stored connector credentials without rewriting source settings")
	var webhook struct {
		Enabled          bool   `gorm:"column:enabled"`
		SecretCiphertext string `gorm:"column:secret_ciphertext"`
	}
	require.NoError(t, f.db.Table("source_gitlab_webhook_configs").Where("data_source_id=?", f.ds.ID).Take(&webhook).Error)
	require.False(t, webhook.Enabled)
	require.Empty(t, webhook.SecretCiphertext)

	// A duplicate clear while a worker lease is active returns the same
	// operation and leaves both the worker token and coordinator generation
	// unchanged.
	claimedToken := operation.FencingToken + 1
	require.NoError(t, f.db.Table("source_cleanup_operations").Where("id=?", operation.ID).Updates(map[string]any{
		"status": types.SourceCleanupRunning, "lease_owner": "live-cleanup-worker",
		"lease_expires_at": time.Now().UTC().Add(time.Minute), "fencing_token": claimedToken, "attempt_count": 1,
	}).Error)
	var cleanedDataSource types.DataSource
	require.NoError(t, f.db.Where("id=?", f.ds.ID).Take(&cleanedDataSource).Error)
	var syncStateBeforeRepeat struct {
		ConfigGeneration int64 `gorm:"column:config_generation"`
		FencingToken     int64 `gorm:"column:fencing_token"`
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&syncStateBeforeRepeat).Error)
	repeated, err := lifecycleRepo.AcceptSourceCleanup(f.ctx, &cleanedDataSource, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)
	require.Equal(t, operation.ID, repeated.ID)
	require.Equal(t, types.SourceCleanupRunning, repeated.Status)
	require.Equal(t, claimedToken, repeated.FencingToken)
	require.Equal(t, "live-cleanup-worker", *repeated.LeaseOwner)
	var syncStateAfterRepeat struct {
		ConfigGeneration int64 `gorm:"column:config_generation"`
		FencingToken     int64 `gorm:"column:fencing_token"`
	}
	require.NoError(t, f.db.Table("source_sync_states").Where("data_source_id=?", f.ds.ID).Take(&syncStateAfterRepeat).Error)
	require.Equal(t, syncStateBeforeRepeat, syncStateAfterRepeat)
	require.NoError(t, f.db.Table("source_cleanup_operations").Where("id=?", operation.ID).Updates(map[string]any{
		"status": types.SourceCleanupPending, "lease_owner": nil, "lease_expires_at": nil,
	}).Error)

	// Inject a deterministic write failure after the immediate query fence.
	require.NoError(t, f.db.Exec(`CREATE FUNCTION source_cleanup_injected_failure() RETURNS trigger
		LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected source cleanup failure'; END $$`).Error)
	require.NoError(t, f.db.Exec("CREATE TRIGGER source_cleanup_injected_failure BEFORE UPDATE ON wiki_pages FOR EACH ROW EXECUTE FUNCTION source_cleanup_injected_failure()").Error)
	t.Cleanup(func() {
		_ = f.db.Exec("DROP TRIGGER IF EXISTS source_cleanup_injected_failure ON wiki_pages").Error
		_ = f.db.Exec("DROP FUNCTION IF EXISTS source_cleanup_injected_failure()").Error
	})
	_, processErr := lifecycleRepo.ProcessSourceCleanupBatch(f.ctx, 1)
	require.Error(t, processErr)
	failed, err := lifecycleRepo.FindSourceCleanup(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SourceCleanupFailed, failed.Status)
	require.True(t, failed.Retryable)
	require.Equal(t, "cleanup_step_failed", failed.ErrorCode)
	require.False(t, dataSourceQueryEnabledForCleanupTest(t, f.db, f.ds.ID), "failed cleanup must not reopen reads")

	require.NoError(t, f.db.Exec("DROP TRIGGER source_cleanup_injected_failure ON wiki_pages").Error)
	require.NoError(t, f.db.Exec("DROP FUNCTION source_cleanup_injected_failure()").Error)
	var retrySource types.DataSource
	require.NoError(t, f.db.Where("id=?", f.ds.ID).Take(&retrySource).Error)
	retried, err := lifecycleRepo.RetrySourceCleanup(f.ctx, &retrySource, operation.ID)
	require.NoError(t, err)
	require.Equal(t, operation.ID, retried.ID)
	require.Equal(t, types.SourceCleanupPending, retried.Status)

	for i := 0; i < 20; i++ {
		_, err = lifecycleRepo.ProcessSourceCleanupBatch(f.ctx, 100)
		require.NoError(t, err)
		stored, findErr := lifecycleRepo.FindSourceCleanup(f.ctx, f.ds.ID)
		require.NoError(t, findErr)
		if stored.Status == types.SourceCleanupCompleted {
			break
		}
	}
	completed, err := lifecycleRepo.FindSourceCleanup(f.ctx, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, types.SourceCleanupCompleted, completed.Status)
	require.False(t, dataSourceQueryEnabledForCleanupTest(t, f.db, f.ds.ID))

	var mixed types.WikiPage
	require.NoError(t, f.db.Where("id=?", mixedPageID).Take(&mixed).Error)
	require.Contains(t, mixed.Content, "Source B body.")
	require.NotContains(t, mixed.Content, "Source A body.")
	require.Equal(t, types.StringArray{otherFileID + "|src/Other.java"}, mixed.SourceRefs)
	require.NotNil(t, mixed.SourceProvenance)
	require.Equal(t, otherSourceID, mixed.SourceProvenance.SourceID)
	var aOnly types.WikiPage
	require.ErrorIs(t, f.db.Where("id=?", aOnlyPageID).Take(&aOnly).Error, gorm.ErrRecordNotFound)
	var bOnly types.WikiPage
	require.NoError(t, f.db.Where("id=?", bOnlyPageID).Take(&bOnly).Error)
	var retainedRevision int64
	require.NoError(t, f.db.Table("wiki_page_revisions").Where("page_id=?", mixedPageID).Count(&retainedRevision).Error)
	require.Zero(t, retainedRevision, "historical mixed page versions that contain source A are withdrawn as a whole")
	var retainedAContributions int64
	require.NoError(t, f.db.Table("source_wiki_page_contributions").Where("source_id=?", f.ds.ID).Count(&retainedAContributions).Error)
	require.Zero(t, retainedAContributions)
	var retainedAArtifacts int64
	require.NoError(t, f.db.Table("source_parsed_artifacts").Where("data_source_id=?", f.ds.ID).Count(&retainedAArtifacts).Error)
	require.Zero(t, retainedAArtifacts)
	require.NoError(t, f.db.Table("source_embedding_artifacts").Where("data_source_id=?", f.ds.ID).Count(&retainedAArtifacts).Error)
	require.Zero(t, retainedAArtifacts)
	var retainedSourceSnapshots, retainedSourceFiles, retainedSourceVersions, retainedSourceRelations int64
	require.NoError(t, f.db.Table("source_snapshots").Where("data_source_id=?", f.ds.ID).Count(&retainedSourceSnapshots).Error)
	require.NoError(t, f.db.Table("source_files").Where("data_source_id=?", f.ds.ID).Count(&retainedSourceFiles).Error)
	require.NoError(t, f.db.Table("source_file_versions sv").Joins("JOIN source_files sf ON sf.id=sv.source_file_id").
		Where("sf.data_source_id=?", f.ds.ID).Count(&retainedSourceVersions).Error)
	require.NoError(t, f.db.Table("source_code_relations").Where("data_source_id=?", f.ds.ID).Count(&retainedSourceRelations).Error)
	require.Zero(t, retainedSourceSnapshots)
	require.Zero(t, retainedSourceFiles)
	require.Zero(t, retainedSourceVersions)
	require.Zero(t, retainedSourceRelations)
	if len(targetChunkIDs) > 0 {
		var retainedIndexedChunks int64
		require.NoError(t, f.db.Table("embeddings").Where("chunk_id IN ?", targetChunkIDs).Count(&retainedIndexedChunks).Error)
		require.Zero(t, retainedIndexedChunks, "the existing snapshot GC must remove embeddings for the cleared snapshot")
	}
	var retainedWebhookDeliveries int64
	require.NoError(t, f.db.Table("source_gitlab_webhook_deliveries").Where("data_source_id=?", f.ds.ID).Count(&retainedWebhookDeliveries).Error)
	require.Zero(t, retainedWebhookDeliveries)
	var activeSourceScopes, activeOtherScopes int64
	require.NoError(t, f.db.Table("source_read_scopes").Where("lease_id=? AND data_source_id=?", leaseID, f.ds.ID).Count(&activeSourceScopes).Error)
	require.NoError(t, f.db.Table("source_read_scopes").Where("lease_id=? AND data_source_id=?", leaseID, otherSourceID).Count(&activeOtherScopes).Error)
	require.Zero(t, activeSourceScopes)
	require.EqualValues(t, 1, activeOtherScopes, "mixed source-read leases retain source B's independent scope")
	var retainedWikiScope struct {
		SourceIDs types.JSON `gorm:"column:source_ids"`
	}
	require.NoError(t, f.db.Table("source_read_wiki_scopes").Where("lease_id=?", leaseID).Take(&retainedWikiScope).Error)
	var sourceIDs []string
	require.NoError(t, json.Unmarshal(retainedWikiScope.SourceIDs, &sourceIDs))
	require.Equal(t, []string{otherSourceID}, sourceIDs)
	var otherSnapshot types.SourceSnapshot
	require.NoError(t, f.db.Where("id=?", otherSnapshotID).Take(&otherSnapshot).Error)
	require.Equal(t, "published", otherSnapshot.State)
}

func sourceCleanupFixtureEvidenceRows(t *testing.T, db *gorm.DB, sourceID string) (types.SourceFile, types.SourceSnapshot, types.SourceFileVersion) {
	t.Helper()
	var file types.SourceFile
	require.NoError(t, db.Where("data_source_id=?", sourceID).Order("id").Take(&file).Error)
	var snapshot types.SourceSnapshot
	require.NoError(t, db.Where("data_source_id=? AND state='published'", sourceID).Take(&snapshot).Error)
	var version types.SourceFileVersion
	require.NoError(t, db.Where("source_file_id=? AND snapshot_id=?", file.ID, snapshot.ID).Take(&version).Error)
	return file, snapshot, version
}

func sourceCleanupFixtureEntry(sourceID, snapshotID, versionID, fileID, path, title, summary, body, digestSalt string) types.SourceWikiWithdrawalEntry {
	digest := sourceCleanupSHA256([]byte(digestSalt))
	evidence := types.SourceWikiEvidence{ID: uuid.NewString(), KnowledgeID: fileID, SourceEvidence: types.SourceEvidence{
		DataSourceID: sourceID, SnapshotID: snapshotID, FileVersionID: versionID,
		ProjectID: "fixture-project", CommitSHA: strings.Repeat(digestSalt, 40)[:40], Path: path,
		Range: types.SourceRange{StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}, Quality: "accepted",
	}, SHA256: digest, TextSHA256: digest}
	provenance := types.SourceWikiProvenance{SourceID: sourceID, TopicKind: "module", TopicKey: "shared-topic",
		ModulePath: path, State: "ready", ApplicableSnapshotID: snapshotID, Evidence: []types.SourceWikiEvidence{evidence}}
	contribution := types.SourceWikiContribution{SourceID: sourceID, TopicKind: provenance.TopicKind, TopicKey: provenance.TopicKey,
		OriginSnapshotID: snapshotID, ApplicableSnapshotID: snapshotID, Body: body,
		Evidence: []types.SourceWikiEvidence{evidence}, State: types.SourceWikiContributionCurrent}
	return types.SourceWikiWithdrawalEntry{Contribution: contribution, Title: title, Summary: summary,
		SourceRefs: []string{fileID + "|" + path}, Provenance: provenance}
}

func insertSourceCleanupFixtureWiki(t *testing.T, db *gorm.DB, pageID string, currentVersion int,
	entries []types.SourceWikiWithdrawalEntry, includeHistory bool,
) {
	t.Helper()
	entries = append([]types.SourceWikiWithdrawalEntry(nil), entries...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Contribution.SourceID < entries[j].Contribution.SourceID })
	pageRefs := make([]string, 0, len(entries))
	pageBodies := make([]string, 0, len(entries))
	for _, entry := range entries {
		pageRefs = append(pageRefs, entry.SourceRefs...)
		pageBodies = append(pageBodies, entry.Contribution.Body)
	}
	sort.Strings(pageRefs)
	primary := entries[0]
	now := time.Now().UTC()
	page := &types.WikiPage{ID: pageID, TenantID: 1, KnowledgeBaseID: "", Slug: "concept/" + pageID,
		Title: primary.Title, PageType: "concept", Status: "published", Content: strings.Join(pageBodies, "\n"),
		Summary: primary.Summary, SourceRefs: pageRefs, ChunkRefs: types.StringArray{}, PageMetadata: types.JSON(`{}`),
		SourceProvenance: &primary.Provenance, Version: currentVersion, CreatedAt: now, UpdatedAt: now}
	var source types.DataSource
	require.NoError(t, db.Where("id=?", entries[0].Contribution.SourceID).Take(&source).Error)
	page.KnowledgeBaseID, page.TenantID = source.KnowledgeBaseID, source.TenantID
	require.NoError(t, db.Create(page).Error)
	insertSourceCleanupFixtureOwners(t, db, page.ID, nil, currentVersion, entries, now)
	if includeHistory {
		revisionID := uuid.NewString()
		revision := &types.WikiPageRevision{ID: revisionID, TenantID: page.TenantID, KnowledgeBaseID: page.KnowledgeBaseID,
			PageID: page.ID, Slug: page.Slug, Version: currentVersion - 1, Title: primary.Title, PageType: page.PageType,
			Status: page.Status, Content: strings.Join(pageBodies, "\n"), Summary: primary.Summary,
			Aliases: types.StringArray{}, SourceRefs: pageRefs, ChunkRefs: types.StringArray{}, PageMetadata: types.JSON(`{}`),
			SourceProvenance: &primary.Provenance, EditedAt: now, CreatedAt: now}
		require.NoError(t, db.Create(revision).Error)
		insertSourceCleanupFixtureOwners(t, db, page.ID, &revisionID, revision.Version, entries, now)
	}
}

func insertSourceCleanupFixtureOwners(t *testing.T, db *gorm.DB, pageID string, revisionID *string, version int,
	entries []types.SourceWikiWithdrawalEntry, now time.Time,
) {
	t.Helper()
	for _, entry := range entries {
		contributionJSON, err := json.Marshal(map[string]any{
			"topic_kind": entry.Contribution.TopicKind, "topic_key": entry.Contribution.TopicKey,
			"source_provenance": entry.Provenance, "title": entry.Title, "summary": entry.Summary,
			"content": entry.Contribution.Body, "source_refs": entry.SourceRefs, "has_unattributed_body": false,
		})
		require.NoError(t, err)
		row := types.SourceWikiPageContribution{PageID: pageID, RevisionID: revisionID,
			SourceID: entry.Contribution.SourceID, PageVersion: version, TopicKind: entry.Contribution.TopicKind,
			TopicKey: entry.Contribution.TopicKey, ApplicableSnapshotID: entry.Contribution.ApplicableSnapshotID,
			TargetSnapshotID: entry.Contribution.ApplicableSnapshotID, State: "ready",
			DependencyFileIDs: types.JSON(`[]`), ModuleMemberFileIDs: types.JSON(`[]`),
			Contribution: types.JSON(contributionJSON), UpdatedAt: now}
		require.NoError(t, db.Create(&row).Error)
		for _, evidence := range entry.Contribution.Evidence {
			evidenceRef := &types.SourceWikiEvidenceRef{ID: uuid.NewString(), PageID: pageID, RevisionID: revisionID,
				Version: version, EvidenceID: evidence.ID, SourceFileID: evidence.KnowledgeID,
				FileVersionID: evidence.FileVersionID, SnapshotID: evidence.SnapshotID,
				Path: evidence.Path, CommitSHA: evidence.CommitSHA}
			require.NoError(t, db.Create(evidenceRef).Error)
		}
	}
}

func dataSourceQueryEnabledForCleanupTest(t *testing.T, db *gorm.DB, sourceID string) bool {
	t.Helper()
	var enabled bool
	require.NoError(t, db.Table("data_sources").Where("id=?", sourceID).Select("source_query_enabled").Scan(&enabled).Error)
	return enabled
}

func sourceCleanupSHA256(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
