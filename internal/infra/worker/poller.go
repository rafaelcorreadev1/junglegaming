package worker

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"wagering-service/internal/infra/lifecycle"
)

// Job runs one round of background work; more reports that work may still be due right away.
type Job func(ctx context.Context) (more bool, err error)

type Poller struct {
	name     string
	job      Job
	interval time.Duration
	// roundTimeout bounds one round, so a hung dependency cannot hold the worker forever.
	roundTimeout time.Duration
	logger       *slog.Logger
}

func NewPoller(name string, job Job, interval, roundTimeout time.Duration, logger *slog.Logger) *Poller {
	return &Poller{name: name, job: job, interval: interval, roundTimeout: roundTimeout, logger: logger.With("worker", name)}
}

func (p *Poller) Attach(lc fx.Lifecycle) {
	lifecycle.Attach(lc, p.Run)
}

// Run starts no new round once pollCtx is cancelled; workCtx interrupts the round in progress.
func (p *Poller) Run(pollCtx, workCtx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		p.drain(pollCtx, workCtx)
		select {
		case <-pollCtx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Poller) drain(pollCtx, workCtx context.Context) {
	for pollCtx.Err() == nil {
		more, err := p.round(workCtx)
		if err != nil && workCtx.Err() == nil {
			p.logger.ErrorContext(workCtx, "background job failed", "error", err)
		}
		// After an error, wait for the next tick instead of spinning against a broken dependency.
		if err != nil || !more {
			return
		}
	}
}

func (p *Poller) round(workCtx context.Context) (bool, error) {
	ctx, cancel := context.WithTimeout(workCtx, p.roundTimeout)
	defer cancel()
	return p.job(ctx)
}
