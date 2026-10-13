package domain

import (
	"errors"
	"testing"
)

type wagerScenario struct {
	t      *testing.T
	wallet *Wallet
	seq    int
}

func newScenario(t *testing.T, balance string) *wagerScenario {
	t.Helper()
	return &wagerScenario{t: t, wallet: openTestWallet(t, balance)}
}

func (s *wagerScenario) newTx(kind TransactionKind, amount, reference string, mutate func(*NewWagerTransactionParams)) *WagerTransaction {
	s.t.Helper()
	s.seq++
	suffix := string(rune('a' + s.seq))
	return newTestTransaction(s.t, func(p *NewWagerTransactionParams) {
		p.ID = "tx-" + suffix
		p.ExternalTransactionID = "ext-" + suffix
		p.IdempotencyKey = "provider-a:ext-" + suffix
		p.Kind = kind
		p.Money = mustMoney(s.t, amount, "BRL")
		p.ReferenceExternalTransactionID = reference
		if mutate != nil {
			mutate(p)
		}
	})
}

func (s *wagerScenario) apply(tx *WagerTransaction, ref Reference) *LedgerEntry {
	s.t.Helper()
	entry, err := ApplyWager(tx, s.wallet, ref, "entry-"+tx.ID(), testLater)
	if err != nil {
		s.t.Fatalf("ApplyWager(%s): %v", tx.Kind(), err)
	}
	return entry
}

func (s *wagerScenario) processed(kind TransactionKind, amount string, ref *WagerTransaction) *WagerTransaction {
	s.t.Helper()
	reference := ""
	if ref != nil {
		reference = ref.ExternalTransactionID()
	}
	tx := s.newTx(kind, amount, reference, nil)
	s.apply(tx, Reference{Transaction: ref})
	if tx.Status() != StatusProcessed {
		s.t.Fatalf("setup %s ended %s (%s)", kind, tx.Status(), tx.FailureCode())
	}
	return tx
}

func (s *wagerScenario) assertWallet(balance string, version int64) {
	s.t.Helper()
	if s.wallet.Balance().Amount() != balance || s.wallet.Version() != version {
		s.t.Errorf("wallet = %v v%d, want %s v%d", s.wallet.Balance(), s.wallet.Version(), balance, version)
	}
}

func assertProcessed(t *testing.T, tx *WagerTransaction, resultBalance string) {
	t.Helper()
	if tx.Status() != StatusProcessed {
		t.Fatalf("Status() = %s (%s), want PROCESSED", tx.Status(), tx.FailureCode())
	}
	if got, observed := tx.ResultBalance(); !observed || got.Amount() != resultBalance {
		t.Errorf("ResultBalance() = %v, %v; want %s", got, observed, resultBalance)
	}
}

func assertRejected(t *testing.T, tx *WagerTransaction, entry *LedgerEntry, code FailureCode) {
	t.Helper()
	if tx.Status() != StatusRejected || tx.FailureCode() != code {
		t.Fatalf("outcome = %s/%q, want REJECTED/%q", tx.Status(), tx.FailureCode(), code)
	}
	if entry != nil {
		t.Error("a rejected transaction must not produce a ledger entry")
	}
}

func TestBetDebitsTheWallet(t *testing.T) {
	s := newScenario(t, "100.00")
	bet := s.newTx(KindBet, "25.00", "", nil)

	entry := s.apply(bet, Reference{})

	assertProcessed(t, bet, "75.00")
	s.assertWallet("75.00", 2)
	if entry == nil || entry.Direction() != LedgerDebit || entry.TransactionID() != bet.ID() {
		t.Fatalf("entry = %+v, want a debit for the bet", entry)
	}
	if entry.BalanceBefore().Amount() != "100.00" || entry.BalanceAfter().Amount() != "75.00" {
		t.Errorf("entry balances = %v -> %v", entry.BalanceBefore(), entry.BalanceAfter())
	}
}

