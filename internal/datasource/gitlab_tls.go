package datasource

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/utils"
)

const (
	gitLabTLSInsecureOriginEnv = "GITLAB_TLS_INSECURE_ORIGIN"
	gitLabTLSInsecureUntilEnv  = "GITLAB_TLS_INSECURE_UNTIL"
	gitLabTLSExceptionMaxAge   = 24 * time.Hour
)

type gitLabTLSOrigin struct {
	key string
}

type gitLabTLSException struct {
	origin  gitLabTLSOrigin
	until   time.Time
	enabled bool
}

// NewGitLabTLSHTTPClient creates a GitLab-only HTTP client that preserves the
// normal SSRF protections and enables an optional, tightly scoped TLS exception.
// The exception is read from process environment and is disabled by default.
func NewGitLabTLSHTTPClient(timeout time.Duration) (*http.Client, error) {
	return newGitLabTLSHTTPClient(timeout, os.LookupEnv, time.Now)
}

func newGitLabTLSHTTPClient(
	timeout time.Duration,
	lookupEnv func(string) (string, bool),
	now func() time.Time,
) (*http.Client, error) {
	if now == nil {
		now = time.Now
	}
	exception, err := parseGitLabTLSException(lookupEnv, now())
	if err != nil {
		return nil, err
	}

	verified := NewConnectorHTTPClient(timeout)
	transportConfig := utils.DefaultSSRFSafeHTTPClientConfig()
	transportConfig.Timeout = timeout
	insecureTransport := utils.NewSSRFSafeTransport(transportConfig)
	// This is the only certificate-verification bypass. The dispatcher selects
	// this separate pool only for the configured HTTPS origin and before expiry;
	// its SSRF-validating wrapper and dial-time IP checks remain in force.
	insecureTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // #nosec G402 -- scoped by gitLabTLSException.
	insecure := utils.NewSSRFSafeHTTPClientWithTransport(transportConfig, insecureTransport)

	verified.Transport = &gitLabTLSRoundTripper{
		verified:  verified.Transport,
		insecure:  insecure.Transport,
		exception: exception,
		now:       now,
	}
	defaultRedirectCheck := verified.CheckRedirect
	verified.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return checkGitLabRedirect(exception, defaultRedirectCheck, req, via)
	}
	return verified, nil
}

type gitLabTLSRoundTripper struct {
	verified  http.RoundTripper
	insecure  http.RoundTripper
	exception gitLabTLSException
	now       func() time.Time
}

func (t *gitLabTLSRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req != nil && req.URL != nil && t.exception.allows(req.URL, t.now()) {
		return t.insecure.RoundTrip(req)
	}
	return t.verified.RoundTrip(req)
}

func (t *gitLabTLSRoundTripper) CloseIdleConnections() {
	closeIdleConnections(t.verified)
	closeIdleConnections(t.insecure)
}

func closeIdleConnections(transport http.RoundTripper) {
	if closer, ok := transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func parseGitLabTLSException(lookupEnv func(string) (string, bool), now time.Time) (gitLabTLSException, error) {
	if lookupEnv == nil {
		return gitLabTLSException{}, fmt.Errorf("GitLab TLS configuration is unavailable")
	}
	originValue, originSet := lookupEnv(gitLabTLSInsecureOriginEnv)
	untilValue, untilSet := lookupEnv(gitLabTLSInsecureUntilEnv)
	if (originSet && originValue != strings.TrimSpace(originValue)) || (untilSet && untilValue != strings.TrimSpace(untilValue)) {
		return gitLabTLSException{}, fmt.Errorf("GitLab TLS exception configuration contains surrounding whitespace")
	}
	originValue = strings.TrimSpace(originValue)
	untilValue = strings.TrimSpace(untilValue)
	if (!originSet && !untilSet) || (originSet && untilSet && originValue == "" && untilValue == "") {
		return gitLabTLSException{}, nil
	}
	if !originSet || !untilSet || originValue == "" || untilValue == "" {
		return gitLabTLSException{}, fmt.Errorf("GitLab TLS exception requires both origin and deadline")
	}

	origin, err := parseGitLabTLSOrigin(originValue)
	if err != nil {
		return gitLabTLSException{}, fmt.Errorf("invalid GitLab TLS exception origin")
	}
	until, err := time.Parse(time.RFC3339, untilValue)
	if err != nil {
		return gitLabTLSException{}, fmt.Errorf("invalid GitLab TLS exception deadline")
	}
	if until.After(now.Add(gitLabTLSExceptionMaxAge)) {
		return gitLabTLSException{}, fmt.Errorf("GitLab TLS exception deadline exceeds 24 hours")
	}
	if !until.After(now) {
		return gitLabTLSException{origin: origin, until: until}, nil
	}
	return gitLabTLSException{origin: origin, until: until, enabled: true}, nil
}

func parseGitLabTLSOrigin(raw string) (gitLabTLSOrigin, error) {
	if raw == "" || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "*#") {
		return gitLabTLSOrigin{}, fmt.Errorf("invalid origin")
	}
	u, err := url.Parse(raw)
	if err != nil || u == nil || strings.ToLower(u.Scheme) != "https" || u.Host == "" || u.User != nil ||
		u.Opaque != "" || u.Path != "" || u.RawPath != "" || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.RawFragment != "" {
		return gitLabTLSOrigin{}, fmt.Errorf("invalid origin")
	}
	key, ok := normalizedHTTPOrigin(u)
	if !ok {
		return gitLabTLSOrigin{}, fmt.Errorf("invalid origin")
	}
	return gitLabTLSOrigin{key: key}, nil
}

func normalizedHTTPOrigin(u *url.URL) (string, bool) {
	if u == nil || u.User != nil || u.Opaque != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	host := u.Hostname()
	if host == "" || strings.ContainsAny(host, "*%") || strings.HasSuffix(u.Host, ":") {
		return "", false
	}
	port := u.Port()
	if port == "" {
		if scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	} else {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 || strconv.Itoa(portNumber) != port {
			return "", false
		}
		port = strconv.Itoa(portNumber)
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(host), port), true
}

func (o gitLabTLSOrigin) matches(u *url.URL) bool {
	if u == nil || strings.ToLower(u.Scheme) != "https" {
		return false
	}
	key, ok := normalizedHTTPOrigin(u)
	return ok && key == o.key
}

func (e gitLabTLSException) allows(u *url.URL, now time.Time) bool {
	return e.enabled && now.Before(e.until) && e.origin.matches(u)
}

func (e gitLabTLSException) hasOrigin(u *url.URL) bool {
	return e.enabled && e.origin.matches(u)
}

func sameHTTPOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	aKey, aOK := normalizedHTTPOrigin(a)
	bKey, bOK := normalizedHTTPOrigin(b)
	return aOK && bOK && aKey == bKey
}

func checkGitLabRedirect(exception gitLabTLSException, fallback func(*http.Request, []*http.Request) error, req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return fallback(req, via)
	}

	exceptionOriginInvolved := exception.hasOrigin(req.URL)
	for _, previous := range via {
		exceptionOriginInvolved = exceptionOriginInvolved || exception.hasOrigin(previous.URL)
	}
	if exceptionOriginInvolved {
		for _, previous := range via {
			if !sameHTTPOrigin(previous.URL, req.URL) || strings.ToLower(req.URL.Scheme) != "https" {
				return fmt.Errorf("GitLab TLS exception redirect blocked")
			}
		}
	}
	if !sameHTTPOrigin(via[len(via)-1].URL, req.URL) {
		req.Header.Del("PRIVATE-TOKEN")
		req.Header.Del("Authorization")
	}
	return fallback(req, via)
}
