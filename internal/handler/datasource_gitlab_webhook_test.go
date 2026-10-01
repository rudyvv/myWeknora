package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gitLabWebhookHTTPService struct {
	interfaces.DataSourceService
	interfaces.GitLabWebhookService
	ds         *types.DataSource
	status     *types.GitLabWebhookStatus
	testResult *types.GitLabWebhookTestResult
	update     types.GitLabWebhookUpdate
}

func (s *gitLabWebhookHTTPService) GetDataSource(context.Context, string) (*types.DataSource, error) {
	return s.ds, nil
}

func (s *gitLabWebhookHTTPService) GetGitLabWebhookStatus(context.Context, string, uint64) (*types.GitLabWebhookStatus, error) {
	return s.status, nil
}

func (s *gitLabWebhookHTTPService) ConfigureGitLabWebhook(_ context.Context, _ string, _ uint64, update types.GitLabWebhookUpdate) (*types.GitLabWebhookStatus, error) {
	s.update = update
	return s.status, nil
}

func (s *gitLabWebhookHTTPService) TestGitLabWebhook(context.Context, string, uint64) (*types.GitLabWebhookTestResult, error) {
	return s.testResult, nil
}

type gitLabWebhookHTTPKBService struct {
	interfaces.KnowledgeBaseService
	kb *types.KnowledgeBase
}

func (s gitLabWebhookHTTPKBService) GetKnowledgeBaseByID(context.Context, string) (*types.KnowledgeBase, error) {
	return s.kb, nil
}

func TestGitLabWebhookAdminHTTPConfigurationAndTestStatus(t *testing.T) {
	gin.SetMode(gin.TestMode)
	secret := "must-not-be-returned"
	status := &types.GitLabWebhookStatus{Enabled: true, Configured: true, CallbackPath: types.GitLabWebhookCallbackPath}
	service := &gitLabWebhookHTTPService{
		ds:     &types.DataSource{ID: "source-1", TenantID: 7, KnowledgeBaseID: "kb-1"},
		status: status,
		testResult: &types.GitLabWebhookTestResult{
			GitLabAccessStatus: "connected", ProjectID: "123", Branch: "main", InboundStatus: "unverified",
			Webhook: *status, SyncSchedule: "0 0 * * * *",
		},
	}
	h := NewDataSourceHandler(service, gitLabWebhookHTTPKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 7}})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(7))
		c.Next()
	})
	r.GET("/datasource/:id/gitlab-webhook", h.GetGitLabWebhook)
	r.PUT("/datasource/:id/gitlab-webhook", h.UpdateGitLabWebhook)
	r.POST("/datasource/:id/gitlab-webhook/test", h.TestGitLabWebhook)

	get := httptest.NewRecorder()
	r.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/datasource/source-1/gitlab-webhook", nil))
	require.Equal(t, http.StatusOK, get.Code)
	require.NotContains(t, get.Body.String(), secret)
	require.Contains(t, get.Body.String(), types.GitLabWebhookCallbackPath)

	put := httptest.NewRecorder()
	r.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/datasource/source-1/gitlab-webhook", strings.NewReader(`{"enabled":true,"secret":"`+secret+`"}`)))
	require.Equal(t, http.StatusOK, put.Code)
	require.Equal(t, "must-not-be-returned", *service.update.Secret)
	require.True(t, *service.update.Enabled)
	require.NotContains(t, put.Body.String(), secret)

	test := httptest.NewRecorder()
	r.ServeHTTP(test, httptest.NewRequest(http.MethodPost, "/datasource/source-1/gitlab-webhook/test", strings.NewReader(`{}`)))
	require.Equal(t, http.StatusOK, test.Code)
	var result types.GitLabWebhookTestResult
	require.NoError(t, json.Unmarshal(test.Body.Bytes(), &result))
	require.Equal(t, "connected", result.GitLabAccessStatus)
	require.Equal(t, "unverified", result.InboundStatus, "outbound success must not imply an inbound delivery")
	require.Equal(t, "123", result.ProjectID)
	require.Equal(t, "main", result.Branch)
}

func TestGitLabWebhookAdminHTTPEnforcesOwningTenant(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &gitLabWebhookHTTPService{
		ds:     &types.DataSource{ID: "source-1", TenantID: 7, KnowledgeBaseID: "kb-1"},
		status: &types.GitLabWebhookStatus{CallbackPath: types.GitLabWebhookCallbackPath},
	}
	h := NewDataSourceHandler(service, gitLabWebhookHTTPKBService{kb: &types.KnowledgeBase{ID: "kb-1", TenantID: 8}})
	r := gin.New()
	r.Use(func(c *gin.Context) {
		c.Set(types.TenantIDContextKey.String(), uint64(7))
		c.Next()
	})
	r.GET("/datasource/:id/gitlab-webhook", h.GetGitLabWebhook)
	response := httptest.NewRecorder()
	r.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/datasource/source-1/gitlab-webhook", nil))
	require.Equal(t, http.StatusForbidden, response.Code)
}
