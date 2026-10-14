package application

import (
	"context"
	"encoding/json"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

type OpenWalletInput struct {
	PlayerID       string
	InitialBalance domain.Money
	CorrelationID  string
}

type OpenWallet struct {
	tx      TxManager
	wallets WalletRepository
	wagers  WagerTransactionRepository
	ledger  LedgerRepository
	outbox  OutboxRepository
	clock   Clock
	ids     IDGenerator
}

func NewOpenWallet(
	tx TxManager,
	wallets WalletRepository,
	wagers WagerTransactionRepository,
	ledger LedgerRepository,
	outbox OutboxRepository,
	clock Clock,
	ids IDGenerator,
) *OpenWallet {
	return &OpenWallet{tx: tx, wallets: wallets, wagers: wagers, ledger: ledger, outbox: outbox, clock: clock, ids: ids}
}

func (uc *OpenWallet) Execute(ctx context.Context, in OpenWalletInput) (*domain.Wallet, error) {
	now := uc.clock.Now()
	wallet, err := domain.OpenWallet(uc.ids.NewID(), in.PlayerID, in.InitialBalance, now)
	if err != nil {
		return nil, err
	}

	err = uc.tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := uc.wallets.Insert(ctx, wallet); err != nil {
			return err
		}
		if wallet.Balance().IsZero() {
			return nil
		}
		return uc.recordOpeningCredit(ctx, wallet, in.CorrelationID)
	})
	if err != nil {
		return nil, err
	}
	return wallet, nil
}

func (uc *OpenWallet) recordOpeningCredit(ctx context.Context, wallet *domain.Wallet, correlationID string) error {
	now := wallet.CreatedAt()
	balance := wallet.Balance()

	opening, err := domain.NewOpeningTransaction(uc.ids.NewID(), wallet.ID(), wallet.PlayerID(), balance, now)
	if err != nil {
		return err
	}
	zero, err := domain.ZeroMoney(wallet.Currency())
	if err != nil {
		return err
	}
	// The wallet is created already holding the balance (version 1), so the entry is built here instead of coming from wallet.Credit.
	entry, err := domain.NewLedgerEntry(uc.ids.NewID(), wallet.ID(), opening.ID(), domain.LedgerCredit, balance, zero, balance, now)
	if err != nil {
		return err
	}

	meta := func() domain.EventMeta {
		return domain.EventMeta{EventID: uc.ids.NewID(), CorrelationID: correlationID, CausationID: opening.ID(), OccurredAt: now}
	}
	processed, err := domain.NewWagerTransactionProcessed(meta(), opening)
	if err != nil {
		return err
	}
	balanceChanged, err := domain.NewWalletBalanceChanged(meta(), wallet, entry)
	if err != nil {
		return err
	}

	if err := uc.wagers.Insert(ctx, opening); err != nil {
		return err
	}
	if err := uc.ledger.Insert(ctx, entry); err != nil {
		return err
	}
	return appendEvents(ctx, uc.outbox, processed, balanceChanged)
}

func appendEvents(ctx context.Context, outbox OutboxRepository, events ...domain.Event) error {
	for _, event := range events {
		payload, err := json.Marshal(event)
		if err != nil {
			return err
		}
		envelope := event.Envelope()
		err = outbox.Append(ctx, OutboxMessage{
			ID:          envelope.EventID,
			AggregateID: envelope.AggregateID,
			EventType:   envelope.EventType,
			Payload:     payload,
			OccurredAt:  envelope.OccurredAt,
		})
		if err != nil {
			return err
		}
	}
	return nil
}
