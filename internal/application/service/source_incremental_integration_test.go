//go:build integration

package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceRepeatedCompleteSnapshotReusesParsingAndVectors(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	before, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 10, 0)
	require.NoError(t, err)
	require.NotEmpty(t, before)
	var old types.SyncResult
	require.NoError(t, json.Unmarshal(before[0].Result, &old))
	initialCalls := f.embedCount.Load()
	initialParseCalls := f.parseCount.Load()
	require.Positive(t, initialCalls)
	syncSourceFixture(t, f)
	require.Equal(t, initialCalls, f.embedCount.Load(), "same complete manifest must reuse actual embedding text/model artifacts")
	require.Equal(t, initialParseCalls, f.parseCount.Load(), "unchanged parsing must be reused at the real parser HTTP boundary")
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 10, 0)
	require.NoError(t, err)
	var current types.SyncResult
	require.NoError(t, json.Unmarshal(logs[0].Result, &current))
	require.Equal(t, old.Source.Members[1].SourceFileID, current.Source.Members[1].SourceFileID)
	require.Equal(t, old.Source.Snapshot.ID, current.Source.Snapshot.ID, "identical publication identity must remain a no-op")
	for _, keywordOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, current.Source.Snapshot.ID, hits[0].Metadata["source_snapshot_id"])
	}
}

