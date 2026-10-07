package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type expectedCommitDataSourceAPI struct {
	interfaces.DataSourceService
	calls int
}

func (s *expectedCommitDataSourceAPI) GetDataSource(context.Context, string) (*types.DataSource, error) {
	return &types.DataSource{ID: "source-one", TenantID: 1, KnowledgeBaseID: "kb-one"}, nil
}
func (s *expectedCommitDataSourceAPI) ManualSync(context.Context, string) (*types.SyncLog, error) {
	s.calls++
	return &types.SyncLog{ID: "log-one"}, nil
}

type expectedCommitKBAPI struct {
	interfaces.KnowledgeBaseService
}

func (expectedCommitKBAPI) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return &types.KnowledgeBase{ID: "kb-one", TenantID: 1}, nil
}

func TestManualSourceSyncRejectsMalformedExpectedCommitBeforeEnqueue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ds := &expectedCommitDataSourceAPI{}
	h := NewDataSourceHandler(ds, expectedCommitKBAPI{})
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(types.TenantIDContextKey.String(), uint64(1)) })
	router.POST("/datasource/:id/sync", h.ManualSync)
	request := httptest.NewRequest(http.MethodPost, "/datasource/source-one/sync", strings.NewReader(`{"expected_commit_sha":"main"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, ds.calls)
}
