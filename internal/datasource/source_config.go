package datasource

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	ContentModeDocument = "document"
	ContentModeSource   = "source"
)

// ContentMode preserves document behavior for configurations predating source mode.
func ContentMode(config *types.DataSourceConfig) (string, error) {
	if config == nil {
		return ContentModeDocument, nil
	}
	value, exists := config.Settings["content_mode"]
	if !exists {
		return ContentModeDocument, nil
	}
	mode, ok := value.(string)
	if !ok || (mode != ContentModeDocument && mode != ContentModeSource) {
		return "", fmt.Errorf("%w: content_mode must be document or source", ErrInvalidConfig)
	}
	return mode, nil
}

// ValidateContentMode checks source selection even when credentials have not yet
// been supplied. Live credential validation remains with the connector.
func ValidateContentMode(connectorType string, config *types.DataSourceConfig) error {
	mode, err := ContentMode(config)
	if err != nil || mode == ContentModeDocument {
		return err
	}
	if connectorType != types.ConnectorTypeGitLab {
		return fmt.Errorf("%w: source mode requires GitLab", ErrInvalidConfig)
	}
	projects, ok := config.Settings["projects"].([]interface{})
	if !ok || len(projects) != 1 {
		return fmt.Errorf("%w: source mode requires one project", ErrInvalidConfig)
	}
	project, ok := projects[0].(map[string]interface{})
	if !ok {
		return ErrInvalidConfig
	}
	id, _ := project["project_id"].(string)
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("%w: project_id is required", ErrInvalidConfig)
	}
	branch, _ := project["ref"].(string)
	if strings.TrimSpace(branch) == "" {
		return fmt.Errorf("%w: source mode requires a specified branch", ErrInvalidConfig)
	}
	return nil
}

type SourceSettings struct {
	Projects []struct {
		ProjectID string   `json:"project_id"`
		Ref       string   `json:"ref"`
		Paths     []string `json:"paths,omitempty"`
	} `json:"projects"`
	ExcludePaths []string `json:"exclude_paths,omitempty"`
	MaxFileBytes int64    `json:"max_file_bytes"`
}

// ParseSourceSettings validates persisted selection rules and assigns a stable
// version to their effective meaning, independent of credentials and UI order.
func ParseSourceSettings(config *types.DataSourceConfig) (*SourceSettings, string, error) {
	if err := ValidateContentMode(types.ConnectorTypeGitLab, config); err != nil {
		return nil, "", err
	}
	mode, _ := ContentMode(config)
	if mode != ContentModeSource {
		return nil, "", fmt.Errorf("%w: source mode is required", ErrInvalidConfig)
	}
	blob, err := json.Marshal(config.Settings)
	if err != nil {
		return nil, "", ErrInvalidConfig
	}
	var settings SourceSettings
	if err := json.Unmarshal(blob, &settings); err != nil {
		return nil, "", fmt.Errorf("%w: invalid source rules", ErrInvalidConfig)
	}
	settings.Projects[0].ProjectID = strings.TrimSpace(settings.Projects[0].ProjectID)
	settings.Projects[0].Ref = strings.TrimSpace(settings.Projects[0].Ref)
	if settings.MaxFileBytes == 0 {
		settings.MaxFileBytes = 2 << 20
	}
	if settings.MaxFileBytes < 1 || settings.MaxFileBytes > 16<<20 {
		return nil, "", fmt.Errorf("%w: max_file_bytes must be between 1 and 16777216", ErrInvalidConfig)
	}
	for _, rules := range []*[]string{&settings.Projects[0].Paths, &settings.ExcludePaths} {
		seen := map[string]bool{}
		out := []string{}
		for _, rule := range *rules {
			rule = strings.TrimSpace(rule)
			if rule == "" || rule == "." || path.IsAbs(rule) || path.Clean(rule) != rule || rule == ".." || strings.HasPrefix(rule, "../") || strings.ContainsAny(rule, "\\\x00\r\n:") {
				return nil, "", fmt.Errorf("%w: rules must be relative repository file or directory paths", ErrInvalidConfig)
			}
			if !seen[rule] {
				out = append(out, rule)
				seen[rule] = true
			}
		}
		sort.Strings(out)
		*rules = out
	}
	canonical, _ := json.Marshal(settings)
	return &settings, fmt.Sprintf("v1:%x", sha256.Sum256(canonical)), nil
}

// GitLabSourceReconciliationEligible reports whether a GitLab source has the
// persisted binding and credentials needed for push-triggered or scheduled
// reconciliation. Error status is recoverable; paused, deleted, and ordinary
// document sources are not.
func GitLabSourceReconciliationEligible(ds *types.DataSource) bool {
	if ds == nil || ds.Type != types.ConnectorTypeGitLab ||
		(ds.Status != types.DataSourceStatusActive && ds.Status != types.DataSourceStatusError) ||
		strings.TrimSpace(ds.KnowledgeBaseID) == "" {
		return false
	}
	config, err := ds.ParseConfig()
	if err != nil || config == nil {
		return false
	}
	mode, err := ContentMode(config)
	if err != nil || mode != ContentModeSource {
		return false
	}
	baseURL, _ := config.Credentials["base_url"].(string)
	accessToken, _ := config.Credentials["access_token"].(string)
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(accessToken) == "" {
		return false
	}
	settings, _, err := ParseSourceSettings(config)
	return err == nil && len(settings.Projects) == 1 &&
		strings.TrimSpace(settings.Projects[0].ProjectID) != "" &&
		strings.TrimSpace(settings.Projects[0].Ref) != ""
}