func TestSourceExclusionPreviewCanPublishEmptyCompleteManifest(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1}}
	pinned, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	oldFile := sourceMember(t, old, "src/Service.java")
	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	config.Settings["exclude_paths"] = []string{"src/Service.java"}
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, config.Settings)
	require.NoError(t, err)
	require.True(t, preview.CanSync, "excluding the final file must remain a publishable complete change")
	// Draft preview and saved configuration do not remove the published member.
	for _, file := range preview.Files {
		if file.Path == "src/Service.java" {
			require.Equal(t, "excluded", file.Status)
		}
	}
	_, err = f.knowledge.GetSourceFile(f.ctx, oldFile.SourceFileID)
	require.NoError(t, err)
	updated := *f.ds
	updated.Config, err = config.ToJSON()
	require.NoError(t, err)
	f.ds, err = f.service.UpdateDataSource(f.ctx, &updated)
	require.NoError(t, err)
	_, err = f.knowledge.GetSourceFile(f.ctx, oldFile.SourceFileID)
	require.NoError(t, err)
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	require.Equal(t, old.Snapshot.CommitSHA, current.Snapshot.CommitSHA)
	require.Equal(t, 0, current.Snapshot.FileCount)
	require.Equal(t, 1, current.Snapshot.DeletedCount)
	require.Equal(t, "published", current.Snapshot.State)
	for _, keywordOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Empty(t, hits)
		oldHits, err := f.kbs.HybridSearch(pinned, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Len(t, oldHits, 1)
	}
	retained, err := f.knowledge.GetSourceFile(pinned, oldFile.SourceFileID)
	require.NoError(t, err)
	require.Equal(t, old.Snapshot.CommitSHA, retained.CommitSHA)
	require.Contains(t, retained.Content, "预约")
	_, err = f.knowledge.GetSourceFile(f.ctx, oldFile.SourceFileID)
	require.Error(t, err)
}
func latestIncrementalRun(t *testing.T, f *javaSourceFixture) *types.SourceRunResult {
	t.Helper()
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 1, 0)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	var result types.SyncResult
	require.NoError(t, json.Unmarshal(logs[0].Result, &result))
	require.NotNil(t, result.Source)
	return result.Source
}
func sourceMember(t *testing.T, run *types.SourceRunResult, path string) types.SourceSnapshotMember {
	t.Helper()
	for _, m := range run.Members {
		if m.Path == path {
			return m
		}
	}
	t.Fatalf("source member absent: %s", path)
	return types.SourceSnapshotMember{}
}
func TestSourceIncrementalCompleteManifestKeepsUniqueRenameAndAccountsForOtherChanges(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{
		"src/Keep.java":   []byte("class Keep { int stableToken() { return 1; } }\n"),
		"src/Delete.java": []byte("class Delete { int goneToken() { return 1; } }\n"),
		"src/Change.java": []byte("class Change { int changeToken() { return 1; } }\n"),
	})
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	original, err := f.knowledge.GetSourceFile(f.ctx, sourceMember(t, old, "src/Service.java").SourceFileID)
	require.NoError(t, err)
	parsedCalls := f.parseCount.Load()
	embedCalls := f.embedCount.Load()
	newSHA := f.advanceFiles(map[string][]byte{
		"src/Service.java": nil, "src/Renamed.java": original.RawContent,
		"src/Delete.java": nil,
		"src/Change.java": []byte("class Change { int changeToken() { return 2; } }\n"),
		"src/Add.java":    []byte("class Add { int addedToken() { return 1; } }\n"),
	})
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	renamed := sourceMember(t, current, "src/Renamed.java")
	require.Equal(t, sourceMember(t, old, "src/Service.java").SourceFileID, renamed.SourceFileID)
	require.Equal(t, "renamed", renamed.Change)
	require.Equal(t, "src/Service.java", renamed.PreviousPath)
	require.Equal(t, 1, current.Snapshot.AddedCount)
	require.Equal(t, 1, current.Snapshot.ChangedCount)
	require.Equal(t, 1, current.Snapshot.DeletedCount)
	require.Equal(t, 1, current.Snapshot.RenamedCount)
	require.Equal(t, 1, current.Snapshot.ReusedFileCount)
	require.Equal(t, int64(3), f.parseCount.Load()-parsedCalls, "moved path must process its path-sensitive chunk context again")
	require.Greater(t, f.embedCount.Load(), embedCalls, "moved path and changed signature/content must not reuse vectors by blob alone")
	for _, keywordOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, KnowledgeIDs: []string{renamed.SourceFileID}, MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, newSHA, hits[0].Metadata["commit_sha"])
		var evidence struct {
			Source types.SourceEvidence `json:"source"`
		}
		require.NoError(t, json.Unmarshal(hits[0].ChunkMetadata, &evidence))
		require.Equal(t, "src/Renamed.java", evidence.Source.Path)
		require.Contains(t, evidence.Source.GitLabURL, "/"+newSHA+"/src/Renamed.java")
		removed, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "goneToken", QueryEmbedding: []float32{1, 0, 0}, KnowledgeIDs: []string{sourceMember(t, old, "src/Delete.java").SourceFileID}, MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Empty(t, removed)
	}
	keep := sourceMember(t, current, "src/Keep.java")
	require.Equal(t, sourceMember(t, old, "src/Keep.java").SourceFileID, keep.SourceFileID)
	require.True(t, keep.ParseReused)
}

func TestSourceForcePushReconcilesAgainstTheCompleteManifest(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{
		"src/BeforeForcePush.java": []byte("class BeforeForcePush { int oldForcePushToken() { return 1; } }\n"),
	})
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	newSHA := f.forcePush()
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	newMember := sourceMember(t, current, "src/ForcePushed.java")

	require.NotEqual(t, old.Snapshot.CommitSHA, newSHA)
	require.Equal(t, newSHA, current.Snapshot.CommitSHA)
	require.Equal(t, "published", current.Snapshot.State)
	require.Equal(t, 2, current.Snapshot.DeletedCount, "the unrelated history must still account for both old source files")
	require.Equal(t, "parsed", newMember.Status)
	for _, query := range []string{"getPushSchedule", "oldForcePushToken"} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: query, MatchCount: 10, DisableVectorMatch: true})
		require.NoError(t, err)
		require.Empty(t, hits, "old identifiers must disappear from the keyword index after force-push publication")
	}
	for _, queryMode := range []struct {
		name                 string
		disableKeywordsMatch bool
		disableVectorMatch   bool
	}{
		{name: "bm25", disableVectorMatch: true},
		{name: "vector", disableKeywordsMatch: true},
		{name: "hybrid"},
	} {
		t.Run(queryMode.name, func(t *testing.T) {
			queryText := "oldForcePushToken"
			if queryMode.name == "bm25" {
				queryText = "forcePushToken"
			}
			hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
				QueryText: queryText, QueryEmbedding: []float32{1, 0, 0}, MatchCount: 10,
				DisableKeywordsMatch: queryMode.disableKeywordsMatch, DisableVectorMatch: queryMode.disableVectorMatch,
			})
			require.NoError(t, err)
			require.NotEmpty(t, hits, "the fixture's shared vector is expected to retrieve the sole current source member")
			for _, hit := range hits {
				require.Equal(t, newMember.SourceFileID, hit.KnowledgeID)
				require.Equal(t, newSHA, hit.Metadata["commit_sha"])
				require.Equal(t, current.Snapshot.ID, hit.Metadata["source_snapshot_id"])
				var metadata struct {
					Source types.SourceEvidence `json:"source"`
				}
				require.NoError(t, json.Unmarshal(hit.ChunkMetadata, &metadata))
				require.Equal(t, newMember.FileVersionID, metadata.Source.FileVersionID)
				require.Equal(t, newSHA, metadata.Source.CommitSHA)
			}
		})
	}
}

