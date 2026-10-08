package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
)

type sourceProjectMemberKey struct{}

const sourceProjectRemovedKey = "source_group_removed"

func (s *DataSourceService) GetSourceProjectLogs(ctx context.Context, id string, limit, offset int) ([]*types.SyncLog, error) {
	return s.GetSyncLogs(context.WithValue(ctx, sourceProjectMemberKey{}, true), id, limit, offset)
}

func sourceProjectID(ds *types.DataSource) string {
	cfg, err := ds.ParseConfig()
	if err != nil || cfg == nil {
		return ""
	}
	rules, _, err := datasource.ParseSourceSettings(cfg)
	if err != nil {
		return ""
	}
	return rules.Projects[0].ProjectID
}

func sourceProjectRemoved(ds *types.DataSource) bool {
	cfg, err := ds.ParseConfig()
	return err == nil && cfg != nil && cfg.Settings[sourceProjectRemovedKey] == true
}

// Internal scheduling and workers use repository rows directly. Only public
// reads project a root and its members as one configurable data source.
func projectSourceGroups(rows []*types.DataSource) []*types.DataSource {
	byID := map[string]*types.DataSource{}
	for _, ds := range rows {
		byID[ds.ID] = ds
		ds.SourceProjects = nil
	}
	result := make([]*types.DataSource, 0, len(rows))
	for _, ds := range rows {
		rootID := datasource.SourceGroupRoot(ds)
		if rootID == "" || byID[rootID] == nil {
			result = append(result, ds)
			continue
		}
		root := byID[rootID]
		if ds.ID == rootID {
			result = append(result, root)
		}
		root.SourceProjects = append(root.SourceProjects, ds)
	}
	for _, ds := range result {
		sort.Slice(ds.SourceProjects, func(i, j int) bool {
			if ds.SourceProjects[i].ID == ds.ID {
				return true
			}
			if ds.SourceProjects[j].ID == ds.ID {
				return false
			}
			return ds.SourceProjects[i].CreatedAt.Before(ds.SourceProjects[j].CreatedAt)
		})
	}
	return result
}

