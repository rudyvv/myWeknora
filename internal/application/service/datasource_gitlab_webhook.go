package service

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const defaultGitLabWebhookReconciliationSchedule = datasource.DefaultSourceSyncSchedule

func (s *DataSourceService) gitLabWebhookRepository() (interfaces.GitLabWebhookRepository, error) {
	repository, ok := s.syncLogRepo.(interfaces.GitLabWebhookRepository)
	if !ok {
		return nil, datasource.ErrGitLabWebhookUnavailable
	}
	return repository, nil
}

func hasGitLabSourceCredentials(config *types.DataSourceConfig) bool {
	if config == nil {
		return false
	}
	baseURL, _ := config.Credentials["base_url"].(string)
	token, _ := config.Credentials["access_token"].(string)
	return strings.TrimSpace(baseURL) != "" && strings.TrimSpace(token) != ""
}

func (s *DataSourceService) gitLabSourceScope(ctx context.Context, id string, tenantID uint64) (*types.DataSource, *types.DataSourceConfig, string, string, error) {
	ds, err := s.GetDataSource(ctx, id)
	if err != nil || ds == nil || ds.TenantID != tenantID || ds.Type != types.ConnectorTypeGitLab {
		return nil, nil, "", "", datasource.ErrDataSourceNotFound
	}
	config, err := ds.ParseConfig()
	if err != nil || config == nil {
		return nil, nil, "", "", datasource.ErrInvalidConfig
	}
	mode, err := datasource.ContentMode(config)
	if err != nil || mode != datasource.ContentModeSource {
		return nil, nil, "", "", datasource.ErrInvalidConfig
	}
	settings, _, err := datasource.ParseSourceSettings(config)
	if err != nil || len(settings.Projects) != 1 {
		return nil, nil, "", "", datasource.ErrInvalidConfig
	}
	if !hasGitLabSourceCredentials(config) {
		return nil, nil, "", "", datasource.ErrInvalidCredentials
	}
	return ds, config, settings.Projects[0].ProjectID, settings.Projects[0].Ref, nil
}

func webhookStatus(config *types.GitLabWebhookConfig) *types.GitLabWebhookStatus {
	return &types.GitLabWebhookStatus{
		Enabled: config.Enabled, Configured: config.Secret != "", CallbackPath: types.GitLabWebhookCallbackPath,
		LastReceivedAt: config.LastReceivedAt, LastEventID: config.LastEventID,
	}
}

