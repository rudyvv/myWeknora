//go:build windows

package main

import (
	"testing"
	"time"
)

func TestDeadRunRecoverableAcceptsTheKnownIncidentShape(t *testing.T) {
	leaseExpiry := time.Date(2026, 10, 7, 9, 49, 56, 0, time.UTC)
	finished := time.Date(2026, 10, 7, 9, 44, 44, 0, time.UTC)
	now := leaseExpiry.Add(time.Hour)
	snapshot := coordinatorSnapshot{
		SourceStatus:     "error",
		ActiveSyncLogID:  "57f39a7c-c445-4dd2-bd76-31bcaf6923f4",
		PendingSyncLogID: "",
		LeaseOwner:       "t22-runtime",
		LeaseExpiresAt:   &leaseExpiry,
		LogStatus:        "running",
		LogFinishedAt:    &finished,
		RunPhase:         "fetching",
		RunRetryCount:    5,
	}
	if !deadRunRecoverable(snapshot, now) {
		t.Fatal("the known dead-run incident shape must be recoverable")
	}
}

func TestDeadRunRecoverableRejectsDeviations(t *testing.T) {
	leaseExpiry := time.Date(2026, 10, 7, 9, 49, 56, 0, time.UTC)
	finished := time.Date(2026, 10, 7, 9, 44, 44, 0, time.UTC)
	now := leaseExpiry.Add(time.Hour)
	base := func() coordinatorSnapshot {
		return coordinatorSnapshot{
			SourceStatus:     "error",
			ActiveSyncLogID:  "57f39a7c-c445-4dd2-bd76-31bcaf6923f4",
			PendingSyncLogID: "",
			LeaseOwner:       "t22-runtime",
			LeaseExpiresAt:   &leaseExpiry,
			LogStatus:        "running",
			LogFinishedAt:    &finished,
			RunPhase:         "fetching",
			RunRetryCount:    5,
		}
	}
	cases := map[string]func(*coordinatorSnapshot){
		"no active run":            func(s *coordinatorSnapshot) { s.ActiveSyncLogID = "" },
		"pending trigger present":  func(s *coordinatorSnapshot) { s.PendingSyncLogID = "pending-id" },
		"lease owner empty":        func(s *coordinatorSnapshot) { s.LeaseOwner = "" },
		"lease missing":            func(s *coordinatorSnapshot) { s.LeaseExpiresAt = nil },
		"lease still live":         func(s *coordinatorSnapshot) { future := now.Add(time.Hour); s.LeaseExpiresAt = &future },
		"source paused":            func(s *coordinatorSnapshot) { s.SourceStatus = "paused" },
		"source active":            func(s *coordinatorSnapshot) { s.SourceStatus = "active" },
		"log queued":               func(s *coordinatorSnapshot) { s.LogStatus = "queued" },
		"log success":              func(s *coordinatorSnapshot) { s.LogStatus = "success" },
		"log canceled":             func(s *coordinatorSnapshot) { s.LogStatus = "canceled" },
		"worker still in flight":   func(s *coordinatorSnapshot) { s.LogFinishedAt = nil },
		"retry budget not exhaust": func(s *coordinatorSnapshot) { s.RunRetryCount = 4 },
	}
	for name, mutate := range cases {
		snapshot := base()
		mutate(&snapshot)
		if deadRunRecoverable(snapshot, now) {
			t.Fatalf("%s: deviation must not be recoverable", name)
		}
	}
}

func TestDeadRunRecoverableTreatsExpiryAtNowAsExpired(t *testing.T) {
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	finished := now.Add(-time.Minute)
	snapshot := coordinatorSnapshot{
		SourceStatus:     "error",
		ActiveSyncLogID:  "57f39a7c-c445-4dd2-bd76-31bcaf6923f4",
		PendingSyncLogID: "",
		LeaseOwner:       "t22-runtime",
		LeaseExpiresAt:   &now,
		LogStatus:        "running",
		LogFinishedAt:    &finished,
		RunPhase:         "fetching",
		RunRetryCount:    5,
	}
	// Matches the product coordinator: a lease not strictly after now is expired.
	if !deadRunRecoverable(snapshot, now) {
		t.Fatal("lease expiry equal to now must be treated as expired, matching the product")
	}
}

func TestDeadRunRecoverableAcceptsRetryCountBeyondBudget(t *testing.T) {
	leaseExpiry := time.Date(2026, 10, 7, 9, 49, 56, 0, time.UTC)
	finished := time.Date(2026, 10, 7, 9, 44, 44, 0, time.UTC)
	now := leaseExpiry.Add(time.Hour)
	snapshot := coordinatorSnapshot{
		SourceStatus:     "error",
		ActiveSyncLogID:  "57f39a7c-c445-4dd2-bd76-31bcaf6923f4",
		PendingSyncLogID: "",
		LeaseOwner:       "t22-runtime",
		LeaseExpiresAt:   &leaseExpiry,
		LogStatus:        "running",
		LogFinishedAt:    &finished,
		RunPhase:         "fetching",
		RunRetryCount:    6,
	}
	if !deadRunRecoverable(snapshot, now) {
		t.Fatal("a retry count above the budget is still the exhausted dead-run shape")
	}
}
