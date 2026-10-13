package domain

import (
	"fmt"
	"slices"
	"time"
)

type TransactionKind string

const (
	KindOpening  TransactionKind = "OPENING"
	KindBet      TransactionKind = "BET"
	KindWin      TransactionKind = "WIN"
	KindLoss     TransactionKind = "LOSS"
	KindRefund   TransactionKind = "REFUND"
	KindRollback TransactionKind = "ROLLBACK"
)

func (k TransactionKind) IsExternal() bool {
	switch k {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return true
	}
	return false
}

func (k TransactionKind) IsReversal() bool {
	return k == KindRefund || k == KindRollback
}

func (k TransactionKind) acceptsReference() bool {
	return k == KindWin || k.IsReversal()
}

type TransactionStatus string

const (
	StatusPending          TransactionStatus = "PENDING"
	StatusPendingReference TransactionStatus = "PENDING_REFERENCE"
	StatusProcessed        TransactionStatus = "PROCESSED"
	StatusRejected         TransactionStatus = "REJECTED"
	StatusFailed           TransactionStatus = "FAILED"
)

var allowedTransitions = map[TransactionStatus][]TransactionStatus{
	StatusPending:          {StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed},
	StatusPendingReference: {StatusProcessed, StatusRejected, StatusFailed},
}

func (s TransactionStatus) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

func (s TransactionStatus) isKnown() bool {
	return s.IsTerminal() || s == StatusPending || s == StatusPendingReference
}

type WagerTransaction struct {
	id       string
	kind     TransactionKind
	status   TransactionStatus
	walletID string
	playerID string
	money    Money

	providerID            string
	externalTransactionID string
	idempotencyKey        string
	payloadHash           string
	roundID               string
	gameID                string

	referenceExternalTransactionID string
	referenceTransactionID         string

	failureCode FailureCode
	// resultBalance is the balance reported to the provider; an invalid Money means it was never observed.
	resultBalance Money

	createdAt time.Time
	updatedAt time.Time
}

type NewWagerTransactionParams struct {
	ID                             string
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	WalletID                       string
	PlayerID                       string
	RoundID                        string
	GameID                         string
	Kind                           TransactionKind
	Money                          Money
	ReferenceExternalTransactionID string
	Now                            time.Time
}

func NewWagerTransaction(p NewWagerTransactionParams) (*WagerTransaction, error) {
	if p.Kind == KindOpening {
		return nil, invalid("kind", "OPENING is reserved for internal wallet opening")
	}
	if !p.Kind.IsExternal() {
		return nil, invalid("kind", "must be BET, WIN, LOSS, REFUND or ROLLBACK")
	}

	required := []struct{ field, value string }{
		{"id", p.ID},
		{"providerId", p.ProviderID},
		{"externalTransactionId", p.ExternalTransactionID},
		{"idempotencyKey", p.IdempotencyKey},
		{"payloadHash", p.PayloadHash},
		{"walletId", p.WalletID},
		{"playerId", p.PlayerID},
		{"roundId", p.RoundID},
		{"gameId", p.GameID},
	}
	for _, r := range required {
		if r.value == "" {
			return nil, invalid(r.field, "is required")
		}
	}
	if p.Now.IsZero() {
		return nil, invalid("now", "is required")
	}

	if err := validateAmountForKind(p.Kind, p.Money); err != nil {
		return nil, err
	}
	if err := validateReferenceForKind(p.Kind, p.ExternalTransactionID, p.ReferenceExternalTransactionID); err != nil {
		return nil, err
	}

	return &WagerTransaction{
		id:                             p.ID,
		kind:                           p.Kind,
		status:                         StatusPending,
		walletID:                       p.WalletID,
		playerID:                       p.PlayerID,
		money:                          p.Money,
		providerID:                     p.ProviderID,
		externalTransactionID:          p.ExternalTransactionID,
		idempotencyKey:                 p.IdempotencyKey,
		payloadHash:                    p.PayloadHash,
		roundID:                        p.RoundID,
		gameID:                         p.GameID,
		referenceExternalTransactionID: p.ReferenceExternalTransactionID,
		createdAt:                      p.Now,
		updatedAt:                      p.Now,
	}, nil
}

