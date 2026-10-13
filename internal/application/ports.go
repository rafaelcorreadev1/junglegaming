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
	ErrConcurrentUpdate             = errors.New("wallet was changed by another writer")

	// ErrUnavailable marks failures worth retrying: the same call may succeed later without any change.
	ErrUnavailable = errors.New("storage temporarily unavailable")
	// ErrInvariantViolation means the database refused a write the domain should never have produced.
	ErrInvariantViolation = errors.New("storage invariant violated")
)

type TxManager interface {
	// WithinTx commits when fn returns nil and rolls back otherwise. Repositories called with the ctx given to fn join the transaction.
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
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
}

type OutboxMessage struct {
	ID          string
	AggregateID string
	EventType   string
	Payload     []byte
	OccurredAt  time.Time
}

type OutboxRepository interface {
	Append(ctx context.Context, message OutboxMessage) error
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewID() string
}
