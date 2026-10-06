package service

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sourceDispatchVolcengine = "volcengine"

type sourceDispatchTestModel struct {
	batchEmbed func(context.Context, []string) ([][]float32, error)
	calls      atomic.Int32
}

func (m *sourceDispatchTestModel) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	m.calls.Add(1)
	return m.batchEmbed(ctx, texts)
}

func TestSourceEmbeddingDispatchVolcengineGlobalLimitAndOrder(t *testing.T) {
	const batchSize = 16
	release := make(chan struct{})
	started := make(chan string, batchSize*2)
	var releaseOnce sync.Once
	releaseAll := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseAll()

	var active atomic.Int32
	var peak atomic.Int32
	model := &sourceDispatchTestModel{batchEmbed: func(ctx context.Context, texts []string) ([][]float32, error) {
		if len(texts) != 1 {
			return nil, fmt.Errorf("expected one text per provider call, got %d", len(texts))
		}
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		started <- texts[0]
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		index, err := strconv.Atoi(texts[0][2:])
		if err != nil {
			return nil, err
		}
		return [][]float32{{float32(index)}}, nil
	}}

	textsA := make([]string, batchSize)
	textsB := make([]string, batchSize)
	for i := 0; i < batchSize; i++ {
		textsA[i] = fmt.Sprintf("a-%02d", i)
		textsB[i] = fmt.Sprintf("b-%02d", i)
	}
	ready := make(chan struct{}, 2)
	type batchResult struct {
		vectors [][]float32
		err     error
	}
	resultsA, resultsB := make(chan batchResult, 1), make(chan batchResult, 1)
	go func() {
		ready <- struct{}{}
		vectors, err := sourceBatchEmbed(context.Background(), sourceDispatchVolcengine, model, textsA)
		resultsA <- batchResult{vectors: vectors, err: err}
	}()
	go func() {
		ready <- struct{}{}
		vectors, err := sourceBatchEmbed(context.Background(), sourceDispatchVolcengine, model, textsB)
		resultsB <- batchResult{vectors: vectors, err: err}
	}()
	<-ready
	<-ready

	for i := 0; i < sourceDispatchMaxConcurrency; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("provider calls did not reach the expected parallelism")
		}
	}
	select {
	case extra := <-started:
		releaseAll()
		t.Fatalf("provider call %q exceeded the process-wide concurrency limit", extra)
	case <-time.After(50 * time.Millisecond):
	}
	if got := peak.Load(); got <= 1 || got > sourceDispatchMaxConcurrency {
		releaseAll()
		t.Fatalf("peak concurrent provider calls = %d, want 2..%d", got, sourceDispatchMaxConcurrency)
	}
	releaseAll()

	for name, resultCh := range map[string]<-chan batchResult{"a": resultsA, "b": resultsB} {
		select {
		case result := <-resultCh:
			if result.err != nil {
				t.Fatalf("batch %s failed: %v", name, result.err)
			}
			wantLen := batchSize
			if len(result.vectors) != wantLen {
				t.Fatalf("batch %s returned %d vectors, want %d", name, len(result.vectors), wantLen)
			}
			for i, vector := range result.vectors {
				if len(vector) != 1 || vector[0] != float32(i) {
					t.Fatalf("batch %s vector %d = %v, want [%d] in input order", name, i, vector, i)
				}
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("batch %s did not drain", name)
		}
	}
}

