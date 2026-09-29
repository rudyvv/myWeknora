package datasource

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
)

// EnqueueSourceSync delivers a database-registered source trigger. The stable
// per-delivery ID is only a queue optimization; PostgreSQL lease/fencing state
// remains the authority when duplicate workers receive the task.
func EnqueueSourceSync(ctx context.Context, enqueuer interfaces.TaskEnqueuer, dispatch types.SourceSyncDispatch, tenantID uint64, sourceID string, initiator types.TaskInitiator) (*asynq.TaskInfo, error) {
	if dispatch.SyncLog == nil || dispatch.SyncLog.ID == "" {
		return nil, fmt.Errorf("source sync dispatch has no sync log")
	}
	trigger := dispatch.Trigger
	if trigger == "" {
		trigger = "recovery"
	}
	payload := &types.DataSourceSyncPayload{
		DataSourceID: sourceID, TenantID: tenantID, SyncLogID: dispatch.SyncLog.ID,
		DeliveryGeneration: dispatch.DeliveryGeneration, Trigger: trigger, Initiator: initiator,
	}
	langfuse.InjectTracing(ctx, payload)
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	task := asynq.NewTask(types.TypeDataSourceSync, payloadJSON,
		asynq.Queue(types.QueueSync), asynq.MaxRetry(5), asynq.Timeout(2*time.Hour),
		asynq.TaskID(fmt.Sprintf("dssource:%s:%d", dispatch.SyncLog.ID, dispatch.DeliveryGeneration)),
	)
	info, err := enqueuer.Enqueue(task)
	if err == asynq.ErrTaskIDConflict {
		return &asynq.TaskInfo{ID: fmt.Sprintf("dssource:%s:%d", dispatch.SyncLog.ID, dispatch.DeliveryGeneration)}, nil
	}
	return info, err
}
