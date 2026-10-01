package source

import (
	"context"
	"crypto/sha256"
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

func chunkContents(chunks []types.ParsedSourceChunk) []string {
	contents := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		contents = append(contents, chunk.Content)
	}
	return contents
}
