package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type previewAPIDataSourceRepo struct {
	interfaces.DataSourceRepository
	source *types.DataSource
}

func (r previewAPIDataSourceRepo) FindByID(context.Context, string) (*types.DataSource, error) {
	return r.source, nil
}

type previewAPIKBRepo struct {
	interfaces.KnowledgeBaseRepository
	kb *types.KnowledgeBase
}

func (r previewAPIKBRepo) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return r.kb, nil
}

func TestSourcePreviewAPIEnforcesTenantAndKnowledgeBaseScopeBeforeRepositoryAccess(t *testing.T) {
	gin.SetMode(gin.TestMode)
	kb := &types.KnowledgeBase{ID: "kb-one", TenantID: 1}
	kbService := service.NewKnowledgeBaseService(previewAPIKBRepo{kb: kb}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	stored := &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: kb.ID, Type: "gitlab", Config: types.JSON(`{"settings":{"content_mode":"source","projects":[{"project_id":"123","ref":"main"}]}}`)}
	dsService := service.NewDataSourceService(previewAPIDataSourceRepo{source: stored}, nil, nil, kbService, nil, datasource.NewConnectorRegistry(), nil, nil, nil, nil, nil, nil, nil)
	h := NewDataSourceHandler(dsService, kbService)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		if tenant, ok := types.TenantIDFromContext(c.Request.Context()); ok {
			c.Set(types.TenantIDContextKey.String(), tenant)
		}
	})
	router.POST("/datasource/:id/source-preview", h.PreviewSource)
	for _, tc := range []struct {
		name   string
		tenant uint64
		scope  *types.TenantAPIKeyScope
		body   string
		status int
	}{
		{name: "missing tenant", body: `{}`, status: http.StatusUnauthorized},
		{name: "different tenant", tenant: 2, body: `{}`, status: http.StatusForbidden},
		{name: "KB outside API key", tenant: 1, scope: &types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb-other"}}, body: `{}`, status: http.StatusForbidden},
		{name: "invalid source selection", tenant: 1, body: `{"settings":{"content_mode":"source","projects":[{"project_id":"123"}]}}`, status: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, "/datasource/source-one/source-preview", strings.NewReader(tc.body))
			request.Header.Set("Content-Type", "application/json")
			ctx := context.WithValue(request.Context(), types.TenantIDContextKey, tc.tenant)
			if tc.scope != nil {
				ctx = types.WithTenantAPIKeyScope(ctx, *tc.scope)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request.WithContext(ctx))
			require.Equal(t, tc.status, response.Code, response.Body.String())
			require.NotContains(t, response.Body.String(), "credentials")
		})
	}
}
