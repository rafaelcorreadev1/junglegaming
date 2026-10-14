package application

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"wagering-service/internal/domain"
)

const (
	maxIdempotencyKeyLength = 255
	// A duplicate that slips past the check under the lock surfaces as a unique violation; one more attempt then finds it.
	maxProcessAttempts = 3
)

type ProcessWagerInput struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	Money                          domain.Money
	ReferenceExternalTransactionID string
	CorrelationID                  string
}

type ProcessWagerResult struct {
	Transaction *domain.WagerTransaction
	// Replay is true when the result comes from an earlier request with the same key and payload.
	Replay bool
}

type ProcessWagerTransaction struct {
	tx      TxManager
	wallets WalletRepository
	wagers  WagerTransactionRepository
	ledger  LedgerRepository
	outbox  OutboxRepository
	clock   Clock
	ids     IDGenerator
}

func NewProcessWagerTransaction(
	tx TxManager,
	wallets WalletRepository,
	wagers WagerTransactionRepository,
	ledger LedgerRepository,
	outbox OutboxRepository,
	clock Clock,
	ids IDGenerator,
) *ProcessWagerTransaction {
	return &ProcessWagerTransaction{tx: tx, wallets: wallets, wagers: wagers, ledger: ledger, outbox: outbox, clock: clock, ids: ids}
}

func (uc *ProcessWagerTransaction) Execute(ctx context.Context, in ProcessWagerInput) (ProcessWagerResult, error) {
	if err := validateProcessInput(in); err != nil {
		return ProcessWagerResult{}, err
	}
	hash, err := payloadHash(in)
	if err != nil {
		return ProcessWagerResult{}, err
	}

	var result ProcessWagerResult
	for attempt := 1; ; attempt++ {
		err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
			var processErr error
			result, processErr = uc.process(ctx, in, hash)
			return processErr
		})
		if !lostDuplicateRace(err) || attempt == maxProcessAttempts {
			break
		}
	}
	if err != nil {
		return ProcessWagerResult{}, err
	}
	return result, nil
}

func validateProcessInput(in ProcessWagerInput) error {
	switch {
	case in.IdempotencyKey == "":
		return &domain.ValidationError{Field: "idempotencyKey", Reason: "is required"}
	case len(in.IdempotencyKey) > maxIdempotencyKeyLength:
		return &domain.ValidationError{Field: "idempotencyKey", Reason: "is too long"}
	case !in.Money.IsValid():
		return &domain.ValidationError{Field: "money", Reason: "is required"}
	case uuid.Validate(in.WalletID) != nil:
		return &domain.ValidationError{Field: "walletId", Reason: "must be a UUID"}
	}
	return nil
}

func lostDuplicateRace(err error) bool {
	return errors.Is(err, ErrDuplicateIdempotencyKey) ||
		errors.Is(err, ErrDuplicateExternalTransaction) ||
		errors.Is(err, ErrAlreadyReversed)
}

func (uc *ProcessWagerTransaction) process(ctx context.Context, in ProcessWagerInput, hash string) (ProcessWagerResult, error) {
	now := uc.clock.Now()
	tx, err := domain.NewWagerTransaction(domain.NewWagerTransactionParams{
		ID:                             uc.ids.NewID(),
		ProviderID:                     in.ProviderID,
		ExternalTransactionID:          in.ExternalTransactionID,
		IdempotencyKey:                 in.IdempotencyKey,
		PayloadHash:                    hash,
		WalletID:                       in.WalletID,
		PlayerID:                       in.PlayerID,
		RoundID:                        in.RoundID,
		GameID:                         in.GameID,
		Kind:                           domain.TransactionKind(in.Kind),
		Money:                          in.Money,
		ReferenceExternalTransactionID: in.ReferenceExternalTransactionID,
		Now:                            now,
	})
	if err != nil {
		return ProcessWagerResult{}, err
	}

	// The lock comes before the duplicate check: a concurrent request for the same operation waits here and then sees the row the first one wrote.
	wallet, err := uc.wallets.GetForUpdate(ctx, in.WalletID)
	if err != nil {
		return ProcessWagerResult{}, err
	}

	existing, err := uc.findDuplicate(ctx, in, hash)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	if existing != nil {
		return ProcessWagerResult{Transaction: existing, Replay: true}, nil
	}

	reference, err := uc.resolveReference(ctx, tx)
	if err != nil {
		return ProcessWagerResult{}, err
	}

	entry, err := domain.ApplyWager(tx, wallet, reference, uc.ids.NewID(), now)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	if err := uc.wagers.Insert(ctx, tx); err != nil {
		return ProcessWagerResult{}, err
	}
	if entry != nil {
		if err := uc.wallets.UpdateBalance(ctx, wallet); err != nil {
			return ProcessWagerResult{}, err
		}
		if err := uc.ledger.Insert(ctx, *entry); err != nil {
			return ProcessWagerResult{}, err
		}
	}

	events, err := wagerEvents(tx, wallet, entry, uc.ids, in.CorrelationID)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	if err := appendEvents(ctx, uc.outbox, events...); err != nil {
		return ProcessWagerResult{}, err
	}
	return ProcessWagerResult{Transaction: tx}, nil
}

