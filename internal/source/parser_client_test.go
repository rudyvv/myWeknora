package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestLanguageForPathIncludesLockedPythonGrammar(t *testing.T) {
	tests := map[string]string{
		"pkg/service.py":       "python",
		"pkg/service.PY":       "python",
		"src/BookingPanel.vue": "vue",
		"src/bookingpanel.VUE": "vue",
		"pkg/App.java":         "java",
		"pkg/app.ts":           "typescript",
		"pkg/component.tsx":    "tsx",
		"pkg/legacy.rb":        "",
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
