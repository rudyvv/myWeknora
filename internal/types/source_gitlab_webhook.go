package types

import "time"

const GitLabWebhookCallbackPath = "/api/v1/gitlab/webhooks/push"

// GitLabWebhookConfig is the private persisted webhook state. Secret is never
// serialized and is only used while authenticating an incoming delivery.
type GitLabWebhookConfig struct {
	DataSourceID   string     `json:"-"`
	TenantID       uint64     `json:"-"`
	Enabled        bool       `json:"enabled"`
	Secret         string     `json:"-"`
	LastReceivedAt *time.Time `json:"last_received_at,omitempty"`
	LastEventID    string     `json:"last_event_id,omitempty"`
}

// GitLabWebhookStatus is the redacted admin-facing view of webhook state.
type GitLabWebhookStatus struct {
	Enabled        bool       `json:"enabled"`
	Configured     bool       `json:"configured"`
	CallbackPath   string     `json:"callback_path"`
	LastReceivedAt *time.Time `json:"last_received_at,omitempty"`
	LastEventID    string     `json:"last_event_id,omitempty"`
}

type GitLabPushEvent struct {
	ProjectID   string
	ProjectPath string
	Ref         string
	Before      string
	After       string
	EventID     string
	DeliveryID  string
}

type GitLabWebhookUpdate struct {
	Enabled     *bool
	Secret      *string
	ClearSecret bool
}

type GitLabWebhookTestResult struct {
	GitLabAccessStatus string              `json:"gitlab_access_status"`
	GitLabAccessError  string              `json:"gitlab_access_error,omitempty"`
	ProjectID          string              `json:"project_id"`
	Branch             string              `json:"branch"`
	CurrentCommitSHA   string              `json:"current_commit_sha,omitempty"`
	InboundStatus      string              `json:"inbound_status"`
	Webhook            GitLabWebhookStatus `json:"webhook"`
	SyncSchedule       string              `json:"sync_schedule"`
}
