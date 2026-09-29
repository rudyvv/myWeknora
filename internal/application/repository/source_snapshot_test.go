package repository

import (
	"testing"
	"time"
)

func TestSourceOutboxRetryDelayIsExponentialAndBounded(t *testing.T) {
	if got := sourceOutboxRetryDelay(1); got != time.Second {
		t.Fatalf("first retry delay = %s, want 1s", got)
	}
	if got := sourceOutboxRetryDelay(4); got != 8*time.Second {
		t.Fatalf("fourth retry delay = %s, want 8s", got)
	}
	if got := sourceOutboxRetryDelay(100); got != 5*time.Minute {
		t.Fatalf("retry delay cap = %s, want 5m", got)
	}
}
