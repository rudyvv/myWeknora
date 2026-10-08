package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestResolveSourceRepositoryUsesGitLabDefaultWithoutGuessing(t *testing.T) {
	allowLocalGitLabServer(t)
	defaultBranch := "release/current"
	requestedBranches := []string{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v4/personal_access_tokens/self":
			_, _ = w.Write([]byte(`{"active":true,"scopes":["read_api","read_repository"]}`))
		case r.URL.Path == "/api/v4/projects/123":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "default_branch": defaultBranch, "http_url_to_repo": server.URL + "/repo.git"})
		case strings.HasPrefix(r.URL.Path, "/api/v4/projects/123/repository/branches/"):
			branch := strings.TrimPrefix(r.URL.Path, "/api/v4/projects/123/repository/branches/")
			requestedBranches = append(requestedBranches, branch)
			if branch == "missing" {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": branch, "commit": map[string]string{"id": strings.Repeat("a", 40)}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	selection := map[string]interface{}{"project_id": "123"}
	config := &types.DataSourceConfig{
		Credentials: map[string]interface{}{"base_url": server.URL, "access_token": "fixture-token"},
		Settings:    map[string]interface{}{"content_mode": "source", "projects": []interface{}{selection}},
	}
	connector := NewConnector()
	resolved, err := connector.ResolveSourceRepository(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, "release/current", resolved.Branch)
	require.Equal(t, strings.Repeat("a", 40), resolved.CommitSHA)
	defaultBranch = "development"
	selection["ref"] = "  "
	resolved, err = connector.ResolveSourceRepository(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, "development", resolved.Branch, "a new resolution follows GitLab's current default")
	selection["ref"] = " hotfix "
	resolved, err = connector.ResolveSourceRepository(context.Background(), config)
	require.NoError(t, err)
	require.Equal(t, "hotfix", resolved.Branch, "explicit branch overrides the default")
	selection["ref"] = "missing"
	_, err = connector.ResolveSourceRepository(context.Background(), config)
	require.Error(t, err, "a missing explicit branch must not fall back to the default")
	selection["ref"] = ""
	defaultBranch = ""
	_, err = connector.ResolveSourceRepository(context.Background(), config)
	require.ErrorContains(t, err, "no default branch")
	require.Equal(t, []string{"release/current", "development", "hotfix", "missing"}, requestedBranches, "no branch guesses for an empty repository")
}