func TestSourceEmbeddingDispatchErrorDrainsAndDoesNotLeakSlots(t *testing.T) {
	var active atomic.Int32
	var slowStarted sync.Once
	var slowStartedCh = make(chan struct{})
	model := &sourceDispatchTestModel{batchEmbed: func(ctx context.Context, texts []string) ([][]float32, error) {
		if len(texts) != 1 {
			return nil, fmt.Errorf("expected one text per provider call, got %d", len(texts))
		}
		active.Add(1)
		defer active.Add(-1)
		if texts[0] == "fail" {
			select {
			case <-slowStartedCh:
				return nil, errors.New("provider detail must not escape the caller")
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		slowStarted.Do(func() { close(slowStartedCh) })
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	texts := []string{"fail", "slow-1", "slow-2", "slow-3", "not-started-1", "not-started-2", "not-started-3", "not-started-4"}
	vectors, err := sourceBatchEmbed(context.Background(), sourceDispatchVolcengine, model, texts)
	if err == nil || vectors != nil {
		t.Fatalf("failed dispatch returned vectors=%v err=%v; want nil vectors and an error", vectors, err)
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("dispatch returned with %d provider calls still active", got)
	}
	if got := model.calls.Load(); got >= int32(len(texts)) {
		t.Fatalf("dispatch started %d calls after failure for %d texts", got, len(texts))
	}

	recovered, err := sourceBatchEmbed(context.Background(), sourceDispatchVolcengine, &sourceDispatchTestModel{batchEmbed: func(_ context.Context, texts []string) ([][]float32, error) {
		return [][]float32{{float32(len(texts))}}, nil
	}}, []string{"recovered"})
	if err != nil || len(recovered) != 1 {
		t.Fatalf("a later dispatch did not acquire released slots: vectors=%v err=%v", recovered, err)
	}
}

func TestSourceEmbeddingDispatchCancellationDrainsAndDoesNotLeakSlots(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{}, 16)
	var active atomic.Int32
	model := &sourceDispatchTestModel{batchEmbed: func(ctx context.Context, texts []string) ([][]float32, error) {
		active.Add(1)
		defer active.Add(-1)
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	texts := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}
	type dispatchResult struct {
		vectors [][]float32
		err     error
	}
	resultCh := make(chan dispatchResult, 1)
	go func() {
		vectors, err := sourceBatchEmbed(ctx, sourceDispatchVolcengine, model, texts)
		resultCh <- dispatchResult{vectors: vectors, err: err}
	}()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatal("provider calls did not start before cancellation")
		}
	}
	cancel()
	select {
	case result := <-resultCh:
		if result.err == nil || result.vectors != nil {
			t.Fatalf("canceled dispatch returned vectors=%v err=%v; want nil vectors and an error", result.vectors, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not drain after cancellation")
	}
	if got := active.Load(); got != 0 {
		t.Fatalf("canceled dispatch returned with %d provider calls still active", got)
	}
	if got := model.calls.Load(); got >= int32(len(texts)) {
		t.Fatalf("dispatch started %d calls after cancellation for %d texts", got, len(texts))
	}

	recovered, err := sourceBatchEmbed(context.Background(), sourceDispatchVolcengine, &sourceDispatchTestModel{batchEmbed: func(_ context.Context, texts []string) ([][]float32, error) {
		return [][]float32{{float32(len(texts))}}, nil
	}}, []string{"recovered"})
	if err != nil || len(recovered) != 1 {
		t.Fatalf("a later dispatch did not acquire released slots: vectors=%v err=%v", recovered, err)
	}
}

func TestSourceEmbeddingDispatchRejectsNonSingleVolcengineResults(t *testing.T) {
	for _, count := range []int{0, 2} {
		t.Run(fmt.Sprintf("vectors-%d", count), func(t *testing.T) {
			model := &sourceDispatchTestModel{batchEmbed: func(context.Context, []string) ([][]float32, error) {
				vectors := make([][]float32, count)
				for i := range vectors {
					vectors[i] = []float32{float32(i)}
				}
				return vectors, nil
			}}
			got, err := sourceBatchEmbed(context.Background(), sourceDispatchVolcengine, model, []string{"one"})
			if err == nil || got != nil {
				t.Fatalf("dispatch returned vectors=%v err=%v; want nil vectors and an error", got, err)
			}
		})
	}
}

func TestSourceEmbeddingDispatchKeepsNativeBatchForOtherProviders(t *testing.T) {
	wantTexts := []string{"first", "second", "third"}
	model := &sourceDispatchTestModel{batchEmbed: func(_ context.Context, texts []string) ([][]float32, error) {
		if strings.Join(texts, ",") != strings.Join(wantTexts, ",") {
			return nil, fmt.Errorf("native batch inputs = %v, want %v", texts, wantTexts)
		}
		return [][]float32{{1}, {2}, {3}}, nil
	}}
	got, err := sourceBatchEmbed(context.Background(), "openai", model, wantTexts)
	if err != nil {
		t.Fatal(err)
	}
	if calls := model.calls.Load(); calls != 1 {
		t.Fatalf("native BatchEmbed called %d times, want one", calls)
	}
	if len(got) != len(wantTexts) {
		t.Fatalf("native batch returned %d vectors, want %d", len(got), len(wantTexts))
	}
	for i, vector := range got {
		if len(vector) != 1 || vector[0] != float32(i+1) {
			t.Fatalf("native vector %d = %v, want [%d]", i, vector, i+1)
		}
	}
}
