//go:build integration

package service

import (
	"fmt"
	"testing"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/source"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSourceLogicalQuotaFailuresKeepPublishedVersionAndOtherSource(t *testing.T) {
	quotas := []struct {
		name      string
		value     func(types.SourceResourceUsage) int64
		setLimit  func(*source.ResourcePolicy, int64)
		newMarker string
	}{
		{
			name:      "original bytes",
			value:     func(usage types.SourceResourceUsage) int64 { return usage.OriginalBytes },
			setLimit:  func(policy *source.ResourcePolicy, limit int64) { policy.OriginalBytesPerSource = limit },
			newMarker: "newOriginalQuotaMarker",
		},
		{
			name:      "parsed cache bytes",
			value:     func(usage types.SourceResourceUsage) int64 { return usage.ParsedCacheBytes },
			setLimit:  func(policy *source.ResourcePolicy, limit int64) { policy.ParsedCacheBytesPerSource = limit },
			newMarker: "newParsedCacheQuotaMarker",
		},
		{
			name:      "vector bytes",
			value:     func(usage types.SourceResourceUsage) int64 { return usage.VectorBytes },
			setLimit:  func(policy *source.ResourcePolicy, limit int64) { policy.VectorBytesPerSource = limit },
			newMarker: "newVectorQuotaMarker",
		},
	}

	for _, quota := range quotas {
		quota := quota
		t.Run(quota.name, func(t *testing.T) {
			initialService := []byte("package demo; public class Service { public String quotaBaselineMarker() { return \"quotaBaselineMarker\"; } }\n")
			initialOther := []byte("package demo; public class Other { public String quotaOtherMarker() { return \"quotaOtherMarker\"; } }\n")
			f := newJavaSourceFixture(t, map[string][]byte{
				"src/Service.java": initialService,
				"src/Other.java":   initialOther,
			})
			ensureT19LifecycleSchema(t, f)
			syncSourceFixture(t, f)
			publishedCommit := f.sha
			aHits := sourceResourceRegressionSearch(t, f, f.ds.ID, "quotaBaselineMarker")
			require.NotEmpty(t, aHits)
			aHandle := aHits[0].KnowledgeID
			aView, err := f.knowledge.GetSourceFile(f.ctx, aHandle)
			require.NoError(t, err)
			require.Equal(t, publishedCommit, aView.CommitSHA)

			other := *f.ds
			other.ID, other.Name = uuid.NewString(), "quota-independent source"
			sourceResourceRegressionSetPaths(t, &other, []string{"src/Other.java"})
			_, err = f.service.CreateDataSource(f.ctx, &other)
			require.NoError(t, err)
			syncSourceFixture(t, f, other.ID)
			otherHits := sourceResourceRegressionSearch(t, f, other.ID, "quotaOtherMarker")
			require.NotEmpty(t, otherHits)
			otherHandle := otherHits[0].KnowledgeID
			otherView, err := f.knowledge.GetSourceFile(f.ctx, otherHandle)
			require.NoError(t, err)
			require.Equal(t, publishedCommit, otherView.CommitSHA)

			usageBefore, err := f.service.sourceSnapshots.GetSourceResourceUsage(f.ctx, f.ds.TenantID, f.ds.ID)
			require.NoError(t, err)
			currentQuotaBytes := quota.value(usageBefore)
			require.Positive(t, currentQuotaBytes, "the real initial publication must have staged this resource type")
			limit := currentQuotaBytes + 1
			policy := source.DefaultResourcePolicy()
			quota.setLimit(&policy, limit)
			require.NoError(t, policy.Validate())
			f.service.sourceSnapshots = repository.NewSourceSnapshotRepositoryWithPolicy(f.db, policy)

			updatedService := fmt.Sprintf(
				"package demo; public class Service { public String %s() { return \"%s\"; } public int %sFact() { return 2; } }\n",
				quota.newMarker, quota.newMarker, quota.newMarker,
			)
			newCommit := f.advanceJava(updatedService)
			require.NotEqual(t, publishedCommit, newCommit)
			failedLog, err := f.service.ManualSync(f.ctx, f.ds.ID)
			require.NoError(t, err)
			processErr := f.service.ProcessSync(f.ctx, sourceResourceRegressionTask(t, f, f.ds.ID, failedLog.ID))
			require.ErrorIs(t, processErr, source.ErrSourceQuotaExceeded)

			usageAfter, err := f.service.sourceSnapshots.GetSourceResourceUsage(f.ctx, f.ds.TenantID, f.ds.ID)
			require.NoError(t, err)
			require.Equal(t, currentQuotaBytes, quota.value(usageAfter),
				"the over-quota payload must roll back in its persistence transaction")
			require.LessOrEqual(t, quota.value(usageAfter), limit)

			stillPublished := sourceResourceRegressionSearch(t, f, f.ds.ID, "quotaBaselineMarker")
			require.NotEmpty(t, stillPublished)
			stillVisible := sourceResourceRegressionSearch(t, f, f.ds.ID, quota.newMarker)
			require.Empty(t, stillVisible, "the rejected source text must remain outside published search")
			currentView, err := f.knowledge.GetSourceFile(f.ctx, aHandle)
			require.NoError(t, err, "the prior source handle must remain readable")
			require.Equal(t, publishedCommit, currentView.CommitSHA)
			require.Contains(t, currentView.Content, "quotaBaselineMarker")

			stillOther := sourceResourceRegressionSearch(t, f, other.ID, "quotaOtherMarker")
			require.NotEmpty(t, stillOther, "quota exhaustion for A must not make source B unreadable")
			currentOtherView, err := f.knowledge.GetSourceFile(f.ctx, otherHandle)
			require.NoError(t, err)
			require.Equal(t, publishedCommit, currentOtherView.CommitSHA)
			require.Contains(t, currentOtherView.Content, "quotaOtherMarker")
		})
	}
}
