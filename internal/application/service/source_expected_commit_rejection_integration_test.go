//go:build integration

package service

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

// An expected commit must never silently fall back to a non-durable manual
// sync: without the source coordinator there is no persisted target a worker
// or crash recovery could honor.
func TestSourceManualSyncExpectedCommitRejectedWithoutDurableCoordinator(t *testing.T) {
	f := newJavaSourceFixture(t)
	f.service.syncLogRepo = struct{ interfaces.SyncLogRepository }{f.service.syncLogRepo}

	preview, err := f.service.PreviewSource(f.ctx, f.ds.ID, nil)
	require.NoError(t, err)
	ctx, err := types.WithSourceSyncExpectedCommit(f.ctx, preview.CommitSHA)
	require.NoError(t, err)

	_, err = f.service.ManualSync(ctx, f.ds.ID)
	require.ErrorContains(t, err, "expected commit requires durable source coordination")
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 50, 0)
	require.NoError(t, err)
	require.Empty(t, logs, "a rejected request without coordination must not create a sync log")
}
