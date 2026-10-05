//go:build integration

package service

import (
	"encoding/json"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestSourceWikiReadProjectionCapacityFailsClosedWithoutBlockingSourceRAG(t *testing.T) {
	f := newJavaSourceFixture(t)
	syncSourceFixture(t, f)
	wiki, generator := newSourceWikiFixture(t, f, func(qa bool) string {
		if qa {
			return `{"supported":true,"reason":"","sections":[0],"uncertain":false}`
		}
		return `{"title":"Scheduling module","summary":"The service returns the push schedule.","sections":[{"text":"getPushSchedule returns the push schedule.","evidence_ids":["e001"],"uncertain":false}]}`
	})
	attempt, err := generator.GenerateModule(f.ctx, types.SourceWikiGenerateRequest{
		KnowledgeBaseID: f.kb.ID,
		SourceID:        f.ds.ID,
		ModulePath:      "src",
		Title:           "Scheduling module",
	})
	require.NoError(t, err)
	require.Equal(t, "ready", attempt.Status, attempt.Reason)

	page, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, attempt.Slug)
	require.NoError(t, err)
	require.NotNil(t, page.SourceProvenance)
	require.Equal(t, "ready", page.SourceProvenance.State)
	require.Len(t, page.SourceProvenance.Evidence, 1)
	require.Equal(t, f.sha, page.SourceProvenance.Evidence[0].CommitSHA)

	// First exercise the public answer path with the real generated evidence.
	readTool := agenttools.NewWikiReadPageTool(
		wiki,
		f.knowledge,
		agenttools.NewWikiScopesFromKBIDs([]string{f.kb.ID}),
		nil,
	)
	readArgs, err := json.Marshal(map[string]any{"slugs": []string{page.Slug}})
	require.NoError(t, err)
	verifiedRead, err := readTool.Execute(f.ctx, readArgs)
	require.NoError(t, err)
	require.True(t, verifiedRead.Success, verifiedRead.Error)
	require.Contains(t, verifiedRead.Output, "getPushSchedule returns the push schedule.")

	// This is only a metadata edit: retain every user-visible and provenance
	// field while making the serialized projection exceed its real byte budget.
	metadata := map[string]json.RawMessage{}
	if len(page.PageMetadata) > 0 && string(page.PageMetadata) != "null" {
		require.NoError(t, json.Unmarshal(page.PageMetadata, &metadata))
	}
	marker, err := json.Marshal(strings.Repeat("x", 17<<20))
	require.NoError(t, err)
	metadata["operator_note"] = marker
	largeMetadata, err := json.Marshal(metadata)
	require.NoError(t, err)
	updatedInput := *page
	updatedInput.PageMetadata = types.JSON(largeMetadata)
	updated, err := wiki.UpdatePage(f.ctx, &updatedInput)
	require.NoError(t, err)
	require.Equal(t, page.Version, updated.Version, "metadata-only update must not create a content revision")
	require.Equal(t, page.Title, updated.Title)
	require.Equal(t, page.Content, updated.Content)
	require.Equal(t, page.Summary, updated.Summary)
	require.Equal(t, page.Aliases, updated.Aliases)
	require.Equal(t, page.SourceRefs, updated.SourceRefs)
	require.Equal(t, page.SourceProvenance, updated.SourceProvenance)
	require.Equal(t, "ready", updated.SourceProvenance.State)
	require.Greater(t, len(updated.PageMetadata), 16<<20)

	// Confirm applicability and the exact raw evidence still resolve publicly
	// after the metadata write; capacity must not be reached by an invalid card.
	applicable, err := wiki.GetPageBySlug(f.ctx, f.kb.ID, page.Slug)
	require.NoError(t, err)
	require.Equal(t, "ready", applicable.SourceProvenance.State)
	require.Equal(t, page.SourceProvenance, applicable.SourceProvenance)
	evidence, err := generator.ReadEvidence(f.ctx, f.kb.ID, page.Slug, 0, page.SourceProvenance.Evidence[0].ID)
	require.NoError(t, err)
	require.Equal(t, f.sha, evidence.CommitSHA)
	require.Contains(t, evidence.Content, "getPushSchedule")

	targets := types.SearchTargets{&types.SearchTarget{
		Type:            types.SearchTargetTypeKnowledgeBase,
		KnowledgeBaseID: f.kb.ID,
		SourceIDs:       []string{f.ds.ID},
	}}
	readCtx, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	released := false
	defer func() {
		if !released {
			release()
		}
	}()
	leaseID, ok := source.ReadLeaseID(readCtx)
	require.True(t, ok)

	// Source keyword retrieval remains available on the very same lease.
	hits, err := f.kbs.HybridSearch(readCtx, f.kb.ID, types.SearchParams{
		QueryText:          "getPushSchedule",
		MatchCount:         10,
		DisableVectorMatch: true,
		SourceIDs:          []string{f.ds.ID},
	})
	require.NoError(t, err)
	require.NotEmpty(t, hits)
	require.Contains(t, hits[0].Content, "getPushSchedule")

	// The same lease must expose an explicit Wiki-only capacity error.
	answerCtx := source.WithWikiAnswerRead(readCtx)
	_, _, err = wiki.(interfaces.WikiReadService).BeginWikiRead(answerCtx, targets)
	require.ErrorContains(t, err, "capacity_exceeded")

	var capture struct {
		Status             string
		PageCount          int64
		ProjectionBytes    int64
		EvidenceOwnerCount int64
	}
	require.NoError(t, f.db.Table("source_read_wiki_projection_captures").
		Select("status,page_count,projection_bytes,evidence_owner_count").
		Where("lease_id=?", leaseID).Take(&capture).Error)
	require.Equal(t, "capacity_exceeded", capture.Status)
	require.Greater(t, capture.PageCount, int64(0), "the fully validated source card must be counted")
	require.LessOrEqual(t, capture.PageCount, int64(1024), "the page-count budget must not be the cause")
	require.Greater(t, capture.ProjectionBytes, int64(16<<20))
	require.Greater(t, capture.EvidenceOwnerCount, int64(0), "the valid card must have real raw evidence owners")
	require.LessOrEqual(t, capture.EvidenceOwnerCount, int64(32768), "the evidence-owner budget must not be the cause")

	var projectionRows, pinnedOwners int64
	require.NoError(t, f.db.Table("source_read_wiki_page_projections").Where("lease_id=?", leaseID).Count(&projectionRows).Error)
	require.NoError(t, f.db.Table("source_read_wiki_evidence_refs").Where("lease_id=?", leaseID).Count(&pinnedOwners).Error)
	require.Zero(t, projectionRows, "capacity failure must not retain a partial card projection")
	require.Zero(t, pinnedOwners, "capacity failure must not retain partial raw evidence owners")

	release()
	released = true
	var leasesAfterRelease, capturesAfterRelease, projectionsAfterRelease, ownersAfterRelease int64
	require.NoError(t, f.db.Table("source_read_leases").Where("id=?", leaseID).Count(&leasesAfterRelease).Error)
	require.NoError(t, f.db.Table("source_read_wiki_projection_captures").Where("lease_id=?", leaseID).Count(&capturesAfterRelease).Error)
	require.NoError(t, f.db.Table("source_read_wiki_page_projections").Where("lease_id=?", leaseID).Count(&projectionsAfterRelease).Error)
	require.NoError(t, f.db.Table("source_read_wiki_evidence_refs").Where("lease_id=?", leaseID).Count(&ownersAfterRelease).Error)
	require.Zero(t, leasesAfterRelease)
	require.Zero(t, capturesAfterRelease)
	require.Zero(t, projectionsAfterRelease)
	require.Zero(t, ownersAfterRelease)
}
