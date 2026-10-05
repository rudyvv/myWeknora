//go:build integration

package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/application/access"
	"github.com/Tencent/WeKnora/internal/application/repository"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func t22WikiTargets(f *javaSourceFixture, sourceIDs ...string) types.SearchTargets {
	return types.SearchTargets{&types.SearchTarget{
		Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID,
		TenantID: f.kb.TenantID, SourceIDs: append([]string(nil), sourceIDs...),
	}}
}

func t22BeginWikiAnswerRead(wiki interfaces.WikiPageService, ctx context.Context, targets types.SearchTargets) (context.Context, func(), error) {
	reader, ok := wiki.(interfaces.WikiReadService)
	if !ok {
		return nil, nil, fmt.Errorf("Wiki service does not implement WikiReadService")
	}
	return reader.BeginWikiRead(source.WithWikiAnswerRead(ctx), targets)
}

func t22SourceWikiResponse(marker string) func(bool) string {
	return func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return fmt.Sprintf(`{"title":"Scheduling module","summary":%q,"sections":[{"text":%q,"evidence_ids":["e001"],"uncertain":false}]}`,
			marker+"_SUMMARY", marker+"_BODY")
	}
}

func t22WikiReadToolResult(t *testing.T, wiki interfaces.WikiPageService, f *javaSourceFixture, ctx context.Context, slug string) (*types.ToolResult, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"slugs": []string{slug}})
	require.NoError(t, err)
	tool := agenttools.NewWikiReadPageTool(wiki, f.knowledge,
		agenttools.NewWikiScopesFromKBIDs([]string{f.kb.ID}), nil)
	return tool.Execute(ctx, args)
}

func t22WikiSearchToolResult(t *testing.T, wiki interfaces.WikiPageService, f *javaSourceFixture, ctx context.Context, query string) (*types.ToolResult, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"queries": []string{query}, "limit": 10})
	require.NoError(t, err)
	tool := agenttools.NewWikiSearchTool(wiki, f.knowledge,
		agenttools.NewWikiScopesFromKBIDs([]string{f.kb.ID}), nil)
	return tool.Execute(ctx, args)
}

func t22RequireWikiReadDenied(t *testing.T, result *types.ToolResult, err error, page *types.WikiPage) {
	t.Helper()
	if err != nil {
		t22RequireExpectedReadDenial(t, err)
		if result != nil {
			require.Empty(t, result.Data, "a denied single-page read must not carry a hidden page structure")
			t22RequireNoPageDetails(t, result.Output, result.Error, page)
		}
		return
	}
	require.NotNil(t, result)
	require.False(t, result.Success, "a single-page read must fail closed, not return a successful but incomplete page")
	t22RequireRecognizedToolDenial(t, result.Error)
	require.Empty(t, result.Data, "a denied single-page read must not carry a hidden page structure")
	t22RequireNoPageDetails(t, result.Output, result.Error, page)
}

func t22RequireWikiSearchEmpty(t *testing.T, result *types.ToolResult, err error, page *types.WikiPage) {
	t.Helper()
	require.NoError(t, err, "search infrastructure errors are not an empty result")
	require.NotNil(t, result)
	require.True(t, result.Success, "a successfully executed search may return no eligible pages")
	require.Contains(t, result.Output, `count="0"`, "the search result must explicitly report no matching page")
	require.Empty(t, result.Data["found_kbs"], "an empty search must not register a hidden page route")
	if page != nil {
		require.NotContains(t, result.Output, page.Slug, "the excluded page slug must not appear in search output")
	}
	t22RequireNoPageDetails(t, result.Output, result.Error, page)
}

func t22RequireRecognizedToolDenial(t *testing.T, denialText string) {
	t.Helper()
	denial := strings.ToLower(denialText)
	require.True(t, strings.Contains(denial, "not found") || strings.Contains(denial, "forbidden") ||
		strings.Contains(denial, "unauthorized") || strings.Contains(denial, "expired") ||
		strings.Contains(denial, "explicitly cleared") || strings.Contains(denial, "error code: 1001") ||
		strings.Contains(denial, "error code: 1002"), "tool failure must be a recognized authorization/lifecycle denial, got: %q", denialText)
}

func t22RequireNoPageDetails(t *testing.T, output, errorText string, page *types.WikiPage) {
	t.Helper()
	if page == nil {
		return
	}
	for _, sensitive := range t22PageSensitiveValues(page) {
		require.NotContains(t, output, sensitive, "denied or empty Wiki output leaked page data")
		require.NotContains(t, errorText, sensitive, "Wiki tool error leaked page data")
	}
}

