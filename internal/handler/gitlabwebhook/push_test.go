package gitlabwebhook

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

type pushHTTPService struct {
	interfaces.GitLabWebhookService
	secret string
	event  types.GitLabPushEvent
	calls  int
}

func (s *pushHTTPService) ReceiveGitLabPush(_ context.Context, secret string, event types.GitLabPushEvent) (bool, bool, error) {
	s.secret, s.event, s.calls = secret, event, s.calls+1
	return true, false, nil
}

func TestPushHookHTTPParsesOnlyRegisteredScopeSignalsAndDeliveryIdentity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &pushHTTPService{}
	r := gin.New()
	r.POST(types.GitLabWebhookCallbackPath, New(service).ReceivePush)
	payload := `{"object_kind":"push","project":{"id":123,"path_with_namespace":"group/repo"},"project_id":999,"ref":"refs/heads/main","before":"0000000000000000000000000000000000000000","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	request := httptest.NewRequest(http.MethodPost, types.GitLabWebhookCallbackPath, strings.NewReader(payload))
	request.Header.Set("X-Gitlab-Event", "Push Hook")
	request.Header.Set("X-Gitlab-Token", "hook-secret")
	request.Header.Set("webhook-id", "delivery-42")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)

	require.Equal(t, http.StatusAccepted, response.Code)
	require.Equal(t, 1, service.calls)
	require.Equal(t, "hook-secret", service.secret)
	require.Equal(t, "123", service.event.ProjectID, "project.id is read as a number and takes precedence over untrusted project_id")
	require.Equal(t, "group/repo", service.event.ProjectPath)
	require.Equal(t, "refs/heads/main", service.event.Ref)
	require.Equal(t, "delivery-42", service.event.DeliveryID)
	require.Equal(t, "delivery-42", service.event.EventID)
}

func TestPushHookHTTPRejectsNonPushEventsBeforeServiceCall(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service := &pushHTTPService{}
	r := gin.New()
	r.POST(types.GitLabWebhookCallbackPath, New(service).ReceivePush)
	request := httptest.NewRequest(http.MethodPost, types.GitLabWebhookCallbackPath, strings.NewReader(`{"object_kind":"merge_request"}`))
	request.Header.Set("X-Gitlab-Event", "Merge Request Hook")
	response := httptest.NewRecorder()
	r.ServeHTTP(response, request)
	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Zero(t, service.calls)
}
