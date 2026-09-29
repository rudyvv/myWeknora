//go:build integration

package service

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agenttools "github.com/Tencent/WeKnora/internal/agent/tools"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestSourceScriptsPublishDualIndexesToolsAndFixedOriginals(t *testing.T) {
	files := map[string][]byte{}
	for _, name := range []string{"booking.js", "reservations.ts"} {
		original, err := os.ReadFile(filepath.Join("..", "..", "..", "sourceparser", "tests", "fixtures", name))
		require.NoError(t, err)
		files["src/"+name] = original
	}
	f := newJavaSourceFixture(t, files)
	// Model/GitLab are external boundaries; Git, parsing, PG and both indexes are real.
	f.embeddingForText = func(text string) []float32 {
		switch {
		case strings.Contains(text, "createBooking") || strings.Contains(text, "创建预约"):
			return []float32{1, 0, 0}
		case strings.Contains(text, "reserve") || strings.Contains(text, "审核预约"):
			return []float32{0, 1, 0}
		default:
			return []float32{0, 0, 1}
		}
	}
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync, "selected JS/TS and Java must be admitted by the real parser's language health")
	syncSourceFixture(t, f)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	pinned, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	for _, example := range []struct{ path, symbol, question string }{
		{"src/booking.js", "createBooking", "创建预约时怎样提交名称"},
		{"src/reservations.ts", "reserve", "审核预约请求后如何返回数据"},
	} {
		t.Run(example.path, func(t *testing.T) {
			for _, query := range []struct {
				text    string
				keyword bool
			}{
				{example.symbol, true}, {example.question, false},
			} {
				hits, searchErr := f.kbs.HybridSearch(pinned, f.kb.ID, types.SearchParams{
					QueryText: query.text, MatchCount: 1, SourceIDs: []string{f.ds.ID},
					DisableVectorMatch: query.keyword, DisableKeywordsMatch: !query.keyword, SkipContextEnrichment: true,
				})
				require.NoError(t, searchErr)
				require.Len(t, hits, 1, "known symbol and independently labelled semantic query must reach real indexes")
				hit := hits[0]
				require.Equal(t, example.path, hit.Metadata["source_path"])
				require.Equal(t, f.sha, hit.Metadata["commit_sha"])
				var envelope struct {
					Source types.SourceEvidence `json:"source"`
				}
				require.NoError(t, json.Unmarshal(hit.ChunkMetadata, &envelope))
				require.Equal(t, "structural", envelope.Source.Quality)
				require.Contains(t, envelope.Source.GitLabURL, "/-/blob/"+f.sha+"/"+example.path+"#L1-")
				original := files[example.path]
				require.Equal(t, string(original), hit.Content)
				require.Equal(t, 0, envelope.Source.Range.StartByte)
				require.Equal(t, len(original), envelope.Source.Range.EndByte)
				view, readErr := f.knowledge.GetSourceFile(pinned, hit.KnowledgeID, envelope.Source.FileVersionID)
				require.NoError(t, readErr)
				require.Equal(t, string(original), view.Content)
				require.Equal(t, f.sha, view.CommitSHA)
				var symbols []types.SourceSymbol
				require.NoError(t, json.Unmarshal(view.Symbols, &symbols))
				var chosen *types.SourceSymbol
				for i := range symbols {
					if symbols[i].Name == example.symbol {
						chosen = &symbols[i]
					}
				}
				require.NotNil(t, chosen)
				require.Contains(t, chosen.QualifiedName, example.symbol)
				if example.symbol == "reserve" {
					require.Equal(t, "src/reservations.ts.Reservations.Scheduler.reserve", chosen.QualifiedName)
					require.Equal(t, "@trace", chosen.Annotations[0].Text)
					require.Equal(t, 243, chosen.Range.StartByte)
					require.Equal(t, 7, chosen.Range.StartLine)
				} else {
					require.Equal(t, types.SourceRange{StartByte: 62, EndByte: 137, StartLine: 3, EndLine: 5}, chosen.Range)
				}
				reader, filename, readErr := f.knowledge.GetKnowledgeFile(pinned, hit.KnowledgeID)
				require.NoError(t, readErr)
				downloaded, readErr := io.ReadAll(reader)
				require.NoError(t, reader.Close())
				require.NoError(t, readErr)
				require.Equal(t, filepath.Base(example.path), filename)
				require.Equal(t, original, downloaded)
				args, encodeErr := json.Marshal(map[string]any{"knowledge_id": hit.KnowledgeID, "query": example.symbol})
				require.NoError(t, encodeErr)
				toolResult, toolErr := agenttools.NewWikiReadSourceDocTool(f.knowledge, f.chunks, targets).Execute(pinned, args)
				require.NoError(t, toolErr)
				require.True(t, toolResult.Success, toolResult.Error)
				require.Contains(t, toolResult.Output, example.symbol)
				toolChunks, encodeErr := json.Marshal(toolResult.Data["chunks"])
				require.NoError(t, encodeErr)
				var readChunks []struct {
					Evidence types.SourceEvidence `json:"source_evidence"`
					Content  string               `json:"content"`
				}
				require.NoError(t, json.Unmarshal(toolChunks, &readChunks))
				require.Len(t, readChunks, 1)
				require.Equal(t, envelope.Source, readChunks[0].Evidence)
				require.Equal(t, string(original), readChunks[0].Content)
				require.Contains(t, toolResult.Output, example.path)
				require.ErrorContains(t, f.chunks.DeleteChunk(pinned, hit.ID), "Git-managed")
				outside, outsideErr := f.kbs.HybridSearch(pinned, f.kb.ID, types.SearchParams{
					QueryText: example.symbol, MatchCount: 10, KnowledgeIDs: []string{uuid.NewString()},
				})
				require.NoError(t, outsideErr)
				require.Empty(t, outside, "JS/TS cannot widen an established question's file scope")
			}
			grepArgs, encodeErr := json.Marshal(map[string]any{"query": example.symbol})
			require.NoError(t, encodeErr)
			result, toolErr := agenttools.NewSourceAwareGrepChunksTool(f.db, targets).Execute(pinned, grepArgs)
			require.NoError(t, toolErr)
			require.True(t, result.Success, result.Error)
			toolChunks, encodeErr := json.Marshal(result.Data["chunk_results"])
			require.NoError(t, encodeErr)
			var grepChunks []struct {
				Evidence types.SourceEvidence `json:"source_evidence"`
			}
			require.NoError(t, json.Unmarshal(toolChunks, &grepChunks))
			require.Len(t, grepChunks, 1)
			require.Equal(t, f.sha, grepChunks[0].Evidence.CommitSHA)
			require.Equal(t, example.path, grepChunks[0].Evidence.Path)
			require.Equal(t, 0, grepChunks[0].Evidence.Range.StartByte)
			require.Equal(t, len(files[example.path]), grepChunks[0].Evidence.Range.EndByte)
			require.Equal(t, 1, grepChunks[0].Evidence.Range.StartLine)
			expectedEndLine := 6
			if strings.HasSuffix(example.path, ".ts") {
				expectedEndLine = 14
			}
			require.Equal(t, expectedEndLine, grepChunks[0].Evidence.Range.EndLine)
			require.Contains(t, result.Output, example.path)
			require.Contains(t, result.Output, example.symbol)
		})
	}
}

