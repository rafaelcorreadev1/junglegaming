package observability

import (
	"log/slog"
	"os"

	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

var Module = fx.Module("observability",
	fx.Provide(
		NewLogger,
		NewRegistry,
		fx.Annotate(NewReconciliationReporter, fx.As(new(application.ReconciliationReporter))),
	),
)

func NewLogger(cfg config.Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	return slog.New(handler)
}
