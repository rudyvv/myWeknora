package types

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

var ErrSourceSyncLeaseLost = errors.New("source sync lease lost")

// SourceSyncLease is the database-issued authority for one source worker.
// Its monotonically increasing fencing token is checked by staging and publish
// transactions; a stale worker cannot regain authority by continuing to run.
type SourceSyncLease struct {
	DataSourceID       string
	TenantID           uint64
	SyncLogID          string
	Owner              string
	ConfigGeneration   int64
	FencingToken       int64
	LeaseRecoveryCount int64
	ExpiresAt          time.Time
	TargetCommitSHA    string
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

type sourceExpectedCommitContextKey struct{}

// WithSourceSyncExpectedCommit carries a client's preview condition. It is
// not lease authority; the service must verify it against the bound branch.
func WithSourceSyncExpectedCommit(ctx context.Context, sha string) (context.Context, error) {
	if len(sha) != 40 || strings.ToLower(sha) != sha {
		return ctx, errors.New("expected commit must be a complete lowercase SHA")
	}
	if _, err := hex.DecodeString(sha); err != nil {
		return ctx, errors.New("expected commit must be a complete lowercase SHA")
	}
	return context.WithValue(ctx, sourceExpectedCommitContextKey{}, sha), nil
}

func SourceSyncExpectedCommitFromContext(ctx context.Context) string {
	sha, _ := ctx.Value(sourceExpectedCommitContextKey{}).(string)
	return sha
}
