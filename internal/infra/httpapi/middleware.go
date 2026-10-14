package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"wagering-service/internal/application"
	"wagering-service/internal/infra/observability"
)

const (
	correlationIDHeader    = "X-Correlation-Id"
	maxCorrelationIDLength = 128
)

type correlationIDKey struct{}

func correlationIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(correlationIDKey{}).(string)
	return id
}

// correlationID accepts the caller's ID so one operation can be followed across services, and creates one otherwise.
func correlationID(ids application.IDGenerator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get(correlationIDHeader)
			if id == "" || len(id) > maxCorrelationIDLength {
				id = ids.NewID()
			}
			w.Header().Set(correlationIDHeader, id)
			ctx := observability.WithFields(context.WithValue(r.Context(), correlationIDKey{}, id), "correlationId", id)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(r.Context(), "panic in http handler", "panic", recovered)
					writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorDetail{Code: "INTERNAL_ERROR", Message: "internal error"}})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(status int) {
	s.status = status
	s.ResponseWriter.WriteHeader(status)
}

// observeRequests logs no headers and no body, so neither the token nor the operation can reach the logs.
func observeRequests(metrics *observability.Metrics, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(recorder, r)
			elapsed := time.Since(start)

			// The pattern, not the path: ids in the path would create one metric series per wallet.
			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = "unmatched"
			}
			metrics.ObserveHTTP(r.Method, route, recorder.status, elapsed)

			level := slog.LevelInfo
			if strings.HasPrefix(route, "/health/") || route == "/metrics" {
				level = slog.LevelDebug
			}
			logger.Log(r.Context(), level, "http request",
				"method", r.Method, "route", route, "status", recorder.status, "durationMs", elapsed.Milliseconds())
		})
	}
}