// findDuplicate returns the stored transaction for a replay, nil for a new operation, or a conflict error.
func (uc *ProcessWagerTransaction) findDuplicate(ctx context.Context, in ProcessWagerInput, hash string) (*domain.WagerTransaction, error) {
	byKey, err := uc.wagers.GetByIdempotencyKey(ctx, in.ProviderID, in.IdempotencyKey)
	switch {
	case err == nil:
		if byKey.PayloadHash() != hash {
			return nil, ErrIdempotencyConflict
		}
		return byKey, nil
	case !errors.Is(err, ErrTransactionNotFound):
		return nil, err
	}

	_, err = uc.wagers.GetByExternalID(ctx, in.ProviderID, in.ExternalTransactionID)
	switch {
	case err == nil:
		return nil, ErrExternalTransactionConflict
	case !errors.Is(err, ErrTransactionNotFound):
		return nil, err
	}
	return nil, nil
}

func (uc *ProcessWagerTransaction) resolveReference(ctx context.Context, tx *domain.WagerTransaction) (domain.Reference, error) {
	if !tx.HasReference() {
		return domain.Reference{}, nil
	}
	referenced, err := uc.wagers.GetByExternalID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	if errors.Is(err, ErrTransactionNotFound) {
		return domain.Reference{}, nil
	}
	if err != nil {
		return domain.Reference{}, err
	}

	reversed, err := uc.wagers.HasProcessedReversal(ctx, referenced.ID())
	if err != nil {
		return domain.Reference{}, err
	}
	return domain.Reference{Transaction: referenced, Reversed: reversed}, nil
}

// wagerEvents builds the events for the state the transaction reached; entry is nil when the balance did not move.
func wagerEvents(tx *domain.WagerTransaction, wallet *domain.Wallet, entry *domain.LedgerEntry, ids IDGenerator, correlationID string) ([]domain.Event, error) {
	meta := func() domain.EventMeta {
		return domain.EventMeta{EventID: ids.NewID(), CorrelationID: correlationID, CausationID: tx.ID(), OccurredAt: tx.UpdatedAt()}
	}

	var events []domain.Event
	switch tx.Status() {
	case domain.StatusProcessed:
		processed, err := domain.NewWagerTransactionProcessed(meta(), tx)
		if err != nil {
			return nil, err
		}
		events = append(events, processed)
	case domain.StatusRejected:
		rejected, err := domain.NewWagerTransactionRejected(meta(), tx)
		if err != nil {
			return nil, err
		}
		events = append(events, rejected)
	case domain.StatusPendingReference:
		pending, err := domain.NewWagerTransactionPendingReference(meta(), tx)
		if err != nil {
			return nil, err
		}
		events = append(events, pending)
	}

	if entry != nil {
		balanceChanged, err := domain.NewWalletBalanceChanged(meta(), wallet, *entry)
		if err != nil {
			return nil, err
		}
		events = append(events, balanceChanged)
	}
	return events, nil
}