func TestSourceScriptMissingOfflineGrammarBlocksPreviewAndPublication(t *testing.T) {
	// Create a valid legacy Java-only cache from verified bytes, without downloads.
	cache := os.Getenv("SOURCE_PARSER_CACHE")
	originalLock, err := os.ReadFile(filepath.Join(cache, "grammar.lock.json"))
	require.NoError(t, err)
	var locked struct {
		PackVersion string `json:"pack_version"`
		BundleSHA   string `json:"bundle_sha256"`
		Grammars    map[string]struct {
			Grammar string `json:"grammar"`
			SHA     string `json:"grammar_sha256"`
		} `json:"grammars"`
	}
	require.NoError(t, json.Unmarshal(originalLock, &locked))
	java := locked.Grammars["java"]
	require.NotEmpty(t, java.Grammar)
	legacyCache := t.TempDir()
	grammar, err := os.ReadFile(filepath.Join(cache, filepath.FromSlash(java.Grammar)))
	require.NoError(t, err)
	destination := filepath.Join(legacyCache, filepath.FromSlash(java.Grammar))
	require.NoError(t, os.MkdirAll(filepath.Dir(destination), 0755))
	require.NoError(t, os.WriteFile(destination, grammar, 0644))
	legacyLock, err := json.Marshal(map[string]any{"pack_version": locked.PackVersion, "bundle_sha256": locked.BundleSHA, "grammar": java.Grammar, "grammar_sha256": java.SHA})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(legacyCache, "grammar.lock.json"), legacyLock, 0644))
	t.Setenv("SOURCE_PARSER_CACHE", legacyCache)
	f := newJavaSourceFixture(t, map[string][]byte{"src/reservations.ts": []byte("export function reserve() { return '预约'; }\r\n")})
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.False(t, preview.CanSync)
	for _, check := range preview.Checks {
		if check.Name == "parser" {
			require.False(t, check.Ready, "Java-only grammar health must not admit selected TS")
		}
	}
	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: f.ds.ID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	require.ErrorContains(t, f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload)), "language grammar is not ready")
	finished, err := f.service.GetSyncLog(f.ctx, log.ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusFailed, finished.Status)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "reserve", MatchCount: 10, SourceIDs: []string{f.ds.ID}})
	require.NoError(t, err)
	require.Empty(t, hits, "an absent grammar must not publish partial Java-only content")
}

