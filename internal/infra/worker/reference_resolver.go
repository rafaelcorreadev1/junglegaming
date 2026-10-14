package worker

import (
	"context"
	"log/slog"
	"time"

	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

var Module = fx.Module("worker",
	fx.Provide(NewReferenceResolver),
	fx.Invoke(func(*ReferenceResolver) {}),
)

type pendingReferenceResolver interface {
	Execute(ctx context.Context) (int, error)
}

type ReferenceResolver struct {
	resolver  pendingReferenceResolver
	interval  time.Duration
	batchSize int
	logger    *slog.Logger
}

func NewReferenceResolver(
	lc fx.Lifecycle,
	resolver *application.ResolvePendingReferences,
	policy application.ReferenceRetryPolicy,
	cfg config.Config,
	logger *slog.Logger,
) *ReferenceResolver {
	w := &ReferenceResolver{resolver: resolver, interval: cfg.ReferencePollInterval, batchSize: policy.BatchSize, logger: logger}

	// The OnStart context expires when startup ends, so the loop needs one of its own.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				w.Run(ctx)
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
	return w
}

// Run blocks until ctx is cancelled.
func (w *ReferenceResolver) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		w.drain(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *ReferenceResolver) drain(ctx context.Context) {
	for ctx.Err() == nil {
		claimed, err := w.resolver.Execute(ctx)
		if err != nil && ctx.Err() == nil {
			w.logger.ErrorContext(ctx, "resolving pending references failed", "claimed", claimed, "error", err)
		}
		if claimed > 0 {
			w.logger.DebugContext(ctx, "pending references attempted", "claimed", claimed)
		}
		// Only a full batch suggests more are due; after an error, wait for the next tick instead of spinning.
		if err != nil || claimed < w.batchSize {
			return
		}
	}
}
