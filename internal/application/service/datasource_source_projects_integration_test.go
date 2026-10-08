//go:build integration

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/handler/dto"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

func configureSourceProjectGroup(t *testing.T, f *javaSourceFixture) *types.DataSource {
	t.Helper()
	cfg, err := f.ds.ParseConfig()
	require.NoError(t, err)
	cfg.Settings["projects"] = []interface{}{
		map[string]interface{}{"project_id": "123", "ref": "", "paths": []string{"src"}, "exclude_paths": []string{"src/ignored"}},
		map[string]interface{}{"project_id": "456", "ref": "", "paths": []string{"src"}, "exclude_paths": []string{"src/other"}},
	}
	incoming := *f.ds
	incoming.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	root, err := f.service.UpdateDataSource(f.ctx, &incoming)
	require.NoError(t, err)
	require.Equal(t, f.ds.ID, root.ID, "existing source ID and published references remain stable")
	require.Len(t, root.SourceProjects, 2)
	return root
}

func processProjectLog(t *testing.T, f *javaSourceFixture, log *types.SyncLog) error {
	t.Helper()
	payload, err := json.Marshal(types.DataSourceSyncPayload{DataSourceID: log.DataSourceID, TenantID: 1, SyncLogID: log.ID, Trigger: "manual"})
	require.NoError(t, err)
	return f.service.ProcessSync(f.ctx, asynq.NewTask(types.TypeDataSourceSync, payload))
}

func TestSourceProjectGroupPublishesRepositoriesIndependently(t *testing.T) {
	f := newJavaSourceFixture(t)
	root := configureSourceProjectGroup(t, f)
	response := dto.NewDataSourceResponse(root)
	require.Len(t, response.Config.Settings["projects"], 2)
	encoded, err := json.Marshal(response)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "fixture-token")
	require.NotContains(t, string(encoded), datasource.SourceGroupRootKey)
	list, err := f.service.ListDataSources(f.ctx, f.kb.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "two execution sources appear as one configurable data source")
	preview, err := f.service.PreviewSource(f.ctx, root.ID, response.Config.Settings)
	require.NoError(t, err)
	require.True(t, preview.CanSync)
	require.Len(t, preview.Projects, 2)
	for _, project := range preview.Projects {
		require.Equal(t, "main", project.Branch)
	}
	_, err = f.service.ManualSync(f.ctx, root.ID)
	require.NoError(t, err)
	logs, err := f.service.GetSyncLogs(f.ctx, root.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2)
	projectLogs, err := f.service.GetSourceProjectLogs(f.ctx, root.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, projectLogs, 1)
	require.Equal(t, root.ID, projectLogs[0].DataSourceID)
	for _, log := range logs {
		require.NoError(t, processProjectLog(t, f, log))
	}
	for _, project := range root.SourceProjects {
		published, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, project.ID)
		require.NoError(t, err)
		require.Equal(t, sourceProjectID(project), published.Snapshot.ProjectID)
		require.Equal(t, f.sha, published.Snapshot.CommitSHA)
	}
	hits, err := f.kbs.HybridSearch(f.ctx, f.kb.ID, types.SearchParams{QueryText: "getPushSchedule", MatchCount: 10})
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.NotEqual(t, hits[0].KnowledgeID, hits[1].KnowledgeID, "same path in separate repositories cannot overwrite one file identity")
	for _, hit := range hits {
		view, err := f.knowledge.GetSourceFile(f.ctx, hit.KnowledgeID)
		require.NoError(t, err)
		require.Equal(t, f.sha, view.CommitSHA)
	}
	// A failure after both projects were queued must not roll back the other
	// project's publication or replace this project's previous usable snapshot.
	f.gitlabProject456Missing = true
	_, err = f.service.ManualSync(f.ctx, root.ID)
	require.NoError(t, err)
	logs, err = f.service.GetSyncLogs(f.ctx, root.ID, 2, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2)
	for _, log := range logs {
		err := processProjectLog(t, f, log)
		if log.SourceProjectID == "456" {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
		}
	}
	for _, project := range root.SourceProjects {
		published, err := f.service.sourceSnapshots.GetPublished(f.ctx, 1, project.ID)
		require.NoError(t, err)
		require.Equal(t, f.sha, published.Snapshot.CommitSHA)
	}
	preview, err = f.service.PreviewSource(f.ctx, root.ID, response.Config.Settings)
	require.NoError(t, err)
	require.True(t, preview.CanSync, "a failing project cannot block the ready project's setup")
	require.True(t, preview.Projects[0].CanSync)
	require.False(t, preview.Projects[1].CanSync)
	require.NotNil(t, preview.Projects[1].Files, "failed project check remains renderable")
}

