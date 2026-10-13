package observability

import (
	"log/slog"
	"os"

	"go.uber.org/fx"

	"wagering-service/internal/config"
)

var Module = fx.Module("observability",
	fx.Provide(NewLogger),
)

func NewLogger(cfg config.Config) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel})
	return slog.New(handler)
}
