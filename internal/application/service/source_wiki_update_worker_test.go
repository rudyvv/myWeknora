package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
)

type sourceWikiUpdatePendingOpsStub struct {
	interfaces.TaskPendingOpsRepository
	row       *types.TaskPendingOp
	deleted   []int64
	released  []int64
	failCount int64
}

func (s *sourceWikiUpdatePendingOpsStub) ClaimBatch(context.Context, string, string, string, int, time.Time) ([]*types.TaskPendingOp, error) {
	if s.row == nil {
		return nil, nil
	}
	return []*types.TaskPendingOp{s.row}, nil
}
func (s *sourceWikiUpdatePendingOpsStub) DeleteByIDs(_ context.Context, ids []int64) error {
	s.deleted = append([]int64(nil), ids...)
	return nil
}
func (s *sourceWikiUpdatePendingOpsStub) ReleaseByIDs(_ context.Context, ids []int64) error {
	s.released = append([]int64(nil), ids...)
	return nil
}
func (s *sourceWikiUpdatePendingOpsStub) IncrFailCount(context.Context, int64) (int, error) {
	s.failCount++
	return int(s.failCount), nil
}
func (s *sourceWikiUpdatePendingOpsStub) PendingCount(context.Context, string, string, string) (int64, error) {
	return 0, nil
}

type sourceWikiUpdateProcessorStub struct {
	called  int
	payload types.SourceWikiUpdatePayload
	err     error
}

func (s *sourceWikiUpdateProcessorStub) ProcessPublishedSourceWikiUpdate(_ context.Context, payload types.SourceWikiUpdatePayload) error {
	s.called++
	s.payload = payload
	return s.err
}

func TestSourceWikiUpdateWorkerClaimsValidatesProcessesAndAcknowledgesExactDelivery(t *testing.T) {
	payload := types.SourceWikiUpdatePayload{
		SchemaVersion: 1, EventID: "event-1", DeliveryID: "event-1:g3", TenantID: 7,
		KnowledgeBaseID: "kb-1", DataSourceID: "source-1", SnapshotID: "snapshot-1", ConfigGeneration: 3,
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	pending := &sourceWikiUpdatePendingOpsStub{row: &types.TaskPendingOp{
		ID: 42, TenantID: 7, TaskType: types.TypeSourceWikiUpdate, Scope: types.TaskScopeKnowledgeBase,
		ScopeID: "kb-1", Op: "published_snapshot", DedupKey: payload.DeliveryID, Payload: encoded,
	}}
	processor := &sourceWikiUpdateProcessorStub{}
	worker := NewSourceWikiUpdateWorker(processor, pending, nil)
	trigger, err := json.Marshal(types.SourceWikiUpdateTriggerPayload{TenantID: 7, KnowledgeBaseID: "kb-1"})
	require.NoError(t, err)
	require.NoError(t, worker.Handle(context.Background(), asynq.NewTask(types.TypeSourceWikiUpdate, trigger)))
	require.Equal(t, 1, processor.called)
	require.Equal(t, payload, processor.payload)
	require.Equal(t, []int64{42}, pending.deleted)
	require.Empty(t, pending.released)
}

func TestSourceWikiUpdateWorkerReleasesFailedDeliveryForRetry(t *testing.T) {
	payload := types.SourceWikiUpdatePayload{
		SchemaVersion: 1, EventID: "event-1", DeliveryID: "event-1:g3", TenantID: 7,
		KnowledgeBaseID: "kb-1", DataSourceID: "source-1", SnapshotID: "snapshot-1", ConfigGeneration: 3,
	}
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	pending := &sourceWikiUpdatePendingOpsStub{row: &types.TaskPendingOp{
		ID: 42, TenantID: 7, TaskType: types.TypeSourceWikiUpdate, Scope: types.TaskScopeKnowledgeBase,
		ScopeID: "kb-1", Op: "published_snapshot", DedupKey: payload.DeliveryID, Payload: encoded,
	}}
	processor := &sourceWikiUpdateProcessorStub{err: errors.New("retry me")}
	worker := NewSourceWikiUpdateWorker(processor, pending, nil)
	trigger, err := json.Marshal(types.SourceWikiUpdateTriggerPayload{TenantID: 7, KnowledgeBaseID: "kb-1"})
	require.NoError(t, err)
	err = worker.Handle(context.Background(), asynq.NewTask(types.TypeSourceWikiUpdate, trigger))
	require.ErrorContains(t, err, "retry me")
	require.Equal(t, 1, processor.called)
	require.Equal(t, int64(1), pending.failCount)
	require.Equal(t, []int64{42}, pending.released)
	require.Empty(t, pending.deleted)
}
