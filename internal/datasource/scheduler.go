package datasource

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/hibiken/asynq"
	"github.com/robfig/cron/v3"
)

// Scheduler manages cron-based periodic sync for data sources.
//
// robfig/cron fires at absolute wall-clock times (e.g. "0 0 * * * *" always fires
// at the top of every hour regardless of when the process started). So multiple
// instances will fire at the same moment. Dedup is handled by two layers:
//
//  1. HasRunningSync — if a previous sync is still running, skip (prevent overlap).
//  2. asynq.TaskID  — deterministic ID per (dataSourceID, minute). Redis ensures
//     only one task with a given ID is enqueued. Losers get ErrTaskIDConflict.
type Scheduler struct {
	cron            *cron.Cron
	dsRepo          interfaces.DataSourceRepository
	syncLogRepo     interfaces.SyncLogRepository
	sourceSnapshots interfaces.SourceSnapshotRepository
	taskEnqueuer    interfaces.TaskEnqueuer

	mu      sync.Mutex
	entries map[string]cron.EntryID // dataSourceID → cron entry ID
}

// NewScheduler creates a new Scheduler.
func NewScheduler(
	dsRepo interfaces.DataSourceRepository,
	syncLogRepo interfaces.SyncLogRepository,
	taskEnqueuer interfaces.TaskEnqueuer,
	sourceSnapshots interfaces.SourceSnapshotRepository,
) *Scheduler {
	return &Scheduler{
		cron: cron.New(cron.WithSeconds(), cron.WithChain(
			cron.Recover(cron.DefaultLogger),
		)),
		dsRepo:          dsRepo,
		syncLogRepo:     syncLogRepo,
		sourceSnapshots: sourceSnapshots,
		taskEnqueuer:    taskEnqueuer,
		entries:         make(map[string]cron.EntryID),
	}
}

// Start loads active data sources for cron registration, then independently
// recovers durable source triggers (including retries for error/paused sources)
// before starting the cron runner.
func (s *Scheduler) Start(ctx context.Context) error {
	dataSources, err := s.dsRepo.FindActive(ctx)
	if err != nil {
		return fmt.Errorf("load active data sources: %w", err)
	}

	for _, ds := range dataSources {
		schedule, _ := scheduledSync(ds)
		if schedule == "" {
			continue
		}
		if err := s.addEntry(ds); err != nil {
			logger.Warnf(ctx, "[Scheduler] failed to register cron for ds=%s schedule=%q: %v",
				ds.ID, schedule, err)
		}
	}
	s.recoverSourceTriggers(ctx, dataSources)
	s.relaySourcePublicationOutbox(ctx)
	if _, err := s.cron.AddFunc("@every 30s", func() { s.reconcileSourceTriggers(context.Background()) }); err != nil {
		logger.Warnf(ctx, "[Scheduler] failed to register source-trigger reconciliation: %v", err)
	}

	s.cron.Start()
	logger.Infof(ctx, "[Scheduler] started with %d cron entries", len(s.entries))
	return nil
}

// Stop gracefully stops the cron runner and waits for running jobs to finish.
func (s *Scheduler) Stop() {
	ctx := s.cron.Stop()
	<-ctx.Done()
}

