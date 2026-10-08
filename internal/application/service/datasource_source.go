package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
)

func saveSourceRulesVersion(ds *types.DataSource, config *types.DataSourceConfig) error {
	mode, err := datasource.ContentMode(config)
	if err != nil || mode != datasource.ContentModeSource {
		return err
	}
	_, version, err := datasource.ParseSourceSettings(config)
	if err != nil {
		return err
	}
	config.Settings["rules_version"] = version
	ds.Config, err = config.ToJSON()
	return err
}

// PreviewSource merges draft settings with stored credentials; preview never
// persists those settings or publishes any source knowledge.
func (s *DataSourceService) PreviewSource(ctx context.Context, id string, settings map[string]interface{}) (*types.SourcePreview, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	ds, err := s.GetDataSource(ctx, id)
	if err != nil {
		return nil, err
	}
	if isSourceModeDataSource(ds) && !sourceLifecycleAllowsConnection(ds) {
		return nil, fmt.Errorf("unbound or cleared source cannot be previewed")
	}
	if ds.Type != types.ConnectorTypeGitLab {
		return nil, datasource.ErrInvalidConfig
	}
	config, err := ds.ParseConfig()
	if err != nil || config == nil {
		return nil, datasource.ErrInvalidConfig
	}
	if settings != nil {
		config.Settings = settings
	}
	rules, version, err := datasource.ParseSourceSettings(config)
	if err != nil {
		return nil, err
	}
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, ds.KnowledgeBaseID)
	if err != nil || kb == nil || kb.TenantID != ds.TenantID {
		return nil, datasource.ErrKnowledgeBaseNotFound
	}
	connector, err := s.connectorRegistry.Get(ds.Type)
	if err != nil {
		return nil, err
	}
	resolver, ok := connector.(datasource.SourceRepositoryResolver)
	if !ok {
		return nil, fmt.Errorf("source repository resolution is unavailable")
	}
	repository, err := resolver.ResolveSourceRepository(ctx, config)
	if err != nil {
		return nil, err
	}
	indexBackendReady := s.sourceIndexBackendReady(ctx, kb)
	profile, profileErr := s.sourceIndexProfile(ctx, kb)
	preflightFailures := map[string]error{}
	preflightCount := 0
	var preflightSize int64
	preflightWithinPipelineLimits := true
	resourcePolicy := s.sourceResources.Policy()
	rules = resourcePolicy.ApplyFileLimit(rules)
	files, err := source.ReadGitWithPolicy(ctx, repository, rules, resourcePolicy, func(file types.SourcePreviewFile, content []byte) error {
		preflightCount++
		preflightSize += int64(len(content))
		if preflightCount > resourcePolicy.MaxSelectedFiles || preflightSize > resourcePolicy.MaxSelectedBytes {
			preflightWithinPipelineLimits = false
		}
		if profileErr != nil || source.LanguageForPath(file.Path) == "" || !preflightWithinPipelineLimits {
			return nil
		}
		if checkErr := source.CheckPreviewIndexFeasibility(file.Path, content, profile); checkErr != nil {
			preflightFailures[file.Path] = checkErr
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, source.ErrResourceLimitExceeded) {
			return nil, fmt.Errorf("%w; narrow the included directories or add exclusions before retrying", err)
		}
		return nil, err
	}
	for index := range files {
		if reason, unfit := preflightFailures[files[index].Path]; unfit {
			files[index].Status = "unindexable"
			files[index].Reason = reason.Error()
		}
	}
	requiredLanguages := []string{}
	seenLanguages := map[string]bool{}
	for _, file := range files {
		if file.Status == "included" || file.Status == "unindexable" {
			language := source.LanguageForPath(file.Path)
			if language != "" && !seenLanguages[language] {
				requiredLanguages = append(requiredLanguages, language)
				seenLanguages[language] = true
			}
		}
	}
	indexMessage := "source mode requires PostgreSQL keyword/vector indexes and an embedding model with a supported tokenizer and configured hard input limit"
	if profileErr != nil {
		indexMessage = profileErr.Error()
	} else if len(preflightFailures) > 0 {
		indexMessage = "one or more included files exceed the model's source index budget; see unindexable file rows"
	}
	preview := &types.SourcePreview{ProjectID: repository.ProjectID, Branch: repository.Branch, CommitSHA: repository.CommitSHA,
		RulesVersion: version, Files: files, Warnings: []string{}, Checks: []types.SourcePreviewCheck{
			{Name: "gitlab_branch", Ready: true, Message: "specified branch resolved and fixed commit fetched"},
			{Name: "indexes", Ready: indexBackendReady && profileErr == nil && len(preflightFailures) == 0, Message: indexMessage},
			{Name: "parser", Ready: sourceParserReady(ctx, requiredLanguages...), Message: "source mode requires a healthy, versioned parser with every selected grammar or text fallback route"},
			{Name: "source_pipeline", Ready: false, Message: "source ingestion pipeline is not available; configuration and preview can be saved"},
		}}
	if !kb.IsWikiEnabled() {
		preview.Warnings = append(preview.Warnings, "Wiki is disabled for this knowledge base")
	}
	if s.sourceSnapshots != nil && s.sourceModelService != nil {
		count, size, supportedOnly := 0, int64(0), true
		for _, file := range files {
			if file.Status != "included" && file.Status != "excluded" {
				supportedOnly = false
			}
			if file.Status == "included" {
				count++
				size += file.Size
				supportedOnly = supportedOnly && source.LanguageForPath(file.Path) != ""
			}
		}
		previous, publishedErr := s.sourceSnapshots.GetPublished(ctx, ds.TenantID, ds.ID)
		canPublishEmpty := publishedErr == nil && previous != nil
		pipeline := &preview.Checks[3]
		pipeline.Ready = supportedOnly && (count > 0 || canPublishEmpty) && count <= resourcePolicy.MaxSelectedFiles && size <= resourcePolicy.MaxSelectedBytes && (kb.VectorStoreID == nil || *kb.VectorStoreID == "") && s.sourceSnapshots.CheckReady(ctx) == nil
		pipeline.Message = "source sync requires files within the configured source resource budgets, supported source, template, and text configuration formats, and built-in PostgreSQL indexes; an existing publication may become empty"
		preview.CanSync = true
		for _, check := range preview.Checks {
			preview.CanSync = preview.CanSync && check.Ready
		}
	}
	return preview, nil
}

