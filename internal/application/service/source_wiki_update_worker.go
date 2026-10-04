package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
)

const sourceWikiUpdateClaimStaleAfter = 2 * time.Hour

type sourceWikiUpdateWorker struct {
	processor  interfaces.SourceWikiUpdateProcessor
	pendingOps interfaces.TaskPendingOpsRepository
	task       interfaces.TaskEnqueuer
}

func NewSourceWikiUpdateWorker(
	processor interfaces.SourceWikiUpdateProcessor,
	pendingOps interfaces.TaskPendingOpsRepository,
	task interfaces.TaskEnqueuer,
) interfaces.TaskHandler {
	return &sourceWikiUpdateWorker{processor: processor, pendingOps: pendingOps, task: task}
}

func (w *sourceWikiUpdateWorker) Handle(ctx context.Context, task *asynq.Task) error {
	if w == nil || w.processor == nil || w.pendingOps == nil || task == nil || task.Type() != types.TypeSourceWikiUpdate {
		return fmt.Errorf("source Wiki update task dependencies or type are invalid")
	}
	var trigger types.SourceWikiUpdateTriggerPayload
	if err := json.Unmarshal(task.Payload(), &trigger); err != nil || trigger.TenantID == 0 || trigger.KnowledgeBaseID == "" {
		return fmt.Errorf("source Wiki update trigger identity is invalid")
	}
	ctx = types.WithExecutionTenant(ctx, trigger.TenantID)
	operation, err := w.pendingOps.ClaimBatch(ctx, types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase,
		trigger.KnowledgeBaseID, 1, time.Now().Add(-sourceWikiUpdateClaimStaleAfter))
	if err != nil || len(operation) == 0 {
		return err
	}
	row := operation[0]
	rowIDs := []int64{row.ID}
	fail := func(cause error) error {
		if _, bumpErr := w.pendingOps.IncrFailCount(ctx, row.ID); bumpErr != nil {
			logger.Warnf(ctx, "[SourceWikiUpdate] failed to increment delivery failure count id=%d: %v", row.ID, bumpErr)
		}
		if releaseErr := w.pendingOps.ReleaseByIDs(ctx, rowIDs); releaseErr != nil {
			logger.Warnf(ctx, "[SourceWikiUpdate] failed to release delivery claim id=%d: %v", row.ID, releaseErr)
		}
		return cause
	}
	var payload types.SourceWikiUpdatePayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil ||
		row.TenantID != trigger.TenantID || row.Scope != types.TaskScopeKnowledgeBase || row.ScopeID != trigger.KnowledgeBaseID ||
		row.TaskType != types.TypeSourceWikiUpdate || row.Op != "published_snapshot" || row.DedupKey != payload.DeliveryID ||
		payload.SchemaVersion != 1 || payload.EventID == "" || payload.DeliveryID == "" || payload.ConfigGeneration <= 0 ||
		payload.TenantID != trigger.TenantID || payload.KnowledgeBaseID != trigger.KnowledgeBaseID || payload.DataSourceID == "" || payload.SnapshotID == "" {
		return fail(fmt.Errorf("source Wiki update delivery payload does not match its durable row"))
	}
	if err := w.processor.ProcessPublishedSourceWikiUpdate(ctx, payload); err != nil {
		return fail(err)
	}
	if err := w.pendingOps.DeleteByIDs(ctx, rowIDs); err != nil {
		return fail(err)
	}
	pending, err := w.pendingOps.PendingCount(ctx, types.TypeSourceWikiUpdate, types.TaskScopeKnowledgeBase, trigger.KnowledgeBaseID)
	if err != nil {
		return err
	}
	if pending > 0 && w.task != nil {
		encoded, marshalErr := json.Marshal(trigger)
		if marshalErr != nil {
			return marshalErr
		}
		followUp := asynq.NewTask(types.TypeSourceWikiUpdate, encoded, asynq.Queue(types.QueueWiki), asynq.MaxRetry(10), asynq.Timeout(60*time.Minute))
		if _, enqueueErr := w.task.Enqueue(followUp); enqueueErr != nil && !errors.Is(enqueueErr, asynq.ErrDuplicateTask) && !errors.Is(enqueueErr, asynq.ErrTaskIDConflict) {
			return enqueueErr
		}
	}
	return nil
}

var _ interfaces.TaskHandler = (*sourceWikiUpdateWorker)(nil)
