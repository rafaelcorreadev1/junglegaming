package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
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
	return NewPoller("test", job.run, interval, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDrainKeepsGoingWhileTheJobReportsMore(t *testing.T) {
	job := &scriptedJob{more: []bool{true, true, false, true}}
	newTestPoller(job, time.Hour).drain(context.Background())

	if job.count() != 3 {
		t.Errorf("job ran %d times, want 3", job.count())
	}
}

func TestDrainStopsAfterAnErrorInsteadOfSpinning(t *testing.T) {
	job := &scriptedJob{more: []bool{true, true}, err: errors.New("database is down")}
	newTestPoller(job, time.Hour).drain(context.Background())

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
		poller.Run(ctx)
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
