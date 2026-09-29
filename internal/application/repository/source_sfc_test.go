package repository

import "testing"

func TestSourceReferencePathAcceptsOnlyRepositoryRelativeLiteralPaths(t *testing.T) {
	tests := []struct {
		component string
		source    string
		want      string
		ok        bool
	}{
		{"src/pages/Panel.vue", "./api.js", "src/pages/api.js", true},
		{"src/Panel.vue", "api.ts", "src/api.ts", true},
		{"src/Panel.vue", "../api.js", "", false},
		{"src/Panel.vue", "/api.js", "", false},
		{"src/Panel.vue", "C:/api.js", "", false},
		{"src/Panel.vue", "https://host/api.js", "", false},
		{"src/Panel.vue", "api.js?raw", "", false},
		{"src/Panel.vue", "api.js#fragment", "", false},
		{"src/Panel.vue", "bad\\path.js", "", false},
		{"src/Panel.vue", "api.vue", "src/api.vue", true},
	}
	for _, test := range tests {
		got, ok := sourceReferencePath(test.component, test.source)
		if got != test.want || ok != test.ok {
			t.Errorf("sourceReferencePath(%q, %q) = (%q, %v), want (%q, %v)",
				test.component, test.source, got, ok, test.want, test.ok)
		}
	}
	if supportedExternalScript("src/api.vue") {
		t.Fatal("Vue file is not an external script grammar")
	}
}