func TestSourceProjectGroupLifecycleAndCredentialsApplyToEveryProject(t *testing.T) {
	f := newJavaSourceFixture(t)
	root := configureSourceProjectGroup(t, f)
	require.NoError(t, f.service.PauseDataSource(f.ctx, root.ID))
	for _, project := range root.SourceProjects {
		stored, err := f.service.dsRepo.FindByID(f.ctx, project.ID)
		require.NoError(t, err)
		require.Equal(t, types.DataSourceStatusPaused, stored.Status)
		require.True(t, stored.SourceQueryEnabled)
	}
	_, err := f.service.UpdateDataSourceCredentials(f.ctx, root.ID, map[string]interface{}{"base_url": "", "access_token": "invalid"})
	require.Error(t, err)
	for _, project := range root.SourceProjects {
		stored, err := f.service.dsRepo.FindByID(f.ctx, project.ID)
		require.NoError(t, err)
		cfg, err := stored.ParseConfig()
		require.NoError(t, err)
		require.Equal(t, "fixture-token", cfg.Credentials["access_token"], "failed replacement cannot partially rotate credentials")
	}
	require.NoError(t, f.service.ResumeDataSource(f.ctx, root.ID))
	// A paused project refuses a trigger, but does not prevent the remaining
	// project being queued. The failed trigger also gets a visible project log.
	memberCtx := context.WithValue(f.ctx, sourceProjectMemberKey{}, true)
	require.NoError(t, f.service.PauseDataSource(memberCtx, root.SourceProjects[1].ID))
	_, err = f.service.ManualSync(f.ctx, root.ID)
	require.Error(t, err)
	logs, err := f.service.GetSyncLogs(f.ctx, root.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2)
	statuses := map[string]string{}
	for _, log := range logs {
		statuses[log.SourceProjectID] = log.Status
	}
	require.Equal(t, types.SyncLogStatusQueued, statuses["123"])
	require.Equal(t, types.SyncLogStatusFailed, statuses["456"])
	_, err = f.service.UnbindDataSource(f.ctx, root.ID)
	require.NoError(t, err)
	for _, project := range root.SourceProjects {
		stored, err := f.service.dsRepo.FindByID(f.ctx, project.ID)
		require.NoError(t, err)
		require.Equal(t, types.SourceBindingUnbound, stored.SourceBindingState)
		require.True(t, stored.SourceQueryEnabled)
	}
	_, err = f.service.ClearSource(f.ctx, root.ID, true, types.SourceCleanupScopeCurrentAndHistory)
	require.NoError(t, err)
	for _, project := range root.SourceProjects {
		stored, err := f.service.GetDataSource(memberCtx, project.ID)
		require.NoError(t, err)
		require.False(t, stored.SourceQueryEnabled)
		require.NotNil(t, stored.SourceCleanup)
	}
}

func TestSourceProjectGroupCreateAndEditPreserveSelection(t *testing.T) {
	f := newJavaSourceFixture(t)
	root := configureSourceProjectGroup(t, f)
	response := dto.NewDataSourceResponse(root)
	incoming := *root
	cfg, err := incoming.ParseConfig()
	require.NoError(t, err)
	cfg.Settings = response.Config.Settings
	cfg.Credentials = map[string]interface{}{"access_token": "must-be-ignored"}
	incoming.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	updated, err := f.service.UpdateDataSource(f.ctx, &incoming)
	require.NoError(t, err)
	require.Len(t, updated.SourceProjects, 2)
	for index, project := range updated.SourceProjects {
		require.Equal(t, root.SourceProjects[index].ID, project.ID)
		cfg, err := project.ParseConfig()
		require.NoError(t, err)
		require.Equal(t, "fixture-token", cfg.Credentials["access_token"])
		rules, _, err := datasource.ParseSourceSettings(cfg)
		require.NoError(t, err)
		if index == 0 {
			require.Equal(t, []string{"src/ignored"}, rules.ExcludePaths)
		} else {
			require.Equal(t, []string{"src/other"}, rules.ExcludePaths)
		}
	}
	created := incoming
	created.ID, created.Name = "", "New multi-project source"
	cfg.Credentials = map[string]interface{}{"base_url": rootCredentialBase(t, root), "access_token": "fixture-token"}
	created.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	newRoot, err := f.service.CreateDataSource(f.ctx, &created)
	require.NoError(t, err)
	require.Len(t, newRoot.SourceProjects, 2)
	require.NotEqual(t, root.ID, newRoot.ID)
}

