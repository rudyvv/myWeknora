package service

import (
	"context"
	"encoding/json"

	"github.com/Tencent/WeKnora/internal/types"
)

// enrichSourceWikiCoverage refreshes only the summary tied to the immutable
// snapshot recorded for each sync log. The summary reader enforces the caller's
// source-read scope and rejects incomplete inventories.
func (s *DataSourceService) enrichSourceWikiCoverage(ctx context.Context, logs []*types.SyncLog) error {
	if s == nil || s.sourceSnapshots == nil || s.sourceWikiCoverage == nil {
		return nil
	}
	for _, log := range logs {
		if log == nil || log.ID == "" || log.DataSourceID == "" || log.TenantID == 0 {
			continue
		}
		result, err := log.ParseResult()
		if err != nil || result == nil || result.Source == nil || result.Source.Snapshot == nil {
			continue
		}
		snapshotRef := result.Source.Snapshot
		if snapshotRef.ID == "" || snapshotRef.SyncLogID == "" {
			continue
		}
		// No-op syncs can reuse a published snapshot owned by an earlier run.
		// Follow that exact durable reference; never substitute the latest source
		// snapshot for the one recorded in this log's result.
		run, err := s.sourceSnapshots.GetRun(ctx, log.TenantID, log.DataSourceID, snapshotRef.SyncLogID)
		if err != nil {
			return err
		}
		if run == nil || run.Snapshot == nil {
			continue
		}
		snapshot := run.Snapshot
		if snapshot.State != "published" || snapshot.ID == "" || snapshot.ID != snapshotRef.ID ||
			snapshot.SyncLogID != snapshotRef.SyncLogID || snapshot.DataSourceID != log.DataSourceID || snapshot.TenantID != log.TenantID ||
			snapshot.KnowledgeBaseID == "" || snapshotRef.KnowledgeBaseID != snapshot.KnowledgeBaseID ||
			snapshotRef.DataSourceID != snapshot.DataSourceID || snapshotRef.TenantID != snapshot.TenantID {
			continue
		}
		coverage, err := s.sourceWikiCoverage.GetSourceWikiCoverageSummary(ctx, snapshot.KnowledgeBaseID, snapshot.DataSourceID, snapshot.ID)
		if err != nil {
			return err
		}
		if result.Source.Telemetry == nil {
			result.Source.Telemetry = types.NewSourceRunTelemetry()
		}
		if coverage == nil && result.Source.Telemetry.WikiCoverage == nil {
			continue
		}
		// A previously cached count is not proof of current coverage: the summary
		// remains unknown until the exact snapshot has a complete inventory.
		result.Source.Telemetry.WikiCoverage = coverage
		encoded, err := json.Marshal(result)
		if err != nil {
			return err
		}
		log.Result = types.JSON(encoded)
	}
	return nil
}
