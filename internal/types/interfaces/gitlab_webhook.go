package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// GitLabWebhookRepository stores optional hook configuration and atomically
// accepts/deduplicates Push Hook receipts with source-sync triggers.
type GitLabWebhookRepository interface {
	GetGitLabWebhookConfig(context.Context, string, uint64) (*types.GitLabWebhookConfig, error)
	SetGitLabWebhookConfig(context.Context, string, uint64, types.GitLabWebhookUpdate) error
	RegisterGitLabPushTrigger(context.Context, *types.DataSource, *types.SyncLog, types.GitLabPushEvent, string) (shouldDispatch bool, duplicate bool, generation int64, err error)
}

// GitLabWebhookService is deliberately separate from general data-source
// management so non-webhook implementations and existing connector fakes do
// not acquire unrelated methods.
type GitLabWebhookService interface {
	GetGitLabWebhookStatus(context.Context, string, uint64) (*types.GitLabWebhookStatus, error)
	ConfigureGitLabWebhook(context.Context, string, uint64, types.GitLabWebhookUpdate) (*types.GitLabWebhookStatus, error)
	TestGitLabWebhook(context.Context, string, uint64) (*types.GitLabWebhookTestResult, error)
	ReceiveGitLabPush(context.Context, string, types.GitLabPushEvent) (accepted bool, duplicate bool, err error)
}