func (s *DataSourceService) GetGitLabWebhookStatus(ctx context.Context, id string, tenantID uint64) (*types.GitLabWebhookStatus, error) {
	ds, _, _, _, err := s.gitLabSourceScope(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	repo, err := s.gitLabWebhookRepository()
	if err != nil {
		return nil, err
	}
	config, err := repo.GetGitLabWebhookConfig(ctx, ds.ID, ds.TenantID)
	if err != nil {
		return nil, err
	}
	return webhookStatus(config), nil
}

func (s *DataSourceService) ConfigureGitLabWebhook(ctx context.Context, id string, tenantID uint64, update types.GitLabWebhookUpdate) (*types.GitLabWebhookStatus, error) {
	ds, _, _, _, err := s.gitLabSourceScope(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	if update.Secret != nil && (strings.TrimSpace(*update.Secret) == "" || len(*update.Secret) > 1024) {
		return nil, errors.New("GitLab webhook secret must contain 1 to 1024 bytes")
	}
	if update.ClearSecret && update.Enabled != nil && *update.Enabled {
		return nil, errors.New("a cleared GitLab webhook secret cannot be enabled")
	}
	if update.ClearSecret && update.Secret != nil {
		return nil, errors.New("a GitLab webhook secret cannot be rotated and cleared in the same request")
	}
	repo, err := s.gitLabWebhookRepository()
	if err != nil {
		return nil, err
	}
	if err := repo.SetGitLabWebhookConfig(ctx, ds.ID, ds.TenantID, update); err != nil {
		return nil, err
	}
	return s.GetGitLabWebhookStatus(ctx, ds.ID, ds.TenantID)
}

// TestGitLabWebhook independently reports read-only project/branch access and
// inbound delivery evidence. A successful GitLab read never marks the inbound
// webhook verified; only an authenticated receipt does that.
func (s *DataSourceService) TestGitLabWebhook(ctx context.Context, id string, tenantID uint64) (*types.GitLabWebhookTestResult, error) {
	ds, _, projectID, branch, err := s.gitLabSourceScope(ctx, id, tenantID)
	if err != nil {
		return nil, err
	}
	status, err := s.GetGitLabWebhookStatus(ctx, ds.ID, tenantID)
	if err != nil {
		return nil, err
	}
	schedule := ds.SyncSchedule
	if strings.TrimSpace(schedule) == "" {
		schedule = defaultGitLabWebhookReconciliationSchedule
	}
	inbound := "unverified"
	if status.LastReceivedAt != nil {
		inbound = "verified"
	}
	result := &types.GitLabWebhookTestResult{
		GitLabAccessStatus: "failed", ProjectID: projectID, Branch: branch,
		InboundStatus: inbound, Webhook: *status, SyncSchedule: schedule,
	}
	preview, err := s.PreviewSource(ctx, ds.ID, nil)
	if err != nil {
		result.GitLabAccessError = err.Error()
		return result, nil
	}
	result.GitLabAccessStatus = "connected"
	result.CurrentCommitSHA = preview.CommitSHA
	return result, nil
}

func validGitLabPushEvent(event types.GitLabPushEvent) bool {
	if event.ProjectID == "" || !strings.HasPrefix(event.Ref, "refs/heads/") || len(event.Ref) <= len("refs/heads/") {
		return false
	}
	for _, revision := range []string{event.Before, event.After} {
		if len(revision) != 40 && len(revision) != 64 {
			return false
		}
		for _, ch := range revision {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f') || (ch >= 'A' && ch <= 'F')) {
				return false
			}
		}
	}
	return true
}

func gitLabDeliveryID(event types.GitLabPushEvent) string {
	if event.DeliveryID != "" {
		return event.DeliveryID
	}
	identity := strings.Join([]string{event.ProjectID, event.Ref, event.Before, event.After}, "\x00")
	hash := sha256.Sum256([]byte(identity))
	return "sha256:" + hex.EncodeToString(hash[:])
}

func registeredGitLabProjectMatches(registeredProject string, event types.GitLabPushEvent) bool {
	return registeredProject != "" && (registeredProject == event.ProjectID || registeredProject == event.ProjectPath)
}

func (s *DataSourceService) ReceiveGitLabPush(ctx context.Context, suppliedSecret string, event types.GitLabPushEvent) (accepted bool, duplicate bool, err error) {
	if suppliedSecret == "" || !validGitLabPushEvent(event) || len(event.DeliveryID) > 256 || len(event.EventID) > 256 {
		return false, false, datasource.ErrGitLabWebhookUnauthorized
	}
	active, err := s.dsRepo.FindActive(ctx)
	if err != nil {
		return false, false, err
	}
	repo, err := s.gitLabWebhookRepository()
	if err != nil {
		return false, false, err
	}
	branch := strings.TrimPrefix(event.Ref, "refs/heads/")
	type authenticatedSource struct {
		ds     *types.DataSource
		secret string
	}
	matches := make([]authenticatedSource, 0, 1)
	for _, candidate := range active {
		if candidate == nil || candidate.Type != types.ConnectorTypeGitLab || candidate.Status != types.DataSourceStatusActive {
			continue
		}
		config, parseErr := candidate.ParseConfig()
		if parseErr != nil || config == nil {
			continue
		}
		mode, modeErr := datasource.ContentMode(config)
		if modeErr != nil || mode != datasource.ContentModeSource || !hasGitLabSourceCredentials(config) {
			continue
		}
		settings, _, settingsErr := datasource.ParseSourceSettings(config)
		if settingsErr != nil || len(settings.Projects) != 1 ||
			!registeredGitLabProjectMatches(settings.Projects[0].ProjectID, event) ||
			settings.Projects[0].Ref != branch {
			continue
		}
		hook, hookErr := repo.GetGitLabWebhookConfig(ctx, candidate.ID, candidate.TenantID)
		if hookErr != nil {
			return false, false, hookErr
		}
		if !hook.Enabled || hook.Secret == "" {
			continue
		}
		if !constantTimeSecretEqual(hook.Secret, suppliedSecret) {
			continue
		}
		matches = append(matches, authenticatedSource{ds: candidate, secret: hook.Secret})
	}
	if len(matches) != 1 {
		return false, false, datasource.ErrGitLabWebhookUnauthorized
	}
	matched, authenticatedSecret := matches[0].ds, matches[0].secret
	event.DeliveryID = gitLabDeliveryID(event)
	if event.EventID == "" {
		event.EventID = event.DeliveryID
	}
	log := &types.SyncLog{DataSourceID: matched.ID, TenantID: matched.TenantID, Status: types.SyncLogStatusQueued, StartedAt: time.Now().UTC()}
	shouldDispatch, duplicate, generation, err := repo.RegisterGitLabPushTrigger(ctx, matched, log, event, authenticatedSecret)
	if err != nil {
		if errors.Is(err, datasource.ErrGitLabWebhookUnauthorized) || errors.Is(err, datasource.ErrDataSourceNotActive) {
			return false, false, datasource.ErrGitLabWebhookUnauthorized
		}
		return false, false, err
	}
	if duplicate {
		return true, true, nil
	}
	if shouldDispatch {
		_, enqueueErr := datasource.EnqueueSourceSync(ctx, s.taskEnqueuer, types.SourceSyncDispatch{
			SyncLog: log, Trigger: "gitlab_webhook", DeliveryGeneration: generation,
		}, matched.TenantID, matched.ID, types.TaskInitiator{})
		if enqueueErr != nil {
			// The receipt and coordinator run already committed. The regular
			// source-trigger recovery loop will redeliver this durable trigger.
			logger.Warnf(ctx, "GitLab Push Hook accepted with pending source trigger ds=%s syncLog=%s: %v", matched.ID, log.ID, enqueueErr)
		}
	}
	return true, false, nil
}

func constantTimeSecretEqual(a, b string) bool {
	left, right := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return len(a) > 0 && subtle.ConstantTimeCompare(left[:], right[:]) == 1
}
