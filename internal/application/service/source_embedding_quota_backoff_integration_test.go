//go:build integration

package service

import (
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

// A provider quota window (TPM/RPM rate limit) must not fail the source run
// while the quota can still recover: the indexing stage backs off and retries
// the same batch inside the run deadline. Vectors already banked in earlier
// batches stay cached, so repeated quota windows converge.
func TestSourceSyncRetriesEmbeddingWithinQuotaBackoffUntilProviderRecovers(t *testing.T) {
	oldBackoff := sourceEmbeddingQuotaBackoff
	sourceEmbeddingQuotaBackoff = 5 * time.Millisecond
	t.Cleanup(func() { sourceEmbeddingQuotaBackoff = oldBackoff })

	f := newJavaSourceFixture(t)
	// The first three provider calls hit the quota window; the fourth succeeds.
	f.embedRateLimitRemaining.Store(3)

	syncSourceFixture(t, f)

	require.GreaterOrEqual(t, f.embedCount.Load(), int64(4),
		"the quota-limited calls must have been retried, not abandoned")
	logs, err := f.service.GetSyncLogs(f.ctx, f.ds.ID, 10, 0)
	require.NoError(t, err)
	require.NotEmpty(t, logs)
	finished, err := f.service.GetSyncLog(f.ctx, logs[0].ID)
	require.NoError(t, err)
	require.Equal(t, types.SyncLogStatusSuccess, finished.Status,
		"a recoverable quota window must not fail the publication")
	result, err := finished.ParseResult()
	require.NoError(t, err)
	require.Equal(t, "published", result.Source.Snapshot.State)
	require.Positive(t, result.Source.Snapshot.EmbeddedChunkCount,
		"the retried batch must still be embedded and published")
}

// When a quota backoff would sleep past the run deadline, the stage must fail
// immediately instead of sleeping beyond the budget; the durable retry_wait
// path then resumes from the vector cache on the next delivery.
func TestSourceSyncQuotaBackoffStopsAtRunDeadlineInsteadOfSleeping(t *testing.T) {
	oldBackoff := sourceEmbeddingQuotaBackoff
	sourceEmbeddingQuotaBackoff = time.Hour
	t.Cleanup(func() { sourceEmbeddingQuotaBackoff = oldBackoff })

	f := newJavaSourceFixture(t)
	// Every provider call stays quota-limited for the whole attempt.
	f.embedRateLimitRemaining.Store(1 << 30)

	log, err := f.service.ManualSync(f.ctx, f.ds.ID)
	require.NoError(t, err)
	started := time.Now()
	err = f.service.ProcessSync(f.ctx, sourceTestSyncTask(t, f, log))
	require.ErrorContains(t, err, "quota backoff budget",
		"an unrecoverable quota window must fail with an explicit quota marker")
	require.Less(t, time.Since(started), time.Minute,
		"a backoff that cannot fit before the deadline must not sleep at all")
	requireSourceRunPhase(t, f, log.ID, "retry_wait")
}
