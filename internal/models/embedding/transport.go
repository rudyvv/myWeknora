package embedding

import (
	"context"
	"fmt"
	"net/http"
	"time"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// sharedEmbeddingHTTPTransport keeps a single SSRF-safe connection pool for
// all embedding clients. Embedders are recreated as model configuration changes,
// but their outbound connections can be safely reused across client instances,
// so the transport (and its keep-alive pool) is built once at package load.
var sharedEmbeddingHTTPTransport = secutils.NewSSRFSafeTransport(
	secutils.DefaultSSRFSafeHTTPClientConfig(),
)

type embeddingHTTPAttemptObserverKey struct{}

type embeddingHTTPAttemptObservation struct {
	inputTexts []string
	observer   func([]string)
}

type embeddingAttemptRoundTripper struct {
	base http.RoundTripper
}

func (t embeddingAttemptRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("embedding request is unavailable")
	}
	if err := req.Context().Err(); err != nil {
		return nil, err
	}
	if observation, ok := req.Context().Value(embeddingHTTPAttemptObserverKey{}).(embeddingHTTPAttemptObservation); ok && observation.observer != nil {
		observation.observer(observation.inputTexts)
	}
	return t.base.RoundTrip(req)
}

// WithHTTPAttemptObserver attaches a per-call observer for outbound embedding
// HTTP RoundTrip attempts. It contains no request data and is inert unless the
// embedder uses the shared embedding HTTP client.
func WithHTTPAttemptObserver(ctx context.Context, inputTexts []string, observer func([]string)) context.Context {
	if ctx == nil || observer == nil {
		return ctx
	}
	return context.WithValue(ctx, embeddingHTTPAttemptObserverKey{}, embeddingHTTPAttemptObservation{
		inputTexts: append([]string(nil), inputTexts...), observer: observer,
	})
}

// WithHTTPAttemptInputTexts narrows the input text estimate for an outbound
// request made from a multi-request BatchEmbed implementation.
func WithHTTPAttemptInputTexts(ctx context.Context, inputTexts []string) context.Context {
	if ctx == nil {
		return ctx
	}
	observation, ok := ctx.Value(embeddingHTTPAttemptObserverKey{}).(embeddingHTTPAttemptObservation)
	if !ok {
		return ctx
	}
	observation.inputTexts = append([]string(nil), inputTexts...)
	return context.WithValue(ctx, embeddingHTTPAttemptObserverKey{}, observation)
}

// validateEmbeddingBaseURL checks that a resolved embedding API base URL is safe
// for outbound requests. Empty URLs are allowed (callers apply provider defaults).
func validateEmbeddingBaseURL(baseURL string) error {
	if baseURL == "" {
		return nil
	}
	if err := secutils.ValidateURLForSSRF(baseURL); err != nil {
		return fmt.Errorf("base URL SSRF check failed: %w", err)
	}
	return nil
}

// newEmbeddingHTTPClient returns an HTTP client with connection-level SSRF
// protection and redirect validation, aligned with internal/models/chat/transport.go.
// All clients share sharedEmbeddingHTTPTransport so keep-alive connections are
// pooled globally, while each keeps its own timeout.
func newEmbeddingHTTPClient(timeout time.Duration) *http.Client {
	cfg := secutils.DefaultSSRFSafeHTTPClientConfig()
	cfg.Timeout = timeout
	return secutils.NewSSRFSafeHTTPClientWithTransport(cfg, embeddingAttemptRoundTripper{base: sharedEmbeddingHTTPTransport})
}
