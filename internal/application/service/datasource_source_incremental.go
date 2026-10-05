package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"path/filepath"
	"strings"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// Reconcile consecutive complete manifests; excluded entries retain managed identity.
// Only a unique disappeared/new pair with exactly equal blob bytes is a rename.
func reconcileSourceMembers(snapshot *types.SourceSnapshot, members []types.SourceSnapshotMember, previous *types.SourceRunResult) {
	oldByPath := map[string]types.SourceSnapshotMember{}
	currentByPath := map[string]*types.SourceSnapshotMember{}
	if previous != nil {
		snapshot.PreviousSnapshotID = previous.Snapshot.ID
		snapshot.PreviousCommitSHA = previous.Snapshot.CommitSHA
		for _, m := range previous.Members {
			oldByPath[m.Path] = m
		}
	}
	for i := range members {
		currentByPath[members[i].Path] = &members[i]
	}
	removedByBlob := map[string][]types.SourceSnapshotMember{}
	addedByBlob := map[string][]*types.SourceSnapshotMember{}
	for _, old := range oldByPath {
		if old.Status == "parsed" && currentByPath[old.Path] == nil {
			removedByBlob[old.BlobSHA] = append(removedByBlob[old.BlobSHA], old)
		}
	}
	for i := range members {
		m := &members[i]
		old, exists := oldByPath[m.Path]
		if exists {
			m.SourceFileID = old.SourceFileID
		}
		if m.Status != "included" {
			if exists && old.Status == "parsed" {
				m.Change = "excluded"
				snapshot.DeletedCount++
			}
			continue
		}
		if exists && old.Status == "parsed" {
			if old.BlobSHA == m.BlobSHA {
				m.Change = "unchanged"
			} else {
				m.Change = "changed"
				snapshot.ChangedCount++
			}
			continue
		}
		if !exists {
			addedByBlob[m.BlobSHA] = append(addedByBlob[m.BlobSHA], m)
		}
		m.Change = "added"
	}
	renamedOld := map[string]bool{}
	for blob, added := range addedByBlob {
		removed := removedByBlob[blob]
		if len(added) == 1 && len(removed) == 1 {
			added[0].SourceFileID = removed[0].SourceFileID
			added[0].Change = "renamed"
			added[0].PreviousPath = removed[0].Path
			added[0].Reason = "unique removed/new blob match"
			renamedOld[removed[0].Path] = true
			snapshot.RenamedCount++
		}
	}
	for _, removed := range removedByBlob {
		for _, m := range removed {
			if !renamedOld[m.Path] {
				snapshot.DeletedCount++
			}
		}
	}
	for i := range members {
		m := &members[i]
		if m.Status != "included" {
			continue
		}
		if m.Change == "added" {
			snapshot.AddedCount++
			if len(removedByBlob) > 0 {
				m.Reason = "rename not established; treated as delete plus add"
			}
		}
		if m.SourceFileID == "" {
			m.SourceFileID = uuid.NewString()
		}
	}
}

func sourceParseArtifactKey(path string, raw []byte, parserVersion, rulesVersion, indexProfileIdentity string) string {
	// Extension is the admitted language/grammar route, and the worker version
	// controls the installed grammar. Paths alter symbol/index context.
	return source.ArtifactKey(source.ProcessingVersion, parserVersion, strings.ToLower(filepath.Ext(path)), path, fmt.Sprintf("%x", sha256.Sum256(raw)), rulesVersion, indexProfileIdentity)
}

func (s *DataSourceService) currentSourceEmbeddingVersion(ctx context.Context, kb *types.KnowledgeBase) (string, error) {
	configured, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return "", err
	}
	if configured == nil {
		return "", fmt.Errorf("source embedding model no longer exists")
	}
	model, err := s.sourceModelService.GetEmbeddingModelForTenant(ctx, kb.EmbeddingModelID, kb.TenantID)
	if err != nil {
		return "", err
	}
	if model == nil || model.GetDimensions() <= 0 {
		return "", fmt.Errorf("source embedding model dimension is invalid")
	}
	current, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return "", err
	}
	if current == nil || source.EmbeddingVersion(current, model.GetDimensions()) != source.EmbeddingVersion(configured, model.GetDimensions()) {
		return "", fmt.Errorf("source embedding configuration changed during initialization")
	}
	return source.EmbeddingVersion(configured, model.GetDimensions()), nil
}

