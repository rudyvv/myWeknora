//go:build integration

package service

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

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
	rejectedVue := []byte("<script src=\"../private.js\"></script>\r\n")
	missingVue := []byte("<script src=\"./missing.js\"></script>\r\n")
	unknownVue := []byte("<template lang=\"pug\">\r\nsection unknown\r\n</template>\r\n")
	api := []byte("export function send(name) { return `预约 ${name}`; }\r\n")
	files := map[string][]byte{
		"src/components/BookingPanel.vue":  component,
		"src/components/ExternalPanel.vue": externalVue,
		"src/components/RejectedPanel.vue": rejectedVue,
		"src/components/MissingPanel.vue":  missingVue,
		"src/components/UnknownPanel.vue":  unknownVue,
		"src/components/api.js":            api,
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
	var externalID string
	require.NoError(t, f.db.Raw("SELECT id FROM source_files WHERE path=?", "src/components/ExternalPanel.vue").Scan(&externalID).Error)
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
	require.Equal(t, "unchecked", narrowRegion.ExternalStatus,
		"a one-file read must not reveal whether an out-of-scope target exists")
	require.Empty(t, narrowRegion.ResolvedPath)

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
	require.Equal(t, "unchecked", missingRegion.ExternalStatus,
		"missing and out-of-scope external targets must remain indistinguishable")
	require.Empty(t, missingRegion.ResolvedPath)

	unknownView, err := f.knowledge.GetSourceFile(f.ctx, unknownID)
	require.NoError(t, err)
	require.Equal(t, "unknown_preprocess", unknownView.Quality)
}
