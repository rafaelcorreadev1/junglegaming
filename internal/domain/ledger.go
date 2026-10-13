package domain

import "time"

type LedgerDirection string

const (
	LedgerDebit  LedgerDirection = "DEBIT"
	LedgerCredit LedgerDirection = "CREDIT"
)

type LedgerEntry struct {
	id            string
	walletID      string
	transactionID string
	direction     LedgerDirection
	amount        Money
	balanceBefore Money
	balanceAfter  Money
	createdAt     time.Time
}

// NewLedgerEntry serves both creation and rehydration: an entry has no behavior to replay, only an equation to hold.
func NewLedgerEntry(
	id, walletID, transactionID string,
	direction LedgerDirection,
	amount, balanceBefore, balanceAfter Money,
	createdAt time.Time,
) (LedgerEntry, error) {
	switch {
	case id == "":
		return LedgerEntry{}, invalid("id", "is required")
	case walletID == "":
		return LedgerEntry{}, invalid("walletId", "is required")
	case transactionID == "":
		return LedgerEntry{}, invalid("transactionId", "is required")
	case createdAt.IsZero():
		return LedgerEntry{}, invalid("createdAt", "is required")
	case !amount.IsPositive():
		return LedgerEntry{}, invalid("amount", "must be greater than zero")
	case !balanceBefore.IsValid() || balanceBefore.IsNegative():
		return LedgerEntry{}, invalid("balanceBefore", "must be a non-negative amount")
	case !balanceAfter.IsValid() || balanceAfter.IsNegative():
		return LedgerEntry{}, invalid("balanceAfter", "must be a non-negative amount")
	}

	var expected Money
	var err error
	switch direction {
	case LedgerDebit:
		expected, err = balanceBefore.Sub(amount)
	case LedgerCredit:
		expected, err = balanceBefore.Add(amount)
	default:
		return LedgerEntry{}, invalid("direction", "must be DEBIT or CREDIT")
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if !expected.Equal(balanceAfter) {
		return LedgerEntry{}, invalid("balanceAfter", "does not match balanceBefore and amount")
	}

	return LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		amount:        amount,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}, nil
}

func (e LedgerEntry) ID() string                 { return e.id }
func (e LedgerEntry) WalletID() string           { return e.walletID }
func (e LedgerEntry) TransactionID() string      { return e.transactionID }
func (e LedgerEntry) Direction() LedgerDirection { return e.direction }
func (e LedgerEntry) Amount() Money              { return e.amount }
func (e LedgerEntry) BalanceBefore() Money       { return e.balanceBefore }
func (e LedgerEntry) BalanceAfter() Money        { return e.balanceAfter }
func (e LedgerEntry) CreatedAt() time.Time       { return e.createdAt }
