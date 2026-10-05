package embedding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func TestNewEmbeddingHTTPClient_ReusesTransport(t *testing.T) {
	firstTimeout := 15 * time.Second
	secondTimeout := 45 * time.Second
	first := newEmbeddingHTTPClient(firstTimeout)
	second := newEmbeddingHTTPClient(secondTimeout)

	if first == second {
		t.Fatal("expected distinct HTTP clients")
	}
	firstGuard, ok := first.Transport.(*secutils.SSRFValidatingRoundTripper)
	if !ok {
		t.Fatalf("expected SSRF-validating transport, got %T", first.Transport)
	}
	secondGuard, ok := second.Transport.(*secutils.SSRFValidatingRoundTripper)
	if !ok {
		t.Fatalf("expected SSRF-validating transport, got %T", second.Transport)
	}
	if firstGuard.Base != secondGuard.Base {
		t.Fatal("expected embedding HTTP clients to share a base transport")
	}
	firstObserved, ok := firstGuard.Base.(embeddingAttemptRoundTripper)
	if !ok {
		t.Fatalf("expected attempt observer wrapper, got %T", firstGuard.Base)
	}
	secondObserved, ok := secondGuard.Base.(embeddingAttemptRoundTripper)
	if !ok {
		t.Fatalf("expected attempt observer wrapper, got %T", secondGuard.Base)
	}
	if firstObserved.base != http.RoundTripper(sharedEmbeddingHTTPTransport) || secondObserved.base != http.RoundTripper(sharedEmbeddingHTTPTransport) {
		t.Fatal("expected embedding HTTP client to use the shared transport")
	}
	if first.Timeout != firstTimeout {
		t.Fatalf("unexpected first client timeout: got %v, want %v", first.Timeout, firstTimeout)
	}
	if second.Timeout != secondTimeout {
		t.Fatalf("unexpected second client timeout: got %v, want %v", second.Timeout, secondTimeout)
	}
}

func TestEmbeddingHTTPAttemptObserverCountsProviderRetryAndSkipsCanceledRequest(t *testing.T) {
	t.Setenv("SSRF_WHITELIST", "127.0.0.1")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	var attempts atomic.Int64
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack first attempt: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2],"index":0}]}`))
	}))
	defer server.Close()
	embedder, err := NewOpenAIEmbedder("test-key", server.URL, "text-embedding-3-small", 511, 2, "model-id", nil)
	if err != nil {
		t.Fatal(err)
	}
	embedder.maxRetries = 1
	var observedInputs [][]string
	ctx := WithHTTPAttemptObserver(context.Background(), []string{"hello"}, func(inputTexts []string) {
		attempts.Add(1)
		observedInputs = append(observedInputs, append([]string(nil), inputTexts...))
	})
	if _, err := embedder.BatchEmbed(ctx, []string{"hello"}); err != nil {
		t.Fatalf("BatchEmbed() after retry: %v", err)
	}
	if got := attempts.Load(); got != 2 {
		t.Fatalf("observed HTTP attempts = %d, want 2", got)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP fixture requests = %d, want 2", got)
	}
	if len(observedInputs) != 2 || len(observedInputs[0]) != 1 || len(observedInputs[1]) != 1 ||
		observedInputs[0][0] != "hello" || observedInputs[1][0] != "hello" {
		t.Fatalf("retry input estimates were not tied to each request attempt: %#v", observedInputs)
	}

	ctx, cancel := context.WithCancel(WithHTTPAttemptObserver(context.Background(), nil, func([]string) { attempts.Add(1) }))
	cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = newEmbeddingHTTPClient(time.Second).Do(req)
	if got := attempts.Load(); got != 2 {
		t.Fatalf("canceled request changed observed attempts to %d, want 2", got)
	}
}

func TestValidateEmbeddingBaseURL_RejectsLoopback(t *testing.T) {
	err := validateEmbeddingBaseURL("http://169.254.169.254/latest/meta-data")
	if err == nil {
		t.Fatal("expected SSRF error for link-local metadata URL")
	}
	if !strings.Contains(err.Error(), "SSRF") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateEmbeddingBaseURL_AllowsEmpty(t *testing.T) {
	if err := validateEmbeddingBaseURL(""); err != nil {
		t.Fatalf("empty base URL should be allowed: %v", err)
	}
}

func TestNewOpenAIEmbedder_RejectsPrivateBaseURL(t *testing.T) {
	_, err := NewOpenAIEmbedder(
		"test-key",
		"http://169.254.169.254/latest/meta-data",
		"text-embedding-3-small",
		511,
		256,
		"model-id",
		nil,
	)
	if err == nil {
		t.Fatal("expected SSRF rejection for link-local metadata URL")
	}
}