func TestBetWithoutFundsIsRejected(t *testing.T) {
	s := newScenario(t, "100.00")
	bet := s.newTx(KindBet, "100.01", "", nil)

	entry := s.apply(bet, Reference{})

	assertRejected(t, bet, entry, FailureInsufficientFunds)
	s.assertWallet("100.00", 1)
	if got, observed := bet.ResultBalance(); !observed || got.Amount() != "100.00" {
		t.Errorf("ResultBalance() = %v, %v; want the untouched balance", got, observed)
	}
}

func TestTwoBetsOfEightyOverOneHundred(t *testing.T) {
	s := newScenario(t, "100.00")
	first := s.newTx(KindBet, "80.00", "", nil)
	second := s.newTx(KindBet, "80.00", "", nil)

	firstEntry := s.apply(first, Reference{})
	secondEntry := s.apply(second, Reference{})

	assertProcessed(t, first, "20.00")
	assertRejected(t, second, secondEntry, FailureInsufficientFunds)
	if firstEntry == nil {
		t.Error("the processed bet must produce the single debit")
	}
	s.assertWallet("20.00", 2)
}

func TestWinCreditsTheWallet(t *testing.T) {
	s := newScenario(t, "100.00")
	win := s.newTx(KindWin, "40.00", "", nil)

	entry := s.apply(win, Reference{})

	assertProcessed(t, win, "140.00")
	s.assertWallet("140.00", 2)
	if entry == nil || entry.Direction() != LedgerCredit {
		t.Errorf("entry = %+v, want a credit", entry)
	}
}

func TestWinMayReferenceABetOfTheSameRound(t *testing.T) {
	s := newScenario(t, "100.00")
	bet := s.processed(KindBet, "25.00", nil)
	win := s.newTx(KindWin, "60.00", bet.ExternalTransactionID(), nil)

	s.apply(win, Reference{Transaction: bet})

	assertProcessed(t, win, "135.00")
	if win.ReferenceTransactionID() != bet.ID() {
		t.Errorf("ReferenceTransactionID() = %q, want %q", win.ReferenceTransactionID(), bet.ID())
	}
}

func TestLossMovesNothing(t *testing.T) {
	s := newScenario(t, "100.00")
	loss := s.newTx(KindLoss, "0.00", "", nil)

	entry := s.apply(loss, Reference{})

	assertProcessed(t, loss, "100.00")
	s.assertWallet("100.00", 1)
	if entry != nil {
		t.Error("LOSS must not produce a ledger entry")
	}
	if !s.wallet.UpdatedAt().Equal(testNow) {
		t.Error("LOSS must not touch the wallet")
	}
}

func TestRefundReturnsTheBet(t *testing.T) {
	s := newScenario(t, "100.00")
	bet := s.processed(KindBet, "25.00", nil)
	refund := s.newTx(KindRefund, "25.00", bet.ExternalTransactionID(), nil)

	entry := s.apply(refund, Reference{Transaction: bet})

	assertProcessed(t, refund, "100.00")
	s.assertWallet("100.00", 3)
	if entry == nil || entry.Direction() != LedgerCredit {
		t.Errorf("entry = %+v, want a credit", entry)
	}
	if refund.ReferenceTransactionID() != bet.ID() {
		t.Errorf("ReferenceTransactionID() = %q, want %q", refund.ReferenceTransactionID(), bet.ID())
	}
}

func TestRollbackMovesAgainstTheOriginal(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(*wagerScenario) *WagerTransaction
		amount        string
		wantDirection LedgerDirection
		wantBalance   string
	}{
		{
			name:          "of a BET credits",
			setup:         func(s *wagerScenario) *WagerTransaction { return s.processed(KindBet, "25.00", nil) },
			amount:        "25.00",
			wantDirection: LedgerCredit,
			wantBalance:   "100.00",
		},
		{
			name:          "of a WIN debits",
			setup:         func(s *wagerScenario) *WagerTransaction { return s.processed(KindWin, "40.00", nil) },
			amount:        "40.00",
			wantDirection: LedgerDebit,
			wantBalance:   "100.00",
		},
		{
			name: "of a REFUND debits",
			setup: func(s *wagerScenario) *WagerTransaction {
				bet := s.processed(KindBet, "25.00", nil)
				return s.processed(KindRefund, "25.00", bet)
			},
			amount:        "25.00",
			wantDirection: LedgerDebit,
			wantBalance:   "75.00",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScenario(t, "100.00")
			original := tt.setup(s)
			rollback := s.newTx(KindRollback, tt.amount, original.ExternalTransactionID(), nil)

			entry := s.apply(rollback, Reference{Transaction: original})

			assertProcessed(t, rollback, tt.wantBalance)
			if entry == nil || entry.Direction() != tt.wantDirection {
				t.Errorf("entry = %+v, want %s", entry, tt.wantDirection)
			}
		})
	}
}