func TestSourceRunRecordsPipelineUnavailableWhenSnapshotRepositoryIsMissing(t *testing.T) {
	f := newJavaSourceFixture(t)
	log := &types.SyncLog{DataSourceID: f.ds.ID, TenantID: 1, Status: types.SyncLogStatusRunning, StartedAt: time.Now().UTC()}
	require.NoError(t, f.service.syncLogRepo.Create(f.ctx, log))
	f.service.sourceSnapshots = nil
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	var processErr error
	require.NotPanics(t, func() {
		processErr = f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	})
	require.ErrorContains(t, processErr, "source ingestion pipeline is not available")
	failed, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, failed.Status)
	var result types.SyncResult
	require.NoError(t, json.Unmarshal(failed.Result, &result))
	require.NotNil(t, result.Source)
	require.Equal(t, "failed", result.Source.Snapshot.State)
	require.False(t, result.Source.Snapshot.PublicationChecked)
	require.Contains(t, result.Source.Snapshot.Error, "source ingestion pipeline is not available")
}

func TestSourceParserReadinessFailureRetainsPublishedStatusAndReadableVersion(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	previousRun := latestIncrementalRun(t, f)
	previousMember := sourceMember(t, previousRun, "src/Service.java")
	previousPublication, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.NotNil(t, previousPublication)
	require.Equal(t, previousRun.Snapshot.ID, previousPublication.Snapshot.ID)
	require.NotNil(t, previousRun.Snapshot.PublishedAt)

	unhealthyParser := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "parser unavailable", http.StatusServiceUnavailable)
	}))
	defer unhealthyParser.Close()
	t.Setenv("SOURCE_PARSER_URL", unhealthyParser.URL)
	require.False(t, sourceParserReady(f.ctx))

	log := &types.SyncLog{DataSourceID: f.ds.ID, TenantID: f.ds.TenantID, Status: types.SyncLogStatusRunning, StartedAt: time.Now().UTC()}
	require.NoError(t, f.service.syncLogRepo.Create(f.ctx, log))
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	processErr := f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	require.ErrorContains(t, processErr, "source indexes or parser are not ready")

	failed, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, failed.Status)
	var result types.SyncResult
	require.NoError(t, json.Unmarshal(failed.Result, &result))
	require.NotNil(t, result.Source)
	require.True(t, result.Source.Snapshot.PublicationChecked)
	require.Equal(t, previousRun.Snapshot.ID, result.Source.Snapshot.PreviousSnapshotID)
	require.Equal(t, previousRun.Snapshot.CommitSHA, result.Source.Snapshot.PreviousCommitSHA)
	require.NotNil(t, result.Source.Snapshot.LastSuccessfulPublishedAt)
	require.NotNil(t, result.Source.Snapshot.PreviousPublishedAt)
	require.WithinDuration(t, *previousRun.Snapshot.PublishedAt, *result.Source.Snapshot.LastSuccessfulPublishedAt, time.Microsecond)
	require.WithinDuration(t, *previousRun.Snapshot.PublishedAt, *result.Source.Snapshot.PreviousPublishedAt, time.Microsecond)

	currentPublication, err := f.service.sourceSnapshots.GetPublished(f.ctx, f.ds.TenantID, f.ds.ID)
	require.NoError(t, err)
	require.Equal(t, previousPublication.Snapshot.ID, currentPublication.Snapshot.ID)
	readable, err := f.knowledge.GetSourceFile(f.ctx, previousMember.SourceFileID)
	require.NoError(t, err)
	require.Equal(t, previousRun.Snapshot.CommitSHA, readable.CommitSHA)
	require.Contains(t, readable.Content, "预约")
}

