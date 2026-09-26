package service

import (
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	weDriveScanRetryDelay = 10 * time.Minute
	weDriveScanLease      = 30 * time.Minute
)

// weDriveScanLifecycle owns scan state transitions, without persistence or
// source approval. Callers authorize and lock the source before applying a
// transition, then persist it in their existing transaction.
type weDriveScanLifecycle struct {
	source *types.WeDriveSource
}

func (scan weDriveScanLifecycle) initialize(ready bool, now time.Time) {
	scan.source.NextScanAt, scan.source.ScanState = nil, types.WeDriveScanStateIdle
	if ready && scan.source.ScanIntervalMinutes > 0 {
		scan.source.NextScanAt, scan.source.ScanState = &now, types.WeDriveScanStateWaiting
	}
}

func (scan weDriveScanLifecycle) changeCadence(interval int, ready bool, now time.Time) {
	source := scan.source
	source.ScanIntervalMinutes = interval
	if source.ScanState == types.WeDriveScanStateRunning {
		return
	}
	source.ScanRetryCount, source.ScanLeaseExpiresAt, source.LastScanErrorCode = 0, nil, ""
	if !ready {
		return
	}
	scan.initialize(true, now)
	if interval > 0 && source.LastCompleteScanAt != nil {
		next := source.LastCompleteScanAt.Add(time.Duration(interval) * time.Minute)
		if next.Before(now) {
			next = now
		}
		source.NextScanAt = &next
	}
}

func (scan weDriveScanLifecycle) hasActiveLease(now time.Time) bool {
	source := scan.source
	return source.ScanState == types.WeDriveScanStateRunning && source.ScanLeaseExpiresAt != nil && source.ScanLeaseExpiresAt.After(now)
}

func (scan weDriveScanLifecycle) claim(trigger string, now time.Time) error {
	source := scan.source
	if trigger == "scheduled" && (source.ScanIntervalMinutes == 0 || source.NextScanAt == nil || source.NextScanAt.After(now)) {
		return ErrWeDriveScanNotDue
	}
	lease := now.Add(weDriveScanLease)
	source.ScanState, source.ScanLeaseExpiresAt, source.LastScanStartedAt, source.LastScanErrorCode = types.WeDriveScanStateRunning, &lease, &now, ""
	if trigger == "manual" {
		source.ScanRetryCount = 0
	}
	return nil
}

func (scan weDriveScanLifecycle) fail(code string, now time.Time) error {
	source := scan.source
	if source.ScanState != types.WeDriveScanStateRunning {
		return ErrWeDriveInvalidState
	}
	source.ScanLeaseExpiresAt, source.LastScanErrorCode = nil, code
	if source.ScanIntervalMinutes > 0 && source.ScanRetryCount == 0 {
		next := now.Add(weDriveScanRetryDelay)
		source.NextScanAt, source.ScanRetryCount, source.ScanState = &next, 1, types.WeDriveScanStateRetryWait
	} else if source.ScanIntervalMinutes > 0 {
		next := now.Add(time.Duration(source.ScanIntervalMinutes) * time.Minute)
		source.NextScanAt, source.ScanRetryCount, source.ScanState = &next, 0, types.WeDriveScanStateFailed
	} else {
		source.NextScanAt, source.ScanRetryCount, source.ScanState = nil, 0, types.WeDriveScanStateFailed
	}
	return nil
}

func (scan weDriveScanLifecycle) complete(now time.Time) {
	source := scan.source
	source.LastCompleteScanAt = &now
	source.ScanRetryCount, source.ScanLeaseExpiresAt, source.LastScanErrorCode = 0, nil, ""
	scan.initialize(true, now)
	if source.ScanIntervalMinutes > 0 {
		next := now.Add(time.Duration(source.ScanIntervalMinutes) * time.Minute)
		source.NextScanAt = &next
	}
}
