package source

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/Tencent/WeKnora/internal/datasource"
	"github.com/shirou/gopsutil/v3/disk"
)

var (
	ErrResourceLimitExceeded = errors.New("source resource budget exceeded")
	ErrStorageBudgetExceeded = errors.New("source storage budget exhausted")
	ErrSourceQuotaExceeded   = errors.New("source logical storage quota exceeded")
)

// ResourcePolicy is the cohesive finite budget for one GitLab source process.
// Original, parsed-cache and vector limits are per source and measure logical
// payload, not PostgreSQL relation or filesystem allocation.
type ResourcePolicy struct {
	MaxSelectedFiles          int
	MaxSelectedBytes          int64
	MaxFileBytes              int64
	MaxConcurrentRuns         int
	RunTimeout                time.Duration
	GitTransferBytes          int64
	GitObjectStageBytes       int64
	SelectedBlobStageBytes    int64
	MinFreeBytes              int64
	MinFreePercent            int
	OriginalBytesPerSource    int64
	ParsedCacheBytesPerSource int64
	VectorBytesPerSource      int64
}

func DefaultResourcePolicy() ResourcePolicy {
	return ResourcePolicy{
		MaxSelectedFiles:          10000,
		MaxSelectedBytes:          512 << 20,
		MaxFileBytes:              16 << 20,
		MaxConcurrentRuns:         1,
		RunTimeout:                30 * time.Minute,
		GitTransferBytes:          1 << 30,
		GitObjectStageBytes:       1 << 30,
		SelectedBlobStageBytes:    512 << 20,
		MinFreeBytes:              256 << 20,
		MinFreePercent:            20,
		OriginalBytesPerSource:    2 << 30,
		ParsedCacheBytesPerSource: 512 << 20,
		VectorBytesPerSource:      4 << 30,
	}
}

func (p ResourcePolicy) Validate() error {
	if p.MaxSelectedFiles <= 0 || p.MaxSelectedBytes <= 0 || p.MaxFileBytes <= 0 ||
		p.MaxConcurrentRuns <= 0 || p.RunTimeout <= 0 || p.GitTransferBytes <= 0 ||
		p.GitObjectStageBytes <= 0 || p.SelectedBlobStageBytes <= 0 || p.MinFreeBytes <= 0 ||
		p.OriginalBytesPerSource <= 0 || p.ParsedCacheBytesPerSource <= 0 || p.VectorBytesPerSource <= 0 {
		return fmt.Errorf("source resource budgets must all be finite positive values")
	}
	if p.MinFreePercent < 1 || p.MinFreePercent > 80 {
		return fmt.Errorf("source minimum free-space percentage must be between 1 and 80")
	}
	if p.MaxSelectedFiles > 100000 || p.MaxFileBytes > 64<<20 || p.MaxSelectedBytes > 8<<30 ||
		p.MaxConcurrentRuns > 32 || p.RunTimeout > 24*time.Hour ||
		p.GitTransferBytes > 8<<30 || p.GitObjectStageBytes > 8<<30 || p.SelectedBlobStageBytes > 8<<30 ||
		p.SelectedBlobStageBytes < p.MaxSelectedBytes || p.MinFreeBytes > 1<<50 ||
		p.OriginalBytesPerSource > 1<<50 || p.ParsedCacheBytesPerSource > 1<<50 || p.VectorBytesPerSource > 1<<50 {
		return fmt.Errorf("source resource budgets exceed the hard safety ceiling or are inconsistent")
	}
	return nil
}

// ApplyFileLimit returns a copy of source rules capped by the process policy.
// A connector-specific lower max_file_bytes remains authoritative.
func (p ResourcePolicy) ApplyFileLimit(rules *datasource.SourceSettings) *datasource.SourceSettings {
	if rules == nil {
		return nil
	}
	capped := *rules
	if capped.MaxFileBytes > p.MaxFileBytes {
		capped.MaxFileBytes = p.MaxFileBytes
	}
	return &capped
}

// CheckTemporaryStorageCapacity fails closed unless the OS temp volume can
// hold both private source stages and the configured free-space reserve.
func CheckTemporaryStorageCapacity(policy ResourcePolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	usage, err := disk.Usage(os.TempDir())
	if err != nil || usage == nil || usage.Total == 0 {
		return ErrStorageBudgetExceeded
	}
	stages := policy.GitObjectStageBytes
	if policy.SelectedBlobStageBytes > math.MaxInt64-stages {
		return ErrStorageBudgetExceeded
	}
	stages += policy.SelectedBlobStageBytes
	reserve, err := minimumFreeReserve(policy, usage.Total)
	if err != nil || reserve > math.MaxInt64-stages {
		return ErrStorageBudgetExceeded
	}
	if uint64(stages+reserve) > usage.Free {
		return ErrStorageBudgetExceeded
	}
	return nil
}

// CheckFreeSpaceReserve rechecks the configured reserve while the temp stages
// grow. It reports OS-temp volume capacity only, never database free space.
func CheckFreeSpaceReserve(policy ResourcePolicy) error {
	usage, err := disk.Usage(os.TempDir())
	if err != nil || usage == nil || usage.Total == 0 {
		return ErrStorageBudgetExceeded
	}
	reserve, err := minimumFreeReserve(policy, usage.Total)
	if err != nil || usage.Free < uint64(reserve) {
		return ErrStorageBudgetExceeded
	}
	return nil
}

func minimumFreeReserve(policy ResourcePolicy, total uint64) (int64, error) {
	if policy.MinFreePercent < 1 || policy.MinFreePercent > 80 || policy.MinFreeBytes <= 0 {
		return 0, ErrStorageBudgetExceeded
	}
	percentReserve := total / 100 * uint64(policy.MinFreePercent)
	if percentReserve > math.MaxInt64 {
		return 0, ErrStorageBudgetExceeded
	}
	reserve := policy.MinFreeBytes
	if int64(percentReserve) > reserve {
		reserve = int64(percentReserve)
	}
	return reserve, nil
}

// RunAdmission provides an in-process bound on concurrently active source
// runs. It intentionally does not claim distributed coordination; the durable
// source lease remains the cross-process fence.
type RunAdmission struct {
	slots chan struct{}
}

type ResourceController struct {
	policy    ResourcePolicy
	admission *RunAdmission
}

func NewResourceController(policy ResourcePolicy) (*ResourceController, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	admission, err := NewRunAdmission(policy.MaxConcurrentRuns)
	if err != nil {
		return nil, err
	}
	return &ResourceController{policy: policy, admission: admission}, nil
}

func (c *ResourceController) Policy() ResourcePolicy {
	if c == nil {
		return DefaultResourcePolicy()
	}
	return c.policy
}

func (c *ResourceController) AcquireRun(ctx context.Context) (func(), error) {
	if c == nil || c.admission == nil {
		return nil, fmt.Errorf("source run admission is unavailable")
	}
	release, err := c.admission.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

func NewRunAdmission(limit int) (*RunAdmission, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("source run admission limit must be finite and positive")
	}
	return &RunAdmission{slots: make(chan struct{}, limit)}, nil
}

func (a *RunAdmission) Acquire(ctx context.Context) (func(), error) {
	if a == nil || a.slots == nil {
		return nil, fmt.Errorf("source run admission is unavailable")
	}
	select {
	case a.slots <- struct{}{}:
		var once sync.Once
		return func() { once.Do(func() { <-a.slots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
