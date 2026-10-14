package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

const retryAfterSeconds = "1"

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

type apiError struct {
	status int
	detail errorDetail
}

func invalidRequest(field, message string) apiError {
	return apiError{http.StatusBadRequest, errorDetail{Code: "INVALID_REQUEST", Message: message, Field: field}}
}

var sentinelErrors = []struct {
	target error
	status int
	code   string
}{
	{domain.ErrInvalidAmount, http.StatusBadRequest, "INVALID_REQUEST"},
	{domain.ErrInvalidCurrency, http.StatusBadRequest, "INVALID_REQUEST"},
	{domain.ErrInvalidMoney, http.StatusBadRequest, "INVALID_REQUEST"},
	{domain.ErrMoneyOverflow, http.StatusBadRequest, "INVALID_REQUEST"},
	{application.ErrWalletNotFound, http.StatusNotFound, "WALLET_NOT_FOUND"},
	{application.ErrTransactionNotFound, http.StatusNotFound, "TRANSACTION_NOT_FOUND"},
	{application.ErrWalletAlreadyExists, http.StatusConflict, "WALLET_ALREADY_EXISTS"},
	{application.ErrIdempotencyConflict, http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT"},
	{application.ErrExternalTransactionConflict, http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT"},
	{application.ErrUnavailable, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	{context.DeadlineExceeded, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
	{context.Canceled, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE"},
}

func toAPIError(err error) apiError {
	var api apiError
	if errors.As(err, &api) {
		return api
	}
	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		return invalidRequest(validation.Field, validation.Error())
	}
	for _, known := range sentinelErrors {
		if errors.Is(err, known.target) {
			return apiError{known.status, errorDetail{Code: known.code, Message: known.target.Error()}}
		}
	}
	// The cause stays in the log; the client must not see internals such as SQL errors.
	return apiError{http.StatusInternalServerError, errorDetail{Code: "INTERNAL_ERROR", Message: "internal error"}}
}

func (e apiError) Error() string { return e.detail.Message }

func writeError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	api := toAPIError(err)
	switch {
	case api.status == http.StatusServiceUnavailable:
		w.Header().Set("Retry-After", retryAfterSeconds)
		logger.WarnContext(r.Context(), "request failed on a transient error", "error", err)
	case api.status >= http.StatusInternalServerError:
		logger.ErrorContext(r.Context(), "request failed", "error", err)
	}
	writeJSON(w, api.status, errorBody{Error: api.detail})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
