package httpapi

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
)

const maxRequestBodyBytes = 1 << 20

type WalletHandler struct {
	open   *application.OpenWallet
	get    *application.GetWallet
	ledger *application.ListLedger
	logger *slog.Logger
}

func NewWalletHandler(open *application.OpenWallet, get *application.GetWallet, ledger *application.ListLedger, logger *slog.Logger) *WalletHandler {
	return &WalletHandler{open: open, get: get, ledger: ledger, logger: logger}
}

type openWalletRequest struct {
	PlayerID string `json:"playerId"`
	// A pointer tells a missing field apart from a present one.
	InitialBalance *domain.Money `json:"initialBalance"`
}

type walletResponse struct {
	ID       string       `json:"id"`
	PlayerID string       `json:"playerId"`
	Balance  domain.Money `json:"balance"`
	Version  int64        `json:"version"`
}

type ledgerEntryResponse struct {
	ID            string                 `json:"id"`
	WalletID      string                 `json:"walletId"`
	TransactionID string                 `json:"transactionId"`
	Direction     domain.LedgerDirection `json:"direction"`
	Money         domain.Money           `json:"money"`
	BalanceBefore domain.Money           `json:"balanceBefore"`
	BalanceAfter  domain.Money           `json:"balanceAfter"`
	CreatedAt     time.Time              `json:"createdAt"`
}

type ledgerPageResponse struct {
	Items []ledgerEntryResponse `json:"items"`
	// NextCursor is null on the last page.
	NextCursor *string `json:"nextCursor"`
}

func toWalletResponse(wallet *domain.Wallet) walletResponse {
	return walletResponse{ID: wallet.ID(), PlayerID: wallet.PlayerID(), Balance: wallet.Balance(), Version: wallet.Version()}
}

func (h *WalletHandler) Open(w http.ResponseWriter, r *http.Request) {
	var request openWalletRequest
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	if request.InitialBalance == nil {
		writeError(w, r, h.logger, invalidRequest("initialBalance", "initialBalance is required"))
		return
	}

	wallet, err := h.open.Execute(r.Context(), application.OpenWalletInput{
		PlayerID:       request.PlayerID,
		InitialBalance: *request.InitialBalance,
		CorrelationID:  correlationIDFrom(r.Context()),
	})
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	w.Header().Set("Location", "/wallets/"+wallet.ID())
	writeJSON(w, http.StatusCreated, toWalletResponse(wallet))
}

func (h *WalletHandler) Get(w http.ResponseWriter, r *http.Request) {
	walletID, err := walletIDParam(r)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	wallet, err := h.get.Execute(r.Context(), walletID)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}
	writeJSON(w, http.StatusOK, toWalletResponse(wallet))
}

func (h *WalletHandler) Ledger(w http.ResponseWriter, r *http.Request) {
	walletID, err := walletIDParam(r)
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	limit := 0
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 {
			writeError(w, r, h.logger, invalidRequest("limit", "limit must be a positive integer"))
			return
		}
	}

	page, err := h.ledger.Execute(r.Context(), application.ListLedgerInput{
		WalletID: walletID,
		Cursor:   r.URL.Query().Get("cursor"),
		Limit:    limit,
	})
	if err != nil {
		writeError(w, r, h.logger, err)
		return
	}

	// An empty slice, not nil, so the JSON is [] instead of null.
	response := ledgerPageResponse{Items: make([]ledgerEntryResponse, len(page.Entries))}
	for i, entry := range page.Entries {
		response.Items[i] = ledgerEntryResponse{
			ID:            entry.ID(),
			WalletID:      entry.WalletID(),
			TransactionID: entry.TransactionID(),
			Direction:     entry.Direction(),
			Money:         entry.Amount(),
			BalanceBefore: entry.BalanceBefore(),
			BalanceAfter:  entry.BalanceAfter(),
			CreatedAt:     entry.CreatedAt(),
		}
	}
	if page.NextCursor != "" {
		response.NextCursor = &page.NextCursor
	}
	writeJSON(w, http.StatusOK, response)
}

// A malformed ID is rejected here because PostgreSQL would fail the query instead of finding nothing.
func walletIDParam(r *http.Request) (string, error) {
	walletID := chi.URLParam(r, "walletId")
	if err := uuid.Validate(walletID); err != nil {
		return "", invalidRequest("walletId", "walletId must be a UUID")
	}
	return walletID, nil
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(target); err != nil {
		// Money reports its own errors while decoding; those keep their specific message.
		for _, moneyErr := range []error{domain.ErrInvalidAmount, domain.ErrInvalidCurrency} {
			if errors.Is(err, moneyErr) {
				return invalidRequest("", err.Error())
			}
		}
		return invalidRequest("", "request body is not valid JSON for this endpoint")
	}
	if decoder.More() {
		return invalidRequest("", "request body must contain a single JSON object")
	}
	return nil
}