func (s *DataSourceService) sourceProjectMembers(ctx context.Context, id string) ([]*types.DataSource, error) {
	if ctx.Value(sourceProjectMemberKey{}) == true {
		return nil, nil
	}
	root, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if datasource.SourceGroupRoot(root) != id {
		return nil, nil
	}
	rows, err := s.dsRepo.FindByKnowledgeBase(ctx, root.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	var members []*types.DataSource
	for _, ds := range rows {
		if ds.TenantID == root.TenantID && datasource.SourceGroupRoot(ds) == id {
			members = append(members, ds)
		}
	}
	sort.Slice(members, func(i, j int) bool { return members[i].ID < members[j].ID })
	return members, nil
}

// fanOutSourceProjects always attempts every member, including after an error.
// Returning an aggregate error prevents the UI claiming that all projects succeeded.
func (s *DataSourceService) fanOutSourceProjects(ctx context.Context, id string, operation func(context.Context, string) error) (bool, error) {
	members, err := s.sourceProjectMembers(ctx, id)
	if err != nil {
		return false, err
	}
	if len(members) == 0 {
		return false, nil
	}
	memberCtx := context.WithValue(ctx, sourceProjectMemberKey{}, true)
	var failures []error
	for _, member := range members {
		if err := operation(memberCtx, member.ID); err != nil {
			failures = append(failures, fmt.Errorf("project %s: %w", sourceProjectID(member), err))
		}
	}
	return true, errors.Join(failures...)
}

func (s *DataSourceService) saveSourceProjectGroup(ctx context.Context, incoming, existing *types.DataSource, cfg *types.DataSourceConfig) (*types.DataSource, error) {
	writer, ok := s.dsRepo.(interfaces.SourceProjectGroupRepository)
	if !ok {
		return nil, fmt.Errorf("atomic source project configuration is unavailable")
	}
	configs, err := datasource.SplitSourceProjects(cfg)
	if err != nil {
		return nil, err
	}
	var before []*types.DataSource
	rootID := incoming.ID
	if existing != nil {
		rootID = existing.ID
		if existing.TenantID != incoming.TenantID || existing.KnowledgeBaseID != incoming.KnowledgeBaseID ||
			!sourceLifecycleAllowsConnection(existing) {
			return nil, datasource.ErrDataSourceNotActive
		}
		before, err = s.sourceProjectMembers(ctx, rootID)
		if err != nil {
			return nil, err
		}
		if len(before) == 0 {
			before = []*types.DataSource{existing}
		}
		// Preserve the anchor's repository identity when it remains selected,
		// even if the user reordered the form.
		for index, candidate := range configs {
			rules, _, _ := datasource.ParseSourceSettings(candidate)
			if rules.Projects[0].ProjectID == sourceProjectID(existing) {
				configs[0], configs[index] = configs[index], configs[0]
				break
			}
		}
	} else {
		kb, kbErr := s.kbService.GetKnowledgeBaseByID(ctx, incoming.KnowledgeBaseID)
		if kbErr != nil || kb == nil || kb.TenantID != incoming.TenantID {
			return nil, datasource.ErrKnowledgeBaseNotFound
		}
		rootID = uuid.NewString()
	}
	used := map[string]bool{}
	members := make([]*types.DataSource, 0, len(configs)+len(before))
	for index, config := range configs {
		var prior *types.DataSource
		rules, _, _ := datasource.ParseSourceSettings(config)
		if existing != nil && sourceProjectID(existing) == rules.Projects[0].ProjectID {
			prior = existing
		} else {
			for _, old := range before {
				if old.ID != rootID && !sourceProjectRemoved(old) && sourceProjectID(old) == rules.Projects[0].ProjectID {
					prior = old
					break
				}
			}
		}
		member := *incoming
		member.SourceProjects = nil
		if prior != nil {
			member = *prior
			member.Name, member.SyncSchedule, member.SyncMode = incoming.Name, incoming.SyncSchedule, incoming.SyncMode
			member.ConflictStrategy, member.SyncDeletions = incoming.ConflictStrategy, incoming.SyncDeletions
			if incoming.Status != "" {
				member.Status = incoming.Status
			}
		} else {
			member.ID = uuid.NewString()
			member.LastSyncAt, member.LastSyncCursor, member.LastSyncResult = nil, nil, nil
			member.LatestSyncLog, member.SourceCleanup, member.ErrorMessage = nil, nil, ""
			member.CreatedAt, member.UpdatedAt = types.DataSource{}.CreatedAt, types.DataSource{}.UpdatedAt
			member.DeletedAt = types.DataSource{}.DeletedAt
			if index == 0 && existing == nil {
				member.ID = rootID
			}
			member.SourceBindingState, member.SourceQueryEnabled = types.SourceBindingBound, true
		}
		config.Settings[datasource.SourceGroupRootKey] = rootID
		delete(config.Settings, sourceProjectRemovedKey)
		config.StripNonSecretCredentials(member.Type)
		member.Config, err = config.ToJSON()
		if err != nil {
			return nil, err
		}
		if err := s.validateDataSourceConfig(ctx, &member); err != nil {
			return nil, err
		}
		members = append(members, &member)
		used[member.ID] = true
	}
	// Removed projects stop contacting GitLab and keep their existing published
	// evidence, as with unbind. Retain them for group clear/delete operations.
	var removed []*types.DataSource
	for _, old := range before {
		if used[old.ID] {
			continue
		}
		member := *old
		config, parseErr := member.ParseConfig()
		if parseErr != nil {
			return nil, parseErr
		}
		config.Settings[sourceProjectRemovedKey] = true
		member.Config, err = config.ToJSON()
		if err != nil {
			return nil, err
		}
		member.Status = types.DataSourceStatusPaused
		member.Name = incoming.Name
		members = append(members, &member)
		removed = append(removed, &member)
	}
	for _, old := range before {
		for _, member := range members {
			if old.ID == member.ID {
				if err := s.advanceSourceConfigGeneration(ctx, old, member); err != nil {
					for _, previous := range before {
						s.restoreSourceConfigurationFromStored(ctx, previous.ID)
					}
					return nil, err
				}
				break
			}
		}
	}
	if err := writer.SaveSourceProjectGroup(ctx, before, members); err != nil {
		for _, old := range before {
			s.restoreSourceConfigurationFromStored(ctx, old.ID)
		}
		return nil, err
	}
	memberCtx := context.WithValue(ctx, sourceProjectMemberKey{}, true)
	for _, member := range members {
		if err := s.advanceSourceConfigGeneration(ctx, member, member); err != nil {
			return nil, err
		}
		if s.scheduler != nil {
			if err := s.scheduler.AddOrUpdate(member); err != nil {
				logger.Warnf(ctx, "failed to register source project schedule ds=%s", member.ID)
			}
		}
	}
	for _, member := range removed {
		if member.ID == rootID {
			if err := s.PauseDataSource(memberCtx, member.ID); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := s.UnbindDataSource(memberCtx, member.ID); err != nil {
			return nil, err
		}
	}
	action := types.AuditActionDataSourceUpdated
	if existing == nil {
		action = types.AuditActionDataSourceCreated
	}
	recordKBActivity(ctx, s.audit, incoming.TenantID, incoming.KnowledgeBaseID, action, "data_source", rootID,
		types.AuditOutcomeSuccess, map[string]any{"name": incoming.Name, "type": incoming.Type, "project_count": len(configs)})
	return s.GetDataSource(ctx, rootID)
}

func (s *DataSourceService) previewSourceProjects(ctx context.Context, id string, settings map[string]interface{}) (*types.SourcePreview, error) {
	ds, err := s.dsRepo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	cfg, err := ds.ParseConfig()
	if err != nil || cfg == nil {
		return nil, datasource.ErrInvalidConfig
	}
	if settings != nil {
		cfg.Settings = settings
	}
	configs, err := datasource.SplitSourceProjects(cfg)
	if err != nil {
		return nil, err
	}
	result := &types.SourcePreview{}
	memberCtx := context.WithValue(ctx, sourceProjectMemberKey{}, true)
	members, err := s.sourceProjectMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, config := range configs {
		previewID := id
		rules, _, _ := datasource.ParseSourceSettings(config)
		for _, member := range members {
			if !sourceProjectRemoved(member) && sourceProjectID(member) == rules.Projects[0].ProjectID {
				previewID = member.ID
				break
			}
		}
		preview, previewErr := s.PreviewSource(memberCtx, previewID, config.Settings)
		if previewErr != nil {
			rules, _, _ := datasource.ParseSourceSettings(config)
			preview = &types.SourcePreview{ProjectID: rules.Projects[0].ProjectID, Branch: rules.Projects[0].Ref, Files: []types.SourcePreviewFile{}, Warnings: []string{}, Checks: []types.SourcePreviewCheck{{Name: "gitlab_branch", Ready: false, Message: previewErr.Error()}}}
		}
		result.Projects = append(result.Projects, preview)
		result.CanSync = result.CanSync || preview.CanSync
	}
	return result, nil
}

func (s *DataSourceService) updateSourceGroupCredentials(ctx context.Context, id string, credentials map[string]interface{}) (bool, error) {
	members, err := s.sourceProjectMembers(ctx, id)
	if err != nil || len(members) == 0 {
		return false, err
	}
	writer, ok := s.dsRepo.(interfaces.SourceProjectGroupRepository)
	if !ok {
		return true, fmt.Errorf("atomic source project configuration is unavailable")
	}
	var before, after []*types.DataSource
	for _, member := range members {
		if sourceProjectRemoved(member) && member.ID != id {
			continue
		}
		if credentials != nil && !sourceLifecycleAllowsConnection(member) {
			return true, datasource.ErrDataSourceNotActive
		}
		cfg, err := member.ParseConfig()
		if err != nil || cfg == nil {
			return true, datasource.ErrInvalidConfig
		}
		copy := *member
		cfg.Credentials = credentials
		cfg.StripNonSecretCredentials(member.Type)
		copy.Config, err = cfg.ToJSON()
		if err != nil {
			return true, err
		}
		if credentials != nil {
			if err := s.validateDataSourceConfig(ctx, &copy); err != nil {
				return true, err
			}
		}
		before, after = append(before, member), append(after, &copy)
	}
	for index, member := range after {
		if err := s.advanceSourceConfigGeneration(ctx, before[index], member); err != nil {
			for _, previous := range before {
				s.restoreSourceConfigurationFromStored(ctx, previous.ID)
			}
			return true, err
		}
	}
	if err := writer.SaveSourceProjectGroup(ctx, before, after); err != nil {
		for _, member := range before {
			s.restoreSourceConfigurationFromStored(ctx, member.ID)
		}
		return true, err
	}
	for index, member := range after {
		if err := s.advanceSourceConfigGeneration(ctx, before[index], member); err != nil {
			return true, err
		}
	}
	if len(after) > 0 {
		recordKBActivity(ctx, s.audit, after[0].TenantID, after[0].KnowledgeBaseID, types.AuditActionDataSourceUpdated,
			"data_source", id, types.AuditOutcomeSuccess, map[string]any{"changed_fields": []string{"credentials"}})
	}
	return true, nil
}

func (s *DataSourceService) sourceProjectGroupLogs(ctx context.Context, id string, limit, offset int) ([]*types.SyncLog, bool, error) {
	members, err := s.sourceProjectMembers(ctx, id)
	if err != nil || len(members) == 0 {
		return nil, false, err
	}
	if limit < 1 || offset < 0 || offset > int(^uint(0)>>1)-limit {
		return nil, true, datasource.ErrDataSourceInvalid
	}
	var logs []*types.SyncLog
	for _, member := range members {
		items, err := s.syncLogRepo.FindByDataSource(ctx, member.ID, limit+offset, 0)
		if err != nil {
			return nil, true, err
		}
		for _, item := range items {
			item.SourceProjectID = sourceProjectID(member)
		}
		logs = append(logs, items...)
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].StartedAt.Equal(logs[j].StartedAt) {
			return strings.Compare(logs[i].ID, logs[j].ID) < 0
		}
		return logs[i].StartedAt.After(logs[j].StartedAt)
	})
	if offset > len(logs) {
		offset = len(logs)
	}
	end := offset + limit
	if end > len(logs) {
		end = len(logs)
	}
	logs = logs[offset:end]
	err = s.enrichSourceWikiCoverage(ctx, logs)
	return logs, true, err
}