func rootCredentialBase(t *testing.T, root *types.DataSource) string {
	t.Helper()
	cfg, err := root.ParseConfig()
	require.NoError(t, err)
	base, _ := cfg.Credentials["base_url"].(string)
	return base
}

func TestSourceProjectGroupRemovingAnchorDoesNotReuseAnotherRepositoryIdentity(t *testing.T) {
	f := newJavaSourceFixture(t)
	root := configureSourceProjectGroup(t, f)
	secondID := root.SourceProjects[1].ID
	response := dto.NewDataSourceResponse(root)
	cfg, err := root.ParseConfig()
	require.NoError(t, err)
	cfg.Settings = response.Config.Settings
	cfg.Settings["projects"] = []interface{}{cfg.Settings["projects"].([]interface{})[1]}
	incoming := *root
	incoming.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	updated, err := f.service.UpdateDataSource(f.ctx, &incoming)
	require.NoError(t, err)
	require.Equal(t, root.ID, updated.ID)
	require.Equal(t, "123", sourceProjectID(updated), "anchor keeps its repository identity and history")
	require.Equal(t, types.DataSourceStatusPaused, updated.Status)
	response = dto.NewDataSourceResponse(updated)
	projects := response.Config.Settings["projects"].([]interface{})
	require.Len(t, projects, 1)
	require.Equal(t, "456", projects[0].(map[string]interface{})["project_id"])
	require.Equal(t, secondID, updated.SourceProjects[1].ID)
	_, err = f.service.UpdateDataSourceCredentials(f.ctx, root.ID, map[string]interface{}{"base_url": rootCredentialBase(t, root), "access_token": "rotated-token"})
	require.NoError(t, err)
	for _, member := range updated.SourceProjects {
		stored, err := f.service.dsRepo.FindByID(f.ctx, member.ID)
		require.NoError(t, err)
		cfg, err := stored.ParseConfig()
		require.NoError(t, err)
		require.Equal(t, "rotated-token", cfg.Credentials["access_token"])
	}
	_, err = f.service.ManualSync(f.ctx, root.ID)
	require.NoError(t, err)
	logs, err := f.service.GetSyncLogs(f.ctx, root.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, logs, 1)
	require.Equal(t, secondID, logs[0].DataSourceID)
	require.NoError(t, f.service.DeleteDataSource(f.ctx, root.ID))
	rows, err := f.service.ListDataSources(f.ctx, f.kb.ID)
	require.NoError(t, err)
	require.Empty(t, rows, "deleting a temporary group cannot leave hidden project drafts")
}

func TestSourceProjectGroupConfigurationRollbackRestoresEveryMember(t *testing.T) {
	f := newJavaSourceFixture(t)
	root := configureSourceProjectGroup(t, f)
	// Only the second repository rejects this edit. The first repository's
	// successful UPDATE must roll back in the same configuration transaction.
	secondID := root.SourceProjects[1].ID // generated UUID, never request input
	require.NoError(t, f.db.Exec(fmt.Sprintf("ALTER TABLE data_sources ADD CONSTRAINT reject_group_edit CHECK (id <> '%s' OR name <> 'reject-group')", secondID)).Error)
	cfg, err := root.ParseConfig()
	require.NoError(t, err)
	cfg.Settings = dto.NewDataSourceResponse(root).Config.Settings
	incoming := *root
	incoming.Name = "reject-group"
	incoming.Config, err = cfg.ToJSON()
	require.NoError(t, err)
	_, err = f.service.UpdateDataSource(f.ctx, &incoming)
	require.Error(t, err)
	for _, project := range root.SourceProjects {
		stored, err := f.service.dsRepo.FindByID(f.ctx, project.ID)
		require.NoError(t, err)
		require.Equal(t, root.Name, stored.Name)
		require.Equal(t, sourceProjectID(project), sourceProjectID(stored))
	}
	_, err = f.service.ManualSync(f.ctx, root.ID)
	require.NoError(t, err, "failed configuration writes restore the stored generation for every project")
	logs, err := f.service.GetSyncLogs(f.ctx, root.ID, 10, 0)
	require.NoError(t, err)
	require.Len(t, logs, 2)
}
