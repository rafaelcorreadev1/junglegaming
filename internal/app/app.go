package app

import (
	"log/slog"

	"go.uber.org/fx"
	"go.uber.org/fx/fxevent"

	"wagering-service/internal/config"
	"wagering-service/internal/infra/httpapi"
	"wagering-service/internal/infra/observability"
	"wagering-service/internal/infra/postgres"
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
		postgres.Module,
		httpapi.Module,
	)
}
