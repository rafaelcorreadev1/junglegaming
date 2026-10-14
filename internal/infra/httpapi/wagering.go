package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
)

const idempotencyKeyHeader = "Idempotency-Key"

type WageringHandler struct {
	process *application.ProcessWagerTransaction
	get     *application.GetWagerTransaction
	logger  *slog.Logger
}

func NewWageringHandler(process *application.ProcessWagerTransaction, get *application.GetWagerTransaction, logger *slog.Logger) *WageringHandler {
	return &WageringHandler{process: process, get: get, logger: logger}
}

type wagerRequest struct {
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	PlayerID                       string        `json:"playerId"`
	WalletID                       string        `json:"walletId"`
	RoundID                        string        `json:"roundId"`
	GameID                         string        `json:"gameId"`
	Kind                           string        `json:"kind"`
	Money                          *domain.Money `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId"`
}

type wagerResultResponse struct {
	TransactionID string                   `json:"transactionId"`
	Status        domain.TransactionStatus `json:"status"`
	FailureCode   domain.FailureCode       `json:"failureCode,omitempty"`
	// Balance is the one observed when the operation was decided, not the current one.
	Balance          *domain.Money `json:"balance,omitempty"`
	IdempotentReplay bool          `json:"idempotentReplay"`
}

type wagerTransactionResponse struct {
	TransactionID                  string                   `json:"transactionId"`
	Kind                           domain.TransactionKind   `json:"kind"`
	Status                         domain.TransactionStatus `json:"status"`
	FailureCode                    domain.FailureCode       `json:"failureCode,omitempty"`
	WalletID                       string                   `json:"walletId"`
	PlayerID                       string                   `json:"playerId"`
	Money                          domain.Money             `json:"money"`
	Balance                        *domain.Money            `json:"balance,omitempty"`
	ProviderID                     string                   `json:"providerId,omitempty"`
	ExternalTransactionID          string                   `json:"externalTransactionId,omitempty"`
	RoundID                        string                   `json:"roundId,omitempty"`
	GameID                         string                   `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string                   `json:"referenceExternalTransactionId,omitempty"`
	CreatedAt                      time.Time                `json:"createdAt"`
	UpdatedAt                      time.Time                `json:"updatedAt"`
}

// statusFor makes each outcome distinguishable by status code alone.
func statusFor(status domain.TransactionStatus) int {
	switch status {
	case domain.StatusProcessed:
		return http.StatusOK
	case domain.StatusRejected, domain.StatusFailed:
		return http.StatusUnprocessableEntity
	default:
		return http.StatusAccepted
	}
}

func resultBalance(tx *domain.WagerTransaction) *domain.Money {
	balance, observed := tx.ResultBalance()
	if !observed {
		return nil
	}
	return &balance
}

func (h *WageringHandler) Process(w http.ResponseWriter, r *http.Request) {
	idempotencyKey := r.Header.Get(idempotencyKeyHeader)
	if idempotencyKey == "" {
		writeError(w, r, h.logger, invalidRequest(idempotencyKeyHeader, "the Idempotency-Key header is required"))
		return
	}

	var request wagerRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	if request.Money == nil {
		writeError(w, r, h.logger, invalidRequest("money", "money is required"))
		return
	}

	result, err := h.process.Execute(r.Context(), application.ProcessWagerInput{
		ProviderID:                     request.ProviderID,
		ExternalTransactionID:          request.ExternalTransactionID,
		IdempotencyKey:                 idempotencyKey,
		PlayerID:                       request.PlayerID,
		WalletID:                       request.WalletID,
		RoundID:                        request.RoundID,
		GameID:                         request.GameID,
		Kind:                           request.Kind,
		Money:                          *request.Money,
		ReferenceExternalTransactionID: request.ReferenceExternalTransactionID,
		CorrelationID:                  correlationIDFrom(r.Context()),
	})
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	tx := result.Transaction
	writeJSON(w, statusFor(tx.Status()), wagerResultResponse{
		TransactionID:    tx.ID(),
		Status:           tx.Status(),
		FailureCode:      tx.FailureCode(),
		Balance:          resultBalance(tx),
		IdempotentReplay: result.Replay,
	})
}

func (h *WageringHandler) GetByID(w http.ResponseWriter, r *http.Request) {
	transactionID := chi.URLParam(r, "transactionId")
	if err := uuid.Validate(transactionID); err != nil {
		writeError(w, r, h.logger, invalidRequest("transactionId", "transactionId must be a UUID"))
		return
	}
	tx, err := h.get.ByID(r.Context(), transactionID)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toWagerTransactionResponse(tx))
}

func (h *WageringHandler) GetByExternalID(w http.ResponseWriter, r *http.Request) {
	tx, err := h.get.ByExternalID(r.Context(), chi.URLParam(r, "providerId"), chi.URLParam(r, "externalTransactionId"))
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toWagerTransactionResponse(tx))
}

func toWagerTransactionResponse(tx *domain.WagerTransaction) wagerTransactionResponse {
	return wagerTransactionResponse{
		TransactionID:                  tx.ID(),
		Kind:                           tx.Kind(),
		Status:                         tx.Status(),
		FailureCode:                    tx.FailureCode(),
		WalletID:                       tx.WalletID(),
		PlayerID:                       tx.PlayerID(),
		Money:                          tx.Money(),
		Balance:                        resultBalance(tx),
		ProviderID:                     tx.ProviderID(),
		ExternalTransactionID:          tx.ExternalTransactionID(),
		RoundID:                        tx.RoundID(),
		GameID:                         tx.GameID(),
		ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(),
		CreatedAt:                      tx.CreatedAt(),
		UpdatedAt:                      tx.UpdatedAt(),
	}
}
