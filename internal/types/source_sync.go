package types

import (
	"context"
	"errors"
	"time"
)

var ErrSourceSyncLeaseLost = errors.New("source sync lease lost")

// SourceSyncLease is the database-issued authority for one source worker.
// Its monotonically increasing fencing token is checked by staging and publish
// transactions; a stale worker cannot regain authority by continuing to run.
type SourceSyncLease struct {
	DataSourceID     string
	TenantID         uint64
	SyncLogID        string
	Owner            string
	ConfigGeneration int64
	FencingToken     int64
	ExpiresAt        time.Time
	TargetCommitSHA  string
}

// SourceSyncDispatch is a durable trigger ready for queue delivery.
type SourceSyncDispatch struct {
	SyncLog            *SyncLog
	Trigger            string
	DeliveryGeneration int64
}

type sourceSyncLeaseContextKey struct{}

func WithSourceSyncLease(ctx context.Context, lease SourceSyncLease) context.Context {
	return context.WithValue(ctx, sourceSyncLeaseContextKey{}, lease)
}

func SourceSyncLeaseFromContext(ctx context.Context) (SourceSyncLease, bool) {
	lease, ok := ctx.Value(sourceSyncLeaseContextKey{}).(SourceSyncLease)
	return lease, ok
}
