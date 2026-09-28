package gitlab

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/types"
)

func (c *Connector) ResolveSourceRepository(ctx context.Context, ds *types.DataSourceConfig) (*types.SourceRepository, error) {
	settings, _, err := datasource.ParseSourceSettings(ds)
	if err != nil {
		return nil, err
	}
	configured, err := c.configured(ds)
	if err != nil {
		return nil, err
	}
	if err := configured.validateSourceToken(ctx); err != nil {
		return nil, err
	}
	selection := settings.Projects[0]
	project, err := configured.client.project(ctx, selection.ProjectID)
	if err != nil {
		return nil, err
	}
	var branch struct {
		Name   string `json:"name"`
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	if err := configured.client.get(ctx, "/projects/"+projectPath(selection.ProjectID)+"/repository/branches/"+url.PathEscape(selection.Ref), &branch); err != nil {
		return nil, err
	}
	if branch.Name != selection.Ref || !validObjectID(branch.Commit.ID) {
		return nil, fmt.Errorf("GitLab branch did not resolve to a commit")
	}
	clone, err := url.Parse(project.HTTPURLToRepo)
	base, _ := url.Parse(configured.client.baseURL)
	if err != nil || clone.User != nil || clone.RawQuery != "" || clone.Fragment != "" || clone.Host != base.Host || clone.Scheme != base.Scheme || (clone.Scheme != "https" && clone.Scheme != "http") || !strings.HasSuffix(clone.Path, ".git") {
		return nil, fmt.Errorf("GitLab clone URL must use the configured instance origin")
	}
	return &types.SourceRepository{ProjectID: selection.ProjectID, Branch: selection.Ref, CommitSHA: branch.Commit.ID, CloneURL: clone.String(), Token: configured.client.token}, nil
}

// Credential rotation remains possible when a selected branch is unavailable.
// Repository and branch availability are checked separately during preview.
func (c *Connector) validateSourceToken(ctx context.Context) error {
	var token struct {
		Active bool     `json:"active"`
		Scopes []string `json:"scopes"`
	}
	if c.client.get(ctx, "/personal_access_tokens/self", &token) != nil {
		return fmt.Errorf("cannot verify read-only GitLab token scopes; token self API must be available")
	}
	readAPI, readRepository := false, false
	for _, scope := range token.Scopes {
		switch scope {
		case "read_api":
			readAPI = true
		case "read_repository":
			readRepository = true
		case "read_user":
		default:
			return fmt.Errorf("source mode requires read-only GitLab token scopes read_api and read_repository")
		}
	}
	if !token.Active || !readAPI || !readRepository {
		return fmt.Errorf("source mode requires read-only GitLab token scopes read_api and read_repository")
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
