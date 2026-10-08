package dto

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

// DataSourceResponse mirrors types.DataSource for response bodies, with the
// connector Credentials map stripped from the Config jsonb. Credential
// presence is exposed via the dedicated /credentials subresource.
//
// Unlike MCP / Model / WebSearch (which have a flat set of named credential
// fields), DataSource credentials are a per-connector atomic map — an OAuth
// token pair, a Confluence email+token bundle, etc. Splitting them at the
// field level would leave half-configured states that can't actually
// authenticate. The subresource therefore exposes only one logical field,
// "credentials", with PUT replacing the whole map and DELETE wiping it.
type DataSourceResponse struct {
	SourceProjectRemoved bool                          `json:"source_project_removed,omitempty"`
	SourceProjects       []*DataSourceResponse         `json:"source_projects,omitempty"`
	ID                   string                        `json:"id"`
	TenantID             uint64                        `json:"tenant_id"`
	KnowledgeBaseID      string                        `json:"knowledge_base_id"`
	Name                 string                        `json:"name"`
	Type                 string                        `json:"type"`
	Config               *DataSourceConfigDTO          `json:"config,omitempty"`
	SyncSchedule         string                        `json:"sync_schedule"`
	SyncMode             string                        `json:"sync_mode"`
	Status               string                        `json:"status"`
	ConflictStrategy     string                        `json:"conflict_strategy"`
	SyncDeletions        bool                          `json:"sync_deletions"`
	LastSyncAt           *time.Time                    `json:"last_sync_at"`
	LastSyncCursor       json.RawMessage               `json:"last_sync_cursor,omitempty"`
	LastSyncResult       json.RawMessage               `json:"last_sync_result,omitempty"`
	ErrorMessage         string                        `json:"error_message,omitempty"`
	SyncLogRetentionDays int                           `json:"sync_log_retention_days"`
	CreatedAt            time.Time                     `json:"created_at"`
	UpdatedAt            time.Time                     `json:"updated_at"`
	TotalItemsSynced     int64                         `json:"total_items_synced"`
	LatestSyncLog        *types.SyncLog                `json:"latest_sync_log,omitempty"`
	SourceLifecycle      *DataSourceSourceLifecycleDTO `json:"source_lifecycle,omitempty"`
	// Single logical credential field — DataSource credentials are a
	// per-connector atomic map, so "configured?" applies to the whole set.
	Credentials map[string]CredentialFieldMetadata `json:"credentials,omitempty"`
}

type DataSourceSourceLifecycleDTO struct {
	BindingState string                `json:"binding_state"`
	QueryEnabled bool                  `json:"query_enabled"`
	Cleanup      *DataSourceCleanupDTO `json:"cleanup,omitempty"`
}

type DataSourceCleanupDTO struct {
	ID        string `json:"id"`
	Status    string `json:"status"`
	Retryable bool   `json:"retryable"`
	ErrorCode string `json:"error_code,omitempty"`
}

// DataSourceConfigDTO is types.DataSourceConfig with the Credentials map
// removed by construction. Type, ResourceIDs and Settings remain visible.
type DataSourceConfigDTO struct {
	Type        string                 `json:"type"`
	ResourceIDs []string               `json:"resource_ids,omitempty"`
	Settings    map[string]interface{} `json:"settings,omitempty"`
}