func (s *DataSourceService) stageSourceIndexes(ctx context.Context, ds *types.DataSource, kb *types.KnowledgeBase, snapshot *types.SourceSnapshot, indexes []*types.IndexInfo, indexProfile source.IndexProfile, usage *sourceEmbeddingUsage) (int, error) {
	config, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return 0, err
	}
	if config == nil {
		return 0, fmt.Errorf("source embedding model no longer exists")
	}
	configuredProfile, err := source.NewIndexProfile(config.Parameters.EmbeddingParameters)
	if err != nil || configuredProfile.Identity != indexProfile.Identity {
		return 0, fmt.Errorf("source embedding token profile changed during initialization")
	}
	model, err := s.sourceModelService.GetEmbeddingModelForTenant(ctx, kb.EmbeddingModelID, kb.TenantID)
	if err != nil {
		return 0, err
	}
	dimension := model.GetDimensions()
	if dimension <= 0 {
		return 0, fmt.Errorf("source embedding model dimension is invalid")
	}
	current, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return 0, err
	}
	if current == nil || source.EmbeddingVersion(current, dimension) != source.EmbeddingVersion(config, dimension) {
		return 0, fmt.Errorf("source embedding configuration changed during initialization")
	}
	currentProfile, err := source.NewIndexProfile(current.Parameters.EmbeddingParameters)
	if err != nil || currentProfile.Identity != indexProfile.Identity {
		return 0, fmt.Errorf("source embedding token profile changed during initialization")
	}
	snapshot.EmbeddingVersion = source.EmbeddingVersion(config, dimension)
	if usage != nil {
		usage.estimateAvailable.Store(true)
	}
	for offset := 0; offset < len(indexes); offset += 32 {
		end := offset + 32
		if end > len(indexes) {
			end = len(indexes)
		}
		batch := indexes[offset:end]
		keys := make([]string, len(batch))
		for i, index := range batch {
			keys[i] = source.ArtifactKey(snapshot.EmbeddingVersion, index.Content)
		}
		cached, err := s.sourceSnapshots.GetEmbeddingArtifacts(ctx, ds.TenantID, ds.ID, keys)
		if err != nil {
			return 0, err
		}
		pendingKeys, texts := []string{}, []string{}
		seen := map[string]bool{}
		for i, index := range batch {
			if _, ok := cached[keys[i]]; !ok && !seen[keys[i]] {
				pendingKeys = append(pendingKeys, keys[i])
				texts = append(texts, index.Content)
				seen[keys[i]] = true
			}
		}
		newVectors := map[string][]float32{}
		if len(texts) > 0 {
			if usage != nil {
				usage.providerCallExpected = true
			}
			embedCtx := ctx
			if usage != nil {
				embedCtx = embedding.WithHTTPAttemptObserver(ctx, texts, func(attemptTexts []string) {
					usage.attempts.Add(1)
					var attemptTokenEstimate int64
					for _, attemptText := range attemptTexts {
						tokens, tokenErr := indexProfile.CountTokens(attemptText)
						if tokenErr != nil {
							usage.estimateAvailable.Store(false)
							return
						}
						attemptTokenEstimate += int64(tokens)
					}
					usage.estimatedTokens.Add(attemptTokenEstimate)
				})
			}
			vectors, err := model.BatchEmbed(embedCtx, texts)
			if err != nil {
				return 0, fmt.Errorf("source embedding request failed")
			}
			if len(vectors) != len(texts) {
				return 0, fmt.Errorf("source embedding result is incomplete")
			}
			for i, vector := range vectors {
				newVectors[pendingKeys[i]] = vector
			}
		}
		mapped := map[string][]float32{}
		for i, index := range batch {
			vector, reused := cached[keys[i]]
			if !reused {
				vector = newVectors[keys[i]]
			}
			if len(vector) != dimension {
				return 0, fmt.Errorf("source embedding dimension mismatch")
			}
			var norm float64
			for _, v := range vector {
				if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
					return 0, fmt.Errorf("source embedding contains non-finite values")
				}
				norm += float64(v) * float64(v)
			}
			if norm == 0 {
				return 0, fmt.Errorf("source embedding has zero norm")
			}
			mapped[index.SourceID] = vector
			if reused {
				snapshot.ReusedVectorCount++
			} else {
				snapshot.EmbeddedChunkCount++
			}
		}
		// Persist provider results before staging index rows so a worker restart
		// after indexing can reuse the same vectors without another model call.
		if err := s.sourceSnapshots.SaveEmbeddingArtifacts(ctx, ds.TenantID, ds.ID, newVectors); err != nil {
			return 0, err
		}
		if err := s.sourceSnapshots.StageIndexes(ctx, batch, mapped); err != nil {
			return 0, err
		}
	}
	if usage != nil {
		usage.completed = true
	}
	return dimension, nil
}
