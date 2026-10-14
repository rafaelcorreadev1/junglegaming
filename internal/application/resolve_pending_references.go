package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"wagering-service/internal/domain"
)

// ReferenceRetryPolicy decides how long a transaction waits for the operation it references.
type ReferenceRetryPolicy struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// MaxAttempts counts every lookup, including the one made when the transaction arrived.
	MaxAttempts int
	// Lease is how long a claimed transaction stays hidden from the other instances.
	Lease     time.Duration
	BatchSize int
}

func DefaultReferenceRetryPolicy() ReferenceRetryPolicy {
	return ReferenceRetryPolicy{
		BaseDelay:   time.Second,
		MaxDelay:    time.Minute,
		MaxAttempts: 10,
		Lease:       30 * time.Second,
		BatchSize:   50,
	}
}

func (p ReferenceRetryPolicy) Validate() error {
	switch {
	case p.BaseDelay <= 0:
		return errors.New("reference retry base delay must be positive")
	case p.MaxDelay < p.BaseDelay:
		return errors.New("reference retry max delay must not be shorter than the base delay")
	case p.MaxAttempts < 1:
		return errors.New("reference max attempts must be at least 1")
	case p.Lease <= 0:
		return errors.New("reference lease must be positive")
	case p.BatchSize < 1:
		return errors.New("reference batch size must be at least 1")
	}
	return nil
}

// delayAfter doubles the wait at each failed attempt: base, 2x, 4x... capped at MaxDelay.
func (p ReferenceRetryPolicy) delayAfter(attempts int) time.Duration {
	delay := p.BaseDelay
	for i := 1; i < attempts; i++ {
		// Checked before doubling so a large attempt count cannot overflow the duration.
		if delay >= p.MaxDelay/2 {
			return p.MaxDelay
		}
		delay *= 2
	}
	return min(delay, p.MaxDelay)
}

func (p ReferenceRetryPolicy) exhausted(attempts int) bool {
	return attempts >= p.MaxAttempts
}

type ResolvePendingReferences struct {
	tx      TxManager
	wallets WalletRepository
	wagers  WagerTransactionRepository
	outcome wagerOutcome
	clock   Clock
	ids     IDGenerator
	retry   ReferenceRetryPolicy
}

func NewResolvePendingReferences(
	tx TxManager,
	wallets WalletRepository,
	wagers WagerTransactionRepository,
	ledger LedgerRepository,
	outbox OutboxRepository,
	clock Clock,
	ids IDGenerator,
	retry ReferenceRetryPolicy,
) *ResolvePendingReferences {
	return &ResolvePendingReferences{
		tx: tx, wallets: wallets, wagers: wagers, clock: clock, ids: ids, retry: retry,
		outcome: wagerOutcome{wallets: wallets, wagers: wagers, ledger: ledger, outbox: outbox, ids: ids},
	}
}

// Execute returns how many transactions it claimed; a full batch means more may be due.
func (uc *ResolvePendingReferences) Execute(ctx context.Context) (int, error) {
	now := uc.clock.Now()
	claimed, err := uc.wagers.ClaimPendingReferences(ctx, now, now.Add(uc.retry.Lease), uc.retry.BatchSize)
	if err != nil {
		return 0, err
	}

	var errs []error
	for _, pending := range claimed {
		// A failure here leaves the lease in place, so the transaction is retried when it expires.
		if err := uc.resolve(ctx, pending); err != nil {
			errs = append(errs, fmt.Errorf("transaction %s: %w", pending.TransactionID, err))
		}
	}
	return len(claimed), errors.Join(errs...)
}

func (uc *ResolvePendingReferences) resolve(ctx context.Context, pending PendingReference) error {
	return uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		// Same order as the request path (wallet, then transactions), so the two never deadlock.
		wallet, err := uc.wallets.GetForUpdate(ctx, pending.WalletID)
		if err != nil {
			return err
		}
		tx, err := uc.wagers.Get(ctx, pending.TransactionID)
		if err != nil {
			return err
		}
		// Another instance may have finished it between the claim and the wallet lock.
		if tx.Status() != domain.StatusPendingReference {
			return nil
		}

		reference, err := resolveReference(ctx, uc.wagers, tx)
		if err != nil {
			return err
		}
		now := uc.clock.Now()
		entry, err := domain.ApplyWager(tx, wallet, reference, uc.ids.NewID(), now)
		if err != nil {
			return err
		}

		if tx.Status() == domain.StatusPendingReference {
			attempts := tx.ReferenceAttempts() + 1
			if !uc.retry.exhausted(attempts) {
				if err := tx.ScheduleReferenceAttempt(now.Add(uc.retry.delayAfter(attempts)), now); err != nil {
					return err
				}
				return uc.wagers.Update(ctx, tx)
			}
			if err := tx.MarkRejected(expiredReferenceCode(reference), wallet.Balance(), now); err != nil {
				return err
			}
		}

		if err := uc.wagers.Update(ctx, tx); err != nil {
			return err
		}
		return uc.outcome.record(ctx, tx, wallet, entry)
	})
}

func expiredReferenceCode(reference domain.Reference) domain.FailureCode {
	if reference.Transaction == nil {
		return domain.FailureReferenceNotFound
	}
	return domain.FailureReferenceNotProcessed
}
