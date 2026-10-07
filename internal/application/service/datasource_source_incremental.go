package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
)

// sourceEmbeddingQuotaBackoff waits out provider quota windows (for example
// Volcengine ModelAccountTpmRateLimitExceeded) before retrying the same
// batch. A variable so tests can shorten the window; the run deadline still
// bounds the total stall budget of one attempt.
var sourceEmbeddingQuotaBackoff = 75 * time.Second

// sourceEmbeddingQuotaBackoffReserve keeps runway after a backoff for the
// retried batch itself before the run deadline expires.
const sourceEmbeddingQuotaBackoffReserve = 2 * time.Minute

// sourceEmbeddingQuotaBackoffMaxAttempts bounds in-stage backoffs even when
// the context carries no deadline.
const sourceEmbeddingQuotaBackoffMaxAttempts = 40

// sourceEmbeddingRateLimited reports whether an embedding error is a quota
// window (account/model TPM or RPM limits, HTTP 429) that can recover by
// waiting, as opposed to a permanent request failure.
func sourceEmbeddingRateLimited(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "ratelimitexceeded") ||
		strings.Contains(message, "too many requests") ||
		strings.Contains(message, "rate limit")
}

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

type sourceIndexBatchStager struct {
	service      *DataSourceService
	ds           *types.DataSource
	snapshot     *types.SourceSnapshot
	config       *types.Model
	model        embedding.Embedder
	indexProfile source.IndexProfile
	dimension    int
}

func (s *DataSourceService) newSourceIndexBatchStager(ctx context.Context, ds *types.DataSource, kb *types.KnowledgeBase,
	snapshot *types.SourceSnapshot, indexProfile source.IndexProfile, usage *sourceEmbeddingUsage) (*sourceIndexBatchStager, error) {
	config, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, fmt.Errorf("source embedding model no longer exists")
	}
	configuredProfile, err := source.NewIndexProfile(config.Parameters.EmbeddingParameters)
	if err != nil || configuredProfile.Identity != indexProfile.Identity {
		return nil, fmt.Errorf("source embedding token profile changed during initialization")
	}
	model, err := s.sourceModelService.GetEmbeddingModelForTenant(ctx, kb.EmbeddingModelID, kb.TenantID)
	if err != nil {
		return nil, err
	}
	dimension := model.GetDimensions()
	if dimension <= 0 {
		return nil, fmt.Errorf("source embedding model dimension is invalid")
	}
	current, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return nil, err
	}
	if current == nil || source.EmbeddingVersion(current, dimension) != source.EmbeddingVersion(config, dimension) {
		return nil, fmt.Errorf("source embedding configuration changed during initialization")
	}
	currentProfile, err := source.NewIndexProfile(current.Parameters.EmbeddingParameters)
	if err != nil || currentProfile.Identity != indexProfile.Identity {
		return nil, fmt.Errorf("source embedding token profile changed during initialization")
	}
	snapshot.EmbeddingVersion = source.EmbeddingVersion(config, dimension)
	if usage != nil {
		usage.estimateAvailable.Store(true)
	}
	return &sourceIndexBatchStager{service: s, ds: ds, snapshot: snapshot, config: config, model: model,
		indexProfile: indexProfile, dimension: dimension}, nil
}

// Volcengine dispatches each text independently. Keep successful results within
// this bounded batch so a quota error in another text does not charge them again
// on every backoff. This cache belongs only to this call and model instance.
type sourceQuotaRetryEmbedder struct {
	model   sourceBatchEmbedder
	mu      sync.Mutex
	vectors map[string][]float32
}

func (m *sourceQuotaRetryEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) != 1 {
		return m.model.BatchEmbed(ctx, texts)
	}
	m.mu.Lock()
	vector, found := m.vectors[texts[0]]
	m.mu.Unlock()
	if found {
		return [][]float32{vector}, nil
	}
	vectors, err := m.model.BatchEmbed(ctx, texts)
	if err == nil && len(vectors) == 1 && len(vectors[0]) > 0 {
		m.mu.Lock()
		m.vectors[texts[0]] = vectors[0]
		m.mu.Unlock()
	}
	return vectors, err
}

