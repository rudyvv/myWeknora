package datasource

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSplitSourceProjectsKeepsPerProjectRulesAndDefaultBranches(t *testing.T) {
	cfg := &types.DataSourceConfig{Settings: map[string]interface{}{"content_mode": "source", SourceGroupRootKey: "untrusted-root", "projects": []interface{}{
		map[string]interface{}{"project_id": "group/one", "paths": []string{}, "exclude_paths": []string{"vendor"}},
		map[string]interface{}{"project_id": "group/two", "ref": "release", "paths": []string{"src"}, "exclude_paths": []string{"src/generated"}},
	}}}
	members, err := SplitSourceProjects(cfg)
	require.NoError(t, err)
	require.Len(t, members, 2)
	first, _, err := ParseSourceSettings(members[0])
	require.NoError(t, err)
	require.Empty(t, first.Projects[0].Ref)
	require.Empty(t, first.Projects[0].Paths)
	require.Equal(t, []string{"vendor"}, first.ExcludePaths)
	second, _, err := ParseSourceSettings(members[1])
	require.NoError(t, err)
	require.Equal(t, "release", second.Projects[0].Ref)
	require.Equal(t, []string{"src/generated"}, second.ExcludePaths)
	require.NotContains(t, members[0].Settings, SourceGroupRootKey)
	require.Equal(t, "untrusted-root", cfg.Settings[SourceGroupRootKey], "normalizing a draft must not mutate its input")
}

func TestSplitSourceProjectsRejectsDuplicatesAndInvalidExclusions(t *testing.T) {
	for _, projects := range [][]interface{}{
		{},
		{map[string]interface{}{"project_id": "123"}, map[string]interface{}{"project_id": " 123 "}},
		{map[string]interface{}{"project_id": "123"}, map[string]interface{}{"project_id": ""}},
		{map[string]interface{}{"project_id": "123"}, map[string]interface{}{"project_id": "456", "exclude_paths": []string{"../outside"}}},
	} {
		_, err := SplitSourceProjects(&types.DataSourceConfig{Settings: map[string]interface{}{"content_mode": "source", "projects": projects}})
		require.Error(t, err)
	}
}