func TestRollbackThatWouldOverdrawHasItsOwnFailureCode(t *testing.T) {
	s := newScenario(t, "0.00")
	win := s.processed(KindWin, "50.00", nil)
	s.processed(KindBet, "30.00", nil)
	rollback := s.newTx(KindRollback, "50.00", win.ExternalTransactionID(), nil)

	entry := s.apply(rollback, Reference{Transaction: win})

	assertRejected(t, rollback, entry, FailureReversalInsufficientFunds)
	s.assertWallet("20.00", 3)
}

func TestReversalWaitsForAReferenceThatHasNotArrived(t *testing.T) {
	s := newScenario(t, "100.00")
	refund := s.newTx(KindRefund, "25.00", "ext-late", nil)

	if entry := s.apply(refund, Reference{}); entry != nil {
		t.Error("a waiting transaction must not produce a ledger entry")
	}
	if refund.Status() != StatusPendingReference {
		t.Fatalf("Status() = %s, want PENDING_REFERENCE", refund.Status())
	}
	s.assertWallet("100.00", 1)

	s.apply(refund, Reference{})
	if refund.Status() != StatusPendingReference {
		t.Fatalf("a second attempt changed the status to %s", refund.Status())
	}

	bet := s.processed(KindBet, "25.00", nil)
	entry := s.apply(refund, Reference{Transaction: bet})

	assertProcessed(t, refund, "100.00")
	if entry == nil || entry.Direction() != LedgerCredit {
		t.Errorf("entry = %+v, want the refund credit", entry)
	}
}

func TestReversalWaitsWhileTheReferenceIsStillPending(t *testing.T) {
	s := newScenario(t, "100.00")
	pendingBet := s.newTx(KindBet, "25.00", "", nil)
	refund := s.newTx(KindRefund, "25.00", pendingBet.ExternalTransactionID(), nil)

	s.apply(refund, Reference{Transaction: pendingBet})

	if refund.Status() != StatusPendingReference {
		t.Errorf("Status() = %s, want PENDING_REFERENCE", refund.Status())
	}
	s.assertWallet("100.00", 1)
}

