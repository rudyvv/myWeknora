package gitlab

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGitLabAPIClientUsesTemporaryTLSException(t *testing.T) {
	allowLocalGitLabServer(t)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/user" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("PRIVATE-TOKEN"); got != "synthetic-api-token" {
			t.Errorf("GitLab API request did not carry its configured synthetic credential")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":42}`))
	}))
	defer server.Close()
	t.Setenv("GITLAB_TLS_INSECURE_ORIGIN", server.URL)
	t.Setenv("GITLAB_TLS_INSECURE_UNTIL", time.Now().Add(time.Hour).Format(time.RFC3339))

	c, err := newClient(server.URL, "synthetic-api-token")
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	defer c.http.CloseIdleConnections()
	if err := c.ping(context.Background()); err != nil {
		t.Fatalf("GitLab API request through configured temporary TLS exception: %v", err)
	}
}

func TestGitLabAPIClientFailsClosedOnInvalidTLSException(t *testing.T) {
	t.Setenv("GITLAB_TLS_INSECURE_ORIGIN", "https://gitlab.example/group")
	t.Setenv("GITLAB_TLS_INSECURE_UNTIL", time.Now().Add(time.Hour).Format(time.RFC3339))
	if _, err := newClient("https://gitlab.example", "synthetic-api-token"); err == nil {
		t.Fatal("newClient() accepted an unsafe TLS exception origin")
	}
}
