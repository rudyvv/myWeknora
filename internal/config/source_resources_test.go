package config

import (
	"testing"
	"time"
)

func TestSourceResourceDefaultsAreFiniteAndCannotBeDisabledWithZero(t *testing.T) {
	got := EffectiveSourceResources(nil)
	if got.MaxSelectedFiles != 10000 || got.MaxSelectedBytes != 512<<20 || got.MaxFileBytes != 16<<20 {
		t.Fatalf("selected-source defaults = files %d, bytes %d, file %d", got.MaxSelectedFiles, got.MaxSelectedBytes, got.MaxFileBytes)
	}
	if got.MaxConcurrentRuns != 1 || got.RunTimeout != 30*time.Minute {
		t.Fatalf("source scheduling defaults = runs %d, timeout %s", got.MaxConcurrentRuns, got.RunTimeout)
	}
	if got.GitTransferBytes != 1<<30 || got.GitObjectStageBytes != 1<<30 || got.SelectedBlobStageBytes != 512<<20 {
		t.Fatalf("source staging defaults = transfer %d, git stage %d, blob stage %d", got.GitTransferBytes, got.GitObjectStageBytes, got.SelectedBlobStageBytes)
	}
	if got.MinFreeBytes != 256<<20 || got.MinFreePercent != 20 || got.OriginalBytesPerSource != 2<<30 ||
		got.ParsedCacheBytesPerSource != 512<<20 || got.VectorBytesPerSource != 4<<30 {
		t.Fatalf("source storage defaults = %+v", got)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("default source resource configuration is invalid: %v", err)
	}

	partial := &SourceResourceConfig{MaxSelectedFiles: 25, MaxSelectedBytes: 64 << 20}
	partial.ApplyDefaults()
	if partial.MaxSelectedFiles != 25 || partial.MaxSelectedBytes != 64<<20 || partial.MaxConcurrentRuns != 1 || partial.VectorBytesPerSource != 4<<30 {
		t.Fatalf("partial source resource configuration did not preserve overrides and finite defaults: %+v", partial)
	}
}

func TestSourceResourceConfigRejectsInvalidBudgets(t *testing.T) {
	settings := EffectiveSourceResources(nil)
	settings.GitTransferBytes = -1
	if err := settings.Validate(); err == nil {
		t.Fatal("negative Git transfer budget was accepted")
	}
	settings = EffectiveSourceResources(nil)
	settings.MinFreePercent = 100
	if err := settings.Validate(); err == nil {
		t.Fatal("unsafe minimum-free percentage was accepted")
	}
}
