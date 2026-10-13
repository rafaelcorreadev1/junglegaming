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
	"wagering-service/internal/infra/system"
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
			application.NewOpenWallet,
			application.NewGetWallet,
			application.NewListLedger,
		),
		httpapi.Module,
	)
}
