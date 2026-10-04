package repository

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceWikiPageProjectionMatchesRevision(t *testing.T) {
	newPair := func() (*types.WikiPage, *types.WikiPageRevision) {
		provenance := func() *types.SourceWikiProvenance {
			return &types.SourceWikiProvenance{
				SourceID: "source-a", TopicKind: "module", TopicKey: "module/src",
				State: "ready", ApplicableSnapshotID: "snapshot-a",
				Evidence: []types.SourceWikiEvidence{{ID: "e001", KnowledgeID: "file-a"}},
			}
		}
		page := &types.WikiPage{
			ID: "page-a", TenantID: 1, KnowledgeBaseID: "kb-a", Version: 4,
			Title: "Scheduling", PageType: "concept", Status: types.WikiPageStatusPublished,
			Content: "Supported body", Summary: "Supported summary",
			Aliases: types.StringArray{"schedule"}, SourceRefs: types.StringArray{"file-a"},
			ChunkRefs: types.StringArray{"chunk-a"}, PageMetadata: types.JSON(`{"managed":true}`),
			SourceProvenance: provenance(),
		}
		revision := &types.WikiPageRevision{
			ID: "revision-a", PageID: page.ID, TenantID: page.TenantID, KnowledgeBaseID: page.KnowledgeBaseID,
			Version: 3, Title: page.Title, PageType: "summary", Status: page.Status,
			Content: page.Content, Summary: page.Summary, Aliases: append(types.StringArray(nil), page.Aliases...),
			SourceRefs: append(types.StringArray(nil), page.SourceRefs...), ChunkRefs: append(types.StringArray(nil), page.ChunkRefs...),
			PageMetadata: append(types.JSON(nil), page.PageMetadata...), SourceProvenance: provenance(),
		}
		return page, revision
	}

	t.Run("page type metadata change preserves source projection", func(t *testing.T) {
		page, revision := newPair()
		require.True(t, sourceWikiPageProjectionMatchesRevision(page, revision))
	})

	mutations := map[string]func(*types.WikiPage){
		"title":             func(page *types.WikiPage) { page.Title += " changed" },
		"body":              func(page *types.WikiPage) { page.Content += " changed" },
		"summary":           func(page *types.WikiPage) { page.Summary += " changed" },
		"aliases":           func(page *types.WikiPage) { page.Aliases = append(page.Aliases, "new alias") },
		"source refs":       func(page *types.WikiPage) { page.SourceRefs = append(page.SourceRefs, "file-b") },
		"chunk refs":        func(page *types.WikiPage) { page.ChunkRefs = append(page.ChunkRefs, "chunk-b") },
		"page metadata":     func(page *types.WikiPage) { page.PageMetadata = types.JSON(`{"managed":false}`) },
		"source provenance": func(page *types.WikiPage) { page.SourceProvenance.State = "stale" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			page, revision := newPair()
			mutate(page)
			require.False(t, sourceWikiPageProjectionMatchesRevision(page, revision))
		})
	}

	t.Run("identity and version mismatches fail closed", func(t *testing.T) {
		for name, mutate := range map[string]func(*types.WikiPage){
			"page":     func(page *types.WikiPage) { page.ID = "other-page" },
			"tenant":   func(page *types.WikiPage) { page.TenantID++ },
			"KB":       func(page *types.WikiPage) { page.KnowledgeBaseID = "other-kb" },
			"version":  func(page *types.WikiPage) { page.Version++ },
			"bad JSON": func(page *types.WikiPage) { page.PageMetadata = types.JSON(`{`) },
		} {
			t.Run(name, func(t *testing.T) {
				page, revision := newPair()
				mutate(page)
				require.False(t, sourceWikiPageProjectionMatchesRevision(page, revision))
			})
		}
	})
}
