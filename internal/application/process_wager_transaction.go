package application

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
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
	retry   ReferenceRetryPolicy
}

func NewProcessWagerTransaction(
	tx TxManager,
	wallets WalletRepository,
	wagers WagerTransactionRepository,
	ledger LedgerRepository,
	outbox OutboxRepository,
	clock Clock,
	ids IDGenerator,
	retry ReferenceRetryPolicy,
) *ProcessWagerTransaction {
	return &ProcessWagerTransaction{
		tx: tx, wallets: wallets, wagers: wagers, ledger: ledger, outbox: outbox, clock: clock, ids: ids, retry: retry,
	}
}

func (uc *ProcessWagerTransaction) Execute(ctx context.Context, in ProcessWagerInput) (ProcessWagerResult, error) {
	hash, err := prepareProcessInput(in)
	if err != nil {
		return ProcessWagerResult{}, err
	}

	var result ProcessWagerResult
	err = withinTxRetryingRaces(ctx, uc.tx, func(ctx context.Context) error {
		var processErr error
		result, processErr = uc.process(ctx, in, hash)
		return processErr
	})
	if err != nil {
		return ProcessWagerResult{}, err
	}
	return result, nil
}

func prepareProcessInput(in ProcessWagerInput) (string, error) {
	if err := validateProcessInput(in); err != nil {
		return "", err
	}
	return payloadHash(in)
}

// withinTxRetryingRaces runs fn in a fresh transaction each time, since a unique violation aborts the one it happened in.
func withinTxRetryingRaces(ctx context.Context, tx TxManager, fn func(ctx context.Context) error) error {
	for attempt := 1; ; attempt++ {
		err := tx.WithinTx(ctx, fn)
		if !lostDuplicateRace(err) || attempt == maxProcessAttempts {
			return err
		}
	}
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
		CorrelationID:                  in.CorrelationID,
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

	reference, err := resolveReference(ctx, uc.wagers, tx)
	if err != nil {
		return ProcessWagerResult{}, err
	}

	entry, err := domain.ApplyWager(tx, wallet, reference, uc.ids.NewID(), now)
	if err != nil {
		return ProcessWagerResult{}, err
	}
	if tx.Status() == domain.StatusPendingReference {
		if err := tx.ScheduleReferenceAttempt(now.Add(uc.retry.delayAfter(1)), now); err != nil {
			return ProcessWagerResult{}, err
		}
	}
	if err := uc.wagers.Insert(ctx, tx); err != nil {
		return ProcessWagerResult{}, err
	}
	outcome := wagerOutcome{wallets: uc.wallets, wagers: uc.wagers, ledger: uc.ledger, outbox: uc.outbox, ids: uc.ids}
	if err := outcome.record(ctx, tx, wallet, entry); err != nil {
		return ProcessWagerResult{}, err
	}
	return ProcessWagerResult{Transaction: tx}, nil
}

// wagerOutcome persists what follows a transaction the caller has already written.
type wagerOutcome struct {
	wallets WalletRepository
	wagers  WagerTransactionRepository
	ledger  LedgerRepository
	outbox  OutboxRepository
	ids     IDGenerator
}

func (o wagerOutcome) record(ctx context.Context, tx *domain.WagerTransaction, wallet *domain.Wallet, entry *domain.LedgerEntry) error {
	if entry != nil {
		if err := o.wallets.UpdateBalance(ctx, wallet); err != nil {
			return err
		}
		if err := o.ledger.Insert(ctx, *entry); err != nil {
			return err
		}
	}

	events, err := wagerEvents(tx, wallet, entry, o.ids)
	if err != nil {
		return err
	}
	if err := appendEvents(ctx, o.outbox, events...); err != nil {
		return err
	}
	if !tx.Status().IsTerminal() {
		return nil
	}
	return o.wagers.WakePendingReferences(ctx, tx.ProviderID(), tx.ExternalTransactionID(), tx.UpdatedAt())
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

func resolveReference(ctx context.Context, wagers WagerTransactionRepository, tx *domain.WagerTransaction) (domain.Reference, error) {
	if !tx.HasReference() {
		return domain.Reference{}, nil
	}
	referenced, err := wagers.GetByExternalID(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	if errors.Is(err, ErrTransactionNotFound) {
		return domain.Reference{}, nil
	}
	if err != nil {
		return domain.Reference{}, err
	}

	reversed, err := wagers.HasProcessedReversal(ctx, referenced.ID())
	if err != nil {
		return domain.Reference{}, err
	}
	return domain.Reference{Transaction: referenced, Reversed: reversed}, nil
}

// wagerEvents builds the events for the state the transaction reached; entry is nil when the balance did not move.
func wagerEvents(tx *domain.WagerTransaction, wallet *domain.Wallet, entry *domain.LedgerEntry, ids IDGenerator) ([]domain.Event, error) {
	meta := func() domain.EventMeta {
		return domain.EventMeta{EventID: ids.NewID(), CorrelationID: tx.CorrelationID(), CausationID: tx.ID(), OccurredAt: tx.UpdatedAt()}
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