func t22PageSensitiveValues(page *types.WikiPage) []string {
	if page == nil {
		return nil
	}
	values := []string{page.Title, page.Summary, page.Content}
	values = append(values, page.SourceRefs...)
	values = append(values, page.ChunkRefs...)
	if page.SourceProvenance != nil {
		for _, evidence := range page.SourceProvenance.Evidence {
			values = append(values, evidence.ID, evidence.KnowledgeID, evidence.FileVersionID,
				evidence.SnapshotID, evidence.Path, evidence.CommitSHA, evidence.SHA256)
		}
	}
	seen := make(map[string]struct{}, len(values))
	unique := values[:0]
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func t22RequireExpectedReadDenial(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	if errors.Is(err, repository.ErrWikiPageNotFound) {
		return
	}
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) {
		require.Contains(t, []apperrors.ErrorCode{apperrors.ErrUnauthorized, apperrors.ErrForbidden, apperrors.ErrNotFound}, appErr.Code,
			"unexpected application error must not be mistaken for an authorization denial: %v", err)
		return
	}
	require.EqualError(t, err, "source question expired, completed or explicitly cleared",
		"only the explicit expired/completed/cleared source-lease error is an expected lifecycle denial")
}

func t22IncludeLibraryPath(t *testing.T, f *javaSourceFixture) {
	t.Helper()
	config, err := f.ds.ParseConfig()
	require.NoError(t, err)
	projects, ok := config.Settings["projects"].([]any)
	require.True(t, ok)
	project, ok := projects[0].(map[string]any)
	require.True(t, ok)
	project["paths"] = []any{"src", "lib"}
	updated := *f.ds
	updated.Config, err = config.ToJSON()
	require.NoError(t, err)
	f.ds, err = f.service.UpdateDataSource(f.ctx, &updated)
	require.NoError(t, err)
}

// One real unrelated-file publication carries the same page version to a new
// applicable snapshot. The next source publication then fences that card and
// validates a new body. The old answer's first Wiki read is deliberately after
// both publications and the regeneration.
func TestSourceWikiAnswerLeaseKeepsExactCardAcrossPublicationAndRegeneration(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{
		"lib/Unrelated.java": []byte("package lib; class Unrelated { int stableValue() { return 1; } }\n"),
	})
	t22IncludeLibraryPath(t, f)
	syncSourceFixture(t, f)
	firstRun := latestIncrementalRun(t, f)
	var generation atomic.Int32
	response := func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		if generation.Add(1) == 1 {
			return `{"title":"Scheduling module","summary":"T22_OLD_VALIDATED_SUMMARY","sections":[{"text":"T22_OLD_VALIDATED_BODY","evidence_ids":["e001"],"uncertain":false}]}`
		}
		return `{"title":"Scheduling module","summary":"T22_NEW_VALIDATED_SUMMARY","sections":[{"text":"T22_NEW_VALIDATED_BODY","evidence_ids":["e001"],"uncertain":false}]}`
	}
	wiki, generator := newSourceWikiFixture(t, f, response)
	request := types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	}
	first, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", first.Status)
	firstPage, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Equal(t, 1, firstPage.Version)
	oldEvidence := firstPage.SourceProvenance.Evidence[0]
	require.Equal(t, firstRun.Snapshot.ID, oldEvidence.SnapshotID)

	// A different module member changes the publication, but not this card.
	f.advanceFiles(map[string][]byte{
		"lib/Unrelated.java": []byte("package lib; class Unrelated { int stableValue() { return 2; } }\n"),
	})
	syncSourceFixture(t, f)
	carryRun := latestIncrementalRun(t, f)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	carried, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Equal(t, firstPage.Version, carried.Version, "applicability carry-forward must not invent a page revision")
	require.Equal(t, carryRun.Snapshot.ID, carried.SourceProvenance.ApplicableSnapshotID)
	require.Equal(t, firstRun.Snapshot.ID, carried.SourceProvenance.Evidence[0].SnapshotID,
		"the raw evidence snapshot may be older than the applicability snapshot")
	require.NotEqual(t, carried.SourceProvenance.ApplicableSnapshotID, carried.SourceProvenance.Evidence[0].SnapshotID)

	oldCtx, releaseOld, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseOld()
	oldCtx = source.WithWikiAnswerRead(oldCtx)

	newSHA := f.advanceJava("package demo; public class Service { public String getPushSchedule() { return \"new schedule\"; } }\n")
	syncSourceFixture(t, f)
	staleCtx, releaseStale, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseStale()
	staleCtx = source.WithWikiAnswerRead(staleCtx)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	generator.(*sourceWikiService).StopSourceWikiBatches()
	updated, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", updated.Status)

	oldPage, err := wiki.GetPageBySlug(oldCtx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Equal(t, firstPage.Version, oldPage.Version)
	require.Contains(t, oldPage.Content, "T22_OLD_VALIDATED_BODY")
	require.NotContains(t, oldPage.Content, "T22_NEW_VALIDATED_BODY")
	oldRead, err := t22WikiReadToolResult(t, wiki, f, oldCtx, first.Slug)
	require.NoError(t, err)
	require.Contains(t, oldRead.Output, "T22_OLD_VALIDATED_BODY")
	require.NotContains(t, oldRead.Output, "T22_NEW_VALIDATED_BODY")
	oldSearch, err := t22WikiSearchToolResult(t, wiki, f, oldCtx, "T22_OLD_VALIDATED_SUMMARY")
	require.NoError(t, err)
	require.Contains(t, oldSearch.Output, "T22_OLD_VALIDATED_SUMMARY")

	currentCtx, releaseCurrent, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseCurrent()
	currentCtx = source.WithWikiAnswerRead(currentCtx)
	current, err := wiki.GetPageBySlug(currentCtx, f.kb.ID, updated.Slug)
	require.NoError(t, err)
	require.Equal(t, "ready", current.SourceProvenance.State)
	require.Equal(t, newSHA, current.SourceProvenance.Evidence[0].CommitSHA)
	require.Contains(t, current.Content, "T22_NEW_VALIDATED_BODY")

	_, staleErr := wiki.GetPageBySlug(staleCtx, f.kb.ID, first.Slug)
	t22RequireWikiReadDenied(t, nil, staleErr, firstPage)
	staleSearch, staleSearchErr := t22WikiSearchToolResult(t, wiki, f, staleCtx, "T22_NO_MATCH_QUERY_OLD")
	t22RequireWikiSearchEmpty(t, staleSearch, staleSearchErr, firstPage)
}

