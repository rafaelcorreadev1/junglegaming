package worker

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"
)

// Job runs one round of background work; more reports that work may still be due right away.
type Job func(ctx context.Context) (more bool, err error)

type Poller struct {
	name     string
	job      Job
	interval time.Duration
	logger   *slog.Logger
}

func NewPoller(name string, job Job, interval time.Duration, logger *slog.Logger) *Poller {
	return &Poller{name: name, job: job, interval: interval, logger: logger.With("worker", name)}
}

// Attach runs the poller between the application's start and stop.
func (p *Poller) Attach(lc fx.Lifecycle) {
	// The OnStart context expires when startup ends, so the loop needs one of its own.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				p.Run(ctx)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			cancel()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

// Run blocks until ctx is cancelled.
func (p *Poller) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Poller) drain(ctx context.Context) {
	for ctx.Err() == nil {
		more, err := p.job(ctx)
		if err != nil && ctx.Err() == nil {
			p.logger.ErrorContext(ctx, "background job failed", "error", err)
		}
		// After an error, wait for the next tick instead of spinning against a broken dependency.
		if err != nil || !more {
			return
		}
	}
}