func TestReferenceRejections(t *testing.T) {
	tests := []struct {
		name     string
		kind     TransactionKind
		amount   string
		original func(*wagerScenario) *WagerTransaction
		reversed bool
		mutate   func(*NewWagerTransactionParams)
		want     FailureCode
	}{
		{
			name:   "reference was rejected",
			kind:   KindRefund,
			amount: "500.00",
			original: func(s *wagerScenario) *WagerTransaction {
				bet := s.newTx(KindBet, "500.00", "", nil)
				s.apply(bet, Reference{})
				return bet
			},
			want: FailureReferenceNotProcessed,
		},
		{
			name:     "refund with a different amount",
			kind:     KindRefund,
			amount:   "24.99",
			original: func(s *wagerScenario) *WagerTransaction { return s.processed(KindBet, "25.00", nil) },
			want:     FailureReferenceAmountMismatch,
		},
		{
			name:     "refund of a win",
			kind:     KindRefund,
			amount:   "25.00",
			original: func(s *wagerScenario) *WagerTransaction { return s.processed(KindWin, "25.00", nil) },
			want:     FailureReferenceKindNotAllowed,
		},
		{
			name:     "win referencing a win",
			kind:     KindWin,
			amount:   "25.00",
			original: func(s *wagerScenario) *WagerTransaction { return s.processed(KindWin, "25.00", nil) },
			want:     FailureReferenceKindNotAllowed,
		},
		{
			name:   "rollback of a rollback",
			kind:   KindRollback,
			amount: "25.00",
			original: func(s *wagerScenario) *WagerTransaction {
				bet := s.processed(KindBet, "25.00", nil)
				return s.processed(KindRollback, "25.00", bet)
			},
			want: FailureReferenceKindNotAllowed,
		},
		{
			name:     "reference already reversed",
			kind:     KindRollback,
			amount:   "25.00",
			original: func(s *wagerScenario) *WagerTransaction { return s.processed(KindBet, "25.00", nil) },
			reversed: true,
			want:     FailureAlreadyReversed,
		},
		{
			name:     "reference from another round",
			kind:     KindRefund,
			amount:   "25.00",
			original: func(s *wagerScenario) *WagerTransaction { return s.processed(KindBet, "25.00", nil) },
			mutate:   func(p *NewWagerTransactionParams) { p.RoundID = "round-2" },
			want:     FailureReferenceMismatch,
		},
		{
			name:     "reference from another provider",
			kind:     KindRefund,
			amount:   "25.00",
			original: func(s *wagerScenario) *WagerTransaction { return s.processed(KindBet, "25.00", nil) },
			mutate:   func(p *NewWagerTransactionParams) { p.ProviderID = "provider-b" },
			want:     FailureReferenceMismatch,
		},
		{
			name:   "reference from another wallet",
			kind:   KindRefund,
			amount: "25.00",
			original: func(s *wagerScenario) *WagerTransaction {
				other := s.newTx(KindBet, "25.00", "", func(p *NewWagerTransactionParams) { p.WalletID = "wallet-2" })
				if err := other.MarkProcessed(mustMoney(s.t, "0.00", "BRL"), testLater); err != nil {
					s.t.Fatalf("MarkProcessed: %v", err)
				}
				return other
			},
			want: FailureReferenceMismatch,
		},
		{
			name:   "reference in another currency",
			kind:   KindRefund,
			amount: "25.00",
			original: func(s *wagerScenario) *WagerTransaction {
				other := s.newTx(KindBet, "25.00", "", func(p *NewWagerTransactionParams) { p.Money = mustMoney(s.t, "25.00", "USD") })
				if err := other.MarkProcessed(mustMoney(s.t, "0.00", "USD"), testLater); err != nil {
					s.t.Fatalf("MarkProcessed: %v", err)
				}
				return other
			},
			want: FailureReferenceMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newScenario(t, "100.00")
			original := tt.original(s)
			balanceBefore, versionBefore := s.wallet.Balance().Amount(), s.wallet.Version()
			tx := s.newTx(tt.kind, tt.amount, original.ExternalTransactionID(), tt.mutate)

			entry := s.apply(tx, Reference{Transaction: original, Reversed: tt.reversed})

			assertRejected(t, tx, entry, tt.want)
			s.assertWallet(balanceBefore, versionBefore)
		})
	}
}

func TestWalletContextRejections(t *testing.T) {
	t.Run("another player", func(t *testing.T) {
		s := newScenario(t, "100.00")
		bet := s.newTx(KindBet, "25.00", "", func(p *NewWagerTransactionParams) { p.PlayerID = "player-2" })

		entry := s.apply(bet, Reference{})

		assertRejected(t, bet, entry, FailurePlayerMismatch)
		s.assertWallet("100.00", 1)
	})

	t.Run("another currency", func(t *testing.T) {
		s := newScenario(t, "100.00")
		bet := s.newTx(KindBet, "25.00", "", func(p *NewWagerTransactionParams) { p.Money = mustMoney(t, "25.00", "USD") })

		entry := s.apply(bet, Reference{})

		assertRejected(t, bet, entry, FailureCurrencyMismatch)
		s.assertWallet("100.00", 1)
	})

	t.Run("loss in another currency", func(t *testing.T) {
		s := newScenario(t, "100.00")
		loss := s.newTx(KindLoss, "0.00", "", func(p *NewWagerTransactionParams) { p.Money = mustMoney(t, "0.00", "USD") })

		entry := s.apply(loss, Reference{})

		assertRejected(t, loss, entry, FailureCurrencyMismatch)
	})

	t.Run("credit above the representable balance", func(t *testing.T) {
		s := newScenario(t, "92233720368547758.07")
		win := s.newTx(KindWin, "0.01", "", nil)

		entry := s.apply(win, Reference{})

		assertRejected(t, win, entry, FailureBalanceLimitExceeded)
		s.assertWallet("92233720368547758.07", 1)
	})
}

