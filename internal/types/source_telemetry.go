package types

import (
	"fmt"
	"math"
	"strings"
	"time"
)

const SourceRunTelemetrySchemaVersion = 1

var sourceRunPhaseKeys = map[string]struct{}{
	"fetching": {}, "parsing": {}, "indexing": {}, "publishing": {},
}

var sourceRunQualityKeys = map[string]struct{}{
	"structural": {}, "partial": {}, "syntax_error": {}, "degraded": {},
	"unknown_preprocess": {}, "text_fallback": {},
}

var sourceStorageMetricKeys = map[string]struct{}{
	"cache": {}, "staging": {}, "original": {}, "vectors": {},
}

const (
	SourceStorageMeasurementLogicalPayload = "logical_payload"
	SourceStorageMeasurementPhysical       = "physical"
)

// SourceRunTelemetry is additive run telemetry persisted in the sync result.
// Missing fields mean unmeasured; only observed zeroes are encoded as zero.
type SourceRunTelemetry struct {
	SchemaVersion       int                            `json:"schema_version"`
	PhaseDurationMS     map[string]int64               `json:"phase_duration_ms,omitempty"`
	SelectedBytes       *int64                         `json:"selected_bytes,omitempty"`
	GitTransferBytes    *int64                         `json:"git_transfer_bytes,omitempty"`
	Storage             map[string]SourceStorageMetric `json:"storage,omitempty"`
	QualityCounts       map[string]int64               `json:"quality_counts,omitempty"`
	ModelUsage          *SourceModelUsageTelemetry     `json:"model_usage,omitempty"`
	WikiCoverage        *SourceWikiCoverageTelemetry   `json:"wiki_coverage,omitempty"`
	PublishedCommitSHA  string                         `json:"published_commit_sha,omitempty"`
	LeaseRecoveries     *int64                         `json:"lease_recoveries,omitempty"`
	CleanupResidueCount *int64                         `json:"cleanup_residue_count,omitempty"`
}

type SourceStorageMetric struct {
	UsedBytes   *int64 `json:"used_bytes,omitempty"`
	LimitBytes  *int64 `json:"limit_bytes,omitempty"`
	Measurement string `json:"measurement"`
}

// SourceModelUsageTelemetry records observed source-indexing calls and safe
// local token estimates. Provider-reported token totals are omitted unless
// they are actually returned by the provider.
type SourceModelUsageTelemetry struct {
	// EmbeddingCalls counts observed outbound HTTP RoundTrip attempts, including
	// provider retries. It is omitted for paths without an observer.
	EmbeddingCalls       *int64 `json:"embedding_calls,omitempty"`
	EstimatedInputTokens *int64 `json:"estimated_input_tokens,omitempty"`
}

// SourceWikiCoverageTelemetry summarizes one complete, source- and
// snapshot-scoped topic inventory. Pointer counters distinguish measured zero
// from unavailable coverage.
type SourceWikiCoverageTelemetry struct {
	EligibleTopics    *int64 `json:"eligible_topics,omitempty"`
	ReadyTopics       *int64 `json:"ready_topics,omitempty"`
	StaleTopics       *int64 `json:"stale_topics,omitempty"`
	FailedTopics      *int64 `json:"failed_topics,omitempty"`
	UngeneratedTopics *int64 `json:"ungenerated_topics,omitempty"`
	DeferredTopics    *int64 `json:"deferred_topics,omitempty"`
}

func NewSourceRunTelemetry() *SourceRunTelemetry {
	return &SourceRunTelemetry{SchemaVersion: SourceRunTelemetrySchemaVersion}
}

func (t *SourceRunTelemetry) AddPhaseDuration(phase string, duration time.Duration) error {
	if t == nil {
		return fmt.Errorf("source telemetry is unavailable")
	}
	if _, ok := sourceRunPhaseKeys[phase]; !ok || duration < 0 {
		return fmt.Errorf("source telemetry phase is invalid")
	}
	if t.PhaseDurationMS == nil {
		t.PhaseDurationMS = make(map[string]int64, len(sourceRunPhaseKeys))
	}
	ms := duration.Milliseconds()
	if ms > math.MaxInt64-t.PhaseDurationMS[phase] {
		return fmt.Errorf("source telemetry phase duration overflow")
	}
	t.PhaseDurationMS[phase] += ms
	return nil
}

