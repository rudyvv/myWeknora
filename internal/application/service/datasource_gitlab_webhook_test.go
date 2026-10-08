package service

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func TestGitLabWebhookPublicServiceIsUnavailable(t *testing.T) {
	// No dependencies: withheld calls must return before accessing any source,
	// saved secret, trigger repository or queue, even with a valid-looking event.
	s := &DataSourceService{}
	ctx := context.Background()
	_, err := s.GetGitLabWebhookStatus(ctx, "source", 1)
	require.ErrorIs(t, err, datasource.ErrGitLabWebhookUnavailable)
	enabled := true
	secret := "existing-secret"
	_, err = s.ConfigureGitLabWebhook(ctx, "source", 1, types.GitLabWebhookUpdate{Enabled: &enabled, Secret: &secret})
	require.ErrorIs(t, err, datasource.ErrGitLabWebhookUnavailable)
	_, err = s.TestGitLabWebhook(ctx, "source", 1)
	require.ErrorIs(t, err, datasource.ErrGitLabWebhookUnavailable)
	accepted, duplicate, err := s.ReceiveGitLabPush(ctx, secret, types.GitLabPushEvent{
		ProjectID: "123", Ref: "refs/heads/main", After: strings.Repeat("a", 40),
	})
	require.ErrorIs(t, err, datasource.ErrGitLabWebhookUnavailable)
	require.False(t, accepted)
	require.False(t, duplicate)
	require.NoError(t, s.ProcessSync(ctx, asynq.NewTask(types.TypeDataSourceSync,
		[]byte(`{"data_source_id":"source","sync_log_id":"old-hook-run","trigger":"gitlab_webhook"}`))))
}

func TestGitLabPushScopeAndDeliveryIdentity(t *testing.T) {
	event := types.GitLabPushEvent{
		ProjectID: "123", ProjectPath: "group/repo", Ref: "refs/heads/main",
		Before: strings.Repeat("0", 40), After: strings.Repeat("a", 40),
	}
	require.True(t, validGitLabPushEvent(event))
	require.True(t, registeredGitLabProjectMatches("123", event))
	require.True(t, registeredGitLabProjectMatches("group/repo", event), "path-based registrations must match GitLab's signed project path field")
	require.False(t, registeredGitLabProjectMatches("other/repo", event))

	first := gitLabDeliveryID(event)
	require.Equal(t, first, gitLabDeliveryID(event), "legacy deliveries without an ID need stable deterministic deduplication")
	event.Ref = "refs/heads/release"
	require.NotEqual(t, first, gitLabDeliveryID(event), "deduplication identity is scoped by ref")
	event.Ref = "refs/tags/v1"
	require.False(t, validGitLabPushEvent(event), "tag pushes are outside the callback scope")
}

func TestGitLabWebhookSecretComparison(t *testing.T) {
	require.True(t, constantTimeSecretEqual("configured-secret", "configured-secret"))
	require.False(t, constantTimeSecretEqual("configured-secret", "other-secret"))
	require.False(t, constantTimeSecretEqual("", ""))
}
