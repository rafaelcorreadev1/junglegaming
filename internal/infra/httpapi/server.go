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
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.uber.org/fx"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/config"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/auth"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/observability"
)

var Module = fx.Module("httpapi",
	fx.Provide(
		fx.Annotate(NewHealthHandler, fx.ParamTags(`group:"readiness"`)),
		NewWalletHandler,
		NewWageringHandler,
		fx.Annotate(newVerifier, fx.As(new(TokenVerifier))),
		NewRouter,
		NewServer,
	),
	// Fx only builds what something asks for; this is what makes the server exist.
	fx.Invoke(func(*http.Server) {}),
)

func NewRouter(
	health *HealthHandler,
	wallets *WalletHandler,
	wagering *WageringHandler,
	verifier TokenVerifier,
	ids application.IDGenerator,
	registry *prometheus.Registry,
	metrics *observability.Metrics,
	logger *slog.Logger,
) http.Handler {
	router := chi.NewRouter()
	router.Use(correlationID(ids), observeRequests(metrics, logger), recoverPanic(logger))

	router.Get("/health/live", health.Live)
	router.Get("/health/ready", health.Ready)
	router.Handle("/metrics", promhttp.HandlerFor(registry, promhttp.HandlerOpts{}))

	router.Group(func(r chi.Router) {
		r.Use(authenticate(verifier, logger))

		r.Group(func(r chi.Router) {
			r.Use(requireRole(logger, auth.RoleInternal))
			r.Post("/wallets", wallets.Open)
			r.Get("/wallets/{walletId}", wallets.Get)
			r.Get("/wallets/{walletId}/ledger", wallets.Ledger)
			r.Post("/wallets/{walletId}/reconciliation", wallets.Reconcile)
		})

		r.With(requireRole(logger, auth.RoleProvider)).Post("/wagering/transactions", wagering.Process)

		r.Group(func(r chi.Router) {
			r.Use(requireRole(logger, auth.RoleProvider, auth.RoleInternal))
			r.Get("/wagering/transactions/{transactionId}", wagering.GetByID)
			r.Get("/providers/{providerId}/wagering/transactions/{externalTransactionId}", wagering.GetByExternalID)
		})
	})
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

func newVerifier(lc fx.Lifecycle, cfg config.Config) (*auth.Verifier, error) {
	verifier, err := auth.NewVerifier(auth.Config{Issuer: cfg.AuthIssuer, JWKSURL: cfg.AuthJWKSURL, Audience: cfg.AuthAudience})
	if err != nil {
		return nil, err
	}
	lc.Append(fx.StartStopHook(verifier.CheckKeys, verifier.Close))
	return verifier, nil
}