func (t *SourceRunTelemetry) CountQuality(quality string) {
	if t == nil {
		return
	}
	if _, ok := sourceRunQualityKeys[quality]; !ok {
		return
	}
	if t.QualityCounts == nil {
		t.QualityCounts = make(map[string]int64, len(sourceRunQualityKeys))
	}
	t.QualityCounts[quality]++
}

func (t *SourceRunTelemetry) Validate() error {
	if t == nil || t.SchemaVersion != SourceRunTelemetrySchemaVersion {
		return fmt.Errorf("source telemetry schema version is invalid")
	}
	if len(t.PhaseDurationMS) > len(sourceRunPhaseKeys) || len(t.QualityCounts) > len(sourceRunQualityKeys) {
		return fmt.Errorf("source telemetry has too many counters")
	}
	for key, value := range t.PhaseDurationMS {
		if _, ok := sourceRunPhaseKeys[key]; !ok || value < 0 {
			return fmt.Errorf("source telemetry phase counter is invalid")
		}
	}
	for key, value := range t.QualityCounts {
		if _, ok := sourceRunQualityKeys[key]; !ok || value < 0 {
			return fmt.Errorf("source telemetry quality counter is invalid")
		}
	}
	if t.SelectedBytes != nil && *t.SelectedBytes < 0 {
		return fmt.Errorf("source telemetry selected byte count is invalid")
	}
	if t.GitTransferBytes != nil && *t.GitTransferBytes < 0 {
		return fmt.Errorf("source telemetry Git transfer byte count is invalid")
	}
	if len(t.Storage) > len(sourceStorageMetricKeys) {
		return fmt.Errorf("source telemetry has too many storage metrics")
	}
	for _, metric := range t.Storage {
		if metric.UsedBytes == nil || *metric.UsedBytes < 0 {
			return fmt.Errorf("source telemetry storage usage is invalid")
		}
		if metric.LimitBytes == nil || *metric.LimitBytes <= 0 {
			return fmt.Errorf("source telemetry storage limit is invalid")
		}
		if metric.Measurement != SourceStorageMeasurementLogicalPayload && metric.Measurement != SourceStorageMeasurementPhysical {
			return fmt.Errorf("source telemetry storage measurement is invalid")
		}
	}
	for key := range t.Storage {
		if _, ok := sourceStorageMetricKeys[key]; !ok {
			return fmt.Errorf("source telemetry storage key is invalid")
		}
	}
	if t.LeaseRecoveries != nil && *t.LeaseRecoveries < 0 || t.CleanupResidueCount != nil && *t.CleanupResidueCount < 0 {
		return fmt.Errorf("source telemetry operational counter is invalid")
	}
	if t.ModelUsage != nil {
		if t.ModelUsage.EmbeddingCalls != nil && *t.ModelUsage.EmbeddingCalls < 0 ||
			t.ModelUsage.EstimatedInputTokens != nil && *t.ModelUsage.EstimatedInputTokens < 0 {
			return fmt.Errorf("source telemetry model usage counter is invalid")
		}
	}
	if t.WikiCoverage != nil {
		coverageCounts := []*int64{t.WikiCoverage.EligibleTopics, t.WikiCoverage.ReadyTopics, t.WikiCoverage.StaleTopics,
			t.WikiCoverage.FailedTopics, t.WikiCoverage.UngeneratedTopics, t.WikiCoverage.DeferredTopics}
		allPresent := true
		anyPresent := false
		for _, value := range coverageCounts {
			allPresent = allPresent && value != nil
			anyPresent = anyPresent || value != nil
			if value != nil && *value < 0 {
				return fmt.Errorf("source telemetry Wiki coverage counter is invalid")
			}
		}
		if anyPresent && !allPresent {
			return fmt.Errorf("source telemetry Wiki coverage summary is incomplete")
		}
		if allPresent {
			total := int64(0)
			for _, value := range coverageCounts[1:] {
				if *value > math.MaxInt64-total {
					return fmt.Errorf("source telemetry Wiki coverage total overflows")
				}
				total += *value
			}
			if total != *t.WikiCoverage.EligibleTopics {
				return fmt.Errorf("source telemetry Wiki coverage total does not match eligible topics")
			}
		}
	}
	if len(t.PublishedCommitSHA) > 128 || strings.ContainsAny(t.PublishedCommitSHA, "\r\n") {
		return fmt.Errorf("source telemetry published commit identifier is invalid")
	}
	return nil
}
