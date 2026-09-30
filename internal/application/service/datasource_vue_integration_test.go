//go:build integration

package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/Tencent/WeKnora/internal/modelcontext"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestSourceVueSFCRegionsPublishAndScopeExternalScriptResolution(t *testing.T) {
	component := []byte("<!-- 😀 before -->\r\n" +
		"<template lang=\"html\">\r\n" +
		"<div><button v-on:click.native=\"reservePanel\">预约😀</button><slot name=\"legacy\" slot-scope=\"p\">{{ p.value }}</slot></div>\r\n" +
		"</template>\r\n" +
		"<script lang=\"ts\">\r\n" +
		"import { send } from \"./api.js\";\r\n" +
		"export default { methods: { reservePanel(name: string) { return send(name); } } };\r\n" +
		"</script>\r\n" +
		"<style lang=\"scss\" scoped>\r\n.panel { color: red; }\r\n</style>\r\n" +
		"<i18n lang=\"json\">\r\n{\"title\":\"🧭\"}\r\n</i18n>\r\n")
	externalVue := []byte("<template><div>external entry</div></template>\r\n" +
		"<script src=\"./api.js\"></script>\r\n")
	selfClosingExternalVue := []byte("<script src=\"./api.js\"/>")
	crossDirectoryExternalVue := []byte("<script src=\"../shared/api.js\"></script>\r\n")
	whitespaceExternalVue := []byte("<script src=\"./api.js\"> \r\n\t </script>\r\n")
	rejectedVue := []byte("<script src=\"../../../private.js\"></script>\r\n")
	missingVue := []byte("<script src=\"./missing.js\"></script>\r\n")
	unknownVue := []byte("<template lang=\"pug\">\r\nsection unknown\r\n</template>\r\n")
	dialectVue := []byte("<template lang=\"ts\">templateDialectMarker</template>\r\n" +
		"<style lang=\"json\">styleDialectMarker</style>\r\n" +
		"<script lang=\"ts\">\r\nexport const scriptDialectMarker = 1;\r\n</script>\r\n")
	diagnosticVue := []byte("<template><div>first</template>\r\n" +
		"<style lang=\"scss\">.diagnosticStyleMarker { color: red; }</style>\r\n")
	eofDiagnosticVue := []byte("<script>const a=1")
	api := []byte("export function send(name) { return `预约 ${name}`; }\r\n")
	files := map[string][]byte{
		"src/components/BookingPanel.vue":       component,
		"src/components/ExternalPanel.vue":      externalVue,
		"src/components/SelfClosingPanel.vue":   selfClosingExternalVue,
		"src/pages/Panel.vue":                   crossDirectoryExternalVue,
		"src/components/RejectedPanel.vue":      rejectedVue,
		"src/components/MissingPanel.vue":       missingVue,
		"src/components/UnknownPanel.vue":       unknownVue,
		"src/components/DialectPanel.vue":       dialectVue,
		"src/components/DiagnosticPanel.vue":    diagnosticVue,
		"src/components/EOFDiagnosticPanel.vue": eofDiagnosticVue,
		"src/components/api.js":                 api,
		"src/shared/api.js":                     api,
	}
	f := newJavaSourceFixture(t, files)
	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	require.True(t, preview.CanSync, "Vue, JavaScript and the embedded TypeScript dialect must be ready")
	syncSourceFixture(t, f)

	for _, keywordsOnly := range []bool{true, false} {
		hits, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
			QueryText: "reservePanel", MatchCount: 100, SourceIDs: []string{f.ds.ID},
			DisableVectorMatch: !keywordsOnly, DisableKeywordsMatch: keywordsOnly,
			SkipContextEnrichment: true,
		})
		require.NoError(t, searchErr)
		var hit *types.SearchResult
		var evidence types.SourceEvidence
		for _, candidate := range hits {
			if candidate.Metadata["source_path"] == "src/components/BookingPanel.vue" && strings.Contains(candidate.Content, "reservePanel") {
				var candidateEvidence struct {
					Source types.SourceEvidence `json:"source"`
				}
				require.NoError(t, json.Unmarshal(candidate.ChunkMetadata, &candidateEvidence))
				if candidateEvidence.Source.Region.Kind == "script" {
					hit = candidate
					evidence = candidateEvidence.Source
					break
				}
			}
		}
		require.NotNil(t, hit, "both keyword and vector routes must retrieve the Vue script body")
		require.Equal(t, f.sha, hit.Metadata["commit_sha"])
		require.Equal(t, "script", evidence.Region.Kind)
		require.Equal(t, "ts", evidence.Region.Language)
		require.Equal(t, "structural", evidence.Region.Quality)
		area := evidence.Range
		require.Equal(t, string(component[area.StartByte:area.EndByte]), hit.Content,
			"evidence is one verifiable contiguous region, never concatenated across SFC blocks")
		require.Contains(t, evidence.GitLabURL, "/-/blob/"+f.sha+"/src/components/BookingPanel.vue#L")
	}

	findIndexedEvidence := func(query, logicalPath, contentMarker string) (*types.SearchResult, *types.SourceEvidence) {
		t.Helper()
		hits, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
			QueryText: query, MatchCount: 100, SourceIDs: []string{f.ds.ID},
			DisableVectorMatch: true, SkipContextEnrichment: true,
		})
		require.NoError(t, searchErr)
		for _, candidate := range hits {
			if candidate.Metadata["source_path"] != logicalPath || !strings.Contains(candidate.Content, contentMarker) {
				continue
			}
			return candidate, source.Evidence(candidate.ChunkMetadata)
		}
		return nil, nil
	}
	for _, test := range []struct {
		query, marker, kind, language, quality string
	}{
		{"templateDialectMarker", "templateDialectMarker", "template", "ts", "unknown_preprocess"},
		{"styleDialectMarker", "styleDialectMarker", "style", "json", "unknown_preprocess"},
		{"scriptDialectMarker", "scriptDialectMarker", "script", "ts", "structural"},
	} {
		hit, evidence := findIndexedEvidence(test.query, "src/components/DialectPanel.vue", test.marker)
		require.NotNil(t, hit, "keyword retrieval should find the indexed %s block", test.kind)
		require.NotNil(t, evidence)
		require.NotNil(t, evidence.Region)
		require.Equal(t, test.kind, evidence.Region.Kind)
		require.Equal(t, test.language, evidence.Region.Language)
		require.Equal(t, test.quality, evidence.Region.Quality)
		require.Equal(t, test.quality, evidence.Quality)
		area := evidence.Range
		require.Equal(t, string(dialectVue[area.StartByte:area.EndByte]), hit.Content)
	}

	diagnosticHit, diagnosticEvidence := findIndexedEvidence(
		"descriptor warning", "src/components/DiagnosticPanel.vue", "first")
	require.NotNil(t, diagnosticHit, "the parser's safe warning label should be searchable from published index text")
	require.NotNil(t, diagnosticEvidence)
	require.NotNil(t, diagnosticEvidence.Region)
	require.Equal(t, "template", diagnosticEvidence.Region.Kind)
	require.Equal(t, "degraded", diagnosticEvidence.Quality)
	require.Equal(t, "degraded", diagnosticEvidence.Region.Quality)
	diagnosticArea := diagnosticEvidence.Range
	require.Equal(t, string(diagnosticVue[diagnosticArea.StartByte:diagnosticArea.EndByte]), diagnosticHit.Content)
	require.Len(t, diagnosticEvidence.Diagnostics, 1)
	require.Equal(t, "vue_sfc_parse_warning", diagnosticEvidence.Diagnostics[0].Code)
	require.Equal(t, types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 1, EndLine: 1}, diagnosticEvidence.Diagnostics[0].Range)
	modelOutput := modelcontext.NewRegistry(true).ModelToolResultForTool("knowledge_search", &types.ToolResult{
		Success: true, Data: map[string]interface{}{
			"display_type": "search_results",
			"results": []map[string]interface{}{{
				"chunk_id": diagnosticHit.ID, "knowledge_id": diagnosticHit.KnowledgeID,
				"knowledge_base_id": f.kb.ID, "knowledge_title": diagnosticHit.KnowledgeTitle,
				"content": diagnosticHit.Content, "source_evidence": diagnosticEvidence,
			}},
		},
	})
	require.Contains(t, modelOutput, `sfc_diagnostics="vue_sfc_parse_warning@L1-L1:`)
	require.Contains(t, modelOutput, "original bytes are retained")
	require.NotContains(t, modelOutput, `resolved_path=`)
	require.NotContains(t, modelOutput, `message=`)

	eofHit, eofEvidence := findIndexedEvidence(
		"descriptor warning", "src/components/EOFDiagnosticPanel.vue", "const a=1")
	require.NotNil(t, eofHit)
	require.NotNil(t, eofEvidence)
	require.NotNil(t, eofEvidence.Region)
	require.Equal(t, "script", eofEvidence.Region.Kind)
	require.Equal(t, "degraded", eofEvidence.Quality)
	require.Equal(t, "degraded", eofEvidence.Region.Quality)
	eofArea := eofEvidence.Range
	require.Equal(t, string(eofDiagnosticVue[eofArea.StartByte:eofArea.EndByte]), eofHit.Content)
	require.Len(t, eofEvidence.Diagnostics, 1)
	require.Equal(t, types.SourceRange{
		StartByte: len(eofDiagnosticVue), EndByte: len(eofDiagnosticVue), StartLine: 1, EndLine: 1,
	}, eofEvidence.Diagnostics[0].Range)

	styleHit, styleEvidence := findIndexedEvidence(
		"diagnosticStyleMarker", "src/components/DiagnosticPanel.vue", "diagnosticStyleMarker")
	require.NotNil(t, styleHit)
	require.NotNil(t, styleEvidence)
	require.NotNil(t, styleEvidence.Region)
	require.Equal(t, "style", styleEvidence.Region.Kind)
	require.Equal(t, "structural", styleEvidence.Quality)
	styleArea := styleEvidence.Range
	require.Equal(t, string(diagnosticVue[styleArea.StartByte:styleArea.EndByte]), styleHit.Content)
	require.Empty(t, styleEvidence.Diagnostics, "a neighboring unaffected block must not inherit the template warning")

	var bookingID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/BookingPanel.vue").Scan(&bookingID).Error)
	bookingView, err := f.knowledge.GetSourceFile(f.ctx, bookingID)
	require.NoError(t, err)
	require.Equal(t, string(component), bookingView.Content)
	var bookingSymbols []types.SourceSymbol
	require.NoError(t, json.Unmarshal(bookingView.Symbols, &bookingSymbols))
	regions := map[string]bool{}
	var reserve *types.SourceSymbol
	for index := range bookingSymbols {
		symbol := &bookingSymbols[index]
		if symbol.Kind == "sfc_region" {
			regions[symbol.Region.Kind] = true
		}
		if symbol.Name == "reservePanel" {
			reserve = symbol
		}
	}
	for _, region := range []string{"template", "script", "style", "custom"} {
		require.True(t, regions[region], "published source-file view should expose the %s region marker", region)
	}
	require.NotNil(t, reserve)
	require.Equal(t, "ts", reserve.Region.Language)
	require.Equal(t, "structural", reserve.Region.Quality)

	parserURL := strings.TrimRight(strings.TrimSpace(os.Getenv("SOURCE_PARSER_URL")), "/")
	componentParsed, err := source.ParseFile(f.ctx, parserURL, "src/components/BookingPanel.vue", component)
	require.NoError(t, err, "the production Go parser client must accept non-empty Vue template, script, and style blocks")
	parsedRegions := map[string]bool{}
	for _, symbol := range componentParsed.Symbols {
		require.Equal(t, symbol.Signature, string(component[symbol.SignatureRange.StartByte:symbol.SignatureRange.EndByte]),
			"every parser signature range must select its exact original UTF-8 bytes")
		if symbol.Kind == "sfc_region" && symbol.Region != nil {
			parsedRegions[symbol.Region.Kind] = true
		}
	}
	for _, region := range []string{"template", "script", "style"} {
		require.True(t, parsedRegions[region], "the production parser client should accept the non-empty %s region", region)
	}
	jsxVue := []byte("<script lang=\"jsx\">\r\nexport const Widget = () => <button>go</button>;\r\n</script>\r\n")
	jsxParsed, err := source.ParseFile(f.ctx, parserURL, "src/components/JSXPanel.vue", jsxVue)
	require.NoError(t, err, "the production Go parser client must accept Vue's jsx language alias")
	var jsxWidget *types.SourceSymbol
	for index := range jsxParsed.Symbols {
		if jsxParsed.Symbols[index].Name == "Widget" {
			jsxWidget = &jsxParsed.Symbols[index]
			break
		}
	}
	require.NotNil(t, jsxWidget)
	require.Equal(t, "jsx", jsxWidget.Region.Language)
	require.Equal(t, "structural", jsxWidget.Region.Quality)
	require.Equal(t, jsxWidget.Signature, string(jsxVue[jsxWidget.SignatureRange.StartByte:jsxWidget.SignatureRange.EndByte]))

	externalParsed, err := source.ParseFile(f.ctx, parserURL,
		"src/components/ExternalPanel.vue", externalVue)
	require.NoError(t, err)
	require.NotEmpty(t, externalParsed.Symbols)
	var parserExternalRegion *types.SourceRegion
	for index := range externalParsed.Symbols {
		region := externalParsed.Symbols[index].Region
		if region != nil && region.ExternalSource != "" {
			parserExternalRegion = region
			break
		}
	}
	require.NotNil(t, parserExternalRegion)
	require.Equal(t, "unchecked", parserExternalRegion.ExternalStatus)
	assertExternalEvidence := func(raw []byte, parsed *types.ParsedSourceFile, expectedStatus, path string) {
		t.Helper()
		var rebuilt strings.Builder
		foundOpening, foundClosing := false, false
		for _, chunk := range parsed.Chunks {
			rebuilt.WriteString(chunk.Content)
			if chunk.Region == nil || chunk.Region.Kind != "script" || chunk.Region.ExternalStatus != expectedStatus {
				continue
			}
			require.Equal(t, "degraded", chunk.Quality)
			require.Equal(t, "degraded", chunk.Region.Quality)
			area := chunk.Range
			require.Greater(t, area.EndByte, area.StartByte)
			require.Equal(t, string(raw[area.StartByte:area.EndByte]), chunk.Content)
			foundOpening = foundOpening || strings.Contains(chunk.Content, "<script")
			foundClosing = foundClosing || strings.Contains(chunk.Content, "</script>")
			evidence := &types.SourceEvidence{
				DataSourceID: f.ds.ID, SnapshotID: "snapshot-id", FileVersionID: "file-version-id",
				ProjectID: "project-id", CommitSHA: f.sha, Path: path, Range: chunk.Range,
				Quality: chunk.Quality, Region: chunk.Region,
			}
			result := &types.ToolResult{Success: true, Data: map[string]interface{}{
				"display_type": "search_results",
				"results": []map[string]interface{}{{"chunk_id": "chunk-id", "knowledge_id": "source-file-id",
					"knowledge_base_id": "kb-id", "knowledge_title": "external Vue script",
					"content": chunk.Content, "source_evidence": evidence}},
			}}
			output := modelcontext.NewRegistry(true).ModelToolResultForTool("knowledge_search", result)
			require.Contains(t, output, `quality="degraded"`)
			require.Contains(t, output, `region_kind="script"`)
			require.Contains(t, output, `external_status="`+expectedStatus+`"`)
			require.NotContains(t, output, `external_source=`)
			require.NotContains(t, output, `resolved_path=`)
		}
		require.Equal(t, string(raw), rebuilt.String())
		require.True(t, foundOpening, "the original opening script tag must carry region evidence")
		require.True(t, foundClosing, "the original closing script tag must carry region evidence")
	}
	assertExternalEvidence(externalVue, externalParsed, "unchecked", "src/components/ExternalPanel.vue")
	selfClosingParsed, err := source.ParseFile(f.ctx, parserURL,
		"src/components/SelfClosingPanel.vue", selfClosingExternalVue)
	require.NoError(t, err)
	var selfClosingRegion *types.SourceRegion
	for _, symbol := range selfClosingParsed.Symbols {
		if symbol.Kind == "sfc_region" && symbol.Region != nil && symbol.Region.Kind == "script" {
			selfClosingRegion = symbol.Region
		}
	}
	require.NotNil(t, selfClosingRegion)
	require.Equal(t, "./api.js", selfClosingRegion.ExternalSource)
	require.Equal(t, "unchecked", selfClosingRegion.ExternalStatus)
	require.Len(t, selfClosingParsed.Chunks, 1)
	require.Equal(t, string(selfClosingExternalVue), selfClosingParsed.Chunks[0].Content,
		"a self-closing external script keeps its positive-width original wrapper as source evidence")
	require.Equal(t, "script", selfClosingParsed.Chunks[0].Region.Kind)
	whitespaceParsed, err := source.ParseFile(f.ctx, parserURL,
		"src/components/WhitespaceExternalPanel.vue", whitespaceExternalVue)
	require.NoError(t, err)
	assertExternalEvidence(whitespaceExternalVue, whitespaceParsed, "unchecked", "src/components/WhitespaceExternalPanel.vue")
	rejectedParsed, err := source.ParseFile(f.ctx, parserURL,
		"src/components/RejectedPanel.vue", rejectedVue)
	require.NoError(t, err)
	assertExternalEvidence(rejectedVue, rejectedParsed, "unchecked", "src/components/RejectedPanel.vue")
	var externalID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/ExternalPanel.vue").Scan(&externalID).Error)
	var selfClosingExternalID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/SelfClosingPanel.vue").Scan(&selfClosingExternalID).Error)
	var crossDirectoryExternalID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/pages/Panel.vue").Scan(&crossDirectoryExternalID).Error)
	var rejectedID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/RejectedPanel.vue").Scan(&rejectedID).Error)
	var missingID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/MissingPanel.vue").Scan(&missingID).Error)
	var unknownID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/UnknownPanel.vue").Scan(&unknownID).Error)
	findScriptRegion := func(view *types.SourceFileView) *types.SourceRegion {
		t.Helper()
		var symbols []types.SourceSymbol
		require.NoError(t, json.Unmarshal(view.Symbols, &symbols))
		for index := range symbols {
			if symbols[index].Region != nil && symbols[index].Region.Kind == "script" && symbols[index].Region.ExternalSource != "" {
				return symbols[index].Region
			}
		}
		return nil
	}

	narrowView, err := f.knowledge.GetSourceFile(f.ctx, externalID)
	require.NoError(t, err)
	narrowRegion := findScriptRegion(narrowView)
	require.NotNil(t, narrowRegion)
	require.Equal(t, "./api.js", narrowRegion.ExternalSource)
	require.Equal(t, "unavailable", narrowRegion.ExternalStatus,
		"a one-file read must explain that an out-of-scope target is unavailable")
	require.Empty(t, narrowRegion.ResolvedPath)
	missingNarrowView, err := f.knowledge.GetSourceFile(f.ctx, missingID)
	require.NoError(t, err)
	missingNarrowRegion := findScriptRegion(missingNarrowView)
	require.NotNil(t, missingNarrowRegion)
	require.Equal(t, "unavailable", missingNarrowRegion.ExternalStatus,
		"an absent target must have the same public status as an out-of-scope target")
	require.Equal(t, narrowRegion.ExternalStatus, missingNarrowRegion.ExternalStatus,
		"source readers must not learn whether the target exists outside their scope")
	require.Empty(t, missingNarrowRegion.ResolvedPath)
	selfClosingNarrowView, err := f.knowledge.GetSourceFile(f.ctx, selfClosingExternalID)
	require.NoError(t, err)
	selfClosingNarrowRegion := findScriptRegion(selfClosingNarrowView)
	require.NotNil(t, selfClosingNarrowRegion)
	require.Equal(t, "unavailable", selfClosingNarrowRegion.ExternalStatus)
	require.Empty(t, selfClosingNarrowRegion.ResolvedPath)
	crossDirectoryNarrowView, err := f.knowledge.GetSourceFile(f.ctx, crossDirectoryExternalID)
	require.NoError(t, err)
	crossDirectoryNarrowRegion := findScriptRegion(crossDirectoryNarrowView)
	require.NotNil(t, crossDirectoryNarrowRegion)
	require.Equal(t, "unavailable", crossDirectoryNarrowRegion.ExternalStatus,
		"path normalization does not bypass the caller's source-file scope")
	require.Empty(t, crossDirectoryNarrowRegion.ResolvedPath)

	targets := types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase,
		KnowledgeBaseID: f.kb.ID, TenantID: 1, SourceIDs: []string{f.ds.ID}}}
	pinned, release, err := f.kbs.(interfaces.SourceReadService).BeginSourceRead(f.ctx, targets)
	require.NoError(t, err)
	defer release()
	broadView, err := f.knowledge.GetSourceFile(pinned, externalID)
	require.NoError(t, err)
	broadRegion := findScriptRegion(broadView)
	require.NotNil(t, broadRegion)
	require.Equal(t, "resolved", broadRegion.ExternalStatus)
	require.Equal(t, "src/components/api.js", broadRegion.ResolvedPath)
	selfClosingBroadView, err := f.knowledge.GetSourceFile(pinned, selfClosingExternalID)
	require.NoError(t, err)
	selfClosingBroadRegion := findScriptRegion(selfClosingBroadView)
	require.NotNil(t, selfClosingBroadRegion)
	require.Equal(t, "resolved", selfClosingBroadRegion.ExternalStatus)
	require.Equal(t, "src/components/api.js", selfClosingBroadRegion.ResolvedPath)
	crossDirectoryView, err := f.knowledge.GetSourceFile(pinned, crossDirectoryExternalID)
	require.NoError(t, err)
	crossDirectoryRegion := findScriptRegion(crossDirectoryView)
	require.NotNil(t, crossDirectoryRegion)
	require.Equal(t, "../shared/api.js", crossDirectoryRegion.ExternalSource)
	require.Equal(t, "resolved", crossDirectoryRegion.ExternalStatus)
	require.Equal(t, "src/shared/api.js", crossDirectoryRegion.ResolvedPath,
		"normalized sibling references resolve only to members of the same authorized snapshot")

	rejectedView, err := f.knowledge.GetSourceFile(pinned, rejectedID)
	require.NoError(t, err)
	rejectedRegion := findScriptRegion(rejectedView)
	require.NotNil(t, rejectedRegion)
	require.Equal(t, "rejected", rejectedRegion.ExternalStatus)
	require.Empty(t, rejectedRegion.ResolvedPath)

	missingView, err := f.knowledge.GetSourceFile(pinned, missingID)
	require.NoError(t, err)
	missingRegion := findScriptRegion(missingView)
	require.NotNil(t, missingRegion)
	require.Equal(t, "unavailable", missingRegion.ExternalStatus,
		"a missing external target must be explicitly reported as unavailable")
	require.Empty(t, missingRegion.ResolvedPath)

	unknownView, err := f.knowledge.GetSourceFile(f.ctx, unknownID)
	require.NoError(t, err)
	require.Equal(t, "unknown_preprocess", unknownView.Quality)

	var apiID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/api.js").Scan(&apiID).Error)
	require.NoError(t, f.db.Exec("UPDATE knowledges SET deleted_at=now() WHERE id=?", apiID).Error)
	deletedTargetView, err := f.knowledge.GetSourceFile(pinned, externalID)
	require.NoError(t, err)
	deletedTargetRegion := findScriptRegion(deletedTargetView)
	require.NotNil(t, deletedTargetRegion)
	require.Equal(t, "unavailable", deletedTargetRegion.ExternalStatus,
		"a soft-deleted source target must be reported as unavailable and not resolved")
	require.Empty(t, deletedTargetRegion.ResolvedPath)
}

