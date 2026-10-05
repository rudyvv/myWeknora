package source

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/utils"
)

func allowLocalGitBridgeTLSFixture(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}

func configureGitLabTLSException(t *testing.T, origin string) {
	t.Helper()
	t.Setenv("GITLAB_TLS_INSECURE_ORIGIN", origin)
	t.Setenv("GITLAB_TLS_INSECURE_UNTIL", time.Now().Add(time.Hour).Format(time.RFC3339))
}

func TestGitBridgeUsesGitLabTLSExceptionWithoutExposingTokenToGit(t *testing.T) {
	allowLocalGitBridgeTLSFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/group/project.git/info/refs" || r.URL.RawQuery != "service=git-upload-pack" {
			t.Errorf("upstream request = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		user, password, ok := r.BasicAuth()
		if !ok || user != "oauth2" || password != "synthetic-git-token" {
			t.Errorf("upstream Git authorization did not match the synthetic credential")
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "" {
			t.Errorf("API token header unexpectedly present on Git request")
		}
		w.Header().Set("Content-Type", "application/x-git-upload-pack-advertisement")
		_, _ = io.WriteString(w, "synthetic-advertisement")
	}))
	defer server.Close()
	configureGitLabTLSException(t, server.URL)

	remote, closeBridge, _, _, err := bridgeGit(context.Background(), &types.SourceRepository{
		CloneURL: server.URL + "/group/project.git",
		Token:    "synthetic-git-token",
	}, 1024)
	if err != nil {
		t.Fatalf("bridgeGit() error = %v", err)
	}
	defer closeBridge()

	resp, err := http.Get(remote + "/info/refs?service=git-upload-pack")
	if err != nil {
		t.Fatalf("loopback Git bridge request: %v", err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if resp.StatusCode != http.StatusOK || string(body) != "synthetic-advertisement" {
		t.Fatal("bridge response status or synthetic payload did not match")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("upstream requests = %d, want 1", got)
	}

	env := sourceGitCommandEnvironment(t.TempDir())
	for _, entry := range env {
		if strings.HasPrefix(entry, "GIT_SSL_NO_VERIFY=") || strings.HasPrefix(entry, "GITLAB_TLS_INSECURE_") || strings.Contains(entry, "synthetic-git-token") {
			t.Fatal("Git subprocess environment contains a TLS bypass or credential")
		}
	}
}

func TestGitBridgeDoesNotFollowRedirectToAnotherOriginOrHTTP(t *testing.T) {
	allowLocalGitBridgeTLSFixture(t)
	var targetHits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	var downgradeHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		downgradeHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer plain.Close()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/group/cross.git/info/refs":
			if r.URL.RawQuery != "service=git-upload-pack" {
				t.Errorf("unexpected upstream query %q", r.URL.RawQuery)
			}
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/group/downgrade.git/info/refs":
			http.Redirect(w, r, plain.URL, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	configureGitLabTLSException(t, server.URL)

	for _, clonePath := range []string{"/group/cross.git", "/group/downgrade.git"} {
		t.Run(clonePath, func(t *testing.T) {
			remote, closeBridge, _, _, err := bridgeGit(context.Background(), &types.SourceRepository{
				CloneURL: server.URL + clonePath,
				Token:    "synthetic-git-token",
			}, 1024)
			if err != nil {
				t.Fatalf("bridgeGit() error = %v", err)
			}
			defer closeBridge()
			resp, err := http.Get(remote + "/info/refs?service=git-upload-pack")
			if err != nil {
				t.Fatalf("loopback Git bridge request: %v", err)
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadGateway {
				t.Fatalf("bridge status = %d, want gateway error for redirect", resp.StatusCode)
			}
		})
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("cross-origin redirect target requests = %d, want 0", got)
	}
	if got := downgradeHits.Load(); got != 0 {
		t.Fatalf("HTTP downgrade target requests = %d, want 0", got)
	}
}

func TestSourceGitCommandEnvironmentRemainsIsolated(t *testing.T) {
	t.Setenv("GIT_SSL_NO_VERIFY", "1")
	t.Setenv("GITLAB_TLS_INSECURE_ORIGIN", "https://gitlab.example")
	t.Setenv("GITLAB_TLS_INSECURE_UNTIL", time.Now().Add(time.Hour).Format(time.RFC3339))
	t.Setenv("PRIVATE_TOKEN", "synthetic-secret")

	env := sourceGitCommandEnvironment(t.TempDir())
	joined := strings.Join(env, "\n")
	for _, forbidden := range []string{"GIT_SSL_NO_VERIFY", "GITLAB_TLS_INSECURE_", "PRIVATE_TOKEN", "synthetic-secret"} {
		if strings.Contains(joined, forbidden) {
			t.Fatalf("Git subprocess environment contains %q", forbidden)
		}
	}
	for _, required := range []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_COUNT=0", "GIT_TERMINAL_PROMPT=0"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("Git subprocess environment is missing %q", required)
		}
	}
}
