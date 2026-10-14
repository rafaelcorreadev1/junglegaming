package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type scriptedResolver struct {
	mu      sync.Mutex
	results []int
	err     error
	calls   int
	called  chan struct{}
}

func (r *scriptedResolver) Execute(context.Context) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	claimed := 0
	if r.calls < len(r.results) {
		claimed = r.results[r.calls]
	}
	r.calls++
	select {
	case r.called <- struct{}{}:
	default:
	}
	return claimed, r.err
}

func (r *scriptedResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newWorker(resolver *scriptedResolver, interval time.Duration) *ReferenceResolver {
	resolver.called = make(chan struct{}, 100)
	return &ReferenceResolver{
		resolver: resolver, interval: interval, batchSize: 10,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestDrainKeepsGoingWhileBatchesComeBackFull(t *testing.T) {
	resolver := &scriptedResolver{results: []int{10, 10, 3, 10}}
	newWorker(resolver, time.Hour).drain(context.Background())

	if resolver.count() != 3 {
		t.Errorf("Execute ran %d times, want 3 (two full batches and the partial one)", resolver.count())
	}
}

func TestDrainStopsAfterAnErrorInsteadOfSpinning(t *testing.T) {
	resolver := &scriptedResolver{results: []int{10, 10}, err: errors.New("database is down")}
	newWorker(resolver, time.Hour).drain(context.Background())

	if resolver.count() != 1 {
		t.Errorf("Execute ran %d times after an error, want 1", resolver.count())
	}
}

func TestRunPollsOnEveryTickAndStopsWhenCancelled(t *testing.T) {
	resolver := &scriptedResolver{}
	worker := newWorker(resolver, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(ctx)
	}()

	for range 3 {
		select {
		case <-resolver.called:
		case <-time.After(2 * time.Second):
			t.Fatal("the worker stopped polling")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}
