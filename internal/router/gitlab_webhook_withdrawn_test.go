package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGitLabWebhookRoutesAreNotExposed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterGitLabWebhookRoutes(r, &handler.DataSourceHandler{})
	RegisterDataSourceRoutes(r.Group("/api/v1"), &handler.DataSourceHandler{}, &handler.DataSourceCredentialsHandler{}, &rbacGuards{})
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, types.GitLabWebhookCallbackPath},
		{http.MethodGet, "/api/v1/datasource/source/gitlab-webhook"},
		{http.MethodPut, "/api/v1/datasource/source/gitlab-webhook"},
		{http.MethodPost, "/api/v1/datasource/source/gitlab-webhook/test"},
		{http.MethodPost, "/api/v1/datasource/source/unbind"},
		{http.MethodPost, "/api/v1/datasource/source/clear-source"},
		{http.MethodPost, "/api/v1/datasource/source/clear-source/retry"},
	} {
		response := httptest.NewRecorder()
		r.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, http.StatusNotFound, response.Code, tc.path)
	}
	// Existing source synchronization routes remain registered.
	paths := map[string]bool{}
	for _, route := range r.Routes() {
		paths[route.Method+" "+route.Path] = true
	}
	for _, path := range []string{"sync", "pause", "resume"} {
		require.True(t, paths["POST /api/v1/datasource/:id/"+path])
	}
	require.True(t, paths["GET /api/v1/datasource/:id/logs"])
	require.True(t, paths["DELETE /api/v1/datasource/:id"])
}
