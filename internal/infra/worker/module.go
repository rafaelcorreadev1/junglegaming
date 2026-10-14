package worker

import (
	"context"
	"log/slog"

	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
	"wagering-service/internal/infra/observability"
	"wagering-service/internal/infra/sqs"
)

var Module = fx.Module("worker",
	fx.Invoke(startReferenceResolver, startOutboxPublisher, startWagerConsumer),
)

func startReferenceResolver(
	lc fx.Lifecycle,
	resolver *application.ResolvePendingReferences,
	policy application.ReferenceRetryPolicy,
	cfg config.Config,
	metrics *observability.Metrics,
	logger *slog.Logger,
) {
	job := func(ctx context.Context) (bool, error) {
		result, err := resolver.Execute(ctx)
		for _, tx := range result.Decided {
			metrics.TransactionDecided(observability.ChannelReferenceWorker, tx)
		}
		for range result.Rescheduled {
			metrics.Retry("reference_resolver")
		}
		return result.Claimed == policy.BatchSize, err
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

func startWagerConsumer(lc fx.Lifecycle, consumer *sqs.Consumer) {
	consumer.Attach(lc)
}