func TestSourceWikiAnswerLeaseExcludesLaterValidatedCard(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, t22SourceWikiResponse("T22_LATE_VALIDATION"))
	oldCtx, releaseOld, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseOld()
	oldCtx = source.WithWikiAnswerRead(oldCtx)

	validated, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", validated.Status)
	latePage, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, validated.Slug)
	require.NoError(t, err)

	_, pageErr := wiki.GetPageBySlug(oldCtx, f.kb.ID, validated.Slug)
	t22RequireWikiReadDenied(t, nil, pageErr, latePage)
	search, err := wiki.SearchPages(oldCtx, f.kb.ID, "T22_LATE_VALIDATION_SUMMARY", 10)
	require.NoError(t, err)
	require.Empty(t, search, "a card first validated after lease capture is not part of that question")
	read, err := t22WikiReadToolResult(t, wiki, f, oldCtx, validated.Slug)
	t22RequireWikiReadDenied(t, read, err, latePage)
	lateSearch, err := t22WikiSearchToolResult(t, wiki, f, oldCtx, "T22_NO_MATCH_QUERY_LATE")
	t22RequireWikiSearchEmpty(t, lateSearch, err, latePage)

	newCtx, releaseNew, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseNew()
	newCtx = source.WithWikiAnswerRead(newCtx)
	newPage, err := wiki.GetPageBySlug(newCtx, f.kb.ID, validated.Slug)
	require.NoError(t, err)
	require.Contains(t, newPage.Content, "T22_LATE_VALIDATION_BODY")
}

