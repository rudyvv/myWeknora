package source

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/tiktoken-go/tokenizer"
)

func TestLanguageForPathRoutesSupportedSourceAndTextFiles(t *testing.T) {
	tests := map[string]string{
		"pkg/service.py":        "python",
		"pkg/service.PY":        "python",
		"pkg/App.java":          "java",
		"pkg/Mapper.xml":        "mybatis-xml",
		"pkg/app.ts":            "typescript",
		"pkg/component.tsx":     "tsx",
		"pkg/component.vue":     "vue",
		"web/page.html":         "text",
		"web/page.jsp":          "text",
		"web/page.ftl":          "text",
		"web/page.ftlh":         "text",
		"web/site.css":          "text",
		"web/site.scss":         "text",
		"web/theme.styl":        "text",
		"config/app.yaml":       "text",
		"config/app.yml":        "text",
		"config/app.json":       "text",
		"config/app.toml":       "text",
		"config/app.properties": "text",
		"Dockerfile":            "text",
		"docker/Dockerfile":     "text",
		"docker/Dockerfile.ci":  "text",
		"pkg/legacy.rb":         "",
		"src/BookingPanel.vue":  "vue",
		"src/bookingpanel.VUE":  "vue",
	}
	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			if got := LanguageForPath(path); got != want {
				t.Fatalf("LanguageForPath(%q) = %q, want %q", path, got, want)
			}
		})
	}
}

func TestParseFileAcceptsBusinessFlowFactsFromParserHTTP(t *testing.T) {
	raw := []byte("fixture")
	digest := sha256.Sum256(raw)
	rangeValue := types.SourceRange{StartByte: 0, EndByte: len(raw), StartLine: 1, EndLine: 1}
	kinds := []string{"java_import", "java_type", "java_supertype_reference", "java_injection", "java_method", "spring_mapping",
		"java_method_call", "java_dynamic_dispatch", "api_request", "api_prefix", "api_proxy"}
	facts := make([]types.ParsedSourceFact, 0, len(kinds))
	for _, kind := range kinds {
		fact := types.ParsedSourceFact{Kind: kind, Name: kind, RoutePath: "/api/fixture",
			HTTPMethod: "GET", Quality: "structural", Range: rangeValue, Text: string(raw)}
		if kind == "java_supertype_reference" {
			fact.TargetName, fact.Namespace, fact.OwnerKind = kind, "app.Worker", "class"
			fact.ReferenceKind, fact.Certainty, fact.Reason = "implements", "uncertain", "identity unresolved"
		}
		facts = append(facts, fact)
	}
	parsed := types.ParsedSourceFile{ParserVersion: "fixture-parser", SHA256: hex.EncodeToString(digest[:]),
		ByteLength: len(raw), Encoding: "utf-8", Quality: "structural", Facts: facts,
		Chunks: []types.ParsedSourceChunk{{Content: string(raw), Range: rangeValue, Quality: "structural"}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(parsed)
	}))
	defer server.Close()

	result, err := ParseFile(context.Background(), server.URL, "Example.java", raw)
	if err != nil {
		t.Fatalf("ParseFile rejected parser-authored business flow facts: %v", err)
	}
	if len(result.Facts) != len(kinds) {
		t.Fatalf("ParseFile returned %d facts, want %d", len(result.Facts), len(kinds))
	}
}

func TestSourceRegionValidationOnlyAcceptsUnresolvedLiteralReferences(t *testing.T) {
	valid := []types.SourceRegion{
		{Kind: "template", Language: "html", Quality: "structural"},
		{Kind: "script", Language: "typescript", Quality: "syntax_error"},
		{Kind: "style", Language: "pug", Quality: "unknown_preprocess"},
		{Kind: "custom", Quality: "text_fallback"},
		{Kind: "script", Quality: "degraded", ExternalSource: "./api.js", ExternalStatus: "unchecked"},
		{Kind: "script", Quality: "degraded", ExternalSource: "../private.js", ExternalStatus: "rejected"},
	}
	for _, region := range valid {
		if !validSourceRegion(&region) {
			t.Fatalf("expected valid source region: %#v", region)
		}
	}
	invalid := []types.SourceRegion{
		{Kind: "unknown", Quality: "structural"},
		{Kind: "style", Quality: "compiled"},
		{Kind: "template", Quality: "structural", ExternalSource: "./api.js", ExternalStatus: "resolved", ResolvedPath: "src/api.js"},
		{Kind: "script", Quality: "structural", ExternalSource: "./api.js", ExternalStatus: "unchecked", ResolvedPath: "src/api.js"},
	}
	for _, region := range invalid {
		if validSourceRegion(&region) {
			t.Fatalf("accepted invalid source region: %#v", region)
		}
	}
}

