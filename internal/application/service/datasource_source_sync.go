package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
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
		return fmt.Errorf("initial Java source sync requires explicitly selected paths")
	}
	if !s.sourceIndexesReady(ctx, kb) || !sourceParserReady(ctx) {
		return fmt.Errorf("source indexes or Java parser are not ready")
	}
	return s.sourceSnapshots.CheckReady(ctx)
}

func (s *DataSourceService) processSourceSync(ctx context.Context, ds *types.DataSource, log *types.SyncLog, kb *types.KnowledgeBase, connector datasource.Connector, config *types.DataSourceConfig, wasPaused bool) (failure error) {
	result := &types.SyncResult{Source: &types.SourceRunResult{Snapshot: &types.SourceSnapshot{ID: uuid.NewString(), TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, SyncLogID: log.ID, State: "fetching"}, Members: []types.SourceSnapshotMember{}}}
	snapshot := result.Source.Snapshot
	created := false
	defer func() {
		if failure != nil {
			snapshot.State, snapshot.Error = "failed", failure.Error()
			if created {
				_ = s.sourceSnapshots.SetState(context.WithoutCancel(ctx), snapshot.ID, "failed", snapshot.Error)
			}
			result.Failed = 1
			data, _ := result.ToJSON()
			s.updateSyncRunResult(context.WithoutCancel(ctx), ds, log, result, data, types.SyncLogStatusFailed, snapshot.Error, wasPaused)
		}
	}()
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
	snapshot.ProjectID, snapshot.CommitSHA, snapshot.RulesVersion = repository.ProjectID, repository.CommitSHA, version
	snapshot.RepositoryURL = strings.TrimSuffix(repository.CloneURL, ".git")
	progress := func(state string) error {
		snapshot.State = state
		data, e := result.ToJSON()
		if e != nil {
			return e
		}
		log.Result = data
		if e = s.syncLogRepo.UpdateResult(ctx, log); e != nil {
			return e
		}
		if created {
			return s.sourceSnapshots.SetState(ctx, snapshot.ID, state, "")
		}
		return nil
	}
	if err := progress("fetching"); err != nil {
		return err
	}
	content := map[string][]byte{}
	var totalBytes int
	manifest, err := source.ReadGit(ctx, repository, rules, func(file types.SourcePreviewFile, raw []byte) error {
		if !strings.HasSuffix(strings.ToLower(file.Path), ".java") {
			return fmt.Errorf("initial source sync supports selected Java files only; narrow the included paths")
		}
		totalBytes += len(raw)
		if len(content) >= 100 || totalBytes > 16<<20 {
			return fmt.Errorf("initial source sync is limited to 100 Java files and 16 MiB; narrow the included paths")
		}
		content[file.Path] = raw
		return nil
	})
	if err != nil {
		return err
	}
	if len(content) == 0 {
		return fmt.Errorf("initial source sync requires at least one selected Java file")
	}
	snapshot.ManifestComplete = true
	snapshot.MemberCount = len(manifest)
	snapshot.FileCount = len(content)
	manifestBytes, _ := json.Marshal(manifest)
	manifestHash := sha256.Sum256(manifestBytes)
	snapshot.ManifestDigest = hex.EncodeToString(manifestHash[:])
	for _, file := range manifest {
		result.Source.Members = append(result.Source.Members, types.SourceSnapshotMember{SnapshotID: snapshot.ID, Path: file.Path, BlobSHA: file.BlobSHA, Size: file.Size, Status: file.Status, Reason: file.Reason, Encoding: file.Encoding, Generated: file.Generated})
	}
	for _, file := range manifest {
		if file.Status != "included" && file.Status != "excluded" {
			return fmt.Errorf("selected source member is not readable: %s (%s)", file.Path, file.Status)
		}
	}
	snapshot.State = "parsing"
	if err := s.sourceSnapshots.Create(ctx, snapshot, result.Source.Members); err != nil {
		return err
	}
	created = true
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
		parsed, err := source.ParseJava(ctx, os.Getenv("SOURCE_PARSER_URL"), member.Path, raw)
		if err != nil {
			return err
		}
		fileID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(ds.ID+"\x00"+member.Path)).String()
		file := &types.SourceFile{ID: fileID, TenantID: ds.TenantID, KnowledgeBaseID: kb.ID, DataSourceID: ds.ID, Path: member.Path}
		symbols, _ := json.Marshal(parsed.Symbols)
		fileVersion := &types.SourceFileVersion{ID: uuid.NewString(), SourceFileID: fileID, SnapshotID: snapshot.ID, BlobSHA: member.BlobSHA, SHA256: parsed.SHA256, Content: raw, Encoding: parsed.Encoding, ParserVersion: parsed.ParserVersion, Quality: parsed.Quality, Symbols: types.JSON(symbols)}
		chunks := make([]*types.Chunk, len(parsed.Chunks))
		for index, part := range parsed.Chunks {
			pathParts := strings.Split(member.Path, "/")
			for j := range pathParts {
				pathParts[j] = url.PathEscape(pathParts[j])
			}
			evidence := types.SourceEvidence{SnapshotID: snapshot.ID, FileVersionID: fileVersion.ID, ProjectID: snapshot.ProjectID, CommitSHA: snapshot.CommitSHA, Path: member.Path, Range: part.Range, Symbols: part.Symbols, Quality: part.Quality, Context: part.Context, GitLabURL: snapshot.RepositoryURL + "/-/blob/" + snapshot.CommitSHA + "/" + strings.Join(pathParts, "/") + fmt.Sprintf("#L%d-%d", part.Range.StartLine, part.Range.EndLine)}
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
	if err := progress("indexing"); err != nil {
		return err
	}
	model, err := s.sourceModelService.GetEmbeddingModelForTenant(ctx, kb.EmbeddingModelID, kb.TenantID)
	if err != nil {
		return err
	}
	dimension := model.GetDimensions()
	if dimension <= 0 {
		return fmt.Errorf("source embedding model dimension is invalid")
	}
	for offset := 0; offset < len(indexes); offset += 32 {
		end := offset + 32
		if end > len(indexes) {
			end = len(indexes)
		}
		batch := indexes[offset:end]
		texts := make([]string, len(batch))
		for i, index := range batch {
			texts[i] = index.Content
		}
		vectors, err := model.BatchEmbed(ctx, texts)
		if err != nil {
			return fmt.Errorf("source embedding request failed")
		}
		if len(vectors) != len(batch) {
			return fmt.Errorf("source embedding result is incomplete")
		}
		mapped := make(map[string][]float32, len(batch))
		for i, vector := range vectors {
			if len(vector) != dimension {
				return fmt.Errorf("source embedding dimension mismatch")
			}
			var norm float64
			for _, v := range vector {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					return fmt.Errorf("source embedding contains non-finite values")
				}
				norm += float64(v) * float64(v)
			}
			if norm == 0 {
				return fmt.Errorf("source embedding has zero norm")
			}
			mapped[batch[i].SourceID] = vector
		}
		if err := s.sourceSnapshots.StageIndexes(ctx, batch, mapped); err != nil {
			return err
		}
	}
	if err := progress("ready"); err != nil {
		return err
	}
	if err := s.sourceSnapshots.Publish(ctx, snapshot, ds, kb, dimension); err != nil {
		return err
	}
	result.Total = snapshot.FileCount
	result.Created = snapshot.FileCount
	data, _ := result.ToJSON()
	s.updateSyncRunResult(ctx, ds, log, result, data, types.SyncLogStatusSuccess, "", wasPaused)
	return nil
}
