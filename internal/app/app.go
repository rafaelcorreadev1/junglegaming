package app

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/config"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/faults"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/httpapi"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/observability"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/postgres"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/sqs"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/system"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/worker"
)

func Options(cfg config.Config) fx.Option {
	return fx.Options(
		fx.Supply(cfg),
		fx.StartTimeout(cfg.ShutdownTimeout),
		fx.StopTimeout(cfg.ShutdownTimeout),
		fx.WithLogger(func(logger *slog.Logger) fxevent.Logger {
			return &fxevent.SlogLogger{Logger: logger}
		}),
		observability.Module,
		system.Module,
		postgres.Module,
		fx.Provide(
			referenceRetryPolicy,
			application.NewOpenWallet,
			application.NewGetWallet,
			application.NewListLedger,
			application.NewReconcileWallet,
			application.NewProcessWagerTransaction,
			application.NewGetWagerTransaction,
			application.NewResolvePendingReferences,
			outboxPolicy,
			application.NewPublishOutbox,
			fx.Annotate(application.NewHandleWagerMessage, fx.As(new(sqs.WagerHandler))),
		),
		sqs.Module,
		fx.Provide(
			readinessProbe(postgres.NewProbe),
			readinessProbe(sqs.NewProbe),
		),
		// At the root, so every module that asks for these gets the instrumented version.
		fx.Decorate(observability.InstrumentTxManager, decoratePublisher, faults.Handler),
		// Workers before HTTP: Fx stops in reverse order, so HTTP stops taking requests first and starts serving last.
		worker.Module,
		httpapi.Module,
	)
}

func readinessProbe(constructor any) any {
	return fx.Annotate(constructor, fx.As(new(httpapi.ReadinessProbe)), fx.ResultTags(`group:"readiness"`))
}

func referenceRetryPolicy(cfg config.Config) (application.ReferenceRetryPolicy, error) {
	policy := application.DefaultReferenceRetryPolicy()
	policy.BaseDelay = cfg.ReferenceRetryBaseDelay
	policy.MaxDelay = cfg.ReferenceRetryMaxDelay
	policy.MaxAttempts = cfg.ReferenceMaxAttempts
	policy.Lease = cfg.ReferenceLease
	return policy, policy.Validate()
}

func outboxPolicy(cfg config.Config) (application.OutboxPolicy, error) {
	policy := application.DefaultOutboxPolicy()
	policy.Lease = cfg.OutboxLease
	return policy, policy.Validate()
}

// decoratePublisher stacks both wrappers in one decorator, because Fx decorates a type only once per scope.
func decoratePublisher(next application.EventPublisher, metrics *observability.Metrics, cfg config.Config, logger *slog.Logger) application.EventPublisher {
	return faults.Publisher(observability.InstrumentPublisher(next, metrics), cfg, logger)
}
