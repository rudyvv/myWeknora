package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SourceWikiBatchPreflightService validates the server-selected source,
// publication and model, then returns a bounded planning preview. It does not
// create a batch or dispatch a provider call.
type SourceWikiBatchPreflightService interface {
	PreflightSourceWikiBatch(context.Context, string, string, types.SourceWikiBatchPreflightRequest) (*types.SourceWikiBatchPreflight, error)
}