func TestSourceWikiAnswerLeaseRechecksShareRevocationAndSourceClear(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, t22SourceWikiResponse("T22_AUTHORIZED_CARD"))
	validated, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", validated.Status)

	require.NoError(t, f.db.Create(&types.User{ID: "owner", Username: "owner", Email: "owner@example.invalid", TenantID: 1}).Error)
	org := &types.Organization{ID: uuid.NewString(), Name: "Wiki projection share", OwnerID: "owner", OwnerTenantID: 1, InviteCode: "t22-wiki-share"}
	require.NoError(t, f.db.Create(org).Error)
	for _, member := range []*types.OrganizationTenantMember{
		{ID: uuid.NewString(), OrganizationID: org.ID, TenantID: 1, Role: types.OrgRoleAdmin, RepresentativeUserID: "owner"},
		{ID: uuid.NewString(), OrganizationID: org.ID, TenantID: 2, Role: types.OrgRoleViewer, RepresentativeUserID: "owner"},
	} {
		require.NoError(t, f.db.Create(member).Error)
	}
	share, err := f.shares.ShareKnowledgeBase(f.ctx, f.kb.ID, org.ID, "owner", 1, types.OrgRoleViewer)
	require.NoError(t, err)
	foreign := types.WithCaller(context.WithValue(context.Background(), types.TenantIDContextKey, uint64(2)), types.Caller{
		TenantID: 2, UserID: "viewer", Role: types.TenantRoleViewer,
	})
	grant, err := access.ResolveKB(foreign, access.KBRequest{Caller: types.CallerFromContext(foreign)}, f.kb, types.OrgRoleViewer, f.shares, nil)
	require.NoError(t, err)
	viewerCtx, releaseViewer, err := t22BeginWikiAnswerRead(wiki, grant.Context(foreign), t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseViewer()
	viewerPage, err := wiki.GetPageBySlug(viewerCtx, f.kb.ID, validated.Slug)
	require.NoError(t, err)
	require.Contains(t, viewerPage.Content, "T22_AUTHORIZED_CARD_BODY")

	require.NoError(t, f.shares.RemoveShare(f.ctx, share.ID, "owner", 1))
	_, err = wiki.GetPageBySlug(viewerCtx, f.kb.ID, validated.Slug)
	t22RequireWikiReadDenied(t, nil, err, viewerPage)
	viewerTool, viewerToolErr := t22WikiReadToolResult(t, wiki, f, viewerCtx, validated.Slug)
	t22RequireWikiReadDenied(t, viewerTool, viewerToolErr, viewerPage)

	ownerCtx, releaseOwner, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	defer releaseOwner()
	ownerPage, err := wiki.GetPageBySlug(ownerCtx, f.kb.ID, validated.Slug)
	require.NoError(t, err)
	ensureT19LifecycleSchema(t, f)
	_, err = t19LifecycleService(t, f).ClearSource(f.ctx, f.ds.ID, true, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)
	var queryEnabled bool
	require.NoError(t, f.db.Model(&types.DataSource{}).Select("source_query_enabled").Where("id=?", f.ds.ID).Scan(&queryEnabled).Error)
	require.False(t, queryEnabled, "clear disables current source queries at its revocation boundary")
	_, err = wiki.GetPageBySlug(ownerCtx, f.kb.ID, validated.Slug)
	t22RequireWikiReadDenied(t, nil, err, ownerPage)
	ownerTool, ownerToolErr := t22WikiReadToolResult(t, wiki, f, ownerCtx, validated.Slug)
	t22RequireWikiReadDenied(t, ownerTool, ownerToolErr, ownerPage)
}

