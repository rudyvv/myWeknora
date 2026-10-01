package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestKeywordOnlyKeepsSourceCodeTiersAheadOfBM25Score(t *testing.T) {
	exact := &types.IndexWithScore{ChunkID: "exact", Score: 0.01, KeywordTier: 0, HasKeywordTier: true}
	closeMatch := &types.IndexWithScore{ChunkID: "normalized", Score: 8, KeywordTier: 1, HasKeywordTier: true}
	bm25 := &types.IndexWithScore{ChunkID: "bm25", Score: 100, KeywordTier: 2, HasKeywordTier: true}
	deduplicated := fuseOrDeduplicate(context.Background(), nil, []*types.IndexWithScore{bm25, closeMatch, exact}, nil)

	if len(deduplicated) != 3 || deduplicated[0].ChunkID != "exact" || deduplicated[1].ChunkID != "normalized" || deduplicated[2].ChunkID != "bm25" {
		t.Fatalf("keyword-only order = %v, want exact > normalized > BM25", chunkIDs(deduplicated))
	}
	if exact.Score != 0.01 {
		t.Fatalf("exact candidate score was modified: %v", exact.Score)
	}
}

func TestHybridRRFConsumesExplicitKeywordTierOrderWithoutChangingWeights(t *testing.T) {
	vector := []*types.IndexWithScore{{ChunkID: "bm25", Score: 0.99}, {ChunkID: "exact", Score: 0.2}}
	keywords := []*types.IndexWithScore{
		{ChunkID: "bm25", Score: 100, KeywordTier: 2, HasKeywordTier: true},
		{ChunkID: "exact", Score: 0.01, KeywordTier: 0, HasKeywordTier: true},
	}
	results := fuseOrDeduplicate(context.Background(), vector, keywords, &types.RetrievalConfig{RRFK: 60, RRFVectorWeight: 1, RRFKeywordWeight: 1})
	if len(results) != 2 || results[0].ChunkID != "bm25" {
		t.Fatalf("RRF result order = %v; exact keyword match must improve its rank, not override vector fusion", chunkIDs(results))
	}
}

func TestOrdinaryKeywordResultsKeepExistingScoreOrdering(t *testing.T) {
	results := fuseOrDeduplicate(context.Background(), nil, []*types.IndexWithScore{
		{ChunkID: "lower", Score: 1}, {ChunkID: "higher", Score: 2},
	}, nil)
	if len(results) != 2 || results[0].ChunkID != "higher" {
		t.Fatalf("ordinary keyword order changed: %v", chunkIDs(results))
	}
}

func chunkIDs(results []*types.IndexWithScore) []string {
	ids := make([]string, len(results))
	for i, result := range results {
		ids[i] = result.ChunkID
	}
	return ids
}
