package domain

import (
	"errors"
	"fmt"
)

var (
	ErrInsufficientFunds = errors.New("insufficient funds")
	ErrInvalidTransition = errors.New("invalid transaction state transition")
	ErrWalletMismatch    = errors.New("transaction does not belong to this wallet")
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason)
}

func invalid(field, reason string) error {
	return &ValidationError{Field: field, Reason: reason}
}

// FailureCode values are part of the external contract; never rename one that has been released.
type FailureCode string

const (
	FailureInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	FailureReversalInsufficientFunds FailureCode = "REVERSAL_INSUFFICIENT_FUNDS"
	FailureBalanceLimitExceeded      FailureCode = "BALANCE_LIMIT_EXCEEDED"
	FailurePlayerMismatch            FailureCode = "PLAYER_MISMATCH"
	FailureCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureReferenceAmountMismatch   FailureCode = "REFERENCE_AMOUNT_MISMATCH"
	FailureReferenceKindNotAllowed   FailureCode = "REFERENCE_KIND_NOT_ALLOWED"
	FailureAlreadyReversed           FailureCode = "ALREADY_REVERSED"
	FailureProcessingFailed          FailureCode = "PROCESSING_FAILED"
)
