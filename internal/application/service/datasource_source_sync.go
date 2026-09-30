package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

func (s *DataSourceService) checkSourceSyncReady(ctx context.Context, kb *types.KnowledgeBase, config *types.DataSourceConfig) error {
	if kb == nil || s.sourceSnapshots == nil || s.sourceModelService == nil {
		return datasource.ErrSourcePipelineUnavailable
	}
	// T02 publishes in one local PG transaction. Bound remote index stores need
	// a later distributed publication adapter and cannot silently participate.
	if kb.VectorStoreID != nil && *kb.VectorStoreID != "" {
		return fmt.Errorf("initial source sync requires the built-in PostgreSQL index store")
	}
	rules, _, err := datasource.ParseSourceSettings(config)
	if err != nil {
		return err
	}
	if len(rules.Projects[0].Paths) == 0 {
		return fmt.Errorf("initial source sync requires explicitly selected paths")
	}
	if !s.sourceIndexesReady(ctx, kb) || !sourceParserReady(ctx) {
		return fmt.Errorf("source indexes or parser are not ready")
	}
	return s.sourceSnapshots.CheckReady(ctx)
}

func (s *DataSourceService) processSourceSync(ctx context.Context, ds *types.DataSource, log *types.SyncLog, kb *types.KnowledgeBase, connector datasource.Connector, config *types.DataSourceConfig, wasPaused bool) (failure error) {
	// An already acknowledged delivery must neither replace its successful log
	// nor re-publish an older run after the source advances. Trigger leasing and
	// unfinished-run recovery belong to the durable trigger workflow.
	if log.Status == types.SyncLogStatusSuccess && log.TenantID == ds.TenantID && log.DataSourceID == ds.ID && s.sourceSnapshots != nil {
		completed, err := s.sourceSnapshots.GetRun(ctx, ds.TenantID, ds.ID, log.ID)
		if err != nil {
			return err
		}
		if completed != nil && completed.Snapshot != nil && completed.Snapshot.State == "published" {
			return nil
		}
	}
	result := &types.SyncResult{Source: &types.SourceRunResult{Snapshot: &types.SourceSnapshot{ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, SyncLogID: log.ID, State: "fetching"}, Members: []types.SourceSnapshotMember{}}}
	snapshot := result.Source.Snapshot
	created := false
	defer func() {
		if failure != nil {
			snapshot.State, snapshot.Error = "failed", failure.Error()
			if created {
				_ = s.sourceSnapshots.UpdateProgress(context.WithoutCancel(ctx), snapshot, result.Source.Members)
			}
			result.Failed = 1
			data, _ := result.ToJSON()
			s.updateSyncRunResult(context.WithoutCancel(ctx), ds, log, result, data, types.SyncLogStatusFailed, snapshot.Error, wasPaused)
		}
	}()
	if s.sourceSnapshots == nil {
		return datasource.ErrSourcePipelineUnavailable
	}
	// Read the current publication before resolving the remote branch. Branch,
	// token and Git transport failures can happen before a candidate snapshot is
	// created; the failed run must still identify the version that remains
	// readable and its last successful publication time.
	previous, err := s.sourceSnapshots.GetPublished(ctx, ds.TenantID, ds.ID)
	if err != nil {
		return err
	}
	existingRun, err := s.sourceSnapshots.GetRun(ctx, ds.TenantID, ds.ID, log.ID)
	if err != nil {
		return err
	}
	if existingRun != nil && existingRun.Snapshot != nil && existingRun.Snapshot.State == "published" {
		result.Source = existingRun
		snapshot = existingRun.Snapshot
		result.Total = snapshot.FileCount
		result.Created = snapshot.AddedCount
		result.Updated = snapshot.ChangedCount + snapshot.RenamedCount
		result.Deleted = snapshot.DeletedCount
		data, _ := result.ToJSON()
		s.updateSyncRunResult(ctx, ds, log, result, data, types.SyncLogStatusSuccess, "", wasPaused)
		return nil
	}
	snapshot.PublicationChecked = true
	if previous != nil && previous.Snapshot != nil {
		snapshot.PreviousSnapshotID = previous.Snapshot.ID
		snapshot.PreviousCommitSHA = previous.Snapshot.CommitSHA
		snapshot.PreviousPublishedAt = previous.Snapshot.PublishedAt
		snapshot.LastSuccessfulPublishedAt = previous.Snapshot.PublishedAt
	}
	if err := s.checkSourceSyncReady(ctx, kb, config); err != nil {
		return err
	}
	rules, version, err := datasource.ParseSourceSettings(config)
	if err != nil {
		return err
	}
	resolver, ok := connector.(datasource.SourceRepositoryResolver)
	if !ok {
		return datasource.ErrSourcePipelineUnavailable
	}
	repository, err := resolver.ResolveSourceRepository(ctx, config)
	if err != nil {
		return err
	}
	if lease, ok := types.SourceSyncLeaseFromContext(ctx); ok {
		if lease.TargetCommitSHA != "" {
			repository.CommitSHA = lease.TargetCommitSHA
		} else if control, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
			if err := control.RecordSourceRunPhase(ctx, lease, "target_resolved", repository.CommitSHA); err != nil {
				return err
			}
			lease.TargetCommitSHA = repository.CommitSHA
			ctx = types.WithSourceSyncLease(ctx, lease)
		}
	}
	snapshot.ProjectID, snapshot.CommitSHA, snapshot.RulesVersion = repository.ProjectID, repository.CommitSHA, version
	snapshot.DetectedCommitSHA, snapshot.TargetCommitSHA = repository.CommitSHA, repository.CommitSHA
	snapshot.RepositoryURL = strings.TrimSuffix(repository.CloneURL, ".git")
	progress := func(state string) error {
		snapshot.State = state
		if lease, ok := types.SourceSyncLeaseFromContext(ctx); ok {
			if control, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
				if e := control.RecordSourceRunPhase(ctx, lease, state, snapshot.CommitSHA); e != nil {
					return e
				}
			}
		}
		data, e := result.ToJSON()
		if e != nil {
			return e
		}
		log.Result = data
		if e = s.syncLogRepo.UpdateResult(ctx, log); e != nil {
			return e
		}
		if created {
			return s.sourceSnapshots.UpdateProgress(ctx, snapshot, result.Source.Members)
		}
		return nil
	}
	if err := progress("fetching"); err != nil {
		return err
	}
	parserVersion, err := source.ParserVersion(ctx, os.Getenv("SOURCE_PARSER_URL"))
	if err != nil {
		return err
	}
	snapshot.ProcessingVersion = source.ArtifactKey(source.ProcessingVersion, parserVersion)
	content := map[string][]byte{}
	checkedLanguages := map[string]bool{}
	var totalBytes int
	manifest, err := source.ReadGit(ctx, repository, rules, func(file types.SourcePreviewFile, raw []byte) error {
		language := source.LanguageForPath(file.Path)
		if language == "" {
			return fmt.Errorf("source sync supports selected Java/JavaScript/TypeScript/Python files only; narrow the included paths")
		}
		if !checkedLanguages[language] {
			if !sourceParserReady(ctx, language) {
				return fmt.Errorf("selected source language grammar is not ready")
			}
			checkedLanguages[language] = true
		}
		totalBytes += len(raw)
		if len(content) >= 100 || totalBytes > 16<<20 {
			return fmt.Errorf("initial source sync is limited to 100 supported source files and 16 MiB; narrow the included paths")
		}
		content[file.Path] = raw
		return nil
	})
	if err != nil {
		return err
	}
	if len(content) == 0 && previous == nil {
		return fmt.Errorf("initial source sync requires at least one selected supported source file")
	}
	snapshot.ManifestComplete = true
	snapshot.MemberCount = len(manifest)
	snapshot.FileCount = len(content)
	manifestBytes, _ := json.Marshal(manifest)
	manifestHash := sha256.Sum256(manifestBytes)
	snapshot.ManifestDigest = hex.EncodeToString(manifestHash[:])
	for _, file := range manifest {
		if file.Status != "included" && file.Status != "excluded" {
			return fmt.Errorf("selected source member is not readable: %s (%s): %w", file.Path, file.Status, asynq.SkipRetry)
		}
	}
	embeddingVersion, err := s.currentSourceEmbeddingVersion(ctx, kb)
	if err != nil {
		return err
	}
	// Persist the effective vector identity with the staged manifest, before
	// any chunk IDs can be reused or vectors can be written. A crash during
	// indexing must not leave a stage whose dimension/model identity is unknown.
	snapshot.EmbeddingVersion = embeddingVersion
	if previous != nil && previous.Snapshot != nil {
		published := previous.Snapshot
		if published.KnowledgeBaseID == snapshot.KnowledgeBaseID && published.CommitSHA == snapshot.CommitSHA && published.ProjectID == snapshot.ProjectID &&
			published.RepositoryURL == snapshot.RepositoryURL && published.ManifestDigest == snapshot.ManifestDigest &&
			published.RulesVersion == snapshot.RulesVersion && published.ProcessingVersion == snapshot.ProcessingVersion &&
			published.EmbeddingVersion == embeddingVersion {
			if err := s.sourceSnapshots.EnsurePublishedSourceWikiUpdate(ctx, ds, published); err != nil {
				return err
			}
			published.DetectedCommitSHA, published.TargetCommitSHA = repository.CommitSHA, repository.CommitSHA
			published.PublicationChecked = true
			result.Source = previous
			result.Total = published.FileCount
			data, err := result.ToJSON()
			if err != nil {
				return err
			}
			s.updateSyncRunResult(ctx, ds, log, result, data, types.SyncLogStatusSuccess, "", wasPaused)
			return nil
		}
	}
	if existingRun != nil && existingRun.Snapshot != nil {
		if existingRun.Snapshot.CommitSHA != repository.CommitSHA || existingRun.Snapshot.ProjectID != snapshot.ProjectID ||
			existingRun.Snapshot.RepositoryURL != snapshot.RepositoryURL || existingRun.Snapshot.ManifestDigest != snapshot.ManifestDigest ||
			existingRun.Snapshot.RulesVersion != version {
			return fmt.Errorf("persisted source stage no longer matches its fixed target: %w", asynq.SkipRetry)
		}
		if existingRun.Snapshot.ProcessingVersion != snapshot.ProcessingVersion {
			return fmt.Errorf("persisted source stage processing version changed; start a new source run: %w", asynq.SkipRetry)
		}
		if existingRun.Snapshot.EmbeddingVersion != embeddingVersion {
			return fmt.Errorf("persisted source stage embedding version or dimension changed; start a new source run: %w", asynq.SkipRetry)
		}
		snapshot = existingRun.Snapshot
		result.Source.Snapshot = snapshot
		result.Source.Members = existingRun.Members
		snapshot.DetectedCommitSHA, snapshot.TargetCommitSHA = repository.CommitSHA, repository.CommitSHA
		snapshot.PublicationChecked = true
		created = true
	} else {
		for _, file := range manifest {
			result.Source.Members = append(result.Source.Members, types.SourceSnapshotMember{SnapshotID: snapshot.ID, Path: file.Path, BlobSHA: file.BlobSHA, Size: file.Size, Status: file.Status, Reason: file.Reason, Encoding: file.Encoding, Generated: file.Generated})
		}
		reconcileSourceMembers(snapshot, result.Source.Members, previous)
		if err := s.sourceSnapshots.Create(ctx, snapshot, result.Source.Members); err != nil {
			return err
		}
		created = true
	}
	snapshot.State = "parsing"
	if err := progress("parsing"); err != nil {
		return err
	}
	var indexes []*types.IndexInfo
	for i := range result.Source.Members {
		member := &result.Source.Members[i]
		raw, selected := content[member.Path]
		if !selected {
			result.Skipped++
			continue
		}
		artifactKey := sourceParseArtifactKey(member.Path, raw, parserVersion, version)
		parsed, err := s.sourceSnapshots.GetParsedArtifact(ctx, ds.TenantID, ds.ID, artifactKey)
		if err != nil {
			return err
		}
		if parsed == nil {
			parsed, err = source.ParseFile(ctx, os.Getenv("SOURCE_PARSER_URL"), member.Path, raw)
			if err == nil && parsed.ParserVersion != parserVersion {
				return fmt.Errorf("source parser version changed during processing")
			}
			if err == nil {
				err = s.sourceSnapshots.SaveParsedArtifact(ctx, ds.TenantID, ds.ID, artifactKey, parsed)
			}
			snapshot.ParsedCount++
		} else {
			member.ParseReused = true
			snapshot.ReusedFileCount++
			snapshot.ReusedChunkCount += len(parsed.Chunks)
		}
		if err != nil {
			return err
		}
		if member.Status == "parsed" {
			reader, ok := s.sourceSnapshots.(interface {
				GetStagedChunkIDs(context.Context, string, string) ([]string, error)
			})
			if !ok {
				return datasource.ErrSourcePipelineUnavailable
			}
			chunkIDs, err := reader.GetStagedChunkIDs(ctx, snapshot.ID, member.FileVersionID)
			if err != nil {
				return err
			}
			if len(chunkIDs) != len(parsed.Chunks) {
				return fmt.Errorf("persisted source stage has incomplete chunks for %s", member.Path)
			}
			for index, part := range parsed.Chunks {
				indexes = append(indexes, &types.IndexInfo{SourceID: chunkIDs[index], ChunkID: chunkIDs[index], SourceType: types.ChunkSourceType,
					KnowledgeID: member.SourceFileID, KnowledgeBaseID: kb.ID, KnowledgeType: types.KnowledgeTypeSource,
					Content: source.SourceIndexText(member.Path, part), IsEnabled: false})
			}
			continue
		}
		fileID := member.SourceFileID
		file := &types.SourceFile{ID: fileID, TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, Path: member.Path}
		symbols, _ := json.Marshal(parsed.Symbols)
		fileVersion := &types.SourceFileVersion{ID: uuid.NewString(), SourceFileID: fileID, SnapshotID: snapshot.ID, BlobSHA: member.BlobSHA, SHA256: parsed.SHA256, Content: raw, Encoding: parsed.Encoding, ParserVersion: parsed.ParserVersion, Quality: parsed.Quality, Symbols: types.JSON(symbols)}
		chunks := make([]*types.Chunk, len(parsed.Chunks))
		for index, part := range parsed.Chunks {
			evidence := types.SourceEvidence{DataSourceID: ds.ID, SnapshotID: snapshot.ID, FileVersionID: fileVersion.ID, ProjectID: snapshot.ProjectID, CommitSHA: snapshot.CommitSHA, Path: member.Path, Range: part.Range, Symbols: part.Symbols, Quality: part.Quality, Context: part.Context, GitLabURL: source.GitLabBlobURL(snapshot.RepositoryURL, snapshot.CommitSHA, member.Path, part.Range)}
			metadata, _ := json.Marshal(map[string]any{"source": evidence})
			chunk := &types.Chunk{ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, KnowledgeID: fileID, Content: part.Content, SourceContent: part.Content, ChunkIndex: index, ChunkType: types.ChunkTypeText, IsEnabled: false, IndexStatus: "pending", StartAt: utf8.RuneCount(raw[:part.Range.StartByte]), EndAt: utf8.RuneCount(raw[:part.Range.EndByte]), Metadata: types.JSON(metadata)}
			chunks[index] = chunk
			indexes = append(indexes, &types.IndexInfo{SourceID: chunk.ID, ChunkID: chunk.ID, SourceType: types.ChunkSourceType, KnowledgeID: fileID, KnowledgeBaseID: kb.ID, KnowledgeType: types.KnowledgeTypeSource, Content: source.SourceIndexText(member.Path, part), IsEnabled: false})
		}
		if err := s.sourceSnapshots.StageFile(ctx, file, fileVersion, chunks); err != nil {
			return err
		}
		member.SourceFileID, member.FileVersionID, member.Status = fileID, fileVersion.ID, "parsed"
		snapshot.ChunkCount += len(chunks)
	}
	snapshot.ChunkCount = len(indexes)
	if err := progress("indexing"); err != nil {
		return err
	}
	dimension, err := s.stageSourceIndexes(ctx, ds, kb, snapshot, indexes)
	if err != nil {
		return err
	}
	if err := progress("ready"); err != nil {
		return err
	}
	if err := s.sourceSnapshots.Publish(ctx, snapshot, ds, kb, dimension); err != nil {
		return err
	}
	snapshot.LastSuccessfulPublishedAt = snapshot.PublishedAt
	result.Total = snapshot.FileCount
	result.Created = snapshot.AddedCount
	result.Updated = snapshot.ChangedCount + snapshot.RenamedCount
	result.Deleted = snapshot.DeletedCount
	data, _ := result.ToJSON()
	s.updateSyncRunResult(ctx, ds, log, result, data, types.SyncLogStatusSuccess, "", wasPaused)
	return nil
}
