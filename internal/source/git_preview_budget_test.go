package source

import (
	"bytes"
	"errors"
	"sync/atomic"
	"testing"
)

func TestGitTransferWriterEnforcesOneCumulativeBudget(t *testing.T) {
	var output bytes.Buffer
	var transferred atomic.Int64
	var exceeded atomic.Bool
	writer := &gitTransferWriter{writer: &output, transferred: &transferred, exceeded: &exceeded, limit: 5}
	if n, err := writer.Write([]byte("abc")); err != nil || n != 3 {
		t.Fatalf("first transfer write = %d, %v", n, err)
	}
	if _, err := writer.Write([]byte("def")); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("cumulative over-budget write error = %v, want resource limit", err)
	}
	if got := transferred.Load(); got != 3 {
		t.Fatalf("transferred bytes = %d, want prior accepted total 3", got)
	}
	if !exceeded.Load() {
		t.Fatal("transfer limit rejection was not recorded for the caller")
	}
	if output.String() != "abc" {
		t.Fatalf("output = %q, want only bytes within the aggregate budget", output.String())
	}
}

type shortGitTransferWriter struct{}

func (shortGitTransferWriter) Write(data []byte) (int, error) {
	if len(data) > 2 {
		return 2, nil
	}
	return len(data), nil
}

func TestGitTransferWriterCountsOnlyBytesRelayed(t *testing.T) {
	var transferred atomic.Int64
	writer := &gitTransferWriter{writer: shortGitTransferWriter{}, transferred: &transferred, limit: 10}
	n, err := writer.Write([]byte("abcd"))
	if err != nil || n != 2 {
		t.Fatalf("short transfer write = %d, %v", n, err)
	}
	if got := transferred.Load(); got != 2 {
		t.Fatalf("transferred bytes = %d, want successfully relayed 2", got)
	}
}
