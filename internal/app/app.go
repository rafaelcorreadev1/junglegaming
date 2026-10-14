package app

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
	"wagering-service/internal/infra/httpapi"
	"wagering-service/internal/infra/observability"
	"wagering-service/internal/infra/postgres"
	"wagering-service/internal/infra/sqs"
	"wagering-service/internal/infra/system"
	"wagering-service/internal/infra/worker"
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
			application.DefaultOutboxPolicy,
			application.NewPublishOutbox,
			application.NewHandleWagerMessage,
		),
		sqs.Module,
		fx.Provide(
			readinessProbe(postgres.NewProbe),
			readinessProbe(sqs.NewProbe),
		),
		// At the root, so every module that asks for these gets the instrumented version.
		fx.Decorate(observability.InstrumentTxManager, observability.InstrumentPublisher),
		httpapi.Module,
		worker.Module,
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
	return policy, policy.Validate()
}
