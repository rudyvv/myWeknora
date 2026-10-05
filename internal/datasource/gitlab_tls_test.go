package datasource

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
)

func allowLocalGitLabTLSFixture(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,::1,localhost")
	utils.ResetSSRFWhitelistForTest()
	t.Cleanup(utils.ResetSSRFWhitelistForTest)
}

func testGitLabHTTPClient(t *testing.T, origin string, until time.Time, now *time.Time) *http.Client {
	t.Helper()
	env := map[string]string{}
	if origin != "" {
		env[gitLabTLSInsecureOriginEnv] = origin
		env[gitLabTLSInsecureUntilEnv] = until.Format(time.RFC3339)
	}
	lookup := func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
	clock := func() time.Time { return *now }
	client, err := newGitLabTLSHTTPClient(3*time.Second, lookup, clock)
	if err != nil {
		t.Fatalf("newGitLabTLSHTTPClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

func TestGitLabTLSClientDefaultsToCertificateVerification(t *testing.T) {
	allowLocalGitLabTLSFixture(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	now := time.Now()
	client := testGitLabHTTPClient(t, "", time.Time{}, &now)
	resp, err := client.Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("self-signed TLS certificate was accepted without an explicit exception")
	}
}

func TestGitLabTLSClientExceptionIsExactAndExpiresPerRequest(t *testing.T) {
	allowLocalGitLabTLSFixture(t)
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	now := time.Now().Truncate(time.Second)
	until := now.Add(time.Hour)
	client := testGitLabHTTPClient(t, server.URL, until, &now)
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("exact configured origin rejected before expiry: %v", err)
	}
	resp.Body.Close()

	// A different origin still uses normal verification, even while the
	// configured origin's temporary exception is active.
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(100)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	resp, err = client.Get(other.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("self-signed certificate at another origin was accepted")
	}

	// Advancing the clock on an already-created client must switch to the
	// independent verified transport rather than reusing an insecure socket.
	now = until
	resp, err = client.Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("self-signed certificate was accepted after the exception expired")
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("TLS requests reaching handlers = %d, want only the pre-expiry request", got)
	}
}

func TestExpiredGitLabTLSConfigFallsBackToNormalVerification(t *testing.T) {
	allowLocalGitLabTLSFixture(t)
	untrusted := httptest.NewTLSServer(http.NotFoundHandler())
	defer untrusted.Close()
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer plain.Close()

	now := time.Now().Truncate(time.Second)
	client := testGitLabHTTPClient(t, untrusted.URL, now.Add(-time.Hour), &now)
	resp, err := client.Get(untrusted.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil {
		t.Fatal("self-signed certificate was accepted with an expired exception")
	}

	resp, err = client.Get(plain.URL)
	if err != nil {
		t.Fatalf("ordinary non-exception HTTP request failed after expiry: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("ordinary HTTP response status = %d, want no content", resp.StatusCode)
	}
}

func TestGitLabTLSExceptionRejectsCrossOriginAndDowngradeRedirects(t *testing.T) {
	allowLocalGitLabTLSFixture(t)
	var crossOriginHits atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Errorf("PRIVATE-TOKEN was forwarded to redirected origin")
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization was forwarded to redirected origin")
		}
		crossOriginHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()

	var downgradeHits atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Errorf("PRIVATE-TOKEN was forwarded on an HTTP downgrade")
		}
		downgradeHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer plain.Close()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cross-origin":
			http.Redirect(w, r, other.URL, http.StatusFound)
		case "/downgrade":
			http.Redirect(w, r, plain.URL, http.StatusFound)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	now := time.Now().Truncate(time.Second)
	client := testGitLabHTTPClient(t, server.URL, now.Add(time.Hour), &now)
	for _, path := range []string{"/cross-origin", "/downgrade"} {
		req, err := http.NewRequest(http.MethodGet, server.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("PRIVATE-TOKEN", "synthetic-token")
		req.Header.Set("Authorization", "Bearer synthetic-token")
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil {
			t.Fatalf("redirect %s was followed", path)
		}
	}
	if got := crossOriginHits.Load(); got != 0 {
		t.Fatalf("cross-origin redirect target requests = %d, want 0", got)
	}
	if got := downgradeHits.Load(); got != 0 {
		t.Fatalf("HTTP downgrade target requests = %d, want 0", got)
	}
}

func TestGitLabHTTPClientStripsCredentialsOnOtherCrossOriginRedirects(t *testing.T) {
	allowLocalGitLabTLSFixture(t)
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("PRIVATE-TOKEN") != "" {
			t.Errorf("PRIVATE-TOKEN was forwarded to another origin")
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("Authorization was forwarded to another origin")
		}
		targetHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()

	now := time.Now()
	client := testGitLabHTTPClient(t, "", time.Time{}, &now)
	req, err := http.NewRequest(http.MethodGet, source.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("PRIVATE-TOKEN", "synthetic-token")
	req.Header.Set("Authorization", "Bearer synthetic-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("verified non-exception redirect: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent || targetHits.Load() != 1 {
		t.Fatalf("redirect result = status %d, target hits %d", resp.StatusCode, targetHits.Load())
	}
}

func TestGitLabTLSConfigRejectsUnsafeOrAmbiguousValues(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, origin, until string
	}{
		{name: "missing deadline", origin: "https://gitlab.example"},
		{name: "missing origin", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "non-HTTPS", origin: "http://gitlab.example", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "path", origin: "https://gitlab.example/group", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "userinfo", origin: "https://user:pass@gitlab.example", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "query", origin: "https://gitlab.example?x=1", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "fragment", origin: "https://gitlab.example#frag", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "wildcard", origin: "https://*.gitlab.example", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "surrounding whitespace", origin: " https://gitlab.example", until: now.Add(time.Hour).Format(time.RFC3339)},
		{name: "invalid timestamp", origin: "https://gitlab.example", until: "tomorrow"},
		{name: "over 24 hours", origin: "https://gitlab.example", until: now.Add(24*time.Hour + time.Second).Format(time.RFC3339)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := func(key string) (string, bool) {
				switch key {
				case gitLabTLSInsecureOriginEnv:
					return tt.origin, tt.origin != ""
				case gitLabTLSInsecureUntilEnv:
					return tt.until, tt.until != ""
				default:
					return "", false
				}
			}
			if _, err := parseGitLabTLSException(lookup, now); err == nil {
				t.Fatal("unsafe GitLab TLS exception configuration was accepted")
			}
		})
	}

	lookup := func(key string) (string, bool) {
		if key == gitLabTLSInsecureOriginEnv {
			return "", true
		}
		return "", false
	}
	if _, err := parseGitLabTLSException(lookup, now); err == nil {
		t.Fatal("incomplete empty configuration was accepted")
	}
}

func TestGitLabTLSDefaultPortIsTheOnlyOriginNormalization(t *testing.T) {
	tests := []struct {
		configured, request string
		want                bool
	}{
		{"https://gitlab.example", "https://gitlab.example:443/api/v4/user", true},
		{"https://gitlab.example:443", "https://GITLAB.EXAMPLE/api/v4/user", true},
		{"https://gitlab.example:8443", "https://gitlab.example:443/api/v4/user", false},
		{"https://gitlab.example", "http://gitlab.example/api/v4/user", false},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s to %s", tt.configured, tt.request), func(t *testing.T) {
			origin, err := parseGitLabTLSOrigin(tt.configured)
			if err != nil {
				t.Fatal(err)
			}
			u, err := http.NewRequest(http.MethodGet, tt.request, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := origin.matches(u.URL); got != tt.want {
				t.Fatalf("origin.matches(%q) = %v, want %v", tt.request, got, tt.want)
			}
		})
	}
}
