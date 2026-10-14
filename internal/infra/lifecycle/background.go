// Package lifecycle runs background loops under the Fx lifecycle with a two-phase stop.
package lifecycle

import (
	"context"
	"time"

	"go.uber.org/fx"
)

// ReleaseMargin is how long before the stop deadline work in progress is interrupted, leaving time to hand it back.
const ReleaseMargin = 3 * time.Second

// Loop must stop taking new work once pollCtx is cancelled and give up work in progress once workCtx is.
type Loop func(pollCtx, workCtx context.Context)

// Attach starts the loop with the application and, on stop, first stops new work, then waits for the work
// in progress until shortly before the deadline, and only then cancels it.
func Attach(lc fx.Lifecycle, loop Loop) {
	// The OnStart context expires when startup ends, so the loop needs contexts of its own.
	pollCtx, stopPolling := context.WithCancel(context.Background())
	workCtx, abortWork := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				loop(pollCtx, workCtx)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			stopPolling()
			graceCtx := stopCtx
			if deadline, ok := stopCtx.Deadline(); ok {
				var cancel context.CancelFunc
				graceCtx, cancel = context.WithDeadline(stopCtx, deadline.Add(-ReleaseMargin))
				defer cancel()
			}
			select {
			case <-done:
				return nil
			case <-graceCtx.Done():
			}

			abortWork()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}