func TestSourceDiagnosticValidationRequiresKnownVueCodeAndOriginalRange(t *testing.T) {
	raw := []byte("<template><div>first</template>")
	valid := types.SourceDiagnostic{
		Code:  "vue_sfc_parse_warning",
		Range: types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 1, EndLine: 1},
	}
	if !validSourceDiagnostic("vue", raw, valid) {
		t.Fatal("expected a known Vue diagnostic with an exact original range to be accepted")
	}
	boundaryRanges := []types.SourceDiagnostic{
		{Code: valid.Code, Range: types.SourceRange{StartByte: 0, EndByte: 10, StartLine: 1, EndLine: 1}},
		{Code: valid.Code, Range: types.SourceRange{StartByte: len(raw), EndByte: len(raw), StartLine: 1, EndLine: 1}},
	}
	for _, diagnostic := range boundaryRanges {
		if !validSourceDiagnostic("vue", raw, diagnostic) {
			t.Fatalf("expected valid original boundary range to be accepted: %#v", diagnostic)
		}
	}
	invalid := []types.SourceDiagnostic{
		{Code: "untrusted_parser_message", Range: valid.Range},
		{Code: valid.Code, Range: types.SourceRange{StartByte: 10, EndByte: len(raw) + 1, StartLine: 1, EndLine: 1}},
		{Code: valid.Code, Range: types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 2, EndLine: 2}},
	}
	for _, diagnostic := range invalid {
		if validSourceDiagnostic("vue", raw, diagnostic) {
			t.Fatalf("accepted invalid Vue diagnostic: %#v", diagnostic)
		}
	}
	if validSourceDiagnostic("javascript", raw, valid) {
		t.Fatal("accepted an SFC diagnostic for a non-Vue source file")
	}
}

func TestSourceIndexTokenCountsHeaderBodyAndCompleteText(t *testing.T) {
	codec, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	chunk := types.ParsedSourceChunk{
		Content: "return \"预约\";",
		Symbols: []string{"Service.getSchedule"},
		Context: []types.SourceContext{{Text: "public String getSchedule()", Range: types.SourceRange{StartByte: 0, EndByte: 28, StartLine: 1, EndLine: 1}}},
	}
	header, body, full, err := sourceIndexTokenCounts(codec, "src/Service.java", chunk)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		text string
		got  int
	}{
		{SourceIndexHeader("src/Service.java", chunk), header},
		{chunk.Content, body},
		{SourceIndexText("src/Service.java", chunk), full},
	} {
		ids, _, encodeErr := codec.Encode(item.text)
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if item.got != len(ids) {
			t.Fatalf("token count = %d, want %d for %q", item.got, len(ids), item.text)
		}
	}
	if header == 0 || body == 0 || full == 0 {
		t.Fatalf("header/body/full counts must each be measured, got %d/%d/%d", header, body, full)
	}
}

func TestParseFileWithProfileDropsOversizedTraceContextBeforeReslicingBody(t *testing.T) {
	raw := []byte(strings.Repeat("a ", 512))
	sha := sha256.Sum256(raw)
	wideContext := types.SourceContext{
		Text: string(raw), Range: types.SourceRange{StartByte: 0, EndByte: len(raw), StartLine: 1, EndLine: 1},
	}
	innerContext := types.SourceContext{
		Text: string(raw[256:768]), Range: types.SourceRange{StartByte: 256, EndByte: 768, StartLine: 1, EndLine: 1},
	}
	parsed := types.ParsedSourceFile{
		ParserVersion: "test-parser-v1", SHA256: hex.EncodeToString(sha[:]), ByteLength: len(raw),
		Encoding: "utf-8", Quality: "structural",
	}
	for start := 0; start < len(raw); start += 32 {
		end := min(start+32, len(raw))
		chunk := types.ParsedSourceChunk{
			Content: string(raw[start:end]),
			Range:   types.SourceRange{StartByte: start, EndByte: end, StartLine: 1, EndLine: 1},
			Quality: "structural", Symbols: []string{"Mapper.query"},
		}
		if start == 512 {
			chunk.Context = []types.SourceContext{wideContext, innerContext}
		}
		parsed.Chunks = append(parsed.Chunks, chunk)
	}

	var parserCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		parserCalls++
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(parsed)
	}))
	defer server.Close()

	profile := IndexProfile{Tokenizer: tokenizer.Cl100kBase, MaxTokens: 64}
	codec, err := tokenizer.Get(profile.Tokenizer)
	require.NoError(t, err)
	headerTokens, _, err := codec.Encode(SourceIndexHeader("src/Mapper.java", parsed.Chunks[16]))
	require.NoError(t, err)
	require.Greater(t, len(headerTokens), profile.MaxTokens, "fixture must exercise an oversized derived header")

	got, err := ParseFileWithProfile(context.Background(), server.URL, "src/Mapper.java", raw, profile)
	require.NoError(t, err)
	require.Equal(t, string(raw), strings.Join(chunkContents(got.Chunks), ""))
	require.Len(t, got.Chunks[16].Context, 0, "range-bearing contexts must be omitted if neither fits")
	require.Equal(t, 1, parserCalls, "the client should trim derived header context instead of retrying smaller source bodies")
	require.Contains(t, got.Diagnostics, types.ParsedSourceDiagnostic{
		Code: "source_index_header_trimmed", Message: "Trace context or nonessential symbols were omitted from derived embedding text to fit the model input limit",
		Range: got.Chunks[16].Range,
	})
	for _, chunk := range got.Chunks {
		header, body, full, countErr := sourceIndexTokenCounts(codec, "src/Mapper.java", chunk)
		require.NoError(t, countErr)
		require.LessOrEqual(t, header, profile.MaxTokens)
		require.LessOrEqual(t, body, profile.MaxTokens)
		require.LessOrEqual(t, full, profile.MaxTokens)
	}
}

