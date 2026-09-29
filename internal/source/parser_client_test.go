package source

import "testing"

func TestLanguageForPathIncludesLockedPythonGrammar(t *testing.T) {
	tests := map[string]string{
		"pkg/service.py":    "python",
		"pkg/service.PY":    "python",
		"pkg/App.java":      "java",
		"pkg/app.ts":        "typescript",
		"pkg/component.tsx": "tsx",
		"pkg/legacy.rb":     "",
	}
	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			if got := LanguageForPath(path); got != want {
				t.Fatalf("LanguageForPath(%q) = %q, want %q", path, got, want)
			}
		})
	}
}
