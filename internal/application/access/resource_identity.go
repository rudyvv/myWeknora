package access

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
)

// Ownership lookups return routing identity only. Authorization must resolve
// the persisted KB before an adapter asks a service for source content or info.
func KnowledgeIdentity(ctx context.Context, lookup interface {
	GetKnowledgeByIDOnly(context.Context, string) (*types.Knowledge, error)
}, id string) (*types.Knowledge, error) {
	if identity, ok := lookup.(interface {
		GetKnowledgeAccessInfo(context.Context, string) (*types.Knowledge, error)
	}); ok {
		return identity.GetKnowledgeAccessInfo(ctx, id)
	}
	return lookup.GetKnowledgeByIDOnly(ctx, id)
}

func ChunkIdentity(ctx context.Context, lookup interface {
	GetChunkByIDOnly(context.Context, string) (*types.Chunk, error)
}, id string) (*types.Chunk, error) {
	if identity, ok := lookup.(interface {
		GetChunkAccessInfo(context.Context, string) (*types.Chunk, error)
	}); ok {
		return identity.GetChunkAccessInfo(ctx, id)
	}
	return lookup.GetChunkByIDOnly(ctx, id)
}
