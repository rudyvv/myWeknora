package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SourceWikiBatchStartService is the guarded write path for creating a batch
// from a fresh server-side preflight; clients cannot submit a plan or budgets.
type SourceWikiBatchStartService interface {
	StartSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatch, error)
}
