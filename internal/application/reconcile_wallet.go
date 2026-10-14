package application

import (
	"context"

	"wagering-service/internal/domain"
)

type Reconciliation struct {
	WalletID          string
	StoredBalance     domain.Money
	CalculatedBalance domain.Money
	// Difference is stored minus calculated, so a positive value means the wallet holds more than its ledger explains.
	Difference     domain.Money
	CheckedEntries int
}

func (r Reconciliation) Consistent() bool {
	return r.Difference.IsZero()
}

// ReconcileWallet rebuilds the balance from the ledger and compares it with the stored one; it never writes.
type ReconcileWallet struct {
	tx       TxManager
	wallets  WalletRepository
	ledger   LedgerRepository
	reporter ReconciliationReporter
}

func NewReconcileWallet(tx TxManager, wallets WalletRepository, ledger LedgerRepository, reporter ReconciliationReporter) *ReconcileWallet {
	return &ReconcileWallet{tx: tx, wallets: wallets, ledger: ledger, reporter: reporter}
}

func (uc *ReconcileWallet) Execute(ctx context.Context, walletID string) (Reconciliation, error) {
	var (
		wallet  *domain.Wallet
		summary LedgerSummary
	)
	// Two queries, one snapshot: a bet committed between them would otherwise show up in the ledger only.
	err := uc.tx.WithinSnapshot(ctx, func(ctx context.Context) error {
		var err error
		if wallet, err = uc.wallets.Get(ctx, walletID); err != nil {
			return err
		}
		summary, err = uc.ledger.Summarize(ctx, walletID)
		return err
	})
	if err != nil {
		return Reconciliation{}, err
	}

	calculated, err := domain.MoneyFromMinorUnits(summary.NetMinorUnits, wallet.Currency())
	if err != nil {
		return Reconciliation{}, err
	}
	difference, err := wallet.Balance().Sub(calculated)
	if err != nil {
		return Reconciliation{}, err
	}

	result := Reconciliation{
		WalletID:          wallet.ID(),
		StoredBalance:     wallet.Balance(),
		CalculatedBalance: calculated,
		Difference:        difference,
		CheckedEntries:    summary.Entries,
	}
	uc.reporter.Report(ctx, result)
	return result, nil
}
