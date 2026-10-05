package service

import "testing"

func TestAddSourceTelemetryCounterPreservesObservedRecoveryValue(t *testing.T) {
	value := int64(1)
	counter := &value
	addSourceTelemetryCounter(&counter, 0)
	if counter == nil || *counter != 1 {
		t.Fatalf("adding a measured zero erased prior residue: %v", counter)
	}
	addSourceTelemetryCounter(&counter, 2)
	if counter == nil || *counter != 3 {
		t.Fatalf("accumulated residue = %v, want 3", counter)
	}
	var unmeasured *int64
	addSourceTelemetryCounter(&unmeasured, 0)
	if unmeasured == nil || *unmeasured != 0 {
		t.Fatalf("measured zero was not recorded: %v", unmeasured)
	}
}
