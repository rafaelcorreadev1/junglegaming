package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"
)

type scriptedJob struct {
	mu     sync.Mutex
	more   []bool
	err    error
	calls  int
	called chan struct{}
}

func (j *scriptedJob) run(context.Context) (bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	more := j.calls < len(j.more) && j.more[j.calls]
	j.calls++
	select {
	case j.called <- struct{}{}:
	default:
	}
	return more, j.err
}

func (j *scriptedJob) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.calls
}

func newTestPoller(job *scriptedJob, interval time.Duration) *Poller {
	job.called = make(chan struct{}, 100)
	return NewPoller("test", job.run, interval, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDrainKeepsGoingWhileTheJobReportsMore(t *testing.T) {
	job := &scriptedJob{more: []bool{true, true, false, true}}
	newTestPoller(job, time.Hour).drain(context.Background(), context.Background())

	if job.count() != 3 {
		t.Errorf("job ran %d times, want 3", job.count())
	}
}

func TestDrainStopsAfterAnErrorInsteadOfSpinning(t *testing.T) {
	job := &scriptedJob{more: []bool{true, true}, err: errors.New("database is down")}
	newTestPoller(job, time.Hour).drain(context.Background(), context.Background())

	if job.count() != 1 {
		t.Errorf("job ran %d times after an error, want 1", job.count())
	}
}

func TestRunPollsOnEveryTickAndStopsWhenCancelled(t *testing.T) {
	job := &scriptedJob{}
	poller := newTestPoller(job, 5*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		poller.Run(ctx, context.Background())
	}()

	for range 3 {
		select {
		case <-job.called:
		case <-time.After(2 * time.Second):
			t.Fatal("the poller stopped polling")
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

func TestAttachStartsWithTheAppAndStopsWithIt(t *testing.T) {
	job := &scriptedJob{}
	poller := newTestPoller(job, 5*time.Millisecond)
	lc := fxtest.NewLifecycle(t)
	poller.Attach(lc)

	lc.RequireStart()
	select {
	case <-job.called:
	case <-time.After(2 * time.Second):
		t.Fatal("the poller did not run after start")
	}
	lc.RequireStop()

	calls := job.count()
	time.Sleep(30 * time.Millisecond)
	if job.count() != calls {
		t.Errorf("the job ran %d more times after stop", job.count()-calls)
	}
}

func TestARoundIsCutAtItsTimeout(t *testing.T) {
	var sawDeadline atomic.Bool
	job := func(ctx context.Context) (bool, error) {
		<-ctx.Done()
		sawDeadline.Store(errors.Is(ctx.Err(), context.DeadlineExceeded))
		return true, ctx.Err()
	}
	poller := NewPoller("test", job, time.Hour, 50*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))

	start := time.Now()
	poller.drain(context.Background(), context.Background())
	if elapsed := time.Since(start); elapsed > 2*time.Second || !sawDeadline.Load() {
		t.Errorf("round took %v, deadline seen = %v; want it cut after 50ms", elapsed, sawDeadline.Load())
	}
}

func TestStopLetsTheRoundInProgressFinishAndStartsNoOther(t *testing.T) {
	started := make(chan struct{}, 10)
	var finished, interrupted atomic.Int32
	job := func(ctx context.Context) (bool, error) {
		started <- struct{}{}
		select {
		case <-time.After(300 * time.Millisecond):
			finished.Add(1)
		case <-ctx.Done():
			interrupted.Add(1)
		}
		// Asks for another round right away, which the stop must refuse.
		return true, nil
	}
	poller := NewPoller("test", job, time.Hour, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	lc := fxtest.NewLifecycle(t)
	poller.Attach(lc)
	lc.RequireStart()
	<-started

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := lc.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if finished.Load() != 1 || interrupted.Load() != 0 || len(started) != 0 {
		t.Errorf("finished %d, interrupted %d, rounds started after stop %d; want 1, 0, 0", finished.Load(), interrupted.Load(), len(started))
	}
}
