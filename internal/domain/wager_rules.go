package domain

import (
	"errors"
	"time"
)

// Reference is what the caller found when resolving referenceExternalTransactionId.
type Reference struct {
	// Transaction is nil while the referenced operation has not arrived.
	Transaction *WagerTransaction
	// Reversed reports that a REFUND or ROLLBACK of Transaction was already processed.
	Reversed bool
}

// ApplyWager reports a business rejection as an outcome (tx ends REJECTED), not as an error.
func ApplyWager(tx *WagerTransaction, wallet *Wallet, ref Reference, entryID string, now time.Time) (*LedgerEntry, error) {
	if tx.status.IsTerminal() {
		return nil, ErrInvalidTransition
	}
	if !tx.kind.IsExternal() {
		return nil, invalid("kind", "only external transactions can be applied")
	}
	if wallet.id != tx.walletID {
		return nil, ErrWalletMismatch
	}

	reject := func(code FailureCode) (*LedgerEntry, error) {
		return nil, tx.MarkRejected(code, wallet.balance, now)
	}

	if wallet.playerID != tx.playerID {
		return reject(FailurePlayerMismatch)
	}
	if wallet.Currency() != tx.money.Currency() {
		return reject(FailureCurrencyMismatch)
	}

	if tx.HasReference() {
		code, waiting := checkReference(tx, ref)
		if waiting {
			if tx.status == StatusPending {
				return nil, tx.MarkPendingReference(now)
			}
			return nil, nil
		}
		if code != "" {
			return reject(code)
		}
		tx.referenceTransactionID = ref.Transaction.id
	}

	direction, moves := movementFor(tx, ref)
	if !moves {
		return nil, tx.MarkProcessed(wallet.balance, now)
	}

	entry, err := wallet.move(direction, entryID, tx.id, tx.money, now)
	switch {
	case errors.Is(err, ErrInsufficientFunds):
		if tx.kind == KindRollback {
			return reject(FailureReversalInsufficientFunds)
		}
		return reject(FailureInsufficientFunds)
	case errors.Is(err, ErrMoneyOverflow):
		return reject(FailureBalanceLimitExceeded)
	case err != nil:
		return nil, err
	}

	if err := tx.MarkProcessed(wallet.balance, now); err != nil {
		return nil, err
	}
	return &entry, nil
}

func checkReference(tx *WagerTransaction, ref Reference) (code FailureCode, waiting bool) {
	target := ref.Transaction
	if target == nil {
		return "", true
	}

	sameContext := target.providerID == tx.providerID &&
		target.playerID == tx.playerID &&
		target.walletID == tx.walletID &&
		target.roundID == tx.roundID &&
		target.money.Currency() == tx.money.Currency()
	if !sameContext {
		return FailureReferenceMismatch, false
	}

	if !referenceKindAllowed(tx.kind, target.kind) {
		return FailureReferenceKindNotAllowed, false
	}

	switch target.status {
	case StatusPending, StatusPendingReference:
		return "", true
	case StatusRejected, StatusFailed:
		return FailureReferenceNotProcessed, false
	}

	if tx.kind.IsReversal() {
		if !target.money.Equal(tx.money) {
			return FailureReferenceAmountMismatch, false
		}
		if ref.Reversed {
			return FailureAlreadyReversed, false
		}
	}
	return "", false
}

func referenceKindAllowed(kind, target TransactionKind) bool {
	switch kind {
	case KindWin, KindRefund:
		return target == KindBet
	case KindRollback:
		return target == KindBet || target == KindWin || target == KindRefund
	}
	return false
}

func movementFor(tx *WagerTransaction, ref Reference) (direction LedgerDirection, moves bool) {
	switch tx.kind {
	case KindBet:
		return LedgerDebit, true
	case KindWin, KindRefund:
		return LedgerCredit, true
	case KindRollback:
		if ref.Transaction.kind == KindBet {
			return LedgerCredit, true
		}
		return LedgerDebit, true
	}
	return "", false
}
