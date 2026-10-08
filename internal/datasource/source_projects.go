package datasource

import (
	"fmt"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

// Group membership is server-owned. Each stored member remains a single-repo
// execution source, so leases, snapshots, file identities and citations stay isolated.
const SourceGroupRootKey = "source_group_root_id"

func SourceGroupRoot(ds *types.DataSource) string {
	if ds == nil || ds.Type != types.ConnectorTypeGitLab {
		return ""
	}
	cfg, err := ds.ParseConfig()
	if err != nil || cfg == nil || cfg.Settings["content_mode"] != ContentModeSource {
		return ""
	}
	id, _ := cfg.Settings[SourceGroupRootKey].(string)
	return id
}

// SplitSourceProjects validates the complete form before any writes, translating
// project-local exclusions to the existing single-repo pipeline contract.
func SplitSourceProjects(cfg *types.DataSourceConfig) ([]*types.DataSourceConfig, error) {
	projects, ok := cfg.Settings["projects"].([]interface{})
	if !ok || len(projects) == 0 {
		return nil, fmt.Errorf("%w: at least one project is required", ErrInvalidConfig)
	}
	seen := map[string]bool{}
	out := make([]*types.DataSourceConfig, 0, len(projects))
	for _, raw := range projects {
		project, ok := raw.(map[string]interface{})
		if !ok {
			return nil, ErrInvalidConfig
		}
		id, _ := project["project_id"].(string)
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return nil, fmt.Errorf("%w: project IDs must be nonempty and distinct", ErrInvalidConfig)
		}
		seen[id] = true
		member := *cfg
		member.Settings = make(map[string]interface{}, len(cfg.Settings))
		for key, value := range cfg.Settings {
			member.Settings[key] = value
		}
		delete(member.Settings, SourceGroupRootKey)
		selection := make(map[string]interface{}, len(project))
		for key, value := range project {
			selection[key] = value
		}
		selection["project_id"] = id
		if exclusions, exists := selection["exclude_paths"]; exists {
			member.Settings["exclude_paths"] = exclusions
		}
		delete(selection, "exclude_paths")
		member.Settings["projects"] = []interface{}{selection}
		if _, _, err := ParseSourceSettings(&member); err != nil {
			return nil, err
		}
		out = append(out, &member)
	}
	return out, nil
}
