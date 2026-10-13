package domain

import (
	"fmt"
	"time"
)

const initialWalletVersion = 1

type Wallet struct {
	id        string
	playerID  string
	balance   Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func OpenWallet(id, playerID string, initialBalance Money, now time.Time) (*Wallet, error) {
	return RehydrateWallet(id, playerID, initialBalance, initialWalletVersion, now, now)
}

func RehydrateWallet(id, playerID string, balance Money, version int64, createdAt, updatedAt time.Time) (*Wallet, error) {
	switch {
	case id == "":
		return nil, invalid("id", "is required")
	case playerID == "":
		return nil, invalid("playerId", "is required")
	case !balance.IsValid():
		return nil, ErrInvalidMoney
	case balance.IsNegative():
		return nil, invalid("balance", "must not be negative")
	case version < initialWalletVersion:
		return nil, invalid("version", "must be at least 1")
	case createdAt.IsZero() || updatedAt.IsZero():
		return nil, invalid("timestamps", "are required")
	}

	return &Wallet{
		id:        id,
		playerID:  playerID,
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}, nil
}

func (w *Wallet) ID() string           { return w.id }
func (w *Wallet) PlayerID() string     { return w.playerID }
func (w *Wallet) Balance() Money       { return w.balance }
func (w *Wallet) Currency() string     { return w.balance.Currency() }
func (w *Wallet) Version() int64       { return w.version }
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }

func (w *Wallet) Debit(entryID, transactionID string, amount Money, now time.Time) (LedgerEntry, error) {
	return w.move(LedgerDebit, entryID, transactionID, amount, now)
}

func (w *Wallet) Credit(entryID, transactionID string, amount Money, now time.Time) (LedgerEntry, error) {
	return w.move(LedgerCredit, entryID, transactionID, amount, now)
}

func (w *Wallet) move(direction LedgerDirection, entryID, transactionID string, amount Money, now time.Time) (LedgerEntry, error) {
	if _, err := w.balance.Cmp(amount); err != nil {
		return LedgerEntry{}, err
	}

	var newBalance Money
	var err error
	if direction == LedgerDebit {
		newBalance, err = w.balance.Sub(amount)
	} else {
		newBalance, err = w.balance.Add(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}
	if newBalance.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: balance %s, debit %s", ErrInsufficientFunds, w.balance, amount)
	}

	entry, err := NewLedgerEntry(entryID, w.id, transactionID, direction, amount, w.balance, newBalance, now)
	if err != nil {
		return LedgerEntry{}, err
	}

	w.balance = newBalance
	w.version++
	w.updatedAt = now
	return entry, nil
}