// sourceEmbeddingBatchWithQuotaBackoff embeds one batch within the fixed run
// budget. Earlier batches remain persisted; successful Volcengine texts in the
// current batch also survive a quota backoff without another provider request.
func sourceEmbeddingBatchWithQuotaBackoff(embedCtx context.Context, runCtx context.Context, provider string, model sourceBatchEmbedder, texts []string) ([][]float32, error) {
	if provider == string(types.ModelSourceVolcengine) {
		model = &sourceQuotaRetryEmbedder{model: model, vectors: make(map[string][]float32, len(texts))}
	}
	for attempt := 0; ; attempt++ {
		vectors, err := sourceBatchEmbed(embedCtx, provider, model, texts)
		if err == nil {
			return vectors, nil
		}
		if !sourceEmbeddingRateLimited(err) {
			return nil, fmt.Errorf("source embedding request failed")
		}
		if attempt >= sourceEmbeddingQuotaBackoffMaxAttempts {
			return nil, fmt.Errorf("source embedding quota backoff attempts exhausted")
		}
		if deadline, ok := runCtx.Deadline(); ok && time.Until(deadline) < sourceEmbeddingQuotaBackoff+sourceEmbeddingQuotaBackoffReserve {
			return nil, fmt.Errorf("source embedding quota backoff budget exhausted before the run deadline")
		}
		select {
		case <-time.After(sourceEmbeddingQuotaBackoff):
		case <-runCtx.Done():
			return nil, fmt.Errorf("source embedding request failed")
		}
	}
}

func (stager *sourceIndexBatchStager) stage(ctx context.Context, batch []*types.IndexInfo, usage *sourceEmbeddingUsage) error {
	if len(batch) == 0 {
		return nil
	}
	keys := make([]string, len(batch))
	for i, index := range batch {
		keys[i] = source.ArtifactKey(stager.snapshot.EmbeddingVersion, index.Content)
	}
	cached, err := stager.service.sourceSnapshots.GetEmbeddingArtifacts(ctx, stager.ds.TenantID, stager.ds.ID, keys)
	if err != nil {
		return err
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
					tokens, tokenErr := stager.indexProfile.CountTokens(attemptText)
					if tokenErr != nil {
						usage.estimateAvailable.Store(false)
						return
					}
					attemptTokenEstimate += int64(tokens)
				}
				usage.estimatedTokens.Add(attemptTokenEstimate)
			})
		}
		vectors, err := sourceEmbeddingBatchWithQuotaBackoff(embedCtx, ctx, stager.config.Parameters.Provider, stager.model, texts)
		if err != nil {
			return err
		}
		if len(vectors) != len(texts) {
			return fmt.Errorf("source embedding result is incomplete")
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
		if len(vector) != stager.dimension {
			return fmt.Errorf("source embedding dimension mismatch")
		}
		var norm float64
		for _, value := range vector {
			if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
				return fmt.Errorf("source embedding contains non-finite values")
			}
			norm += float64(value) * float64(value)
		}
		if norm == 0 {
			return fmt.Errorf("source embedding has zero norm")
		}
		mapped[index.SourceID] = vector
		if reused {
			stager.snapshot.ReusedVectorCount++
		} else {
			stager.snapshot.EmbeddedChunkCount++
		}
	}
	// Persist provider results before staging index rows so a worker restart
	// after indexing can reuse the same vectors without another model call.
	if err := stager.service.sourceSnapshots.SaveEmbeddingArtifacts(ctx, stager.ds.TenantID, stager.ds.ID, newVectors); err != nil {
		return err
	}
	return stager.service.sourceSnapshots.StageIndexes(ctx, batch, mapped)
}

func (s *DataSourceService) stageSourceIndexes(ctx context.Context, ds *types.DataSource, kb *types.KnowledgeBase, snapshot *types.SourceSnapshot, indexes []*types.IndexInfo, indexProfile source.IndexProfile, usage *sourceEmbeddingUsage) (int, error) {
	stager, err := s.newSourceIndexBatchStager(ctx, ds, kb, snapshot, indexProfile, usage)
	if err != nil {
		return 0, err
	}
	for offset := 0; offset < len(indexes); offset += 32 {
		end := min(offset+32, len(indexes))
		if err := stager.stage(ctx, indexes[offset:end], usage); err != nil {
			return 0, err
		}
	}
	if usage != nil {
		usage.completed = true
	}
	return stager.dimension, nil
}