// NewDataSourceResponse converts a stored entity into its response shape.
// Returns nil for nil input.
func NewDataSourceResponse(ds *types.DataSource) *DataSourceResponse {
	if ds == nil {
		return nil
	}
	var cfgDTO *DataSourceConfigDTO
	configured := false
	if parsed, err := ds.ParseConfig(); err == nil && parsed != nil {
		cfgDTO = &DataSourceConfigDTO{
			Type:        parsed.Type,
			ResourceIDs: parsed.ResourceIDs,
			Settings:    parsed.Settings,
		}
		enrichRSSFeedURLsInSettings(ds.Type, parsed, cfgDTO)
		configured = parsed.HasConfiguredCredentials(ds.Type)
	}
	response := &DataSourceResponse{
		ID:                   ds.ID,
		TenantID:             ds.TenantID,
		KnowledgeBaseID:      ds.KnowledgeBaseID,
		Name:                 ds.Name,
		Type:                 ds.Type,
		Config:               cfgDTO,
		SyncSchedule:         ds.SyncSchedule,
		SyncMode:             ds.SyncMode,
		Status:               ds.Status,
		ConflictStrategy:     ds.ConflictStrategy,
		SyncDeletions:        ds.SyncDeletions,
		LastSyncAt:           ds.LastSyncAt,
		LastSyncCursor:       json.RawMessage(ds.LastSyncCursor),
		LastSyncResult:       json.RawMessage(ds.LastSyncResult),
		ErrorMessage:         ds.ErrorMessage,
		SyncLogRetentionDays: ds.SyncLogRetentionDays,
		CreatedAt:            ds.CreatedAt,
		UpdatedAt:            ds.UpdatedAt,
		TotalItemsSynced:     ds.TotalItemsSynced,
		LatestSyncLog:        ds.LatestSyncLog,
		Credentials: map[string]CredentialFieldMetadata{
			"credentials": {Configured: configured},
		},
	}
	if cfgDTO != nil {
		response.SourceProjectRemoved = cfgDTO.Settings["source_group_removed"] == true
	}
	if configuredSourceMode(ds) {
		bindingState := ds.SourceBindingState
		if bindingState == "" {
			bindingState = types.SourceBindingBound
		}
		queryEnabled := ds.SourceQueryEnabled
		if ds.SourceBindingState == "" {
			queryEnabled = true
		}
		lifecycle := &DataSourceSourceLifecycleDTO{BindingState: bindingState, QueryEnabled: queryEnabled}
		if op := ds.SourceCleanup; op != nil {
			lifecycle.Cleanup = &DataSourceCleanupDTO{ID: op.ID, Status: op.Status, Retryable: op.Retryable, ErrorCode: op.ErrorCode}
		}
		response.SourceLifecycle = lifecycle
	}
	if ds.Type == types.ConnectorTypeWeComDrive {
		response.ErrorMessage = safeWeDriveError(ds.ErrorMessage)
		response.LastSyncResult = safeWeDriveResult(ds.LastSyncResult)
		response.LatestSyncLog = SafeWeDriveSyncLog(ds.LatestSyncLog)
	}
	if cfgDTO != nil {
		delete(cfgDTO.Settings, datasource.SourceGroupRootKey)
		delete(cfgDTO.Settings, "source_group_removed")
	}
	if len(ds.SourceProjects) > 0 && cfgDTO != nil {
		projects := []interface{}{}
		for _, member := range ds.SourceProjects {
			copy := *member
			copy.SourceProjects = nil
			item := NewDataSourceResponse(&copy)
			response.SourceProjects = append(response.SourceProjects, item)
			if item.Config == nil || item.SourceProjectRemoved {
				continue
			}
			if selection, ok := item.Config.Settings["projects"].([]interface{}); ok && len(selection) == 1 {
				project, ok := selection[0].(map[string]interface{})
				if !ok {
					continue
				}
				project["exclude_paths"] = item.Config.Settings["exclude_paths"]
				projects = append(projects, project)
			}
		}
		cfgDTO.Settings["projects"] = projects
		delete(cfgDTO.Settings, "exclude_paths")
		response.SourceProjectRemoved = false // the logical data source remains configured
		// The headline represents the group; member rows retain their own state.
		allPaused, anyError, anyBound, anyQuery := true, false, false, false
		priority := map[string]int{"running": 5, "queued": 4, "failed": 3, "partial": 2, "success": 1, "canceled": 1}
		cleanupPriority := map[string]int{"pending": 4, "running": 3, "failed": 2, "completed": 1}
		response.LatestSyncLog = nil
		if response.SourceLifecycle != nil {
			response.SourceLifecycle.Cleanup = nil
		}
		for _, member := range response.SourceProjects {
			if !member.SourceProjectRemoved {
				allPaused = allPaused && member.Status == types.DataSourceStatusPaused
				anyError = anyError || member.Status == types.DataSourceStatusError
			}
			if member.SourceLifecycle != nil {
				anyBound = anyBound || member.SourceLifecycle.BindingState == types.SourceBindingBound
				anyQuery = anyQuery || member.SourceLifecycle.QueryEnabled
				if op := member.SourceLifecycle.Cleanup; op != nil && response.SourceLifecycle != nil {
					current := response.SourceLifecycle.Cleanup
					if current == nil || cleanupPriority[op.Status] > cleanupPriority[current.Status] {
						response.SourceLifecycle.Cleanup = op
					}
				}
			}
			if log := member.LatestSyncLog; log != nil && !member.SourceProjectRemoved {
				current := response.LatestSyncLog
				if current == nil || priority[log.Status] > priority[current.Status] || priority[log.Status] == priority[current.Status] && log.StartedAt.After(current.StartedAt) {
					response.LatestSyncLog = log
				}
			}
		}
		response.Status = types.DataSourceStatusActive
		if allPaused {
			response.Status = types.DataSourceStatusPaused
		} else if anyError {
			response.Status = types.DataSourceStatusError
		}
		if response.SourceLifecycle != nil {
			response.SourceLifecycle.QueryEnabled = anyQuery
			response.SourceLifecycle.BindingState = types.SourceBindingUnbound
			if anyBound {
				response.SourceLifecycle.BindingState = types.SourceBindingBound
			}
		}
	}
	return response
}