func TestApplyWagerRefusesMisuse(t *testing.T) {
	t.Run("terminal transaction", func(t *testing.T) {
		s := newScenario(t, "100.00")
		bet := s.processed(KindBet, "25.00", nil)

		_, err := ApplyWager(bet, s.wallet, Reference{}, "entry-again", testLater)

		if !errors.Is(err, ErrInvalidTransition) {
			t.Errorf("error = %v, want ErrInvalidTransition", err)
		}
		s.assertWallet("75.00", 2)
	})

	t.Run("wallet of another transaction", func(t *testing.T) {
		s := newScenario(t, "100.00")
		bet := s.newTx(KindBet, "25.00", "", func(p *NewWagerTransactionParams) { p.WalletID = "wallet-2" })

		_, err := ApplyWager(bet, s.wallet, Reference{}, "entry-x", testLater)

		if !errors.Is(err, ErrWalletMismatch) {
			t.Errorf("error = %v, want ErrWalletMismatch", err)
		}
		if bet.Status() != StatusPending {
			t.Errorf("Status() = %s, want PENDING", bet.Status())
		}
	})

	t.Run("internal opening", func(t *testing.T) {
		s := newScenario(t, "100.00")
		opening, err := RehydrateWagerTransaction(WagerTransactionState{
			ID: "tx-open", Kind: KindOpening, Status: StatusPending, WalletID: "wallet-1", PlayerID: "player-1",
			Money: mustMoney(t, "10.00", "BRL"), CreatedAt: testNow, UpdatedAt: testNow,
		})
		if err != nil {
			t.Fatalf("RehydrateWagerTransaction: %v", err)
		}

		_, err = ApplyWager(opening, s.wallet, Reference{}, "entry-x", testLater)

		assertValidationError(t, err, "kind")
		s.assertWallet("100.00", 1)
	})

	t.Run("stored bet carrying a reference", func(t *testing.T) {
		s := newScenario(t, "100.00")
		original := s.processed(KindBet, "25.00", nil)
		bet, err := RehydrateWagerTransaction(WagerTransactionState{
			ID: "tx-odd", Kind: KindBet, Status: StatusPending, WalletID: "wallet-1", PlayerID: "player-1",
			Money: mustMoney(t, "25.00", "BRL"), ProviderID: "provider-a", ExternalTransactionID: "ext-odd",
			RoundID: "round-1", ReferenceExternalTransactionID: original.ExternalTransactionID(),
			CreatedAt: testNow, UpdatedAt: testNow,
		})
		if err != nil {
			t.Fatalf("RehydrateWagerTransaction: %v", err)
		}

		entry := s.apply(bet, Reference{Transaction: original})

		assertRejected(t, bet, entry, FailureReferenceKindNotAllowed)
		s.assertWallet("75.00", 2)
	})

	t.Run("missing ledger entry id", func(t *testing.T) {
		s := newScenario(t, "100.00")
		bet := s.newTx(KindBet, "25.00", "", nil)

		_, err := ApplyWager(bet, s.wallet, Reference{}, "", testLater)

		assertValidationError(t, err, "id")
		if bet.Status() != StatusPending {
			t.Errorf("Status() = %s, want PENDING", bet.Status())
		}
		s.assertWallet("100.00", 1)
	})
}
