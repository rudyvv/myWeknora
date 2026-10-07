package service

import (
	"context"
	"encoding/json"
	"path"
	"strings"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func (s *knowledgeService) GetKnowledgeAccessInfo(ctx context.Context, id string) (*types.Knowledge, error) {
	row, err := s.repo.GetKnowledgeByIDOnly(ctx, id)
	if err != nil {
		return nil, err
	}
	return &types.Knowledge{ID: row.ID, TenantID: row.TenantID, KnowledgeBaseID: row.KnowledgeBaseID}, nil
}

func (s *knowledgeService) GetSourceFile(ctx context.Context, id string, versionID ...string) (*types.SourceFileView, error) {
	return s.readSourceFile(ctx, id, true, versionID...)
}

func (s *knowledgeService) readSourceFile(ctx context.Context, id string, content bool, versionID ...string) (*types.SourceFileView, error) {
	knowledge, err := s.repo.GetKnowledgeByIDOnly(ctx, id)
	if err != nil {
		return nil, err
	}
	if knowledge == nil || knowledge.Type != types.KnowledgeTypeSource {
		if knowledge == nil {
			return nil, apperrors.NewBadRequestError("source knowledge is unavailable")
		}
		return nil, apperrors.NewBadRequestError("knowledge is not a repository source file")
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, knowledge.KnowledgeBaseID)
	if err != nil {
		return nil, err
	}
	if _, err := resolveKBReadTenant(ctx, kb, s.kbShareService); err != nil {
		return nil, err
	}
	reader, ok := s.repo.(interfaces.SourceFileRepository)
	ctx, release, err := beginSourceRead(ctx, s.kbService, types.SearchTargets{&types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: kb.ID, KnowledgeIDs: []string{id}}})
	if err != nil {
		return nil, err
	}
	defer release()
	if !content {
		if infoReader, supported := s.repo.(interfaces.SourceFileInfoRepository); supported {
			return infoReader.ReadPublishedSourceFileInfo(ctx, kb.TenantID, id)
		}
	}
	if !ok {
		return nil, apperrors.NewServiceUnavailableError("source file reader is unavailable")
	}
	return reader.ReadPublishedSourceFile(ctx, kb.TenantID, id, versionID...)
}

// Source descriptions and tags remain current, while code provenance follows
// the question's immutable manifest rather than the mutable Knowledge row.
func (s *knowledgeService) sourceKnowledgeInfo(ctx context.Context, knowledge *types.Knowledge) (*types.Knowledge, error) {
	if knowledge == nil || knowledge.Type != types.KnowledgeTypeSource {
		return knowledge, nil
	}
	file, err := s.readSourceFile(ctx, knowledge.ID, false)
	if err != nil {
		return nil, err
	}
	metadata := knowledge.GetMetadata()
	metadata["datasource_id"] = file.DataSourceID
	metadata["source_snapshot_id"] = file.SnapshotID
	metadata["source_file_version_id"] = file.FileVersionID
	metadata["commit_sha"] = file.CommitSHA
	metadata["project_id"] = file.ProjectID
	metadata["source_path"] = file.Path
	metadata["repository_url"] = file.RepositoryURL
	metadata["source_quality"] = file.Quality
	knowledge.FileSize = file.FileSize
	knowledge.FileName = path.Base(file.Path)
	knowledge.FileHash = file.SHA256
	knowledge.FileType = strings.TrimPrefix(path.Ext(file.Path), ".")
	knowledge.FolderPath = path.Dir(file.Path)
	if knowledge.FolderPath == "." {
		knowledge.FolderPath = ""
	}
	knowledge.Title = file.Path
	knowledge.Source = file.RepositoryURL
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	knowledge.Metadata = encoded
	return knowledge, nil
}

func (s *knowledgeService) sourceKnowledgeInfos(ctx context.Context, rows []*types.Knowledge, err error) ([]*types.Knowledge, error) {
	if err != nil {
		return nil, err
	}
	// Pin the whole result page once. Acquiring a scope for each file also
	// captures the Wiki projection each time and makes ordinary lists time out.
	targets := make(types.SearchTargets, 0)
	byKB := make(map[string]*types.SearchTarget)
	for _, row := range rows {
		if row == nil || row.Type != types.KnowledgeTypeSource {
			continue
		}
		target := byKB[row.KnowledgeBaseID]
		if target == nil {
			target = &types.SearchTarget{Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: row.KnowledgeBaseID}
			byKB[row.KnowledgeBaseID] = target
			targets = append(targets, target)
		}
		target.KnowledgeIDs = append(target.KnowledgeIDs, row.ID)
	}
	if len(targets) > 0 {
		var release func()
		ctx, release, err = beginSourceRead(ctx, s.kbService, targets)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	for i, row := range rows {
		rows[i], err = s.sourceKnowledgeInfo(ctx, row)
		if err != nil {
			return nil, err
		}
	}
	return rows, nil
}
