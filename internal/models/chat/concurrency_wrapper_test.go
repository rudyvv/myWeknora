package chat

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/models/limiter"
	"github.com/Tencent/WeKnora/internal/types"
)

// fakeChat is a minimal Chat whose stream emits continuously until ctx is done,
// so we can exercise the concurrency wrapper's slot lifecycle.
type fakeChat struct {
	id      string
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (f *fakeChat) GetModelName() string { return f.id }
func (f *fakeChat) GetModelID() string   { return f.id }

func (f *fakeChat) Chat(ctx context.Context, _ []Message, _ *ChatOptions) (*types.ChatResponse, error) {
	if f.entered != nil {
		f.calls.Add(1)
		f.entered <- struct{}{}
		select {
		case <-f.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &types.ChatResponse{}, nil
}

func (f *fakeChat) ChatStream(ctx context.Context, _ []Message, _ *ChatOptions) (<-chan types.StreamResponse, error) {
	ch := make(chan types.StreamResponse)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case ch <- types.StreamResponse{}:
			}
		}
	}()
	return ch, nil
}

// TestConcurrencyChatInteractiveNotGated verifies interactive calls bypass the
// governor entirely even at limit 1.
func TestConcurrencyChatInteractiveNotGated(t *testing.T) {
	t.Cleanup(func() { limiter.SetGovernor(nil, 0) })
	limiter.SetGovernor(limiter.NewLocalLimiter(), 1)

	w := &concurrencyChat{inner: &fakeChat{id: "model-x"}}
	// No background marker: neither call should be throttled.
	if _, err := w.Chat(context.Background(), nil, nil); err != nil {
		t.Fatalf("interactive chat: %v", err)
	}
	if _, err := w.Chat(context.Background(), nil, nil); err != nil {
		t.Fatalf("interactive chat 2: %v", err)
	}
}

// TestConcurrencyChatStreamReleasesOnAbandon verifies that when the consumer
// stops reading and cancels the context, the held slot is released rather than
// leaked (the #9 fix).
func TestConcurrencyChatStreamReleasesOnAbandon(t *testing.T) {
	t.Cleanup(func() { limiter.SetGovernor(nil, 0) })
	limiter.SetGovernor(limiter.NewLocalLimiter(), 1)

	const id = "model-y"
	w := &concurrencyChat{inner: &fakeChat{id: id}}

	streamCtx, cancel := context.WithCancel(types.WithBackgroundTask(context.Background()))
	out, err := w.ChatStream(streamCtx, nil, nil)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	// Consume one item so the relay goroutine is running and holding the slot.
	<-out

	// The single slot is now held: a background acquire must block.
	blocked := make(chan struct{})
	go func() {
		rel := limiter.Gate(types.WithBackgroundTask(context.Background()), id)
		rel()
		close(blocked)
	}()
	select {
	case <-blocked:
		t.Fatal("slot should be held by the live stream")
	case <-time.After(50 * time.Millisecond):
	}

	// Abandon the stream: stop reading and cancel. The relay must release.
	cancel()
	select {
	case <-blocked:
		// released and reacquired successfully
	case <-time.After(2 * time.Second):
		t.Fatal("stream slot leaked: not released after consumer abandoned + cancelled")
	}
}

func TestConcurrencyChatCanceledWaitDoesNotStartProviderCall(t *testing.T) {
	t.Cleanup(func() { limiter.SetGovernor(nil, 0) })
	limiter.SetGovernor(limiter.NewLocalLimiter(), 1)

	const id = "model-cancel-wait"
	f := &fakeChat{id: id, entered: make(chan struct{}, 2), release: make(chan struct{})}
	w := &concurrencyChat{inner: f}
	firstDone := make(chan error, 1)
	go func() {
		_, err := w.Chat(types.WithBackgroundTask(context.Background()), nil, nil)
		firstDone <- err
	}()
	defer func() {
		close(f.release)
		select {
		case <-firstDone:
		case <-time.After(2 * time.Second):
			t.Error("first provider call did not finish after release")
		}
	}()
	select {
	case <-f.entered: // first provider call owns the only slot
	case <-time.After(2 * time.Second):
		t.Fatal("first provider call did not enter")
	}
	f.calls.Store(0)

	ctx, cancel := context.WithCancel(types.WithBackgroundTask(context.Background()))
	secondDone := make(chan error, 1)
	go func() {
		_, err := w.Chat(ctx, nil, nil)
		secondDone <- err
	}()
	waitForModelLimiterWaiter(t, id)
	cancel()

	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled model waiter did not return")
	}
	select {
	case <-f.entered:
		t.Fatal("canceled waiter started a provider call after the model slot wait")
	default:
	}
	if got := f.calls.Load(); got != 0 {
		t.Fatalf("provider calls from the canceled waiter = %d, want 0", got)
	}
}

func waitForModelLimiterWaiter(t *testing.T, modelID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats, available, err := limiter.RuntimeStats(context.Background())
		if err == nil && available {
			for _, stat := range stats {
				if stat.ModelID == modelID && stat.Waiting > 0 {
					return
				}
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("model %q never reported a waiting caller", modelID)
}