func TestSourceWikiAnswerLeasePinsRawEvidenceAcrossRevisionPruneAndGC(t *testing.T) {
	f := newJavaSourceFixture(t, map[string][]byte{
		"lib/Unrelated.java": []byte("package lib; class Unrelated { int stableValue() { return 1; } }\n"),
	})
	t22IncludeLibraryPath(t, f)
	syncSourceFixture(t, f)
	firstRun := latestIncrementalRun(t, f)
	wiki, generator := newSourceWikiFixture(t, f, t22SourceWikiResponse("T22_PRUNE_PIN"))
	request := types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID, SourceID: f.ds.ID, ModulePath: "src", Title: "Scheduling module",
	}
	first, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", first.Status)
	firstPage, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	oldEvidence := firstPage.SourceProvenance.Evidence[0]
	require.Equal(t, firstRun.Snapshot.ID, oldEvidence.SnapshotID)

	// Carry this same page version onto a newer applicability snapshot before
	// opening the question. Its lease must pin the newer retrieval snapshot,
	// while the exact raw evidence remains owned by the older snapshot.
	f.advanceFiles(map[string][]byte{
		"lib/Unrelated.java": []byte("package lib; class Unrelated { int stableValue() { return 2; } }\n"),
	})
	syncSourceFixture(t, f)
	carryRun := latestIncrementalRun(t, f)
	require.NotEqual(t, firstRun.Snapshot.ID, carryRun.Snapshot.ID)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	carried, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Equal(t, firstPage.Version, carried.Version)
	require.Equal(t, carryRun.Snapshot.ID, carried.SourceProvenance.ApplicableSnapshotID)
	require.Equal(t, firstRun.Snapshot.ID, carried.SourceProvenance.Evidence[0].SnapshotID)
	require.NotEqual(t, carried.SourceProvenance.ApplicableSnapshotID, carried.SourceProvenance.Evidence[0].SnapshotID,
		"the raw evidence snapshot is intentionally older than current applicability")

	oldCtx, releaseOld, err := t22BeginWikiAnswerRead(wiki, f.ctx, t22WikiTargets(f, f.ds.ID))
	require.NoError(t, err)
	oldCtx = source.WithWikiAnswerRead(oldCtx)
	leaseID, ok := source.ReadLeaseID(oldCtx)
	require.True(t, ok)
	var leaseSnapshotCount int64
	require.NoError(t, f.db.Table("source_read_scopes").Where("lease_id=? AND snapshot_id=?", leaseID, firstRun.Snapshot.ID).Count(&leaseSnapshotCount).Error)
	require.Zero(t, leaseSnapshotCount, "the Wiki answer lease must not accidentally pin the older raw-evidence snapshot as its RAG snapshot")
	require.NoError(t, f.db.Table("source_read_scopes").Where("lease_id=? AND snapshot_id=?", leaseID, carryRun.Snapshot.ID).Count(&leaseSnapshotCount).Error)
	require.EqualValues(t, 1, leaseSnapshotCount, "the lease must pin the carried-forward applicability snapshot")

	f.advanceJava("package demo; public class Service { public String getPushSchedule() { return \"new schedule\"; } }\n")
	syncSourceFixture(t, f)
	drainSourceWikiReviewUpdateLane(t, f, generator)
	generator.(*sourceWikiService).StopSourceWikiBatches()
	updated, err := generator.GenerateModule(f.ctx, request)
	require.NoError(t, err)
	require.Equal(t, "ready", updated.Status)
	current, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Greater(t, current.Version, firstPage.Version)

	// Prune the body revision after the immutable answer projection was pinned.
	revisions := repository.NewWikiPageRepository(f.db)
	require.NoError(t, revisions.PruneRevisions(f.ctx, types.WikiRevisionPruneRequest{
		PageID: firstPage.ID, HardKeepFromVersion: current.Version,
	}))
	var count int64
	require.NoError(t, f.db.Model(&types.WikiPageRevision{}).Where("page_id=? AND version=?", firstPage.ID, firstPage.Version).Count(&count).Error)
	require.Zero(t, count, "the test must remove the old body revision before reading through the lease")

	collector := repository.NewSourceSnapshotRepository(f.db)
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", oldEvidence.SnapshotID).Count(&count).Error)
	require.Positive(t, count, "the old raw-evidence snapshot must still have retrieval artifacts before GC")
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", carryRun.Snapshot.ID).Count(&count).Error)
	require.Positive(t, count, "the lease's applicability snapshot must still have retrieval artifacts before GC")
	_, err = collector.CollectRetiredSourceVersions(f.ctx, 100)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", oldEvidence.SnapshotID).Count(&count).Error)
	require.Zero(t, count, "old retrieval artifacts are collectable independently from exact raw evidence")
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", carryRun.Snapshot.ID).Count(&count).Error)
	require.Positive(t, count, "the active lease continues to protect its distinct applicability/RAG snapshot")
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&count).Error)
	require.EqualValues(t, 1, count, "the active answer projection retains the exact old raw version after pruning")

	pinnedPage, err := wiki.GetPageBySlug(oldCtx, f.kb.ID, first.Slug)
	require.NoError(t, err)
	require.Equal(t, firstPage.Version, pinnedPage.Version)
	require.Contains(t, pinnedPage.Content, "T22_PRUNE_PIN_BODY")
	require.NotContains(t, pinnedPage.Content, "new schedule")
	raw, err := generator.ReadEvidence(oldCtx, f.kb.ID, first.Slug, 0, oldEvidence.ID)
	require.NoError(t, err)
	require.Equal(t, oldEvidence.SHA256, raw.SHA256)
	require.Equal(t, oldEvidence.CommitSHA, raw.CommitSHA)
	require.Contains(t, string(raw.RawContent), "预约")
	require.NotContains(t, string(raw.RawContent), "new schedule")

	releaseOld()
	_, err = collector.CollectRetiredSourceVersions(f.ctx, 100)
	require.NoError(t, err)
	require.NoError(t, f.db.Table("source_file_versions").Where("id=?", oldEvidence.FileVersionID).Count(&count).Error)
	require.Zero(t, count, "release removes the projection owner and leaves ordinary GC to collect the raw version")
	require.NoError(t, f.db.Table("source_chunk_references").Where("snapshot_id=?", carryRun.Snapshot.ID).Count(&count).Error)
	require.Zero(t, count, "release removes the remaining RAG lease protection for the applicability snapshot")
}
