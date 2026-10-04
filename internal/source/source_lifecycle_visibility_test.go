package source

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestSourceVisibilityGatesEachSourceWithoutInvalidatingMixedLease(t *testing.T) {
	lease := types.SourceReadLease{ID: "38a8d615-0c91-4fae-949d-16e816449237", HasSources: true}
	ctx, release := WithReadScope(context.Background(), lease, nil, func() {})
	defer release()

	snapshotSQL := SnapshotSQL(ctx, "ss.id", "ss.data_source_id", "sm.source_file_id")
	wikiSQL := SourcePermissionSQL(ctx, "c.source_id", "e->>'knowledge_id'")
	require.True(t, strings.Contains(snapshotSQL, "source_query_enabled"), snapshotSQL)
	require.True(t, strings.Contains(wikiSQL, "source_query_enabled"), wikiSQL)
	require.Contains(t, snapshotSQL, "ss.data_source_id")
	require.Contains(t, wikiSQL, "c.source_id")
}
