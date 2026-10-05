package interfaces

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// SourceWikiBatchReadService exposes parent status and per-topic coverage
// without making an attempt draft readable to viewers.
type SourceWikiBatchReadService interface {
	ListSourceWikiBatches(context.Context, string, string) ([]types.SourceWikiBatch, error)
	GetSourceWikiBatch(context.Context, string, string, string) (*types.SourceWikiBatch, error)
	ListSourceWikiCoverage(context.Context, string, string) ([]types.SourceWikiCoverageTopic, error)
	GetSourceWikiCoverageSummary(context.Context, string, string, string) (*types.SourceWikiCoverageTelemetry, error)
}
