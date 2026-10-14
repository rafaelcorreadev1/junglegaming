package application

import (
	"context"
	"errors"
	"time"

	"wagering-service/internal/domain"
)

var (
	ErrWalletNotFound      = errors.New("wallet not found")
	ErrTransactionNotFound = errors.New("transaction not found")

	ErrWalletAlreadyExists          = errors.New("a wallet already exists for this player and currency")
	ErrDuplicateExternalTransaction = errors.New("external transaction already registered")
	ErrDuplicateIdempotencyKey      = errors.New("idempotency key already used")
	ErrAlreadyReversed              = errors.New("referenced transaction already has a processed reversal")
	ErrIdempotencyConflict          = errors.New("idempotency key reused with a different payload")
	ErrExternalTransactionConflict  = errors.New("external transaction already sent with another idempotency key")
	ErrConcurrentUpdate             = errors.New("wallet was changed by another writer")
	ErrMessageConflict              = errors.New("message id already received with a different body")

	// ErrUnavailable marks failures worth retrying: the same call may succeed later without any change.
	ErrUnavailable = errors.New("storage temporarily unavailable")
	// ErrInvariantViolation means the database refused a write the domain should never have produced.
	ErrInvariantViolation = errors.New("storage invariant violated")
)

type TxManager interface {
	// WithinTx commits when fn returns nil and rolls back otherwise. Repositories called with the ctx given to fn join the transaction.
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
	// WithinSnapshot runs fn in a read-only transaction where every read sees the same committed state.
	WithinSnapshot(ctx context.Context, fn func(ctx context.Context) error) error
}

type WalletRepository interface {
	Insert(ctx context.Context, wallet *domain.Wallet) error
	Get(ctx context.Context, id string) (*domain.Wallet, error)
	// GetForUpdate holds the wallet row lock until the surrounding transaction ends.
	GetForUpdate(ctx context.Context, id string) (*domain.Wallet, error)
	// UpdateBalance expects exactly one movement since the wallet was loaded.
	UpdateBalance(ctx context.Context, wallet *domain.Wallet) error
}

type WagerTransactionRepository interface {
	Insert(ctx context.Context, tx *domain.WagerTransaction) error
	// Update persists only what a state transition changes.
	Update(ctx context.Context, tx *domain.WagerTransaction) error
	Get(ctx context.Context, id string) (*domain.WagerTransaction, error)
	GetByExternalID(ctx context.Context, providerID, externalTransactionID string) (*domain.WagerTransaction, error)
	GetByIdempotencyKey(ctx context.Context, providerID, idempotencyKey string) (*domain.WagerTransaction, error)
	// HasProcessedReversal reports whether a REFUND or ROLLBACK of the transaction was already processed.
	HasProcessedReversal(ctx context.Context, transactionID string) (bool, error)
	// ClaimPendingReferences hands each due transaction to a single caller and hides it from the others until leaseUntil.
	ClaimPendingReferences(ctx context.Context, now, leaseUntil time.Time, limit int) ([]PendingReference, error)
	// WakePendingReferences makes the transactions waiting for this operation due immediately.
	WakePendingReferences(ctx context.Context, providerID, externalTransactionID string, now time.Time) error
}

type PendingReference struct {
	TransactionID string
	WalletID      string
}

// LedgerRecord pairs an entry with its insertion order, which exists only in storage.
type LedgerRecord struct {
	Sequence int64
	Entry    domain.LedgerEntry
}

type LedgerRepository interface {
	Insert(ctx context.Context, entry domain.LedgerEntry) error
	// ListByWallet returns up to limit records with a sequence greater than afterSequence, oldest first.
	ListByWallet(ctx context.Context, walletID string, afterSequence int64, limit int) ([]LedgerRecord, error)
	Summarize(ctx context.Context, walletID string) (LedgerSummary, error)
}

type LedgerSummary struct {
	// NetMinorUnits is credits minus debits.
	NetMinorUnits int64
	Entries       int
}

type OutboxMessage struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     []byte
	OccurredAt  time.Time
}

// OutboxRecord is a stored event read back for publication.
type OutboxRecord struct {
	Message  OutboxMessage
	Sequence int64
	Attempts int
}

type OutboxRepository interface {
	Append(ctx context.Context, message OutboxMessage) error
	// ClaimDue leases until leaseUntil the unpublished events of up to aggregates aggregates whose oldest
	// unpublished event is due, at most perAggregate each, oldest first. An aggregate leased by another
	// caller is skipped entirely, so its events are never sent out of order.
	ClaimDue(ctx context.Context, now, leaseUntil time.Time, aggregates, perAggregate int) ([]OutboxRecord, error)
	MarkPublished(ctx context.Context, id string, now time.Time) error
	// Reschedule counts a failed publication and releases the event until next.
	Reschedule(ctx context.Context, id string, next time.Time, lastError string) error
	// Release returns claimed events that were not attempted, so they can be claimed again at once.
	Release(ctx context.Context, ids []string) error
}

// InboxRecord is what an earlier delivery of a message left behind.
type InboxRecord struct {
	PayloadHash string
	Completed   bool
}

type InboxRepository interface {
	// Receive records the first delivery of a message and reports true; for a later delivery it returns the stored record and false.
	Receive(ctx context.Context, consumerName, messageID, payloadHash string, now time.Time) (InboxRecord, bool, error)
	Complete(ctx context.Context, consumerName, messageID string, now time.Time) error
}

type ReconciliationReporter interface {
	Report(ctx context.Context, reconciliation Reconciliation)
}

type EventPublisher interface {
	// Publish may deliver the same message more than once; consumers deduplicate by its ID.
	Publish(ctx context.Context, message OutboxMessage) error
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID() string
}
