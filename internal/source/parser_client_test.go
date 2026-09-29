package source

import (
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
	raw := []byte("<template><div>first</template>\r\n")
	valid := types.SourceDiagnostic{
		Code:  "vue_sfc_parse_warning",
		Range: types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 1, EndLine: 1},
	}
	chunk := types.SourceRange{StartByte: 0, EndByte: len(raw), StartLine: 1, EndLine: 1}
	if !validSourceDiagnostic("vue", raw, chunk, valid) {
		t.Fatal("expected a known Vue diagnostic with an exact original range to be accepted")
	}
	crossingRange := types.SourceDiagnostic{
		Code:  valid.Code,
		Range: types.SourceRange{StartByte: 10, EndByte: 20, StartLine: 1, EndLine: 1},
	}
	affectedChunk := types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 1, EndLine: 1}
	if !validSourceDiagnostic("vue", raw, affectedChunk, crossingRange) {
		t.Fatal("expected an exact diagnostic range to remain valid when it extends beyond its attributed start chunk")
	}
	invalid := []types.SourceDiagnostic{
		{Code: "untrusted_parser_message", Range: valid.Range},
		{Code: valid.Code, Range: types.SourceRange{StartByte: 10, EndByte: len(raw) + 1, StartLine: 1, EndLine: 1}},
		{Code: valid.Code, Range: types.SourceRange{StartByte: 10, EndByte: 15, StartLine: 2, EndLine: 2}},
	}
	for _, diagnostic := range invalid {
		if validSourceDiagnostic("vue", raw, chunk, diagnostic) {
			t.Fatalf("accepted invalid Vue diagnostic: %#v", diagnostic)
		}
	}
	if validSourceDiagnostic("javascript", raw, chunk, valid) {
		t.Fatal("accepted an SFC diagnostic for a non-Vue source file")
	}
	if validSourceDiagnostic("vue", raw, types.SourceRange{StartByte: 15, EndByte: len(raw), StartLine: 1, EndLine: 1}, valid) {
		t.Fatal("accepted a diagnostic whose start position is outside the attributed chunk")
	}
}
