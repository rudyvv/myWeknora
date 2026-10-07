//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// This public CLI seam only opens a read-only transaction in the fixed clone.
// It exercises the same transaction settings used by the real migration.
func TestPreflightChecksRealMigrationTransactionReadOnly(t *testing.T) {
	if os.Getenv(dsnEnvName) == "" {
		t.Fatal("dedicated clone environment required")
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"--preflight"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("preflight exit=%d safe_status=%s", code, stderr.String())
	}
	var result struct {
		Status           string `json:"status"`
		ReadOnly         bool   `json:"readonly"`
		StatementTimeout string `json:"statement_timeout"`
		LockTimeout      string `json:"lock_timeout"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal("invalid metadata output")
	}
	if result.Status != "preflight_complete" || !result.ReadOnly || result.StatementTimeout != "8s" || result.LockTimeout != "3s" {
		t.Fatalf("migration transaction settings not validated: %+v", result)
	}
}
