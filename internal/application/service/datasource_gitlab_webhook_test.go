package service

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

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
