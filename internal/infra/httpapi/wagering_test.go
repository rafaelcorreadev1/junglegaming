package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

type wager struct {
	ProviderID  string `json:"providerId"`
	ExternalID  string `json:"externalTransactionId"`
	PlayerID    string `json:"playerId"`
	WalletID    string `json:"walletId"`
	RoundID     string `json:"roundId"`
	GameID      string `json:"gameId"`
	Kind        string `json:"kind"`
	Money       any    `json:"money,omitempty"`
	ReferenceID string `json:"referenceExternalTransactionId,omitempty"`
}

func newWager(walletID, externalID, kind, amount string) wager {
	return wager{
		ProviderID: "provider-a", ExternalID: externalID, PlayerID: playerID, WalletID: walletID,
		RoundID: "round-987", GameID: "fortune-chimp", Kind: kind,
		Money: map[string]string{"amount": amount, "currency": "BRL"},
	}
}

func (e *testEnv) send(key string, w wager) *httptest.ResponseRecorder {
	e.t.Helper()
	body, err := json.Marshal(w)
	if err != nil {
		e.t.Fatalf("marshal: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/wagering/transactions", strings.NewReader(string(body)))
	if key != "" {
		request.Header.Set(idempotencyKeyHeader, key)
	}
	token := w.ProviderID
	if token == "" {
		token = "provider-a"
	}
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	return recorder
}

// sendOK sends with the conventional key and expects the given status.
func (e *testEnv) sendOK(w wager, status int) wagerResultResponse {
	e.t.Helper()
	response := e.send(w.ProviderID+":"+w.ExternalID, w)
	if response.Code != status {
		e.t.Fatalf("%s %s: status %d, want %d (body %s)", w.Kind, w.ExternalID, response.Code, status, response.Body)
	}
	return decode[wagerResultResponse](e.t, response)
}

func (e *testEnv) balance(walletID string) (string, int64) {
	e.t.Helper()
	wallet := decode[walletResponse](e.t, e.do(http.MethodGet, "/wallets/"+walletID, ""))
	return wallet.Balance.Amount(), wallet.Version
}

func (e *testEnv) assertWallet(walletID, balance string, version int64) {
	e.t.Helper()
	if got, gotVersion := e.balance(walletID); got != balance || gotVersion != version {
		e.t.Errorf("wallet = %s v%d, want %s v%d", got, gotVersion, balance, version)
	}
}

func amountOf(money *domain.Money) string {
	if money == nil {
		return "<none>"
	}
	return money.Amount()
}

func TestBetDebitsTheWalletAndRecordsLedgerAndEvents(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("1000.00")

	result := env.sendOK(newWager(wallet.ID, "transaction-123", "BET", "25.00"), http.StatusOK)

	if result.Status != domain.StatusProcessed || amountOf(result.Balance) != "975.00" || result.IdempotentReplay || result.TransactionID == "" {
		t.Errorf("result = %+v", result)
	}
	env.assertWallet(wallet.ID, "975.00", 2)

	checks := map[string]int{
		"SELECT count(*) FROM wager_transactions WHERE kind = 'BET' AND status = 'PROCESSED' AND result_balance_minor = 97500":                   1,
		"SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'DEBIT' AND balance_before_minor = 100000 AND balance_after_minor = 97500": 1,
		"SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionProcessed' AND payload #>> '{data,kind}' = 'BET'":                1,
		"SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged' AND payload #>> '{data,walletVersion}' = '2'":              1,
		"SELECT count(*) FROM outbox_events": 4,
	}
	for query, want := range checks {
		if got := env.count(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}

func TestWinCreditsAndLossMovesNothing(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")

	win := env.sendOK(newWager(wallet.ID, "win-1", "WIN", "40.00"), http.StatusOK)
	if amountOf(win.Balance) != "140.00" {
		t.Errorf("win balance = %s", amountOf(win.Balance))
	}
	env.assertWallet(wallet.ID, "140.00", 2)

	loss := env.sendOK(newWager(wallet.ID, "loss-1", "LOSS", "0.00"), http.StatusOK)
	if loss.Status != domain.StatusProcessed || amountOf(loss.Balance) != "140.00" {
		t.Errorf("loss = %+v", loss)
	}
	env.assertWallet(wallet.ID, "140.00", 2)

	if n := env.count("SELECT count(*) FROM wallet_ledger_entries"); n != 2 {
		t.Errorf("ledger has %d entries, want 2 (opening and win)", n)
	}
	lossEvents := "SELECT count(*) FROM outbox_events WHERE payload #>> '{data,transactionId}' = $1"
	if n := env.count(lossEvents, loss.TransactionID); n != 1 {
		t.Errorf("LOSS produced %d events, want only WagerTransactionProcessed", n)
	}
}

func TestBusinessRejectionsArePersistedAndAnswered422(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("10.00")

	tests := []struct {
		name   string
		wager  wager
		code   domain.FailureCode
		mutate func(*wager)
	}{
		{name: "bet above the balance", wager: newWager(wallet.ID, "bet-1", "BET", "25.00"), code: domain.FailureInsufficientFunds},
		{name: "wallet of another player", wager: newWager(wallet.ID, "bet-2", "BET", "1.00"), code: domain.FailurePlayerMismatch,
			mutate: func(w *wager) { w.PlayerID = "someone-else" }},
		{name: "another currency", wager: newWager(wallet.ID, "bet-3", "BET", "1.00"), code: domain.FailureCurrencyMismatch,
			mutate: func(w *wager) { w.Money = map[string]string{"amount": "1.00", "currency": "USD"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.mutate != nil {
				tt.mutate(&tt.wager)
			}
			result := env.sendOK(tt.wager, http.StatusUnprocessableEntity)

			if result.Status != domain.StatusRejected || result.FailureCode != tt.code || amountOf(result.Balance) != "10.00" {
				t.Errorf("result = %+v, want REJECTED/%s reporting 10.00", result, tt.code)
			}
			stored := "SELECT count(*) FROM wager_transactions WHERE id = $1 AND status = 'REJECTED' AND failure_code = $2"
			if n := env.count(stored, result.TransactionID, string(tt.code)); n != 1 {
				t.Errorf("rejection was not persisted")
			}
			event := "SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionRejected' AND payload #>> '{data,transactionId}' = $1"
			if n := env.count(event, result.TransactionID); n != 1 {
				t.Errorf("rejection produced %d WagerTransactionRejected events", n)
			}

			replay := env.sendOK(tt.wager, http.StatusUnprocessableEntity)
			if !replay.IdempotentReplay || replay.TransactionID != result.TransactionID || replay.FailureCode != tt.code {
				t.Errorf("replay = %+v", replay)
			}
		})
	}

	env.assertWallet(wallet.ID, "10.00", 1)
	if n := env.count("SELECT count(*) FROM wallet_ledger_entries"); n != 1 {
		t.Errorf("rejections wrote to the ledger: %d entries", n)
	}
}

func TestReplayReturnsTheOriginalResult(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("1000.00")
	bet := newWager(wallet.ID, "transaction-123", "BET", "25.00")

	first := env.sendOK(bet, http.StatusOK)
	env.sendOK(newWager(wallet.ID, "transaction-124", "BET", "100.00"), http.StatusOK)
	env.sendOK(newWager(wallet.ID, "transaction-125", "WIN", "500.00"), http.StatusOK)

	replay := env.sendOK(bet, http.StatusOK)
	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID {
		t.Errorf("replay = %+v, want the first transaction", replay)
	}
	if amountOf(replay.Balance) != "975.00" {
		t.Errorf("replay balance = %s, want the 975.00 observed originally", amountOf(replay.Balance))
	}

	equivalent := bet
	equivalent.Money = map[string]string{"amount": "25", "currency": "BRL"}
	if normalized := env.sendOK(equivalent, http.StatusOK); !normalized.IdempotentReplay {
		t.Errorf(`"25" and "25.00" must hash alike: %+v`, normalized)
	}

	env.assertWallet(wallet.ID, "1375.00", 4)
	if n := env.count("SELECT count(*) FROM wager_transactions WHERE external_transaction_id = 'transaction-123'"); n != 1 {
		t.Errorf("replays created %d rows", n)
	}
}

func TestIdempotencyConflicts(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("1000.00")
	bet := newWager(wallet.ID, "transaction-123", "BET", "25.00")
	key := "provider-a:transaction-123"
	env.sendOK(bet, http.StatusOK)

	changes := map[string]func(*wager){
		"amount":   func(w *wager) { w.Money = map[string]string{"amount": "26.00", "currency": "BRL"} },
		"round":    func(w *wager) { w.RoundID = "round-988" },
		"game":     func(w *wager) { w.GameID = "other-game" },
		"kind":     func(w *wager) { w.Kind = "WIN" },
		"player":   func(w *wager) { w.PlayerID = "someone-else" },
		"external": func(w *wager) { w.ExternalID = "transaction-999" },
	}
	for field, change := range changes {
		t.Run("same key with another "+field, func(t *testing.T) {
			changed := bet
			change(&changed)
			assertError(t, env.send(key, changed), http.StatusConflict, "IDEMPOTENCY_KEY_CONFLICT", "")
		})
	}

	t.Run("same operation with another key", func(t *testing.T) {
		assertError(t, env.send("another-key", bet), http.StatusConflict, "EXTERNAL_TRANSACTION_CONFLICT", "")
	})

	t.Run("another provider may reuse key and external id", func(t *testing.T) {
		other := bet
		other.ProviderID = "provider-b"
		if response := env.send(key, other); response.Code != http.StatusOK {
			t.Errorf("status = %d, body %s", response.Code, response.Body)
		}
	})

	env.assertWallet(wallet.ID, "950.00", 3)
}

func TestSameBetFiftyTimesInParallelDebitsOnce(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("1000.00")
	bet := newWager(wallet.ID, "transaction-123", "BET", "25.00")
	const requests = 50

	results := make(chan *httptest.ResponseRecorder, requests)
	var wg sync.WaitGroup
	for range requests {
		wg.Go(func() { results <- env.send("provider-a:transaction-123", bet) })
	}
	wg.Wait()
	close(results)

	originals, ids := 0, map[string]bool{}
	for response := range results {
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", response.Code, response.Body)
		}
		result := decode[wagerResultResponse](t, response)
		if amountOf(result.Balance) != "975.00" {
			t.Errorf("balance = %s, want 975.00 in every answer", amountOf(result.Balance))
		}
		if !result.IdempotentReplay {
			originals++
		}
		ids[result.TransactionID] = true
	}

	if originals != 1 || len(ids) != 1 {
		t.Errorf("%d answers were not replays and %d transaction ids were returned; want 1 and 1", originals, len(ids))
	}
	env.assertWallet(wallet.ID, "975.00", 2)
	if n := env.count("SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'DEBIT'"); n != 1 {
		t.Errorf("ledger has %d debits, want 1", n)
	}
}

func TestTwoBetsOfEightyOverOneHundredInParallel(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	bets := []wager{
		newWager(wallet.ID, "bet-a", "BET", "80.00"),
		newWager(wallet.ID, "bet-b", "BET", "80.00"),
	}

	responses := make([]*httptest.ResponseRecorder, len(bets))
	var wg sync.WaitGroup
	for i, bet := range bets {
		wg.Go(func() { responses[i] = env.send(bet.ProviderID+":"+bet.ExternalID, bet) })
	}
	wg.Wait()

	outcome := map[domain.TransactionStatus]int{}
	first := make([]wagerResultResponse, len(bets))
	for i, response := range responses {
		first[i] = decode[wagerResultResponse](t, response)
		outcome[first[i].Status]++
		if first[i].Status == domain.StatusRejected && (response.Code != http.StatusUnprocessableEntity || first[i].FailureCode != domain.FailureInsufficientFunds) {
			t.Errorf("rejected bet = %d %+v", response.Code, first[i])
		}
	}
	if outcome[domain.StatusProcessed] != 1 || outcome[domain.StatusRejected] != 1 {
		t.Fatalf("outcome = %v, want one PROCESSED and one REJECTED", outcome)
	}

	assertFinalState := func() {
		t.Helper()
		env.assertWallet(wallet.ID, "20.00", 2)
		if n := env.count("SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'DEBIT'"); n != 1 {
			t.Errorf("ledger has %d debits, want 1", n)
		}
	}
	assertFinalState()

	for i, bet := range bets {
		replay := decode[wagerResultResponse](t, env.send(bet.ProviderID+":"+bet.ExternalID, bet))
		if !replay.IdempotentReplay || replay.Status != first[i].Status || replay.TransactionID != first[i].TransactionID ||
			amountOf(replay.Balance) != amountOf(first[i].Balance) {
			t.Errorf("resending %s = %+v, want the original %+v", bet.ExternalID, replay, first[i])
		}
	}
	assertFinalState()
}

func TestWalletsAdvanceIndependently(t *testing.T) {
	env := newTestEnv(t)
	const wallets, betsPerWallet = 5, 10

	ids := make([]string, wallets)
	for i := range ids {
		response := env.do(http.MethodPost, "/wallets", openBody(fmt.Sprintf("player-%d", i), "100.00", "BRL"))
		ids[i] = decode[walletResponse](t, response).ID
	}

	var wg sync.WaitGroup
	for i, walletID := range ids {
		for n := range betsPerWallet {
			wg.Go(func() {
				bet := newWager(walletID, fmt.Sprintf("bet-%d-%d", i, n), "BET", "1.00")
				bet.PlayerID = fmt.Sprintf("player-%d", i)
				if response := env.send(bet.ExternalID, bet); response.Code != http.StatusOK {
					t.Errorf("bet %s: %d %s", bet.ExternalID, response.Code, response.Body)
				}
			})
		}
	}
	wg.Wait()

	for _, walletID := range ids {
		env.assertWallet(walletID, "90.00", betsPerWallet+1)
	}
}

func TestWagerRequestValidation(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	valid := newWager(wallet.ID, "transaction-123", "BET", "25.00")

	tests := []struct {
		name   string
		key    string
		mutate func(*wager)
		status int
		code   string
		field  string
	}{
		{"missing idempotency key", "", nil, http.StatusBadRequest, "INVALID_REQUEST", "Idempotency-Key"},
		{"idempotency key too long", strings.Repeat("k", 256), nil, http.StatusBadRequest, "INVALID_REQUEST", "idempotencyKey"},
		{"internal kind", "k", func(w *wager) { w.Kind = "OPENING" }, http.StatusBadRequest, "INVALID_REQUEST", "kind"},
		{"unknown kind", "k", func(w *wager) { w.Kind = "CASHOUT" }, http.StatusBadRequest, "INVALID_REQUEST", "kind"},
		{"lowercase kind", "k", func(w *wager) { w.Kind = "bet" }, http.StatusBadRequest, "INVALID_REQUEST", "kind"},
		{"missing provider", "k", func(w *wager) { w.ProviderID = "" }, http.StatusForbidden, "FORBIDDEN", ""},
		{"missing external id", "k", func(w *wager) { w.ExternalID = "" }, http.StatusBadRequest, "INVALID_REQUEST", "externalTransactionId"},
		{"missing player", "k", func(w *wager) { w.PlayerID = "" }, http.StatusBadRequest, "INVALID_REQUEST", "playerId"},
		{"missing round", "k", func(w *wager) { w.RoundID = "" }, http.StatusBadRequest, "INVALID_REQUEST", "roundId"},
		{"missing game", "k", func(w *wager) { w.GameID = "" }, http.StatusBadRequest, "INVALID_REQUEST", "gameId"},
		{"missing money", "k", func(w *wager) { w.Money = nil }, http.StatusBadRequest, "INVALID_REQUEST", "money"},
		{"bet of zero", "k", func(w *wager) { w.Money = map[string]string{"amount": "0.00", "currency": "BRL"} }, http.StatusBadRequest, "INVALID_REQUEST", "money"},
		{"negative bet", "k", func(w *wager) { w.Money = map[string]string{"amount": "-5.00", "currency": "BRL"} }, http.StatusBadRequest, "INVALID_REQUEST", "money"},
		{"loss with an amount", "k", func(w *wager) { w.Kind = "LOSS" }, http.StatusBadRequest, "INVALID_REQUEST", "money"},
		{"amount as a JSON number", "k", func(w *wager) { w.Money = map[string]any{"amount": 25.5, "currency": "BRL"} }, http.StatusBadRequest, "INVALID_REQUEST", ""},
		{"bet carrying a reference", "k", func(w *wager) { w.ReferenceID = "other" }, http.StatusBadRequest, "INVALID_REQUEST", "referenceExternalTransactionId"},
		{"refund without reference", "k", func(w *wager) { w.Kind = "REFUND" }, http.StatusBadRequest, "INVALID_REQUEST", "referenceExternalTransactionId"},
		{"malformed wallet id", "k", func(w *wager) { w.WalletID = "not-a-uuid" }, http.StatusBadRequest, "INVALID_REQUEST", "walletId"},
		{"unknown wallet", "k", func(w *wager) { w.WalletID = "0192f291-27dd-7d3f-8071-5f8685deef37" }, http.StatusNotFound, "WALLET_NOT_FOUND", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := valid
			if tt.mutate != nil {
				tt.mutate(&request)
			}
			assertError(t, env.send(tt.key, request), tt.status, tt.code, tt.field)
		})
	}

	if n := env.count("SELECT count(*) FROM wager_transactions WHERE kind <> 'OPENING'"); n != 0 {
		t.Errorf("invalid requests persisted %d transactions", n)
	}
	env.assertWallet(wallet.ID, "100.00", 1)
}

func TestReferencesAreResolvedWhenTheOperationArrives(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("100.00")
	env.sendOK(newWager(wallet.ID, "bet-1", "BET", "30.00"), http.StatusOK)

	win := newWager(wallet.ID, "win-1", "WIN", "60.00")
	win.ReferenceID = "bet-1"
	if result := env.sendOK(win, http.StatusOK); amountOf(result.Balance) != "130.00" {
		t.Errorf("win referencing the bet = %+v", result)
	}

	refund := newWager(wallet.ID, "refund-1", "REFUND", "30.00")
	refund.ReferenceID = "bet-1"
	if result := env.sendOK(refund, http.StatusOK); amountOf(result.Balance) != "160.00" {
		t.Errorf("refund = %+v", result)
	}

	rollback := newWager(wallet.ID, "rollback-1", "ROLLBACK", "30.00")
	rollback.ReferenceID = "bet-1"
	if result := env.sendOK(rollback, http.StatusUnprocessableEntity); result.FailureCode != domain.FailureAlreadyReversed {
		t.Errorf("second reversal = %+v, want ALREADY_REVERSED", result)
	}

	early := newWager(wallet.ID, "refund-2", "REFUND", "10.00")
	early.ReferenceID = "bet-not-yet"
	pending := env.sendOK(early, http.StatusAccepted)
	if pending.Status != domain.StatusPendingReference || pending.Balance != nil {
		t.Errorf("refund before its bet = %+v", pending)
	}
	event := "SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionPendingReference' AND payload #>> '{data,transactionId}' = $1"
	if n := env.count(event, pending.TransactionID); n != 1 {
		t.Errorf("pending reference produced %d events", n)
	}
	if replay := env.sendOK(early, http.StatusAccepted); !replay.IdempotentReplay || replay.TransactionID != pending.TransactionID {
		t.Errorf("replay of a pending operation = %+v", replay)
	}

	env.assertWallet(wallet.ID, "160.00", 4)
}

func TestTransactionQueries(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("10.00")
	rejected := env.sendOK(newWager(wallet.ID, "transaction-123", "BET", "25.00"), http.StatusUnprocessableEntity)

	byID := env.do(http.MethodGet, "/wagering/transactions/"+rejected.TransactionID, "")
	byExternal := env.do(http.MethodGet, "/providers/provider-a/wagering/transactions/transaction-123", "")
	for name, response := range map[string]*httptest.ResponseRecorder{"by id": byID, "by external id": byExternal} {
		if response.Code != http.StatusOK {
			t.Fatalf("%s: status %d, body %s", name, response.Code, response.Body)
		}
		tx := decode[wagerTransactionResponse](t, response)
		if tx.TransactionID != rejected.TransactionID || tx.Status != domain.StatusRejected || tx.FailureCode != domain.FailureInsufficientFunds ||
			tx.Kind != domain.KindBet || tx.Money.Amount() != "25.00" || amountOf(tx.Balance) != "10.00" ||
			tx.ProviderID != "provider-a" || tx.ExternalTransactionID != "transaction-123" || tx.WalletID != wallet.ID {
			t.Errorf("%s = %+v", name, tx)
		}
	}

	errors := []struct {
		name   string
		path   string
		status int
		code   string
		field  string
	}{
		{"unknown id", "/wagering/transactions/0192f291-27dd-7d3f-8071-5f8685deef37", http.StatusNotFound, "TRANSACTION_NOT_FOUND", ""},
		{"malformed id", "/wagering/transactions/123", http.StatusBadRequest, "INVALID_REQUEST", "transactionId"},
		{"unknown external id", "/providers/provider-a/wagering/transactions/nope", http.StatusNotFound, "TRANSACTION_NOT_FOUND", ""},
		{"external id of another provider", "/providers/provider-b/wagering/transactions/transaction-123", http.StatusNotFound, "TRANSACTION_NOT_FOUND", ""},
	}
	for _, tt := range errors {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, env.do(http.MethodGet, tt.path, ""), tt.status, tt.code, tt.field)
		})
	}
}
