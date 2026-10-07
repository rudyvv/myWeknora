package repository

import (
	"strings"
	"testing"
)

func TestSourceWikiModuleProjectionPathByteBudgetBoundaries(t *testing.T) {
	const (
		maxPathBytes  = 4096
		maxTotalBytes = 8 << 20
	)
	tests := []struct {
		name        string
		current     int64
		path        string
		wantTotal   int64
		wantFailure bool
	}{
		{name: "single path exact boundary", path: strings.Repeat("p", maxPathBytes), wantTotal: maxPathBytes},
		{name: "single path one byte over", path: strings.Repeat("p", maxPathBytes+1), wantFailure: true},
		{name: "UTF-8 byte exact boundary", path: strings.Repeat("界", 1365) + "x", wantTotal: maxPathBytes},
		{name: "aggregate exact boundary", current: maxTotalBytes - maxPathBytes, path: strings.Repeat("p", maxPathBytes), wantTotal: maxTotalBytes},
		{name: "aggregate one byte over", current: maxTotalBytes - maxPathBytes + 1, path: strings.Repeat("p", maxPathBytes), wantFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := addSourceWikiModuleProjectionPathBytes(tt.current, tt.path)
			if tt.wantFailure {
				if err == nil {
					t.Fatalf("path budget accepted %d current bytes plus %d path bytes", tt.current, len(tt.path))
				}
				if got != tt.current {
					t.Fatalf("failed path budget changed accumulated bytes to %d, want unchanged %d", got, tt.current)
				}
				return
			}
			if err != nil || got != tt.wantTotal {
				t.Fatalf("path byte budget = %d, %v; want %d, nil", got, err, tt.wantTotal)
			}
		})
	}
}
