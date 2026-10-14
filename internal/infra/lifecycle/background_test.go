package lifecycle

import (
	"context"
	"testing"
	"time"

	"go.uber.org/fx/fxtest"
)

func TestStopInterruptsWorkThatOutlivesTheGracePeriod(t *testing.T) {
	interrupted := make(chan time.Time, 1)
	lc := fxtest.NewLifecycle(t)
	Attach(lc, func(pollCtx, workCtx context.Context) {
		<-pollCtx.Done()
		// Ignores the stop request, like a message still being processed.
		<-workCtx.Done()
		interrupted <- time.Now()
	})
	lc.RequireStart()

	deadline := time.Now().Add(ReleaseMargin + 500*time.Millisecond)
	stopCtx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()
	if err := lc.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	at := <-interrupted
	if left := deadline.Sub(at); left < ReleaseMargin-time.Second || left > ReleaseMargin {
		t.Errorf("work interrupted %v before the deadline, want about %v", left, ReleaseMargin)
	}
}

func TestStopReturnsAsSoonAsTheLoopEnds(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	Attach(lc, func(pollCtx, _ context.Context) { <-pollCtx.Done() })
	lc.RequireStart()

	stopCtx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := time.Now()
	if err := lc.Stop(stopCtx); err != nil || time.Since(start) > time.Second {
		t.Errorf("stop = %v after %v; want an immediate return", err, time.Since(start))
	}
}

func TestStopReportsALoopThatNeverEnds(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	block := make(chan struct{})
	t.Cleanup(func() { close(block) })
	Attach(lc, func(context.Context, context.Context) { <-block })
	lc.RequireStart()

	stopCtx, cancel := context.WithTimeout(context.Background(), ReleaseMargin+200*time.Millisecond)
	defer cancel()
	if err := lc.Stop(stopCtx); err == nil {
		t.Error("stop must fail when the loop ignores both contexts")
	}
}