func TestSourceWikiEvidenceRemainsReadableAfterForcePushAndGitUnavailable(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"Returns a schedule.","sections":[{"text":"getPushSchedule returns a schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module"})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status)
	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.Len(t, page.SourceProvenance.Evidence, 1)
	oldEvidence := page.SourceProvenance.Evidence[0]
	require.Equal(t, old.Snapshot.CommitSHA, oldEvidence.CommitSHA)

	newSHA := f.forcePush()
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	require.Equal(t, newSHA, current.Snapshot.CommitSHA)
	require.Equal(t, 1, current.Snapshot.DeletedCount)
	// The API and Git transport no longer offer the previous branch state. A
	// saved Wiki evidence read must use its retained database file version.
	f.gitlabBranchMissing = true
	f.gitTransportUnavailable = true
	retained, err := generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 0, oldEvidence.ID)
	require.NoError(t, err)
	require.Equal(t, oldEvidence.FileVersionID, retained.FileVersionID)
	require.Equal(t, old.Snapshot.CommitSHA, retained.CommitSHA)
	require.Contains(t, retained.Content, "预约")
}

func TestSourceRemoteFailuresKeepPublishedVersionAndExposeLastSuccess(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)

	for _, failure := range []struct {
		name string
		set  func(bool)
	}{
		{name: "branch missing", set: func(value bool) { f.gitlabBranchMissing = value }},
		{name: "token invalid", set: func(value bool) { f.gitlabTokenInvalid = value }},
		{name: "git fetch unavailable", set: func(value bool) { f.gitTransportUnavailable = value }},
	} {
		t.Run(failure.name, func(t *testing.T) {
			failure.set(true)
			log, done := startIncrementalRun(t, f)
			err := <-done
			require.Error(t, err)
			failed, getErr := f.service.GetSyncLog(f.ctx, log.ID)
			require.NoError(t, getErr)
			require.Equal(t, types.SyncLogStatusFailed, failed.Status)
			var result types.SyncResult
			require.NoError(t, json.Unmarshal(failed.Result, &result))
			require.NotNil(t, result.Source)
			require.Equal(t, old.Snapshot.CommitSHA, result.Source.Snapshot.PreviousCommitSHA)
			require.NotNil(t, result.Source.Snapshot.LastSuccessfulPublishedAt)
			assertIncrementalOldPublication(t, f, sourceMember(t, old, "src/Service.java").SourceFileID, old.Snapshot.CommitSHA)
			failure.set(false)
		})
	}
}

func TestSourceAmbiguousContentRenameIsExplicitDeleteAndAdd(t *testing.T) {
	raw := []byte("class Same { int sameToken() { return 1; } }\n")
	f := newJavaSourceFixture(t, map[string][]byte{"src/First.java": raw, "src/Second.java": raw})
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	f.advanceFiles(map[string][]byte{"src/First.java": nil, "src/Second.java": nil, "src/NewFirst.java": raw, "src/NewSecond.java": raw})
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	require.Equal(t, 0, current.Snapshot.RenamedCount)
	require.Equal(t, 2, current.Snapshot.AddedCount)
	require.Equal(t, 2, current.Snapshot.DeletedCount)
	for _, path := range []string{"src/NewFirst.java", "src/NewSecond.java"} {
		member := sourceMember(t, current, path)
		require.Equal(t, "added", member.Change)
		require.Contains(t, member.Reason, "delete plus add")
		require.NotEqual(t, sourceMember(t, old, "src/First.java").SourceFileID, member.SourceFileID)
		require.NotEqual(t, sourceMember(t, old, "src/Second.java").SourceFileID, member.SourceFileID)
	}
}