func (s *DataSourceService) sourceIndexBackendReady(ctx context.Context, kb *types.KnowledgeBase) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if kb == nil || !kb.IsKeywordEnabled() || !kb.IsVectorEnabled() || kb.EmbeddingModelID == "" || s.sourceRetrieve == nil {
		return false
	}
	if kb.VectorStoreID != nil && *kb.VectorStoreID != "" && s.sourceOwnership == nil {
		return false
	}
	if tenant, ok := types.TenantInfoFromContext(ctx); !ok || tenant.ID != kb.TenantID {
		if s.tenantRepo == nil {
			return false
		}
		tenant, err := s.tenantRepo.GetTenantByID(ctx, kb.TenantID)
		if err != nil || tenant == nil || tenant.ID != kb.TenantID {
			return false
		}
		ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
	}
	engine, err := retriever.CreateRetrieveEngineForKB(ctx, s.sourceRetrieve, s.sourceOwnership, kb.TenantID, kb.VectorStoreID)
	return err == nil && engine.SupportsEngine(types.PostgresRetrieverEngineType, types.KeywordsRetrieverType, types.VectorRetrieverType) && engine.CheckSourceIndexes(ctx) == nil
}

func (s *DataSourceService) sourceIndexProfile(ctx context.Context, kb *types.KnowledgeBase) (source.IndexProfile, error) {
	if s.sourceModels == nil || kb == nil || kb.EmbeddingModelID == "" {
		return source.IndexProfile{}, fmt.Errorf("source embedding model is unavailable")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	model, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	if err != nil {
		return source.IndexProfile{}, err
	}
	if model == nil || (!model.IsBuiltin && model.TenantID != kb.TenantID) || model.Type != types.ModelTypeEmbedding || model.Status != types.ModelStatusActive {
		return source.IndexProfile{}, fmt.Errorf("source embedding model is unavailable or inactive")
	}
	if model.Parameters.EmbeddingParameters.Dimension <= 0 {
		return source.IndexProfile{}, fmt.Errorf("source embedding model dimension is invalid")
	}
	return source.NewIndexProfile(model.Parameters.EmbeddingParameters)
}

func sourceParserReady(ctx context.Context, requiredLanguages ...string) bool {
	endpoint := strings.TrimRight(strings.TrimSpace(os.Getenv("SOURCE_PARSER_URL")), "/")
	if endpoint == "" {
		return false
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/health", nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return false
	}
	var health struct {
		Ready         bool     `json:"ready"`
		ParserVersion string   `json:"parser_version"`
		Languages     []string `json:"languages"`
	}
	if json.NewDecoder(http.MaxBytesReader(nil, response.Body, 16384)).Decode(&health) != nil || !health.Ready || health.ParserVersion == "" {
		return false
	}
	if len(requiredLanguages) == 0 {
		requiredLanguages = []string{"java"}
	}
	available := map[string]bool{}
	for _, language := range health.Languages {
		available[language] = true
	}
	for _, language := range requiredLanguages {
		if !available[language] {
			return false
		}
	}
	return true
}
