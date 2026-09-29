package service

import (
	"context"
	"encoding/json"
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
	files, err := source.PreviewGit(ctx, repository, rules)
	if err != nil {
		return nil, err
	}
	requiredLanguages := []string{}
	seenLanguages := map[string]bool{}
	for _, file := range files {
		if file.Status == "included" {
			language := source.LanguageForPath(file.Path)
			if language != "" && !seenLanguages[language] {
				requiredLanguages = append(requiredLanguages, language)
				seenLanguages[language] = true
			}
		}
	}
	preview := &types.SourcePreview{ProjectID: repository.ProjectID, Branch: repository.Branch, CommitSHA: repository.CommitSHA,
		RulesVersion: version, Files: files, Warnings: []string{}, Checks: []types.SourcePreviewCheck{
			{Name: "gitlab_branch", Ready: true, Message: "specified branch resolved and fixed commit fetched"},
			{Name: "indexes", Ready: s.sourceIndexesReady(ctx, kb), Message: "source mode requires a resolved PostgreSQL backend with keyword and vector indexes and an embedding model"},
			{Name: "parser", Ready: sourceParserReady(ctx, requiredLanguages...), Message: "source mode requires a healthy, versioned parser with every selected language grammar"},
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
		pipeline := &preview.Checks[3]
		pipeline.Ready = len(rules.Projects[0].Paths) > 0 && supportedOnly && count > 0 && count <= 100 && size <= 16<<20 && (kb.VectorStoreID == nil || *kb.VectorStoreID == "") && s.sourceSnapshots.CheckReady(ctx) == nil
		pipeline.Message = "initial sync requires explicit paths, 1–100 Java/JavaScript/TypeScript files, at most 16 MiB and the built-in PostgreSQL indexes"
		preview.CanSync = true
		for _, check := range preview.Checks {
			preview.CanSync = preview.CanSync && check.Ready
		}
	}
	return preview, nil
}

func (s *DataSourceService) sourceIndexesReady(ctx context.Context, kb *types.KnowledgeBase) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !kb.IsKeywordEnabled() || !kb.IsVectorEnabled() || kb.EmbeddingModelID == "" || s.sourceRetrieve == nil {
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
	if err != nil || !engine.SupportsEngine(types.PostgresRetrieverEngineType, types.KeywordsRetrieverType, types.VectorRetrieverType) || engine.CheckSourceIndexes(ctx) != nil || s.sourceModels == nil {
		return false
	}
	model, err := s.sourceModels.GetByID(ctx, kb.TenantID, kb.EmbeddingModelID)
	return err == nil && model != nil && (model.IsBuiltin || model.TenantID == kb.TenantID) && model.Type == types.ModelTypeEmbedding && model.Status == types.ModelStatusActive && model.Parameters.EmbeddingParameters.Dimension > 0
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