func TestSourceSameCommitChangedRulesAndModelUseControlledArtifactVersions(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	initialParse, initialEmbed := f.parseCount.Load(), f.embedCount.Load()
	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	config.Settings["max_file_bytes"] = 3 << 20
	updated := *f.ds
	updated.Config, err = config.ToJSON()
	require.NoError(t, err)
	f.ds, err = f.service.UpdateDataSource(f.ctx, &updated)
	require.NoError(t, err)
	syncSourceFixture(t, f)
	rulesRun := latestIncrementalRun(t, f)
	require.Equal(t, old.Snapshot.CommitSHA, rulesRun.Snapshot.CommitSHA)
	require.NotEqual(t, old.Snapshot.RulesVersion, rulesRun.Snapshot.RulesVersion)
	require.Greater(t, f.parseCount.Load(), initialParse)
	require.Equal(t, initialEmbed, f.embedCount.Load(), "identical actual embedding text may reuse vectors across a filter/rule version change")
	model, err := f.modelService.GetModelByID(f.ctx, f.kb.EmbeddingModelID)
	require.NoError(t, err)
	model.Parameters.ExtraConfig = map[string]string{"source_model_revision": "controlled-revision-two"}
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))
	parseBeforeModel, embedBeforeModel := f.parseCount.Load(), f.embedCount.Load()
	syncSourceFixture(t, f)
	modelRun := latestIncrementalRun(t, f)
	require.Equal(t, old.Snapshot.CommitSHA, modelRun.Snapshot.CommitSHA)
	require.NotEqual(t, rulesRun.Snapshot.EmbeddingVersion, modelRun.Snapshot.EmbeddingVersion)
	require.Equal(t, parseBeforeModel, f.parseCount.Load(), "model changes do not invalidate the separately versioned parser")
	require.Greater(t, f.embedCount.Load(), embedBeforeModel)
	require.Equal(t, 1, modelRun.Snapshot.ReusedFileCount)
	require.Positive(t, modelRun.Snapshot.EmbeddedChunkCount)
}
func TestSourceUpdateKeepsPublishedVersionDuringParsingAndVectorFailure(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	oldFile := sourceMember(t, old, "src/Service.java")
	f.advanceJava("class Service { String getPushSchedule() { return \"vector failure revision\"; } }\n")
	f.parseStarted = make(chan struct{}, 1)
	f.parseRelease = make(chan struct{})
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	f.embedVector = []float32{0, 0, 0}
	log, done := startIncrementalRun(t, f)
	defer func() { close(f.parseRelease); close(f.embedRelease); <-done }()
	select {
	case <-f.parseStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("real parser boundary not reached")
	}
	assertIncrementalOldPublication(t, f, oldFile.SourceFileID, old.Snapshot.CommitSHA)
	running, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	var progress types.SyncResult
	require.NoError(t, json.Unmarshal(running.Result, &progress))
	require.Equal(t, "parsing", progress.Source.Snapshot.State)
	f.parseRelease <- struct{}{}
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("embedding boundary not reached")
	}
	assertIncrementalOldPublication(t, f, oldFile.SourceFileID, old.Snapshot.CommitSHA)
	running, err = f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(running.Result, &progress))
	require.Equal(t, "indexing", progress.Source.Snapshot.State)
	f.embedRelease <- struct{}{}
	require.ErrorContains(t, <-done, "zero norm")
	assertIncrementalOldPublication(t, f, oldFile.SourceFileID, old.Snapshot.CommitSHA)
	failed, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, failed.Status)
	require.NoError(t, json.Unmarshal(failed.Result, &progress))
	require.Equal(t, old.Snapshot.CommitSHA, progress.Source.Snapshot.PreviousCommitSHA)
}

