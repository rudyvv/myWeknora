package source

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDefaultResourcePolicyUsesFiniteSourceBudgets(t *testing.T) {
	policy := DefaultResourcePolicy()
	if policy.MaxSelectedFiles != 10000 {
		t.Fatalf("MaxSelectedFiles = %d, want 10000", policy.MaxSelectedFiles)
	}
	if policy.MaxSelectedBytes != 512<<20 {
		t.Fatalf("MaxSelectedBytes = %d, want 512 MiB", policy.MaxSelectedBytes)
	}
	if policy.MaxFileBytes != 16<<20 {
		t.Fatalf("MaxFileBytes = %d, want 16 MiB", policy.MaxFileBytes)
	}
	if policy.MaxConcurrentRuns != 1 {
		t.Fatalf("MaxConcurrentRuns = %d, want 1", policy.MaxConcurrentRuns)
	}
	if policy.RunTimeout != 30*time.Minute {
		t.Fatalf("RunTimeout = %s, want 30m", policy.RunTimeout)
	}
	if policy.GitTransferBytes != 1<<30 || policy.GitObjectStageBytes != 1<<30 {
		t.Fatalf("Git budgets = transfer %d / stage %d, want 1 GiB each", policy.GitTransferBytes, policy.GitObjectStageBytes)
	}
	if policy.SelectedBlobStageBytes != 512<<20 {
		t.Fatalf("SelectedBlobStageBytes = %d, want 512 MiB", policy.SelectedBlobStageBytes)
	}
	if policy.MinFreeBytes != 256<<20 || policy.MinFreePercent != 20 {
		t.Fatalf("free-space reserve = %d bytes / %d%%, want 256 MiB / 20%%", policy.MinFreeBytes, policy.MinFreePercent)
	}
	if policy.OriginalBytesPerSource != 2<<30 || policy.ParsedCacheBytesPerSource != 512<<20 || policy.VectorBytesPerSource != 4<<30 {
		t.Fatalf("logical quotas = original %d, parsed cache %d, vectors %d", policy.OriginalBytesPerSource, policy.ParsedCacheBytesPerSource, policy.VectorBytesPerSource)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("default source resource policy is invalid: %v", err)
	}
}

func TestResourceControllerCanceledWaitDoesNotLeakAdmission(t *testing.T) {
	controller, err := NewResourceController(DefaultResourcePolicy())
	if err != nil {
		t.Fatal(err)
	}
	release, err := controller.AcquireRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := controller.AcquireRun(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled admission wait error = %v, want deadline exceeded", err)
	}
	release()
	next, err := controller.AcquireRun(context.Background())
	if err != nil {
		t.Fatalf("released admission slot could not be reacquired: %v", err)
	}
	next()
}

func TestResourcePolicyRejectsUnboundedOrInvalidBudgets(t *testing.T) {
	base := DefaultResourcePolicy()
	for name, mutate := range map[string]func(*ResourcePolicy){
		"selected files":                    func(p *ResourcePolicy) { p.MaxSelectedFiles = 0 },
		"selected bytes":                    func(p *ResourcePolicy) { p.MaxSelectedBytes = -1 },
		"per-file bytes":                    func(p *ResourcePolicy) { p.MaxFileBytes = 0 },
		"concurrency":                       func(p *ResourcePolicy) { p.MaxConcurrentRuns = 0 },
		"deadline":                          func(p *ResourcePolicy) { p.RunTimeout = 0 },
		"git transfer":                      func(p *ResourcePolicy) { p.GitTransferBytes = 0 },
		"git stage":                         func(p *ResourcePolicy) { p.GitObjectStageBytes = -1 },
		"blob stage":                        func(p *ResourcePolicy) { p.SelectedBlobStageBytes = 0 },
		"free reserve percent":              func(p *ResourcePolicy) { p.MinFreePercent = 100 },
		"original quota":                    func(p *ResourcePolicy) { p.OriginalBytesPerSource = 0 },
		"parsed quota":                      func(p *ResourcePolicy) { p.ParsedCacheBytesPerSource = -1 },
		"vector quota":                      func(p *ResourcePolicy) { p.VectorBytesPerSource = 0 },
		"unbounded file size":               func(p *ResourcePolicy) { p.MaxFileBytes = 65 << 20 },
		"blob stage below selection budget": func(p *ResourcePolicy) { p.SelectedBlobStageBytes = p.MaxSelectedBytes - 1 },
	} {
		t.Run(name, func(t *testing.T) {
			policy := base
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("Validate() accepted a non-finite or invalid source resource budget")
			}
		})
	}
}
