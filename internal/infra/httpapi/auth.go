package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"wagering-service/internal/application"
	"wagering-service/internal/infra/auth"
)

type TokenVerifier interface {
	Verify(ctx context.Context, raw string) (auth.Principal, error)
}

var (
	errUnauthenticated = apiError{http.StatusUnauthorized, errorDetail{Code: "UNAUTHENTICATED", Message: "a valid bearer token is required"}}
	errForbidden       = apiError{http.StatusForbidden, errorDetail{Code: "FORBIDDEN", Message: "the token does not allow this operation"}}
)

func authenticate(verifier TokenVerifier, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, ok := bearerToken(r)
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				writeError(w, r, logger, errUnauthenticated)
				return
			}
			principal, err := verifier.Verify(r.Context(), raw)
			if err != nil {
				logger.InfoContext(r.Context(), "access token rejected", "error", err, "correlationId", correlationIDFrom(r.Context()))
				w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
				writeError(w, r, logger, errUnauthenticated)
				return
			}
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), principal)))
		})
	}
}

func requireRole(logger *slog.Logger, roles ...auth.Role) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, _ := auth.PrincipalFrom(r.Context())
			for _, role := range roles {
				if principal.Has(role) {
					next.ServeHTTP(w, r)
					return
				}
			}
			writeError(w, r, logger, errForbidden)
		})
	}
}

func bearerToken(r *http.Request) (string, bool) {
	scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || strings.TrimSpace(token) == "" {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// visibleTo answers another provider's transaction with the same 404 as a missing one, so its existence is not revealed.
func visibleTo(r *http.Request, providerID string) error {
	if principal, _ := auth.PrincipalFrom(r.Context()); !principal.CanSee(providerID) {
		return application.ErrTransactionNotFound
	}
	return nil
}
