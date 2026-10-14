package worker

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

var Module = fx.Module("worker",
	fx.Invoke(startReferenceResolver, startOutboxPublisher),
)

func startReferenceResolver(
	lc fx.Lifecycle,
	resolver *application.ResolvePendingReferences,
	policy application.ReferenceRetryPolicy,
	cfg config.Config,
	logger *slog.Logger,
) {
	job := func(ctx context.Context) (bool, error) {
		claimed, err := resolver.Execute(ctx)
		return claimed == policy.BatchSize, err
	}
	NewPoller("reference-resolver", job, cfg.ReferencePollInterval, logger).Attach(lc)
}

func startOutboxPublisher(lc fx.Lifecycle, publisher *application.PublishOutbox, cfg config.Config, logger *slog.Logger) {
	job := func(ctx context.Context) (bool, error) {
		_, full, err := publisher.Execute(ctx)
		return full, err
	}
	NewPoller("outbox-publisher", job, cfg.OutboxPollInterval, logger).Attach(lc)
}