func TestSourceBrokenScriptsKeepReadableOriginalAndExplicitQualityAfterSync(t *testing.T) {
	files := map[string][]byte{
		"src/broken.js": []byte("\ufeffexport function valid() { return '中文😀'; }\r\nexport function broken( {\r\nconst readableAfterError = '坏语法后保留JS';\r\n"),
		"src/broken.ts": []byte("export type BookingId = string;\r\nexport function broken( {\r\nconst readableAfterError = '坏语法后保留TS😀';\r\n"),
	}
	f := newJavaSourceFixture(t, files)
	syncSourceFixture(t, f)
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "readableAfterError", MatchCount: 10, DisableVectorMatch: true, SkipContextEnrichment: true})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	for _, hit := range hits {
		var envelope struct {
			Source types.SourceEvidence `json:"source"`
		}
		require.NoError(t, json.Unmarshal(hit.ChunkMetadata, &envelope))
		require.Equal(t, "syntax_error", envelope.Source.Quality)
		view, err := f.knowledge.GetSourceFile(f.ctx, hit.KnowledgeID, envelope.Source.FileVersionID)
		require.NoError(t, err)
		require.Equal(t, "syntax_error", view.Quality)
		require.Equal(t, f.sha, view.CommitSHA)
		require.Equal(t, string(files[view.Path]), view.Content)
		require.Equal(t, string(files[view.Path]), hit.Content)
		require.Equal(t, 0, envelope.Source.Range.StartByte)
		require.Equal(t, len(files[view.Path]), envelope.Source.Range.EndByte)
	}
}

func TestSourceScriptsKeepQuestionSnapshotAcrossNewScriptPublication(t *testing.T) {
	files := map[string][]byte{
		"src/booking.js":      []byte("export function reserveJS() { return '旧JS预约😀'; }\r\n"),
		"src/reservations.ts": []byte("export function reserveTS(): string { return '旧TS预约😀'; }\r\n"),
	}
	f := newJavaSourceFixture(t, files)
	syncSourceFixture(t, f)
	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	pinned, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	oldHits, err := f.kbs.HybridSearch(pinned, f.kb.ID, types.SearchParams{QueryText: "reserveJS reserveTS", MatchCount: 10, DisableVectorMatch: true})
	require.NoError(t, err)
	require.Len(t, oldHits, 2)
	oldVersions := map[string]string{}
	for _, hit := range oldHits {
		view, err := f.knowledge.GetSourceFile(pinned, hit.KnowledgeID)
		require.NoError(t, err)
		oldVersions[hit.KnowledgeID] = view.FileVersionID
	}
	updated := map[string][]byte{
		"src/booking.js":      []byte("export function reserveJS() { return '新JS预约😀'; }\r\n"),
		"src/reservations.ts": []byte("export function reserveTS(): string { return '新TS预约😀'; }\r\n"),
	}
	newSHA := f.advanceFiles(updated)
	syncSourceFixture(t, f)
	for _, question := range []struct {
		ctx       context.Context
		sha       string
		originals map[string][]byte
	}{
		{pinned, f.sha, files}, {f.ctx, newSHA, updated},
	} {
		for _, keyword := range []bool{true, false} {
			hits, err := f.kbs.HybridSearch(question.ctx, f.kb.ID, types.SearchParams{QueryText: "reserveJS reserveTS", MatchCount: 10, DisableVectorMatch: keyword, DisableKeywordsMatch: !keyword, SourceIDs: []string{f.ds.ID}})
			require.NoError(t, err)
			// The vector fixture also returns Java; assert both script members independently.
			foundScripts := 0
			for _, hit := range hits {
				path := hit.Metadata["source_path"]
				expected, script := question.originals[path]
				if !script {
					continue
				}
				foundScripts++
				require.Equal(t, question.sha, hit.Metadata["commit_sha"])
				require.Equal(t, string(expected), hit.Content)
				view, err := f.knowledge.GetSourceFile(question.ctx, hit.KnowledgeID)
				require.NoError(t, err)
				require.Equal(t, question.sha, view.CommitSHA)
				require.Equal(t, string(expected), view.Content)
				args, err := json.Marshal(map[string]any{"knowledge_id": hit.KnowledgeID, "query": "reserve"})
				require.NoError(t, err)
				output, err := agenttools.NewWikiReadSourceDocTool(f.knowledge, f.chunks, targets).Execute(question.ctx, args)
				require.NoError(t, err)
				require.True(t, output.Success, output.Error)
				require.Contains(t, output.Output, string(expected))
				raw, err := json.Marshal(output.Data["chunks"])
				require.NoError(t, err)
				var chunks []struct {
					Evidence types.SourceEvidence `json:"source_evidence"`
				}
				require.NoError(t, json.Unmarshal(raw, &chunks))
				require.Len(t, chunks, 1)
				require.Equal(t, view.CommitSHA, chunks[0].Evidence.CommitSHA)
				require.Equal(t, view.FileVersionID, chunks[0].Evidence.FileVersionID)
			}
			require.Equal(t, 2, foundScripts)
		}
	}
	for fileID, versionID := range oldVersions {
		view, err := f.knowledge.GetSourceFile(pinned, fileID, versionID)
		require.NoError(t, err)
		require.Equal(t, f.sha, view.CommitSHA)
		_, err = f.knowledge.GetSourceFile(f.ctx, fileID, versionID)
		require.Error(t, err, "current reads must not silently fall back to an old explicit file version")
	}
}
