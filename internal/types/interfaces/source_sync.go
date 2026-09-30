package interfaces

import (
	"context"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// SourceSyncControlRepository is the PostgreSQL-backed coordination boundary
// for durable source triggers. It is intentionally separate from the ordinary
// document sync contracts.
type SourceSyncControlRepository interface {
	RegisterSourceTrigger(context.Context, *types.DataSource, *types.SyncLog, string) (bool, int64, error)
	IsCurrentSourceDelivery(context.Context, *types.DataSource, string, int64) (bool, error)
	ClaimSourceRun(context.Context, *types.DataSource, string, int64, string, time.Duration) (types.SourceSyncLease, bool, error)
	RenewSourceRun(context.Context, types.SourceSyncLease, time.Duration) (types.SourceSyncLease, error)
	ReleaseSourceRun(context.Context, types.SourceSyncLease, bool) (*types.SourceSyncDispatch, error)
	RecoverSourceTriggers(context.Context, *types.DataSource) ([]types.SourceSyncDispatch, error)
	RecoverAllSourceTriggers(context.Context) ([]types.SourceSyncDispatch, error)
	AdvanceSourceConfig(context.Context, *types.DataSource, bool) error
	PauseSourceSync(context.Context, *types.DataSource) error
	RecordSourceRunPhase(context.Context, types.SourceSyncLease, string, string) error
	CommitSourceRunResult(context.Context, types.SourceSyncLease, *types.DataSource) error
}
