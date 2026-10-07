//go:build windows

package main

import "testing"

func TestRuntimeTLSRejectsExtensionBeyondFrozenWindow(t *testing.T) {
	t.Setenv("GITLAB_TLS_INSECURE_ORIGIN", "https://gitlab.p.it")
	t.Setenv("GITLAB_TLS_INSECURE_UNTIL", "2026-10-07T16:31:00Z")
	if _, err := rootGitLabTLS(); err == nil {
		t.Fatal("TLS exception exceeded frozen runtime window")
	}
}

func TestRuntimeTLSAllowsNormalCertificateVerification(t *testing.T) {
	t.Setenv("GITLAB_TLS_INSECURE_ORIGIN", "")
	t.Setenv("GITLAB_TLS_INSECURE_UNTIL", "")
	env, err := rootGitLabTLS()
	if err != nil || len(env) != 0 {
		t.Fatal("normal certificate verification must remain available")
	}
}
