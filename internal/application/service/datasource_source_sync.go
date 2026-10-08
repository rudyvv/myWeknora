package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

type sourceWikiDerivationLimits struct {
	maxFacts          int64
	maxCanonicalBytes int64
}

func (s *DataSourceService) checkSourceSyncReady(ctx context.Context, kb *types.KnowledgeBase, config *types.DataSourceConfig) (source.IndexProfile, error) {
	if kb == nil || s.sourceSnapshots == nil || s.sourceModelService == nil {
		return source.IndexProfile{}, datasource.ErrSourcePipelineUnavailable
	}
	// T02 publishes in one local PG transaction. Bound remote index stores need
	// a later distributed publication adapter and cannot silently participate.
	if kb.VectorStoreID != nil && *kb.VectorStoreID != "" {
		return source.IndexProfile{}, fmt.Errorf("initial source sync requires the built-in PostgreSQL index store")
	}
	_, _, err := datasource.ParseSourceSettings(config)
	if err != nil {
		return source.IndexProfile{}, err
	}
	if !s.sourceIndexBackendReady(ctx, kb) {
		return source.IndexProfile{}, fmt.Errorf("source indexes are not ready")
	}
	indexProfile, err := s.sourceIndexProfile(ctx, kb)
	if err != nil {
		return source.IndexProfile{}, fmt.Errorf("source embedding token profile is invalid: %w", err)
	}
	if !sourceParserReady(ctx) {
		return source.IndexProfile{}, fmt.Errorf("source parser is not ready")
	}
	if err := s.sourceSnapshots.CheckReady(ctx); err != nil {
		return source.IndexProfile{}, err
	}
	return indexProfile, nil
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
	telemetry := types.NewSourceRunTelemetry()
	if prior, err := log.ParseResult(); err == nil && prior != nil && prior.Source != nil && prior.Source.Telemetry != nil {
		if prior.Source.Telemetry.Validate() == nil {
			telemetry = prior.Source.Telemetry
		}
	}
	if lease, ok := types.SourceSyncLeaseFromContext(ctx); ok {
		leaseRecoveries := lease.LeaseRecoveryCount
		telemetry.LeaseRecoveries = &leaseRecoveries
	}
	result := &types.SyncResult{Source: &types.SourceRunResult{Snapshot: &types.SourceSnapshot{ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, SyncLogID: log.ID, State: "fetching"}, Members: []types.SourceSnapshotMember{}, Telemetry: telemetry}}
	snapshot := result.Source.Snapshot
	created := false
	var fetchingStarted, parsingStarted, indexingStarted, publishingStarted time.Time
	defer func() {
		finishSourceTelemetryPhase(telemetry, "fetching", &fetchingStarted)
		finishSourceTelemetryPhase(telemetry, "parsing", &parsingStarted)
		finishSourceTelemetryPhase(telemetry, "indexing", &indexingStarted)
		finishSourceTelemetryPhase(telemetry, "publishing", &publishingStarted)
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
		result.Source.Telemetry = telemetry
		snapshot = existingRun.Snapshot
		telemetry.PublishedCommitSHA = snapshot.CommitSHA
		s.recordSourceWikiCoverage(ctx, telemetry, kb.ID, ds.ID, snapshot.ID)
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
	indexProfile, err := s.checkSourceSyncReady(ctx, kb, config)
	if err != nil {
		return err
	}
	rules, version, err := datasource.ParseSourceSettings(config)
	if err != nil {
		return err
	}
	resourcePolicy := s.sourceResources.Policy()
	rules = resourcePolicy.ApplyFileLimit(rules)
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
	snapshot.ProcessingVersion = source.ArtifactKey(source.ProcessingVersion, parserVersion, indexProfile.Identity)
	blobStage, err := source.NewBlobStage(resourcePolicy.SelectedBlobStageBytes)
	if err != nil {
		return err
	}
	defer func() {
		if err := blobStage.Close(); err != nil {
			incrementSourceTelemetryCounter(&telemetry.CleanupResidueCount)
			if failure == nil {
				failure = err
			}
		}
	}()
	checkedLanguages := map[string]bool{}
	selectedCount := 0
	var totalBytes int64
	fetchingStarted = time.Now()
	manifest, gitMetrics, err := source.ReadGitWithPolicyMetrics(ctx, repository, rules, resourcePolicy, func(file types.SourcePreviewFile, raw []byte) error {
		language := source.LanguageForPath(file.Path)
		if language == "" {
			return fmt.Errorf("source sync supports selected source, template, and text configuration files only; narrow the included paths")
		}
		if !checkedLanguages[language] {
			if !sourceParserReady(ctx, language) {
				return fmt.Errorf("selected source language grammar is not ready")
			}
			checkedLanguages[language] = true
		}
		selectedCount++
		totalBytes += int64(len(raw))
		if selectedCount > resourcePolicy.MaxSelectedFiles || totalBytes > resourcePolicy.MaxSelectedBytes || int64(len(raw)) > resourcePolicy.MaxFileBytes {
			return source.ErrResourceLimitExceeded
		}
		if err := blobStage.Put(file.Path, raw); err != nil {
			return err
		}
		return source.CheckFreeSpaceReserve(resourcePolicy)
	})
	finishSourceTelemetryPhase(telemetry, "fetching", &fetchingStarted)
	if gitMetrics.TransferBytesMeasured {
		transferred := gitMetrics.TransferBytes
		telemetry.GitTransferBytes = &transferred
	}
	if gitMetrics.ObjectStageMeasured {
		addSourceTelemetryCounter(&telemetry.CleanupResidueCount, gitMetrics.CleanupResidueCount)
	}
	if err != nil {
		return err
	}
	selectedBytes := int64(totalBytes)
	telemetry.SelectedBytes = &selectedBytes
	telemetry.Storage = ensureSourceStorage(telemetry.Storage)
	if gitMetrics.ObjectStageMeasured {
		// staging is a logical payload metric: private Git object-directory file
		// lengths plus verified selected-blob spool content. It is not allocated
		// filesystem blocks or free disk space; each stage has a separate limit.
		stagingUsed := gitMetrics.ObjectStageBytes + blobStage.UsedBytes()
		stagingLimit := resourcePolicy.GitObjectStageBytes + resourcePolicy.SelectedBlobStageBytes
		telemetry.Storage["staging"] = sourceStorageMetric(stagingUsed, stagingLimit, types.SourceStorageMeasurementLogicalPayload)
	}
	if selectedCount == 0 && previous == nil {
		return fmt.Errorf("initial source sync requires at least one selected supported source file")
	}
	snapshot.ManifestComplete = true
	snapshot.MemberCount = len(manifest)
	snapshot.FileCount = selectedCount
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
			if lease, ok := types.SourceSyncLeaseFromContext(ctx); ok {
				if control, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
					if err := control.RecordSourceRunPhase(ctx, lease, "published", published.CommitSHA); err != nil {
						return err
					}
				}
			}
			if err := s.sourceSnapshots.EnsurePublishedSourceWikiUpdate(ctx, ds, published); err != nil {
				return err
			}
			published.DetectedCommitSHA, published.TargetCommitSHA = repository.CommitSHA, repository.CommitSHA
			published.PublicationChecked = true
			result.Source = previous
			result.Source.Telemetry = telemetry
			telemetry.PublishedCommitSHA = published.CommitSHA
			s.recordSourceWikiCoverage(ctx, telemetry, kb.ID, ds.ID, published.ID)
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
	parsingStarted = time.Now()
	telemetry.QualityCounts = make(map[string]int64)
	snapshot.ChunkCount = 0
	embeddingUsage := sourceEmbeddingUsage{}
	indexStager, err := s.newSourceIndexBatchStager(ctx, ds, kb, snapshot, indexProfile, &embeddingUsage)
	if err != nil {
		return err
	}
	indexBatch := make([]*types.IndexInfo, 0, 32)
	flushIndexBatch := func() error {
		if len(indexBatch) == 0 {
			return nil
		}
		indexingStarted = time.Now()
		stageErr := indexStager.stage(ctx, indexBatch, &embeddingUsage)
		finishSourceTelemetryPhase(telemetry, "indexing", &indexingStarted)
		if stageErr != nil {
			return stageErr
		}
		indexBatch = indexBatch[:0]
		return nil
	}
	appendIndex := func(index *types.IndexInfo) error {
		indexBatch = append(indexBatch, index)
		if len(indexBatch) == cap(indexBatch) {
			return flushIndexBatch()
		}
		return nil
	}
	for i := range result.Source.Members {
		member := &result.Source.Members[i]
		if member.Status != "included" && member.Status != "parsed" {
			result.Skipped++
			continue
		}
		raw, err := blobStage.Read(member.Path, resourcePolicy.MaxFileBytes)
		if err != nil {
			return err
		}
		artifactKey := sourceParseArtifactKey(member.Path, raw, parserVersion, version, indexProfile.Identity)
		parsed, err := s.sourceSnapshots.GetParsedArtifact(ctx, ds.TenantID, ds.ID, artifactKey)
		if err != nil {
			return err
		}
		if parsed == nil {
			parsed, err = source.ParseFileWithProfile(ctx, os.Getenv("SOURCE_PARSER_URL"), member.Path, raw, indexProfile)
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
		telemetry.CountQuality(parsed.Quality)
		_, err = json.Marshal(parsed.Facts)
		if err != nil {
			return fmt.Errorf("source parser facts could not be bounded")
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
				if err := appendIndex(&types.IndexInfo{SourceID: chunkIDs[index], ChunkID: chunkIDs[index], SourceType: types.ChunkSourceType,
					KnowledgeID: member.SourceFileID, KnowledgeBaseID: kb.ID, KnowledgeType: types.KnowledgeTypeSource,
					Content: source.SourceIndexText(member.Path, part), IsEnabled: false}); err != nil {
					return err
				}
			}
			snapshot.ChunkCount += len(parsed.Chunks)
			continue
		}
		fileID := member.SourceFileID
		file := &types.SourceFile{ID: fileID, TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, Path: member.Path}
		symbols, _ := json.Marshal(parsed.Symbols)
		facts, _ := json.Marshal(parsed.Facts)
		diagnostics, _ := json.Marshal(parsed.Diagnostics)
		fileVersion := &types.SourceFileVersion{ID: uuid.NewString(), SourceFileID: fileID, SnapshotID: snapshot.ID, BlobSHA: member.BlobSHA, SHA256: parsed.SHA256, Content: raw, Encoding: parsed.Encoding, ParserVersion: parsed.ParserVersion, Quality: parsed.Quality, Symbols: types.JSON(symbols), Facts: types.JSON(facts), Diagnostics: types.JSON(diagnostics)}
		chunks := make([]*types.Chunk, len(parsed.Chunks))
		fileIndexes := make([]*types.IndexInfo, len(parsed.Chunks))
		for index, part := range parsed.Chunks {
			indexText := source.SourceIndexText(member.Path, part)
			evidence := types.SourceEvidence{DataSourceID: ds.ID, SnapshotID: snapshot.ID, FileVersionID: fileVersion.ID, ProjectID: snapshot.ProjectID, CommitSHA: snapshot.CommitSHA, Path: member.Path, Range: part.Range, Symbols: part.Symbols, Quality: part.Quality, Context: part.Context, Region: part.Region, Diagnostics: part.Diagnostics, GitLabURL: source.GitLabBlobURL(snapshot.RepositoryURL, snapshot.CommitSHA, member.Path, part.Range)}
			metadata, _ := json.Marshal(map[string]any{"source": evidence})
			chunk := &types.Chunk{ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, KnowledgeID: fileID, Content: part.Content, SourceContent: part.Content, ChunkIndex: index, ChunkType: types.ChunkTypeText, IsEnabled: false, IndexStatus: "pending", StartAt: utf8.RuneCount(raw[:part.Range.StartByte]), EndAt: utf8.RuneCount(raw[:part.Range.EndByte]), Metadata: types.JSON(metadata)}
			chunks[index] = chunk
			fileIndexes[index] = &types.IndexInfo{SourceID: chunk.ID, ChunkID: chunk.ID, SourceType: types.ChunkSourceType, KnowledgeID: fileID, KnowledgeBaseID: kb.ID, KnowledgeType: types.KnowledgeTypeSource, Content: indexText, IsEnabled: false}
		}
		if err := s.sourceSnapshots.StageFile(ctx, file, fileVersion, chunks); err != nil {
			return err
		}
		for _, index := range fileIndexes {
			if err := appendIndex(index); err != nil {
				return err
			}
		}
		member.SourceFileID, member.FileVersionID, member.Status = fileID, fileVersion.ID, "parsed"
		snapshot.ChunkCount += len(chunks)
	}
	if err := flushIndexBatch(); err != nil {
		return err
	}
	embeddingUsage.completed = true
	relationMembers, capacityExceeded, err := s.sourceSnapshots.LoadSourceRelationMembers(ctx, ds.TenantID, kb.ID, ds.ID, snapshot.ID,
		s.sourceWikiLimits.maxFacts, s.sourceWikiLimits.maxCanonicalBytes)
	if err != nil {
		return err
	}
	if capacityExceeded {
		if err := s.sourceSnapshots.DeferWikiDerivation(ctx, ds.TenantID, kb.ID, ds.ID, snapshot.ID); err != nil {
			return err
		}
		snapshot.WikiDerivationState, snapshot.RelationCount, snapshot.RelationsStaged = "deferred_capacity", 0, false
	} else {
		relations := source.CorrelateSourceFacts(ds.TenantID, ds.ID, snapshot.ID, relationMembers)
		if len(relations) > types.SourceWikiSkeletonMaxRelations {
			if err := s.sourceSnapshots.DeferWikiDerivation(ctx, ds.TenantID, kb.ID, ds.ID, snapshot.ID); err != nil {
				return err
			}
			snapshot.WikiDerivationState, snapshot.RelationCount, snapshot.RelationsStaged = "deferred_capacity", 0, false
		} else {
			if err := s.sourceSnapshots.StageRelations(ctx, ds.TenantID, ds.ID, snapshot.ID, relations); err != nil {
				return err
			}
			snapshot.WikiDerivationState, snapshot.RelationCount, snapshot.RelationsStaged = "complete", len(relations), true
		}
	}
	if err := blobStage.Close(); err != nil {
		incrementSourceTelemetryCounter(&telemetry.CleanupResidueCount)
		return err
	}
	finishSourceTelemetryPhase(telemetry, "parsing", &parsingStarted)
	recordSourceEmbeddingUsage(telemetry, &embeddingUsage)
	if err := progress("indexing"); err != nil {
		return err
	}
	dimension := indexStager.dimension
	if usage, usageErr := s.sourceSnapshots.GetSourceResourceUsage(ctx, ds.TenantID, ds.ID); usageErr == nil {
		telemetry.Storage = ensureSourceStorage(telemetry.Storage)
		telemetry.Storage["original"] = sourceStorageMetric(usage.OriginalBytes, resourcePolicy.OriginalBytesPerSource, types.SourceStorageMeasurementLogicalPayload)
		telemetry.Storage["cache"] = sourceStorageMetric(usage.ParsedCacheBytes, resourcePolicy.ParsedCacheBytesPerSource, types.SourceStorageMeasurementLogicalPayload)
		telemetry.Storage["vectors"] = sourceStorageMetric(usage.VectorBytes, resourcePolicy.VectorBytesPerSource, types.SourceStorageMeasurementLogicalPayload)
	}
	if err := progress("ready"); err != nil {
		return err
	}
	publishingStarted = time.Now()
	if err := s.sourceSnapshots.Publish(ctx, snapshot, ds, kb, dimension); err != nil {
		finishSourceTelemetryPhase(telemetry, "publishing", &publishingStarted)
		return err
	}
	finishSourceTelemetryPhase(telemetry, "publishing", &publishingStarted)
	telemetry.PublishedCommitSHA = snapshot.CommitSHA
	s.recordSourceWikiCoverage(ctx, telemetry, kb.ID, ds.ID, snapshot.ID)
	snapshot.LastSuccessfulPublishedAt = snapshot.PublishedAt
	result.Total = snapshot.FileCount
	result.Created = snapshot.AddedCount
	result.Updated = snapshot.ChangedCount + snapshot.RenamedCount
	result.Deleted = snapshot.DeletedCount
	data, _ := result.ToJSON()
	s.updateSyncRunResult(ctx, ds, log, result, data, types.SyncLogStatusSuccess, "", wasPaused)
	return nil
}

func (s *DataSourceService) recordSourceWikiCoverage(ctx context.Context, telemetry *types.SourceRunTelemetry, knowledgeBaseID, sourceID, snapshotID string) {
	if s == nil || s.sourceWikiCoverage == nil || telemetry == nil {
		return
	}
	coverage, err := s.sourceWikiCoverage.GetSourceWikiCoverageSummary(ctx, knowledgeBaseID, sourceID, snapshotID)
	if err == nil && coverage != nil {
		telemetry.WikiCoverage = coverage
	}
}