func validateAmountForKind(kind TransactionKind, money Money) error {
	if !money.IsValid() {
		return ErrInvalidMoney
	}
	if kind == KindLoss {
		if !money.IsZero() {
			return invalid("money", "LOSS requires an amount of 0.00")
		}
		return nil
	}
	if !money.IsPositive() {
		return invalid("money", fmt.Sprintf("%s requires an amount greater than zero", kind))
	}
	return nil
}

func validateReferenceForKind(kind TransactionKind, externalID, referenceExternalID string) error {
	const field = "referenceExternalTransactionId"
	switch {
	case kind.IsReversal() && referenceExternalID == "":
		return invalid(field, fmt.Sprintf("is required for %s", kind))
	case !kind.acceptsReference() && referenceExternalID != "":
		return invalid(field, fmt.Sprintf("is not accepted for %s", kind))
	case referenceExternalID != "" && referenceExternalID == externalID:
		return invalid(field, "must not reference the transaction itself")
	}
	return nil
}

// NewOpeningTransaction is born PROCESSED: opening has no dependency that could leave it pending.
func NewOpeningTransaction(id, walletID, playerID string, initialBalance Money, now time.Time) (*WagerTransaction, error) {
	switch {
	case id == "":
		return nil, invalid("id", "is required")
	case walletID == "":
		return nil, invalid("walletId", "is required")
	case playerID == "":
		return nil, invalid("playerId", "is required")
	case now.IsZero():
		return nil, invalid("now", "is required")
	case !initialBalance.IsValid():
		return nil, ErrInvalidMoney
	case !initialBalance.IsPositive():
		return nil, invalid("money", "OPENING requires an amount greater than zero")
	}

	return &WagerTransaction{
		id:            id,
		kind:          KindOpening,
		status:        StatusProcessed,
		walletID:      walletID,
		playerID:      playerID,
		money:         initialBalance,
		resultBalance: initialBalance,
		createdAt:     now,
		updatedAt:     now,
	}, nil
}

type WagerTransactionState struct {
	ID                             string
	Kind                           TransactionKind
	Status                         TransactionStatus
	WalletID                       string
	PlayerID                       string
	Money                          Money
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PayloadHash                    string
	RoundID                        string
	GameID                         string
	ReferenceExternalTransactionID string
	ReferenceTransactionID         string
	FailureCode                    FailureCode
	ResultBalance                  Money
	CreatedAt                      time.Time
	UpdatedAt                      time.Time
}

func RehydrateWagerTransaction(s WagerTransactionState) (*WagerTransaction, error) {
	switch {
	case s.ID == "":
		return nil, invalid("id", "is required")
	case s.Kind != KindOpening && !s.Kind.IsExternal():
		return nil, invalid("kind", "is unknown")
	case !s.Status.isKnown():
		return nil, invalid("status", "is unknown")
	case s.WalletID == "" || s.PlayerID == "":
		return nil, invalid("walletId/playerId", "are required")
	case !s.Money.IsValid():
		return nil, ErrInvalidMoney
	case s.Kind.IsExternal() && (s.ProviderID == "" || s.ExternalTransactionID == ""):
		return nil, invalid("providerId/externalTransactionId", "are required for external transactions")
	case s.CreatedAt.IsZero() || s.UpdatedAt.IsZero():
		return nil, invalid("timestamps", "are required")
	}

	return &WagerTransaction{
		id:                             s.ID,
		kind:                           s.Kind,
		status:                         s.Status,
		walletID:                       s.WalletID,
		playerID:                       s.PlayerID,
		money:                          s.Money,
		providerID:                     s.ProviderID,
		externalTransactionID:          s.ExternalTransactionID,
		idempotencyKey:                 s.IdempotencyKey,
		payloadHash:                    s.PayloadHash,
		roundID:                        s.RoundID,
		gameID:                         s.GameID,
		referenceExternalTransactionID: s.ReferenceExternalTransactionID,
		referenceTransactionID:         s.ReferenceTransactionID,
		failureCode:                    s.FailureCode,
		resultBalance:                  s.ResultBalance,
		createdAt:                      s.CreatedAt,
		updatedAt:                      s.UpdatedAt,
	}, nil
}

