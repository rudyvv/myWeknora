package service

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/Tencent/WeKnora/internal/infrastructure/docparser"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/models/utils/ollama"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"go.uber.org/dig"
	"gorm.io/gorm"
)

func provideNilRecoveryDependency[T any](container *dig.Container) error {
	return container.Provide(func() T {
		var dependency T
		return dependency
	})
}

// This lightweight acceptance resolves the real constructor path that the
// production startup Invoke traverses, including KnowledgeBaseService's late
// storage, ownership, resource-catalog and scheduler dependencies.
func TestSourceWikiAttemptRecoveryConstructorGraphResolves(t *testing.T) {
	container := dig.New()
	for _, provide := range []func() error{
		func() error { return provideNilRecoveryDependency[interfaces.KnowledgeBaseRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.KnowledgeRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.ChunkRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.KBShareRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.KBShareService](container) },
		func() error { return provideNilRecoveryDependency[interfaces.RetrieveEngineRegistry](container) },
		func() error { return provideNilRecoveryDependency[retriever.TenantStoreOwnership](container) },
		func() error { return provideNilRecoveryDependency[interfaces.TenantRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.FileService](container) },
		func() error { return provideNilRecoveryDependency[interfaces.StorageBackendResolver](container) },
		func() error { return provideNilRecoveryDependency[interfaces.RetrieveGraphRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.TaskEnqueuer](container) },
		func() error { return provideNilRecoveryDependency[interfaces.TaskInspector](container) },
		func() error { return provideNilRecoveryDependency[interfaces.TaskPendingOpsRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.DataSourceRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.SyncLogRepository](container) },
		func() error {
			return container.Provide(func() *datasource.Scheduler { return &datasource.Scheduler{} })
		},
		func() error { return provideNilRecoveryDependency[interfaces.AuditLogService](container) },
		func() error { return provideNilRecoveryDependency[interfaces.ResourceCatalog](container) },
		func() error { return provideNilRecoveryDependency[interfaces.WikiPageRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.AgentShareService](container) },
		func() error { return provideNilRecoveryDependency[interfaces.ModelRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.CustomAgentRepository](container) },
		func() error {
			return container.Provide(func() *ollama.OllamaService { return &ollama.OllamaService{} })
		},
		func() error { return provideNilRecoveryDependency[embedding.EmbedderPooler](container) },
		func() error { return provideNilRecoveryDependency[interfaces.TenantService](container) },
		func() error { return container.Provide(func() *redis.Client { return &redis.Client{} }) },
		func() error { return container.Provide(func() *config.Config { return &config.Config{} }) },
		func() error { return provideNilRecoveryDependency[interfaces.DocumentReader](container) },
		func() error { return provideNilRecoveryDependency[interfaces.ChunkService](container) },
		func() error { return provideNilRecoveryDependency[interfaces.KnowledgeTagRepository](container) },
		func() error { return provideNilRecoveryDependency[interfaces.KnowledgeTagService](container) },
		func() error {
			return container.Provide(func() *docparser.ImageResolver { return &docparser.ImageResolver{} })
		},
		func() error { return provideNilRecoveryDependency[SpanTracker](container) },
		func() error { return container.Provide(func() *gorm.DB { return &gorm.DB{} }) },
		func() error { return container.Provide(NewKnowledgeBaseService) },
		func() error { return container.Provide(NewModelService) },
		func() error { return container.Provide(NewWikiPageService) },
		func() error { return container.Provide(NewKnowledgeService) },
		func() error { return container.Provide(NewSourceWikiService) },
		func() error { return container.Provide(NewSourceWikiAttemptRecovery) },
	} {
		require.NoError(t, provide())
	}

	var recovery *SourceWikiAttemptRecovery
	require.NoError(t, container.Invoke(func(resolved *SourceWikiAttemptRecovery) {
		recovery = resolved
	}))
	require.NotNil(t, recovery)
}

func TestSourceWikiRecoveryBootstrapInvokeFollowsContainerProviders(t *testing.T) {
	path := filepath.Join("..", "..", "container", "container.go")
	source, err := os.ReadFile(path)
	require.NoError(t, err)
	fileSet := token.NewFileSet()
	file, err := parser.ParseFile(fileSet, path, source, 0)
	require.NoError(t, err)

	var buildContainer *ast.FuncDecl
	for _, declaration := range file.Decls {
		if function, ok := declaration.(*ast.FuncDecl); ok && function.Name.Name == "BuildContainer" {
			buildContainer = function
			break
		}
	}
	require.NotNil(t, buildContainer)

	var lastProvider, recoveryInvoke token.Pos
	ast.Inspect(buildContainer.Body, func(node ast.Node) bool {
		if _, nestedFunction := node.(*ast.FuncLit); nestedFunction {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != "container" {
			return true
		}
		switch selector.Sel.Name {
		case "Provide":
			if call.Pos() > lastProvider {
				lastProvider = call.Pos()
			}
		case "Invoke":
			for _, argument := range call.Args {
				if target, ok := argument.(*ast.Ident); ok && target.Name == "startSourceWikiAttemptRecovery" {
					recoveryInvoke = call.Pos()
				}
			}
		}
		return true
	})
	require.NotZero(t, lastProvider, "BuildContainer must register providers")
	require.NotZero(t, recoveryInvoke, "BuildContainer must start SourceWiki recovery")
	require.Greater(t, recoveryInvoke, lastProvider, "recovery startup resolves the full graph and must follow all provider registrations")
}
