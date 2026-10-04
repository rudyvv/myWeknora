package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// DataSourceLifecycleService is kept separate from ordinary CRUD so existing
// document-only adapters are not forced to grow source lifecycle behavior.
type DataSourceLifecycleService interface {
	UnbindDataSource(context.Context, string) (*types.DataSource, error)
	ClearSource(context.Context, string, bool, string) (*types.DataSource, error)
	RetryClearSource(context.Context, string, string) (*types.DataSource, error)
}

// SourceLifecycleRepository owns lifecycle fences and durable cleanup
// operations on the source coordinator database.
type SourceLifecycleRepository interface {
	FindSourceCleanup(context.Context, string) (*types.SourceCleanupOperation, error)
	FindSourceCleanups(context.Context, []string) (map[string]*types.SourceCleanupOperation, error)
	UnbindSourceSync(context.Context, *types.DataSource) error
	AcceptSourceCleanup(context.Context, *types.DataSource, string) (*types.SourceCleanupOperation, error)
	RetrySourceCleanup(context.Context, *types.DataSource, string) (*types.SourceCleanupOperation, error)
	ProcessSourceCleanupBatch(context.Context, int) (int, error)
}
