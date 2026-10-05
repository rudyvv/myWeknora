package types

import (
	"encoding/json"
	"testing"
	"time"
)

func TestSourceWikiCoverageTelemetryUsesContractJSONNames(t *testing.T) {
	eligible, ready, stale, failed, ungenerated, deferred := int64(6), int64(1), int64(1), int64(1), int64(2), int64(1)
	coverage := SourceWikiCoverageTelemetry{
		EligibleTopics: &eligible, ReadyTopics: &ready, StaleTopics: &stale,
		FailedTopics: &failed, UngeneratedTopics: &ungenerated, DeferredTopics: &deferred,
	}
	encoded, err := json.Marshal(coverage)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"eligible":6,"ready":1,"stale":1,"failed":1,"ungenerated":2,"deferred":1}`
	if string(encoded) != want {
		t.Fatalf("Wiki coverage JSON = %s, want %s", encoded, want)
	}
}

func TestSourceRunTelemetryAllowlistedAndAccumulatesDurations(t *testing.T) {
	telemetry := NewSourceRunTelemetry()
	if err := telemetry.AddPhaseDuration("fetching", 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := telemetry.AddPhaseDuration("fetching", 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got := telemetry.PhaseDurationMS["fetching"]; got != 2000 {
		t.Fatalf("fetching duration = %d ms, want 2000", got)
	}
	if err := telemetry.AddPhaseDuration("wiki_generation", time.Second); err == nil {
		t.Fatal("telemetry accepted a non-allowlisted phase")
	}
	telemetry.CountQuality("structural")
	telemetry.CountQuality("unknown_quality")
	if len(telemetry.QualityCounts) != 1 || telemetry.QualityCounts["structural"] != 1 {
		t.Fatalf("quality counts include an unknown value: %#v", telemetry.QualityCounts)
	}
	if err := telemetry.Validate(); err != nil {
		t.Fatalf("valid telemetry was rejected: %v", err)
	}
}

func TestSourceRunTelemetryRejectsUnboundedOrNegativeValues(t *testing.T) {
	telemetry := NewSourceRunTelemetry()
	telemetry.PhaseDurationMS = map[string]int64{"fetching": -1}
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted a negative phase duration")
	}
	telemetry = NewSourceRunTelemetry()
	telemetry.QualityCounts = map[string]int64{"unexpected": 1}
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted an unknown quality key")
	}
	used, limit := int64(42), int64(100)
	telemetry = NewSourceRunTelemetry()
	telemetry.Storage = map[string]SourceStorageMetric{
		"staging": {UsedBytes: &used, LimitBytes: &limit, Measurement: SourceStorageMeasurementLogicalPayload},
	}
	if err := telemetry.Validate(); err != nil {
		t.Fatalf("telemetry rejected a valid storage metric: %v", err)
	}
	telemetry.Storage["selected_blob_stage"] = telemetry.Storage["staging"]
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted a non-contract storage key")
	}
	delete(telemetry.Storage, "selected_blob_stage")
	telemetry.Storage["staging"] = SourceStorageMetric{UsedBytes: &used, LimitBytes: &limit, Measurement: "physical temp blocks"}
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted a non-enum storage measurement")
	}
	negative := int64(-1)
	telemetry = NewSourceRunTelemetry()
	telemetry.ModelUsage = &SourceModelUsageTelemetry{EmbeddingCalls: &negative}
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted a negative model usage counter")
	}
	telemetry = NewSourceRunTelemetry()
	eligible, ready, stale, failed, ungenerated, deferred := int64(3), int64(1), int64(0), int64(1), int64(1), int64(0)
	telemetry.WikiCoverage = &SourceWikiCoverageTelemetry{
		EligibleTopics: &eligible, ReadyTopics: &ready, StaleTopics: &stale, FailedTopics: &failed,
		UngeneratedTopics: &ungenerated, DeferredTopics: &deferred,
	}
	if err := telemetry.Validate(); err != nil {
		t.Fatalf("telemetry rejected a complete Wiki coverage summary: %v", err)
	}
	telemetry.WikiCoverage.ReadyTopics = nil
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted a partial Wiki coverage summary")
	}
	telemetry = NewSourceRunTelemetry()
	telemetry.WikiCoverage = &SourceWikiCoverageTelemetry{ReadyTopics: &negative}
	if err := telemetry.Validate(); err == nil {
		t.Fatal("telemetry accepted a negative Wiki coverage counter")
	}
}
