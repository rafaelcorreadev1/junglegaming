package httpapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

var Module = fx.Module("httpapi",
	fx.Provide(
		NewHealthHandler,
		NewWalletHandler,
		NewWageringHandler,
		NewRouter,
		NewServer,
	),
	// Fx only builds what something asks for; this is what makes the server exist.
	fx.Invoke(func(*http.Server) {}),
)

func NewRouter(health *HealthHandler, wallets *WalletHandler, wagering *WageringHandler, ids application.IDGenerator, logger *slog.Logger) http.Handler {
	router := chi.NewRouter()
	router.Use(correlationID(ids), recoverPanic(logger))

	router.Get("/health/live", health.Live)
	router.Get("/health/ready", health.Ready)

	router.Post("/wallets", wallets.Open)
	router.Get("/wallets/{walletId}", wallets.Get)
	router.Get("/wallets/{walletId}/ledger", wallets.Ledger)

	router.Post("/wagering/transactions", wagering.Process)
	router.Get("/wagering/transactions/{transactionId}", wagering.GetByID)
	router.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", wagering.GetByExternalID)
	return router
}

func NewServer(lc fx.Lifecycle, cfg config.Config, handler http.Handler, logger *slog.Logger) *http.Server {
	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			// Listening here, outside the goroutine, turns a port conflict into a startup failure.
			listener, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return fmt.Errorf("listen on %s: %w", server.Addr, err)
			}
			logger.Info("http server listening", "addr", listener.Addr().String())

			go func() {
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("http server stopped unexpectedly", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("http server shutting down")
			return server.Shutdown(ctx)
		},
	})

	return server
}