func TestParseFileWithProfileFitsCompleteDerivedTextWithoutChangingSourceEvidence(t *testing.T) {
	raw := []byte(strings.Repeat("x ", 512))
	digest := sha256.Sum256(raw)
	const path = "src/Mapper.java"
	const tokenLimit = 495
	contextRange := types.SourceRange{StartByte: 0, EndByte: 960, StartLine: 1, EndLine: 1}
	traceContext := types.SourceContext{Text: string(raw[contextRange.StartByte:contextRange.EndByte]), Range: contextRange}
	chunkSymbols := []string{"Mapper.query"}
	firstChunk := types.ParsedSourceChunk{
		Content: string(raw[:64]),
		Range:   types.SourceRange{StartByte: 0, EndByte: 64, StartLine: 1, EndLine: 1},
		Quality: "structural", Symbols: chunkSymbols, Context: []types.SourceContext{traceContext},
	}
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	require.NoError(t, err)
	headerTokens, bodyTokens, fullTokens, err := sourceIndexTokenCounts(codec, path, firstChunk)
	require.NoError(t, err)
	require.LessOrEqual(t, headerTokens, tokenLimit, "the fixture must have a context header within budget")
	require.LessOrEqual(t, bodyTokens, tokenLimit, "the 64-byte body alone must fit")
	require.Greater(t, fullTokens, tokenLimit, "the joined header plus 64-byte body must exceed budget")

	symbolRange := types.SourceRange{StartByte: 0, EndByte: 2, StartLine: 1, EndLine: 1}
	signatureRange := types.SourceRange{StartByte: 0, EndByte: 1, StartLine: 1, EndLine: 1}
	originalSymbols := []types.SourceSymbol{{
		Kind: "method", Name: "query", QualifiedName: "Mapper.query", Signature: string(raw[:1]),
		Range: symbolRange, SignatureRange: signatureRange,
	}}
	factRange := types.SourceRange{StartByte: 0, EndByte: 2, StartLine: 1, EndLine: 1}
	originalFacts := []types.ParsedSourceFact{{
		Kind: "java_method", Name: "Mapper.query", Quality: "structural", Range: factRange, Text: string(raw[:2]),
	}}
	var budgets, roundCoverage []int
	var invalidRequest bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Path          string `json:"path"`
			SHA256        string `json:"sha256"`
			ContentBase64 string `json:"content_base64"`
			ChunkMaxBytes int    `json:"chunk_max_bytes"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			invalidRequest = true
			http.Error(w, "invalid parser request", http.StatusBadRequest)
			return
		}
		requestBytes, decodeErr := base64.StdEncoding.DecodeString(request.ContentBase64)
		if decodeErr != nil || string(requestBytes) != string(raw) || request.Path != path || request.SHA256 != hex.EncodeToString(digest[:]) || request.ChunkMaxBytes <= 0 {
			invalidRequest = true
			http.Error(w, "unexpected parser request", http.StatusBadRequest)
			return
		}
		budgets = append(budgets, request.ChunkMaxBytes)
		parsed := types.ParsedSourceFile{
			ParserVersion: "fixture-parser-v1", SHA256: hex.EncodeToString(digest[:]), ByteLength: len(raw),
			Encoding: "utf-8", Quality: "structural", Symbols: originalSymbols, Facts: originalFacts,
		}
		for start := 0; start < len(requestBytes); {
			end := min(start+request.ChunkMaxBytes, len(requestBytes))
			span := types.SourceRange{StartByte: start, EndByte: end, StartLine: 1, EndLine: 1}
			parsed.Chunks = append(parsed.Chunks, types.ParsedSourceChunk{
				Content: string(requestBytes[start:end]), Range: span, Quality: "structural",
				Symbols: chunkSymbols, Context: []types.SourceContext{traceContext},
			})
			start = end
		}
		covered := 0
		for _, chunk := range parsed.Chunks {
			if chunk.Range.StartByte != covered || chunk.Range.EndByte <= covered {
				invalidRequest = true
				break
			}
			covered = chunk.Range.EndByte
		}
		roundCoverage = append(roundCoverage, covered)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(parsed)
	}))
	defer server.Close()

	got, err := ParseFileWithProfile(context.Background(), server.URL, path, raw, IndexProfile{Tokenizer: tokenizer.Cl100kBase, MaxTokens: tokenLimit})
	require.NoError(t, err, "parser byte-budget rounds: %v", budgets)
	require.False(t, invalidRequest)
	require.Greater(t, len(budgets), 1, "oversized bodies should exercise multiple parser byte-budget rounds")
	require.Equal(t, 4096, budgets[0])
	for _, covered := range roundCoverage {
		require.Equal(t, len(raw), covered, "each parser round must cover the complete source")
	}
	require.Equal(t, string(raw), strings.Join(chunkContents(got.Chunks), ""), "the original file must not be truncated or rewritten")
	require.Equal(t, originalSymbols, got.Symbols, "parser-authored global symbols must remain intact")
	require.Equal(t, originalFacts, got.Facts, "parser-authored facts must remain intact")
	require.Contains(t, got.Diagnostics, types.ParsedSourceDiagnostic{
		Code: "source_index_header_trimmed", Message: "Trace context or nonessential symbols were omitted from derived embedding text to fit the model input limit",
		Range: got.Chunks[0].Range,
	})
	cursor := 0
	for _, chunk := range got.Chunks {
		require.Equal(t, cursor, chunk.Range.StartByte)
		require.Greater(t, chunk.Range.EndByte, cursor)
		require.Equal(t, string(raw[chunk.Range.StartByte:chunk.Range.EndByte]), chunk.Content)
		require.Equal(t, 1, chunk.Range.StartLine)
		require.Equal(t, 1, chunk.Range.EndLine)
		require.Empty(t, chunk.Context, "nonessential trace context should be removed before symbols")
		require.Equal(t, chunkSymbols, chunk.Symbols, "symbols should remain when removing context is sufficient")
		_, body, full, countErr := sourceIndexTokenCounts(codec, path, chunk)
		require.NoError(t, countErr)
		require.LessOrEqual(t, body, tokenLimit)
		require.LessOrEqual(t, full, tokenLimit)
		cursor = chunk.Range.EndByte
	}
	require.Equal(t, len(raw), cursor, "the final chunk coordinates must reach EOF")
}

func TestParseFileWithProfileRejectsRequiredPathHeaderOverBudget(t *testing.T) {
	const tokenLimit = 64
	path := "src/" + strings.Repeat("verylongsegment/", 1000) + "Mapper.java"
	raw := []byte("x")
	digest := sha256.Sum256(raw)
	codec, err := tokenizer.Get(tokenizer.Cl100kBase)
	require.NoError(t, err)
	headerTokens, err := sourceIndexHeaderTokenCount(codec, path, types.ParsedSourceChunk{})
	require.NoError(t, err)
	require.Greater(t, headerTokens, tokenLimit, "the required path alone must exceed the profile budget")

	parsed := types.ParsedSourceFile{
		ParserVersion: "fixture-parser-v1", SHA256: hex.EncodeToString(digest[:]), ByteLength: len(raw),
		Encoding: "utf-8", Quality: "structural",
		Chunks: []types.ParsedSourceChunk{{
			Content: string(raw), Range: types.SourceRange{StartByte: 0, EndByte: len(raw), StartLine: 1, EndLine: 1}, Quality: "structural",
		}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(parsed)
	}))
	defer server.Close()

	_, err = ParseFileWithProfile(context.Background(), server.URL, path, raw, IndexProfile{Tokenizer: tokenizer.Cl100kBase, MaxTokens: tokenLimit})
	require.ErrorContains(t, err, "source index header exceeds the embedding model token budget after removing trace context")
}

func chunkContents(chunks []types.ParsedSourceChunk) []string {
	contents := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		contents = append(contents, chunk.Content)
	}
	return contents
}