// AddOrUpdate registers (or re-registers) a cron entry for the given data source.
func (s *Scheduler) AddOrUpdate(ds *types.DataSource) error {
	schedule, sourceMode := scheduledSync(ds)
	if ds.Status == types.DataSourceStatusActive && sourceMode {
		if err := s.recoverQueuedSourceRuns(context.Background(), ds); err != nil {
			logger.Errorf(context.Background(), "[Scheduler] failed to recover source triggers for ds=%s: %v", ds.ID, err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[ds.ID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, ds.ID)
	}

	if ds.Status != types.DataSourceStatusActive || schedule == "" {
		return nil
	}

	return s.addEntryLocked(ds)
}

// Remove removes the cron entry for a data source.
func (s *Scheduler) Remove(dataSourceID string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if entryID, ok := s.entries[dataSourceID]; ok {
		s.cron.Remove(entryID)
		delete(s.entries, dataSourceID)
	}
}

func (s *Scheduler) addEntry(ds *types.DataSource) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.addEntryLocked(ds)
}

func (s *Scheduler) addEntryLocked(ds *types.DataSource) error {
	dsID := ds.ID
	tenantID := ds.TenantID
	schedule, _ := scheduledSync(ds)

	entryID, err := s.cron.AddFunc(schedule, func() {
		s.triggerSync(dsID, tenantID)
	})
	if err != nil {
		return fmt.Errorf("invalid cron expression %q: %w", schedule, err)
	}

	s.entries[dsID] = entryID
	return nil
}

const DefaultSourceSyncSchedule = "0 0 * * * *"

// scheduledSync keeps the historical opt-in scheduling behavior for document
// sources while giving source-mode repositories the agreed hourly default.
func scheduledSync(ds *types.DataSource) (string, bool) {
	if ds == nil {
		return "", false
	}
	config, err := ds.ParseConfig()
	if err != nil {
		return ds.SyncSchedule, false
	}
	mode, err := ContentMode(config)
	if err != nil || mode != ContentModeSource {
		return ds.SyncSchedule, false
	}
	if ds.SyncSchedule == "" {
		return DefaultSourceSyncSchedule, true
	}
	return ds.SyncSchedule, true
}

// recoverQueuedSourceRuns re-enqueues registrations that survived a process
// restart but were not yet delivered to the task queue. The stable task ID
// makes recovery idempotent when the original enqueue actually succeeded.
func (s *Scheduler) recoverQueuedSourceRuns(ctx context.Context, ds *types.DataSource) error {
	if control, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
		dispatches, err := control.RecoverSourceTriggers(ctx, ds)
		if err != nil {
			return err
		}
		for _, dispatch := range dispatches {
			if dispatch.SyncLog == nil {
				continue
			}
			if _, err := EnqueueSourceSync(ctx, s.taskEnqueuer, dispatch, ds.TenantID, ds.ID, types.TaskInitiator{}); err != nil {
				logger.Errorf(ctx, "[Scheduler] failed to redeliver source trigger ds=%s syncLog=%s: %v", ds.ID, dispatch.SyncLog.ID, err)
			}
		}
		return nil
	}
	logs, err := s.syncLogRepo.FindByDataSource(ctx, ds.ID, 1000, 0)
	if err != nil {
		return err
	}
	for _, syncLog := range logs {
		if syncLog == nil || syncLog.Status != types.SyncLogStatusQueued || syncLog.TenantID != ds.TenantID {
			continue
		}
		dispatch := types.SourceSyncDispatch{SyncLog: syncLog, Trigger: "recovery", DeliveryGeneration: 1}
		_, err := EnqueueSourceSync(ctx, s.taskEnqueuer, dispatch, ds.TenantID, ds.ID, types.TaskInitiator{})
		if err != nil && err != asynq.ErrTaskIDConflict {
			logger.Errorf(ctx, "[Scheduler] failed to redeliver source trigger ds=%s syncLog=%s: %v", ds.ID, syncLog.ID, err)
		}
	}
	return nil
}

func (s *Scheduler) reconcileSourceTriggers(ctx context.Context) {
	s.relaySourcePublicationOutbox(ctx)
	if _, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
		// Durable retries are not conditional on cron eligibility or status.
		s.recoverSourceTriggers(ctx, nil)
		return
	}
	dataSources, err := s.dsRepo.FindActive(ctx)
	if err != nil {
		logger.Errorf(ctx, "[Scheduler] failed to list sources for trigger reconciliation: %v", err)
		return
	}
	s.recoverSourceTriggers(ctx, dataSources)
}

func (s *Scheduler) recoverSourceTriggers(ctx context.Context, activeSources []*types.DataSource) {
	if control, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
		dispatches, err := control.RecoverAllSourceTriggers(ctx)
		if err != nil {
			logger.Errorf(ctx, "[Scheduler] failed to recover durable source triggers: %v", err)
			return
		}
		for _, dispatch := range dispatches {
			if dispatch.SyncLog == nil {
				continue
			}
			log := dispatch.SyncLog
			if _, err := EnqueueSourceSync(ctx, s.taskEnqueuer, dispatch, log.TenantID, log.DataSourceID, types.TaskInitiator{}); err != nil {
				logger.Errorf(ctx, "[Scheduler] failed to redeliver source trigger ds=%s syncLog=%s: %v", log.DataSourceID, log.ID, err)
			}
		}
		return
	}
	for _, ds := range activeSources {
		_, sourceMode := scheduledSync(ds)
		if !sourceMode {
			continue
		}
		if err := s.recoverQueuedSourceRuns(ctx, ds); err != nil {
			logger.Errorf(ctx, "[Scheduler] failed to recover source triggers for ds=%s: %v", ds.ID, err)
		}
	}
}

func (s *Scheduler) relaySourcePublicationOutbox(ctx context.Context) {
	if s.sourceSnapshots == nil {
		return
	}
	accepted, err := s.sourceSnapshots.RelaySourcePublicationOutbox(ctx, 100)
	if err != nil {
		logger.Errorf(ctx, "[Scheduler] failed to relay published source Wiki updates: %v", err)
		return
	}
	if accepted > 0 {
		logger.Infof(ctx, "[Scheduler] accepted %d published source Wiki update(s)", accepted)
	}
}

