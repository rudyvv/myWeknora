package source

import (
	"errors"
	"os"
	"testing"
)

func TestBlobStageEnforcesLimitReadsOneBlobAndCleansUp(t *testing.T) {
	stage, err := NewBlobStage(5)
	if err != nil {
		t.Fatal(err)
	}
	root := stage.root
	if err := stage.Put("src/a.go", []byte("12345")); err != nil {
		t.Fatalf("Put() failed: %v", err)
	}
	if err := stage.Put("src/b.go", []byte("x")); !errors.Is(err, ErrResourceLimitExceeded) {
		t.Fatalf("Put() over the stage limit = %v, want resource limit", err)
	}
	got, err := stage.Read("src/a.go", 5)
	if err != nil || string(got) != "12345" {
		t.Fatalf("Read() = %q, %v; want staged blob", got, err)
	}
	if _, err := stage.Read("src/a.go", 4); err == nil {
		t.Fatal("Read() accepted content beyond the per-file read limit")
	}
	if stage.UsedBytes() != 5 {
		t.Fatalf("UsedBytes() = %d, want 5", stage.UsedBytes())
	}
	if err := stage.Close(); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("staging root still exists after Close(): %v", err)
	}
}