func TestSourceVue2RepresentativeAcceptance(t *testing.T) {
	componentDir := strings.TrimSpace(os.Getenv("SOURCE_TEST_VUE2_COMPONENT_DIR"))
	if componentDir == "" {
		t.Skip("set SOURCE_TEST_VUE2_COMPONENT_DIR to opt into the local representative Vue 2 acceptance test")
	}
	componentNames := []string{"index.vue", "list.vue", "qr.vue"}
	fixtureFiles := make(map[string][]byte, len(componentNames))
	originalFiles := make(map[string][]byte, len(componentNames))
	for _, name := range componentNames {
		raw, err := os.ReadFile(filepath.Join(componentDir, name))
		if err != nil {
			t.Fatal("could not read a representative Vue component")
		}
		originalFiles[name] = raw
		fixtureFiles["src/pages/User/ConfirmTimetable/"+name] = raw
	}

	projectSHACommand := exec.Command("git", "-C", componentDir, "rev-parse", "HEAD")
	projectSHABytes, err := projectSHACommand.Output()
	require.NoError(t, err, "the representative component directory must belong to a Git checkout")
	projectSHA := strings.TrimSpace(string(projectSHABytes))
	require.Len(t, projectSHA, 40)

	f := newJavaSourceFixture(t, fixtureFiles)
	// The integration fixture configures silent SQL logging before constructing
	// repositories so no clone can emit SQL or representative source parameters.
	syncSourceFixture(t, f)
	parserVersion, err := source.ParserVersion(f.ctx, os.Getenv("SOURCE_PARSER_URL"))
	require.NoError(t, err)
	require.NotEmpty(t, parserVersion)

	type fileAggregate struct {
		name, digest                                                   string
		bytes, crlf, cjk, chunks, symbols, coordinates, imports, calls int
	}
	aggregates := make([]fileAggregate, 0, len(componentNames))
	for _, name := range componentNames {
		raw := originalFiles[name]
		logicalPath := "src/pages/User/ConfirmTimetable/" + name
		parsed, parseErr := source.ParseFile(f.ctx, os.Getenv("SOURCE_PARSER_URL"), logicalPath, raw)
		require.NoError(t, parseErr, "production source parser rejected a representative component")
		require.Equal(t, len(raw), parsed.ByteLength)
		require.Equal(t, fmt.Sprintf("%x", sha256.Sum256(raw)), parsed.SHA256)

		aggregate := fileAggregate{name: name, digest: parsed.SHA256, bytes: len(raw), crlf: bytes.Count(raw, []byte("\r\n"))}
		for _, r := range string(raw) {
			if unicode.Is(unicode.Han, r) {
				aggregate.cjk++
			}
		}
		cursor := 0
		for _, chunk := range parsed.Chunks {
			span := chunk.Range
			if span.StartByte != cursor || span.EndByte <= span.StartByte || span.EndByte > len(raw) ||
				!bytes.Equal(raw[span.StartByte:span.EndByte], []byte(chunk.Content)) {
				t.Fatal("representative chunk coordinates did not select the exact original bytes")
			}
			if span.StartLine != bytes.Count(raw[:span.StartByte], []byte{'\n'})+1 {
				t.Fatal("representative chunk start-line coordinate did not match the original bytes")
			}
			last := span.EndByte - 1
			if last < span.StartByte {
				last = span.StartByte
			}
			if span.EndLine != bytes.Count(raw[:last], []byte{'\n'})+1 {
				t.Fatal("representative chunk end-line coordinate did not match the original bytes")
			}
			cursor = span.EndByte
			aggregate.chunks++
			aggregate.coordinates++
		}
		if cursor != len(raw) {
			t.Fatal("representative chunk coordinates did not cover the original file")
		}
		regions := make(map[string]bool)
		for _, symbol := range parsed.Symbols {
			if symbol.Kind == "sfc_region" && symbol.Region != nil {
				regions[symbol.Region.Kind] = true
			}
			span := symbol.SignatureRange
			if span.StartByte < 0 || span.EndByte < span.StartByte || span.EndByte > len(raw) ||
				!bytes.Equal(raw[span.StartByte:span.EndByte], []byte(symbol.Signature)) {
				t.Fatal("representative symbol coordinates did not select the exact original bytes")
			}
			aggregate.symbols++
			aggregate.coordinates++
			inScript := symbol.Region != nil && symbol.Region.Kind == "script"
			if symbol.Kind == "import" && inScript {
				aggregate.imports++
			}
			if inScript && (symbol.Kind == "function" || symbol.Kind == "method") {
				bodyStart := span.EndByte
				bodyEnd := symbol.Range.EndByte
				if bodyStart >= 0 && bodyEnd > bodyStart && bodyEnd <= len(raw) && hasCallSyntax(raw[bodyStart:bodyEnd]) {
					aggregate.calls++
				}
			}
		}
		require.True(t, regions["template"], "representative component should expose its template region")
		require.True(t, regions["script"], "representative component should expose its script region")
		require.Greater(t, aggregate.crlf, 0, "representative source should retain Windows line endings")
		require.Greater(t, aggregate.cjk, 0, "representative source should retain CJK text")
		require.Greater(t, aggregate.chunks, 0)
		require.Greater(t, aggregate.symbols, 0)
		if name == "qr.vue" {
			require.Greater(t, aggregate.imports, 0, "representative QR component should expose import structure")
		}
		if name == "index.vue" || name == "list.vue" {
			require.Greater(t, aggregate.calls, 0, "representative page should expose an API-call-shaped function body")
		}
		aggregates = append(aggregates, aggregate)
	}

	keywordHits, vectorHits, publicReads, modelEvidenceSlices := 0, 0, 0, 0
	for _, aggregate := range aggregates {
		logicalPath := "src/pages/User/ConfirmTimetable/" + aggregate.name
		for _, keywordOnly := range []bool{true, false} {
			hits, searchErr := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{
				QueryText: "ConfirmTimetable", MatchCount: 500, SourceIDs: []string{f.ds.ID},
				DisableVectorMatch: !keywordOnly, DisableKeywordsMatch: keywordOnly,
				SkipContextEnrichment: true,
			})
			require.NoError(t, searchErr)
			var selected *types.SearchResult
			var selectedEvidence *types.SourceEvidence
			for _, candidate := range hits {
				if candidate.Metadata["source_path"] != logicalPath {
					continue
				}
				evidence := source.Evidence(candidate.ChunkMetadata)
				if evidence == nil || evidence.Region == nil {
					continue
				}
				selected, selectedEvidence = candidate, evidence
				if evidence.Region.Language != "" {
					break
				}
			}
			if selected == nil || selectedEvidence == nil {
				t.Fatal("a public source index route did not return bounded Vue evidence for a representative file")
			}
			raw := originalFiles[aggregate.name]
			span := selectedEvidence.Range
			if span.StartByte < 0 || span.EndByte <= span.StartByte || span.EndByte > len(raw) ||
				!bytes.Equal(raw[span.StartByte:span.EndByte], []byte(selected.Content)) {
				t.Fatal("retrieved source evidence did not select the exact original byte range")
			}
			if span.StartLine != bytes.Count(raw[:span.StartByte], []byte{'\n'})+1 {
				t.Fatal("retrieved evidence start line did not match the original source")
			}
			last := span.EndByte - 1
			if last < span.StartByte {
				last = span.StartByte
			}
			if span.EndLine != bytes.Count(raw[:last], []byte{'\n'})+1 {
				t.Fatal("retrieved evidence end line did not match the original source")
			}
			if keywordOnly {
				keywordHits++
			} else {
				vectorHits++
			}

			view, readErr := f.knowledge.GetSourceFile(f.ctx, selected.KnowledgeID)
			require.NoError(t, readErr)
			viewDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(view.Content)))
			if viewDigest != aggregate.digest || view.SHA256 != aggregate.digest || view.FileSize != int64(len(raw)) || view.CommitSHA != f.sha {
				t.Fatal("authorized public source-file read did not match the verified source identity")
			}
			publicReads++

			modelResult := &types.ToolResult{Success: true, Data: map[string]interface{}{
				"display_type": "search_results",
				"results": []map[string]interface{}{{
					"chunk_id": selected.ID, "knowledge_id": selected.KnowledgeID,
					"knowledge_base_id": f.kb.ID, "knowledge_title": selected.KnowledgeTitle,
					"source_evidence": selectedEvidence,
				}},
			}}
			modelOutput := modelcontext.NewRegistry(true).ModelToolResultForTool("knowledge_search", modelResult)
			for _, required := range []string{`region_kind="`, `quality="`, `start_line="`, `end_line="`, `symbols="`} {
				if !strings.Contains(modelOutput, required) {
					t.Fatal("actual model-facing evidence slice omitted a required source coordinate or region field")
				}
			}
			if selectedEvidence.Region.Language != "" && !strings.Contains(modelOutput, `region_language="`) {
				t.Fatal("actual model-facing evidence slice omitted the parser-provided region language")
			}
			if strings.Contains(modelOutput, "private-relative-target") || strings.Contains(modelOutput, "resolved_path=") {
				t.Fatal("model-facing evidence disclosed an unresolved local source path")
			}
			modelEvidenceSlices++
		}
	}
	require.Equal(t, len(aggregates), keywordHits)
	require.Equal(t, len(aggregates), vectorHits)
	t.Logf("vue2_acceptance project_sha=%s fixture_sha=%s parser_version=%s keyword_files=%d vector_files=%d public_reads=%d model_evidence_slices=%d",
		projectSHA, f.sha, parserVersion, keywordHits, vectorHits, publicReads, modelEvidenceSlices)
	for _, aggregate := range aggregates {
		t.Logf("vue2_file name=%s sha256=%s bytes=%d crlf=%d cjk=%d chunks=%d symbols=%d coordinates=%d imports=%d call_bodies=%d",
			aggregate.name, aggregate.digest, aggregate.bytes, aggregate.crlf, aggregate.cjk,
			aggregate.chunks, aggregate.symbols, aggregate.coordinates, aggregate.imports, aggregate.calls)
	}
}

func hasCallSyntax(body []byte) bool {
	for index := 0; index < len(body); index++ {
		if body[index] != '(' {
			continue
		}
		cursor := index - 1
		for cursor >= 0 && (body[cursor] == ' ' || body[cursor] == '\t' || body[cursor] == '\r' || body[cursor] == '\n') {
			cursor--
		}
		if cursor >= 0 && ((body[cursor] >= 'a' && body[cursor] <= 'z') ||
			(body[cursor] >= 'A' && body[cursor] <= 'Z') || body[cursor] == '_' || body[cursor] == '$' ||
			(body[cursor] >= '0' && body[cursor] <= '9') || body[cursor] == ')') {
			return true
		}
	}
	return false
}
