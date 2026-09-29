package source

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRelationPagingContextPreservesCursorAndOnlyLowersDefaultLimit(t *testing.T) {
	ctx := WithRelationCursor(context.Background(), "opaque")
	ctx = WithRelationPageSize(ctx, 20)
	require.Equal(t, "opaque", RelationCursorFromContext(ctx))
	require.Equal(t, 20, RelationPageSizeFromContext(ctx, 100))

	ctx = WithRelationPageSize(ctx, 101)
	require.Equal(t, 20, RelationPageSizeFromContext(ctx, 100), "a caller cannot raise the repository's bounded page size")
	require.Equal(t, 100, RelationPageSizeFromContext(context.Background(), 100))
}