func TestSourceUpdateKeywordFailureRetainsPreviousCompletePublication(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	oldFile := sourceMember(t, old, "src/Service.java")
	f.advanceJava("class Service { String getPushSchedule() { return \"keyword failure revision\"; } }\n")
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	log, done := startIncrementalRun(t, f)
	defer func() { close(f.embedRelease); <-done }()
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("embedding boundary not reached")
	}
	assertIncrementalOldPublication(t, f, oldFile.SourceFileID, old.Snapshot.CommitSHA)
	require.NoError(t, f.db.Exec("DROP INDEX embeddings_search_idx").Error)
	f.embedRelease <- struct{}{}
	require.ErrorContains(t, <-done, "keyword index")
	// Restore only the test fixture's damaged external index, then verify both actual routes.
	require.NoError(t, f.db.Exec("CREATE INDEX embeddings_search_idx ON embeddings USING bm25 (id, content, knowledge_base_id, knowledge_id, tag_id) WITH (key_field='id')").Error)
	assertIncrementalOldPublication(t, f, oldFile.SourceFileID, old.Snapshot.CommitSHA)
	failed, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, failed.Status)
}

func startIncrementalRun(t *testing.T, f *javaSourceFixture) (*types.SyncLog, chan error) {
	t.Helper()
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		defer close(done)
		done <- f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
	}()
	return log, done
}
func assertIncrementalOldPublication(t *testing.T, f *javaSourceFixture, fileID, sha string) {
	t.Helper()
	for _, keywordOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0}, KnowledgeIDs: []string{fileID}, MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, sha, hits[0].Metadata["commit_sha"])
		require.Contains(t, hits[0].Content, "预约")
	}
	file, err := f.knowledge.GetSourceFile(f.ctx, fileID)
	require.NoError(t, err)
	require.Equal(t, sha, file.CommitSHA)
}
func TestSourceDuplicateSuccessfulDeliveryDoesNotCreateOrReplacePublication(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 1, 0)
	require.NoError(t, err)
	first := latestIncrementalRun(t, f)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: logs[0].ID, Trigger: "manual"})
	require.NoError(t, err)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	duplicated, err := f.service.GetSyncLog(f.ctx, logs[0].ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, duplicated.Status)
	var result types.SyncResult
	require.NoError(t, json.Unmarshal(duplicated.Result, &result))
	require.Equal(t, first.Snapshot.ID, result.Source.Snapshot.ID)
	newSHA := f.advanceJava("class Service { String getPushSchedule() { return \"newer publication\"; } }\n")
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	require.NoError(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)))
	file, err := f.knowledge.GetSourceFile(f.ctx, sourceMember(t, current, "src/Service.java").SourceFileID)
	require.NoError(t, err)
	require.Equal(t, newSHA, file.CommitSHA)
}
func TestSourceReplayRequiresPersistedRunTenantAndSourceIdentity(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 1, 0)
	require.NoError(t, err)
	other := *f.ds
	other.ID = uuid.NewString()
	other.Name = "different source"
	_, err = f.service.CreateDataSource(f.ctx, &other)
	require.NoError(t, err)
	for _, payload := range []types.DataSourceSyncPayload{
		{DataSourceID: f.ds.ID, TenantID: 2, SyncLogID: logs[0].ID, Trigger: "manual"},
		{DataSourceID: other.ID, TenantID: 1, SyncLogID: logs[0].ID, Trigger: "manual"},
	} {
		encoded, err := json.Marshal(payload)
		require.NoError(t, err)
		require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, encoded)), "identity")
		retained, err := f.service.GetSyncLog(f.ctx, logs[0].ID)
		require.NoError(t, err)
		require.Equal(t, types.SyncLogStatusSuccess, retained.Status)
	}
}
func TestSourceSameCommitDimensionChangeRebuildsVectorsAndKeepsParserArtifact(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	first := latestIncrementalRun(t, f)
	parserCalls := f.parseCount.Load()
	model, err := f.modelService.GetModelByID(f.ctx, f.kb.EmbeddingModelID)
	require.NoError(t, err)
	model.Parameters.EmbeddingParameters.Dimension = 4
	f.embedVector = []float32{1, 0, 0, 0}
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))
	syncSourceFixture(t, f)
	current := latestIncrementalRun(t, f)
	require.Equal(t, first.Snapshot.CommitSHA, current.Snapshot.CommitSHA)
	require.Equal(t, parserCalls, f.parseCount.Load())
	require.NotEqual(t, first.Snapshot.EmbeddingVersion, current.Snapshot.EmbeddingVersion)
	require.Positive(t, current.Snapshot.EmbeddedChunkCount)
	require.Zero(t, current.Snapshot.ReusedVectorCount)
	for _, keywordOnly := range []bool{true, false} {
		hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", QueryEmbedding: []float32{1, 0, 0, 0}, MatchCount: 10, DisableVectorMatch: keywordOnly, DisableKeywordsMatch: !keywordOnly})
		require.NoError(t, err)
		require.Len(t, hits, 1)
		require.Equal(t, current.Snapshot.ID, hits[0].Metadata["source_snapshot_id"])
	}
}