func configuredSourceMode(ds *types.DataSource) bool {
	if ds == nil {
		return false
	}
	config, err := ds.ParseConfig()
	if err != nil || config == nil {
		return false
	}
	return config.Settings["content_mode"] == "source"
}

// Historical WeDrive failures can contain share URLs or local paths. Extract
// only connector-defined aggregate reasons; never forward arbitrary log text.
var safeWeDriveReasons = []string{
	"Agent could not create some share links because the WeCom Drive sharing UI was unavailable",
	"Agent could not create some share links; check the source user's share permission and tenant sharing policy",
	"Agent could not create some share links because the WeCom Drive sharing operation failed",
	"Some offline files do not have a usable share link",
	"Some online documents are not accessible to the configured WeCom CLI identity",
	"Some online documents could not be exported by the configured WeCom CLI identity",
	"Some offline file share links cannot be downloaded by the configured WeCom CLI identity",
	"Some offline files with a share link could not be downloaded by the configured WeCom CLI identity",
	"Some WeCom Drive content is not accessible to the configured WeCom CLI identity",
	"Some WeCom Drive content is no longer available to the configured WeCom CLI identity",
	"The configured WeCom CLI credentials were rejected",
	"The WeCom CLI bot daily file-content retrieval quota has been reached; retry after the quota resets",
	"WeCom CLI rate limit was reached; retry later",
	"WeCom CLI credential storage is unavailable",
	"WeCom CLI request timed out",
	"Some online document types are not supported",
	"Some files could not be fetched from WeCom Drive",
}

var safeWeDriveCount = regexp.MustCompile(`^\s*\(([0-9]{1,9}) items?\)`)
var safeWeDriveExtension = regexp.MustCompile(`^(?:\.[a-zA-Z0-9_-]{1,16}|\[no extension\]): [1-9][0-9]{0,8} items?$`)

func safeWeDriveError(message string) string {
	if message == "" {
		return ""
	}
	var safe []string
	for _, reason := range safeWeDriveReasons {
		index := strings.Index(message, reason)
		if index < 0 {
			continue
		}
		count := safeWeDriveCount.FindStringSubmatch(message[index+len(reason):])
		if len(count) == 2 {
			safe = append(safe, reason+" ("+count[1]+" items)")
		}
	}
	const unsupportedPrefix = "Unsupported file formats are not supported ("
	if index := strings.Index(message, unsupportedPrefix); index >= 0 {
		tail := message[index+len(unsupportedPrefix):]
		if end := strings.IndexByte(tail, ')'); end >= 0 {
			formats := strings.Split(tail[:end], "; ")
			valid := len(formats) > 0 && len(formats) <= 50
			for _, format := range formats {
				valid = valid && safeWeDriveExtension.MatchString(format)
			}
			if valid {
				safe = append(safe, unsupportedPrefix+strings.Join(formats, "; ")+")")
			}
		}
	}
	if len(safe) > 0 {
		return strings.Join(safe, "; ")
	}
	return "WeCom Drive sync failed; see server logs"
}

func safeWeDriveResult(raw types.JSON) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	// Older per-item samples may contain raw connector errors. The aggregate
	// counts remain available; individual diagnostics stay server-side.
	delete(result, "errors")
	safe, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	return safe
}

func SafeWeDriveSyncLog(log *types.SyncLog) *types.SyncLog {
	if log == nil {
		return nil
	}
	safe := *log
	safe.ErrorMessage = safeWeDriveError(log.ErrorMessage)
	safe.Result = types.JSON(safeWeDriveResult(log.Result))
	return &safe
}

// enrichRSSFeedURLsInSettings copies feed_urls from credentials into settings
// for API responses. Feed URLs are not secrets but may still live in the
// encrypted credentials blob on rows created before they moved to settings.
func enrichRSSFeedURLsInSettings(dsType string, parsed *types.DataSourceConfig, cfgDTO *DataSourceConfigDTO) {
	if dsType != types.ConnectorTypeRSS || parsed == nil || cfgDTO == nil {
		return
	}
	if cfgDTO.Settings != nil {
		if v, ok := cfgDTO.Settings["feed_urls"].(string); ok && strings.TrimSpace(v) != "" {
			return
		}
	}
	raw, ok := parsed.Credentials["feed_urls"]
	if !ok {
		return
	}
	feedURLs, ok := raw.(string)
	if !ok || strings.TrimSpace(feedURLs) == "" {
		return
	}
	if cfgDTO.Settings == nil {
		cfgDTO.Settings = make(map[string]interface{})
	}
	cfgDTO.Settings["feed_urls"] = feedURLs
}

func NewDataSourceResponses(dss []*types.DataSource) []*DataSourceResponse {
	out := make([]*DataSourceResponse, 0, len(dss))
	for _, d := range dss {
		out = append(out, NewDataSourceResponse(d))
	}
	return out
}
