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