func TestSourcePendingEmbeddingModelChangeCannotPublishVectorsForObsoleteVersion(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	oldFile := sourceMember(t, old, "src/Service.java")
	f.advanceJava("class Service { String getPushSchedule() { return \"pending model revision\"; } }\n")
	f.embedStarted = make(chan struct{}, 1)
	f.embedRelease = make(chan struct{})
	_, done := startIncrementalRun(t, f)
	defer func() { close(f.embedRelease); <-done }()
	select {
	case <-f.embedStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("embedding boundary not reached")
	}
	model, err := f.modelService.GetModelByID(f.ctx, f.kb.EmbeddingModelID)
	require.NoError(t, err)
	model.Name = "changed-effective-model"
	require.NoError(t, f.modelService.UpdateModel(f.ctx, model))
	f.embedRelease <- struct{}{}
	require.ErrorContains(t, <-done, "embedding configuration changed")
	assertIncrementalOldPublication(t, f, oldFile.SourceFileID, old.Snapshot.CommitSHA)
}

func TestSourceRecreatedOldPathDoesNotStealRenamedFileIdentity(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	old := latestIncrementalRun(t, f)
	original := sourceMember(t, old, "src/Service.java")
	file, err := f.knowledge.GetSourceFile(f.ctx, original.SourceFileID)
	require.NoError(t, err)
	require.NoError(t, f.knowledge.UpdateKnowledge(f.ctx, &types.Knowledge{ID: original.SourceFileID, Description: "stable user description", DescriptionSpecified: true}))
	f.advanceFiles(map[string][]byte{"src/Service.java": nil, "src/Moved.java": file.RawContent})
	syncSourceFixture(t, f)
	renamed := latestIncrementalRun(t, f)
	require.Equal(t, original.SourceFileID, sourceMember(t, renamed, "src/Moved.java").SourceFileID)
	f.advanceFiles(map[string][]byte{"src/Service.java": []byte("class Replacement { int replacementToken() { return 2; } }\n")})
	syncSourceFixture(t, f)
	recreated := latestIncrementalRun(t, f)
	require.NotEqual(t, original.SourceFileID, sourceMember(t, recreated, "src/Service.java").SourceFileID)
	require.Equal(t, original.SourceFileID, sourceMember(t, recreated, "src/Moved.java").SourceFileID)
	info, err := f.knowledge.GetKnowledgeByID(f.ctx, original.SourceFileID)
	require.NoError(t, err)
	require.Equal(t, "stable user description", info.Description)
	moved, err := f.knowledge.GetSourceFile(f.ctx, original.SourceFileID)
	require.NoError(t, err)
	require.Equal(t, "src/Moved.java", moved.Path)
}