func (t *WagerTransaction) ID() string                    { return t.id }
func (t *WagerTransaction) Kind() TransactionKind         { return t.kind }
func (t *WagerTransaction) Status() TransactionStatus     { return t.status }
func (t *WagerTransaction) WalletID() string              { return t.walletID }
func (t *WagerTransaction) PlayerID() string              { return t.playerID }
func (t *WagerTransaction) Money() Money                  { return t.money }
func (t *WagerTransaction) ProviderID() string            { return t.providerID }
func (t *WagerTransaction) ExternalTransactionID() string { return t.externalTransactionID }
func (t *WagerTransaction) IdempotencyKey() string        { return t.idempotencyKey }
func (t *WagerTransaction) PayloadHash() string           { return t.payloadHash }
func (t *WagerTransaction) RoundID() string               { return t.roundID }
func (t *WagerTransaction) GameID() string                { return t.gameID }
func (t *WagerTransaction) FailureCode() FailureCode      { return t.failureCode }
func (t *WagerTransaction) CreatedAt() time.Time          { return t.createdAt }
func (t *WagerTransaction) UpdatedAt() time.Time          { return t.updatedAt }

func (t *WagerTransaction) ReferenceExternalTransactionID() string {
	return t.referenceExternalTransactionID
}

func (t *WagerTransaction) ReferenceTransactionID() string { return t.referenceTransactionID }

func (t *WagerTransaction) HasReference() bool { return t.referenceExternalTransactionID != "" }

func (t *WagerTransaction) ResultBalance() (Money, bool) {
	return t.resultBalance, t.resultBalance.IsValid()
}

func (t *WagerTransaction) MarkPendingReference(now time.Time) error {
	if !t.HasReference() {
		return fmt.Errorf("%w: %s has no reference to wait for", ErrInvalidTransition, t.kind)
	}
	return t.transition(StatusPendingReference, now)
}

func (t *WagerTransaction) MarkProcessed(resultBalance Money, now time.Time) error {
	if !resultBalance.IsValid() || resultBalance.IsNegative() {
		return invalid("resultBalance", "must be a non-negative amount")
	}
	if err := t.transition(StatusProcessed, now); err != nil {
		return err
	}
	t.resultBalance = resultBalance
	return nil
}

// MarkRejected accepts an uninitialized resultBalance for rejections decided without reading the wallet.
func (t *WagerTransaction) MarkRejected(code FailureCode, resultBalance Money, now time.Time) error {
	if code == "" {
		return invalid("failureCode", "is required")
	}
	if err := t.transition(StatusRejected, now); err != nil {
		return err
	}
	t.failureCode = code
	t.resultBalance = resultBalance
	return nil
}

func (t *WagerTransaction) MarkFailed(code FailureCode, now time.Time) error {
	if code == "" {
		return invalid("failureCode", "is required")
	}
	if err := t.transition(StatusFailed, now); err != nil {
		return err
	}
	t.failureCode = code
	return nil
}

func (t *WagerTransaction) transition(to TransactionStatus, now time.Time) error {
	if now.IsZero() {
		return invalid("now", "is required")
	}
	if !slices.Contains(allowedTransitions[t.status], to) {
		return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, t.status, to)
	}
	t.status = to
	t.updatedAt = now
	return nil
}