// triggerSync is called by the cron runner on each tick.
//
// Layer 1 — DB: if a previous sync is still running, skip. This prevents
// overlap when a sync takes longer than the cron interval.
//
// Layer 2 — Redis: deterministic asynq.TaskID = "dssync:<dsID>:<minute>".
// Since robfig/cron fires at absolute wall-clock times, all instances trigger
// at the same minute. The first Enqueue wins; others get ErrTaskIDConflict.
func (s *Scheduler) triggerSync(dataSourceID string, tenantID uint64) {
	ctx := context.Background()

	ds, err := s.dsRepo.FindByID(ctx, dataSourceID)
	if err != nil || ds == nil || ds.Status != types.DataSourceStatusActive {
		logger.Infof(ctx, "[Scheduler] skipping sync for ds=%s (not active or not found)", dataSourceID)
		return
	}
	_, sourceMode := scheduledSync(ds)
	if sourceMode {
		if control, ok := s.syncLogRepo.(interfaces.SourceSyncControlRepository); ok {
			syncLog := &types.SyncLog{DataSourceID: dataSourceID, TenantID: tenantID, Status: types.SyncLogStatusQueued, StartedAt: time.Now().UTC()}
			shouldDispatch, generation, err := control.RegisterSourceTrigger(ctx, ds, syncLog, "schedule")
			if err != nil {
				logger.Errorf(ctx, "[Scheduler] failed to register scheduled source trigger for ds=%s: %v", dataSourceID, err)
				return
			}
			if !shouldDispatch {
				return
			}
			_, err = EnqueueSourceSync(ctx, s.taskEnqueuer, types.SourceSyncDispatch{
				SyncLog: syncLog, Trigger: "schedule", DeliveryGeneration: generation,
			}, tenantID, dataSourceID, types.TaskInitiator{})
			if err != nil {
				logger.Errorf(ctx, "[Scheduler] scheduled source trigger remains pending after queue error ds=%s: %v", dataSourceID, err)
			}
			return
		}
	}

	// Layer 1: prevent overlap with a still-running sync
	if running, _ := s.syncLogRepo.HasRunningSync(ctx, dataSourceID); running {
		logger.Infof(ctx, "[Scheduler] skipping sync for ds=%s (previous sync still running)", dataSourceID)
		return
	}

	syncLog := &types.SyncLog{
		DataSourceID: dataSourceID,
		TenantID:     tenantID,
		Status:       types.SyncLogStatusRunning,
		StartedAt:    time.Now().UTC(),
	}
	if err := s.syncLogRepo.Create(ctx, syncLog); err != nil {
		logger.Errorf(ctx, "[Scheduler] failed to create sync log for ds=%s: %v", dataSourceID, err)
		return
	}

	payload := &types.DataSourceSyncPayload{
		DataSourceID: dataSourceID,
		TenantID:     tenantID,
		SyncLogID:    syncLog.ID,
		ForceFull:    false,
		Trigger:      "schedule",
	}
	langfuse.InjectTracing(ctx, payload)
	payloadJSON, _ := json.Marshal(payload)
	task := asynq.NewTask(types.TypeDataSourceSync, payloadJSON)

	// Layer 2: deterministic TaskID — all instances in the same minute produce the same ID
	taskID := fmt.Sprintf("dssync:%s:%s", dataSourceID, time.Now().UTC().Truncate(time.Minute).Format("200601021504"))

	_, err = s.taskEnqueuer.Enqueue(task,
		asynq.Queue(types.QueueSync),
		asynq.MaxRetry(5),
		asynq.Timeout(2*time.Hour),
		asynq.TaskID(taskID),
	)
	if err != nil {
		if err == asynq.ErrTaskIDConflict {
			logger.Infof(ctx, "[Scheduler] sync already enqueued by another instance for ds=%s", dataSourceID)
			syncLog.Status = types.SyncLogStatusCanceled
			now := time.Now().UTC()
			syncLog.FinishedAt = &now
			syncLog.ErrorMessage = "deduplicated: another instance enqueued first"
			_ = s.syncLogRepo.Update(ctx, syncLog)
			return
		}
		logger.Errorf(ctx, "[Scheduler] failed to enqueue sync task for ds=%s: %v", dataSourceID, err)
		syncLog.Status = types.SyncLogStatusFailed
		now := time.Now().UTC()
		syncLog.FinishedAt = &now
		syncLog.ErrorMessage = fmt.Sprintf("enqueue failed: %v", err)
		_ = s.syncLogRepo.Update(ctx, syncLog)
		return
	}

	logger.Infof(ctx, "[Scheduler] sync task enqueued for ds=%s syncLog=%s", dataSourceID, syncLog.ID)
}

// EntryCount returns the number of active cron entries (for testing/monitoring).
func (s *Scheduler) EntryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
