package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	"wagering-service/internal/application"
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
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), correlationIDKey{}, id)))
		})
	}
}

func recoverPanic(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					logger.ErrorContext(r.Context(), "panic in http handler", "panic", recovered, "correlationId", correlationIDFrom(r.Context()))
					writeJSON(w, http.StatusInternalServerError, errorBody{Error: errorDetail{Code: "INTERNAL_ERROR", Message: "internal error"}})
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}
