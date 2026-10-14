package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
	"wagering-service/internal/infra/postgres"
	"wagering-service/internal/infra/system"
	"wagering-service/internal/testdb"
)

const playerID = "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1"

type testEnv struct {
	t       *testing.T
	pool    *pgxpool.Pool
	router  http.Handler
	tx      application.TxManager
	wallets application.WalletRepository
	wagers  application.WagerTransactionRepository
	ledger  application.LedgerRepository
	ids     application.IDGenerator
	clock   application.Clock
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	pool := testdb.NewPool(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	env := &testEnv{
		t:       t,
		pool:    pool,
		tx:      postgres.NewTxManager(pool),
		wallets: postgres.NewWalletRepository(pool),
		wagers:  postgres.NewWagerTransactionRepository(pool),
		ledger:  postgres.NewLedgerRepository(pool),
		ids:     system.UUIDGenerator{},
		clock:   system.Clock{},
	}
	outbox := postgres.NewOutboxRepository(pool)

	walletHandler := NewWalletHandler(
		application.NewOpenWallet(env.tx, env.wallets, env.wagers, env.ledger, outbox, env.clock, env.ids),
		application.NewGetWallet(env.wallets),
		application.NewListLedger(env.wallets, env.ledger),
		logger,
	)
	wageringHandler := NewWageringHandler(
		application.NewProcessWagerTransaction(env.tx, env.wallets, env.wagers, env.ledger, outbox, env.clock, env.ids),
		application.NewGetWagerTransaction(env.wagers),
		logger,
	)
	env.router = NewRouter(NewHealthHandler(pool, logger), walletHandler, wageringHandler, env.ids, logger)
	return env
}

func (e *testEnv) do(method, path, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	recorder := httptest.NewRecorder()
	e.router.ServeHTTP(recorder, request)
	return recorder
}

func (e *testEnv) count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("count: %v", err)
	}
	return n
}

func (e *testEnv) openWallet(amount string) walletResponse {
	e.t.Helper()
	response := e.do(http.MethodPost, "/wallets", openBody(playerID, amount, "BRL"))
	if response.Code != http.StatusCreated {
		e.t.Fatalf("open wallet: status %d, body %s", response.Code, response.Body)
	}
	return decode[walletResponse](e.t, response)
}

// placeBet writes through the repositories because the wagering endpoint only exists from spec 07 on.
func (e *testEnv) placeBet(walletID, amount string) {
	e.t.Helper()
	money, err := domain.ParseMoney(amount, "BRL")
	if err != nil {
		e.t.Fatalf("ParseMoney: %v", err)
	}
	err = e.tx.WithinTx(context.Background(), func(ctx context.Context) error {
		wallet, err := e.wallets.GetForUpdate(ctx, walletID)
		if err != nil {
			return err
		}
		external := e.ids.NewID()
		tx, err := domain.NewWagerTransaction(domain.NewWagerTransactionParams{
			ID: e.ids.NewID(), ProviderID: "provider-a", ExternalTransactionID: external, IdempotencyKey: external,
			PayloadHash: "hash", WalletID: wallet.ID(), PlayerID: wallet.PlayerID(), RoundID: "round-1", GameID: "game-1",
			Kind: domain.KindBet, Money: money, Now: e.clock.Now(),
		})
		if err != nil {
			return err
		}
		entry, err := domain.ApplyWager(tx, wallet, domain.Reference{}, e.ids.NewID(), e.clock.Now())
		if err != nil {
			return err
		}
		if entry == nil {
			return errors.New("bet did not move the balance")
		}
		if err := e.wagers.Insert(ctx, tx); err != nil {
			return err
		}
		if err := e.wallets.UpdateBalance(ctx, wallet); err != nil {
			return err
		}
		return e.ledger.Insert(ctx, *entry)
	})
	if err != nil {
		e.t.Fatalf("place bet: %v", err)
	}
}

func openBody(player, amount, currency string) string {
	return fmt.Sprintf(`{"playerId":%q,"initialBalance":{"amount":%q,"currency":%q}}`, player, amount, currency)
}

func decode[T any](t *testing.T, response *httptest.ResponseRecorder) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(response.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %s: %v", response.Body, err)
	}
	return value
}

func assertError(t *testing.T, response *httptest.ResponseRecorder, status int, code, field string) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", response.Code, status, response.Body)
	}
	body := decode[errorBody](t, response)
	if body.Error.Code != code || body.Error.Field != field || body.Error.Message == "" {
		t.Errorf("error = %+v, want code %q and field %q", body.Error, code, field)
	}
}

func TestOpenWalletWithPositiveBalanceRecordsTheOpeningCredit(t *testing.T) {
	env := newTestEnv(t)

	request := httptest.NewRequest(http.MethodPost, "/wallets", strings.NewReader(openBody(playerID, "1000.00", "BRL")))
	request.Header.Set(correlationIDHeader, "corr-from-client")
	response := httptest.NewRecorder()
	env.router.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", response.Code, response.Body)
	}
	wallet := decode[walletResponse](t, response)
	if wallet.PlayerID != playerID || wallet.Balance.String() != "1000.00 BRL" || wallet.Version != 1 || wallet.ID == "" {
		t.Errorf("wallet = %+v", wallet)
	}
	if got := response.Header().Get("Location"); got != "/wallets/"+wallet.ID {
		t.Errorf("Location = %q", got)
	}
	if got := response.Header().Get(correlationIDHeader); got != "corr-from-client" {
		t.Errorf("%s = %q, want the one sent by the client", correlationIDHeader, got)
	}

	checks := map[string]int{
		"SELECT count(*) FROM wallets": 1,
		"SELECT count(*) FROM wager_transactions WHERE kind = 'OPENING' AND status = 'PROCESSED' AND amount_minor = 100000 AND provider_id IS NULL": 1,
		"SELECT count(*) FROM wallet_ledger_entries WHERE direction = 'CREDIT' AND balance_before_minor = 0 AND balance_after_minor = 100000":       1,
		"SELECT count(*) FROM outbox_events": 2,
		"SELECT count(*) FROM outbox_events WHERE event_type = 'WagerTransactionProcessed' AND payload #>> '{data,kind}' = 'OPENING'":                  1,
		"SELECT count(*) FROM outbox_events WHERE event_type = 'WalletBalanceChanged' AND payload #>> '{data,walletVersion}' = '1'":                    1,
		"SELECT count(*) FROM outbox_events WHERE payload ->> 'correlationId' = 'corr-from-client' AND payload ->> 'aggregateId' = aggregate_id::text": 2,
		"SELECT count(*) FROM outbox_events WHERE payload #>> '{data,providerId}' IS NOT NULL":                                                         0,
	}
	for query, want := range checks {
		if got := env.count(query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}

	read := env.do(http.MethodGet, "/wallets/"+wallet.ID, "")
	if read.Code != http.StatusOK || decode[walletResponse](t, read) != wallet {
		t.Errorf("GET wallet = %d %s", read.Code, read.Body)
	}

	ledger := env.do(http.MethodGet, "/wallets/"+wallet.ID+"/ledger", "")
	page := decode[ledgerPageResponse](t, ledger)
	if ledger.Code != http.StatusOK || len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("ledger = %d %s", ledger.Code, ledger.Body)
	}
	entry := page.Items[0]
	if entry.Direction != domain.LedgerCredit || entry.Money.Amount() != "1000.00" ||
		entry.BalanceBefore.Amount() != "0.00" || entry.BalanceAfter.Amount() != "1000.00" || entry.WalletID != wallet.ID {
		t.Errorf("opening entry = %+v", entry)
	}
}

func TestOpenWalletWithZeroBalanceCreatesOnlyTheWallet(t *testing.T) {
	env := newTestEnv(t)

	wallet := env.openWallet("0.00")

	if wallet.Balance.String() != "0.00 BRL" || wallet.Version != 1 {
		t.Errorf("wallet = %+v", wallet)
	}
	for _, table := range []string{"wager_transactions", "wallet_ledger_entries", "outbox_events"} {
		if n := env.count("SELECT count(*) FROM " + table); n != 0 {
			t.Errorf("%s has %d rows, want 0", table, n)
		}
	}

	ledger := env.do(http.MethodGet, "/wallets/"+wallet.ID+"/ledger", "")
	if got := strings.TrimSpace(ledger.Body.String()); got != `{"items":[],"nextCursor":null}` {
		t.Errorf("empty ledger = %s", got)
	}
}

func TestOpenWalletTwiceForTheSamePlayerAndCurrencyConflicts(t *testing.T) {
	env := newTestEnv(t)
	env.openWallet("1000.00")

	second := env.do(http.MethodPost, "/wallets", openBody(playerID, "50.00", "BRL"))
	assertError(t, second, http.StatusConflict, "WALLET_ALREADY_EXISTS", "")

	if wallets, openings, events := env.count("SELECT count(*) FROM wallets"), env.count("SELECT count(*) FROM wager_transactions"), env.count("SELECT count(*) FROM outbox_events"); wallets != 1 || openings != 1 || events != 2 {
		t.Errorf("after the conflict: %d wallets, %d transactions, %d events", wallets, openings, events)
	}

	otherCurrency := env.do(http.MethodPost, "/wallets", openBody(playerID, "50.00", "USD"))
	if otherCurrency.Code != http.StatusCreated {
		t.Errorf("same player in another currency: status %d, body %s", otherCurrency.Code, otherCurrency.Body)
	}
}

func TestOpenWalletRejectsInvalidRequests(t *testing.T) {
	env := newTestEnv(t)

	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"malformed JSON", `{"playerId":`, ""},
		{"empty body", ``, ""},
		{"two objects", openBody(playerID, "1.00", "BRL") + openBody(playerID, "1.00", "BRL"), ""},
		{"unknown field", `{"playerId":"p","initialBalance":{"amount":"1.00","currency":"BRL"},"version":7}`, ""},
		{"missing player", openBody("", "1.00", "BRL"), "playerId"},
		{"missing initial balance", `{"playerId":"p"}`, "initialBalance"},
		{"negative balance", openBody(playerID, "-1.00", "BRL"), "balance"},
		{"amount as a JSON number", `{"playerId":"p","initialBalance":{"amount":10.5,"currency":"BRL"}}`, ""},
		{"three decimal places", openBody(playerID, "10.000", "BRL"), ""},
		{"scientific notation", openBody(playerID, "1e3", "BRL"), ""},
		{"unsupported currency", openBody(playerID, "10.00", "XXX"), ""},
		{"lowercase currency", openBody(playerID, "10.00", "brl"), ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, env.do(http.MethodPost, "/wallets", tt.body), http.StatusBadRequest, "INVALID_REQUEST", tt.field)
		})
	}

	if n := env.count("SELECT count(*) FROM wallets"); n != 0 {
		t.Errorf("invalid requests created %d wallets", n)
	}
}

func TestWalletReadErrors(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("10.00")
	unknown := "0192f291-27dd-7d3f-8071-5f8685deef37"

	tests := []struct {
		name   string
		path   string
		status int
		code   string
		field  string
	}{
		{"unknown wallet", "/wallets/" + unknown, http.StatusNotFound, "WALLET_NOT_FOUND", ""},
		{"malformed wallet id", "/wallets/not-a-uuid", http.StatusBadRequest, "INVALID_REQUEST", "walletId"},
		{"ledger of an unknown wallet", "/wallets/" + unknown + "/ledger", http.StatusNotFound, "WALLET_NOT_FOUND", ""},
		{"ledger with a malformed wallet id", "/wallets/1/ledger", http.StatusBadRequest, "INVALID_REQUEST", "walletId"},
		{"cursor not issued by the API", "/wallets/" + wallet.ID + "/ledger?cursor=abc", http.StatusBadRequest, "INVALID_REQUEST", "cursor"},
		{"limit of zero", "/wallets/" + wallet.ID + "/ledger?limit=0", http.StatusBadRequest, "INVALID_REQUEST", "limit"},
		{"limit that is not a number", "/wallets/" + wallet.ID + "/ledger?limit=ten", http.StatusBadRequest, "INVALID_REQUEST", "limit"},
		{"limit above the maximum", "/wallets/" + wallet.ID + "/ledger?limit=201", http.StatusBadRequest, "INVALID_REQUEST", "limit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertError(t, env.do(http.MethodGet, tt.path, ""), tt.status, tt.code, tt.field)
		})
	}
}

func TestLedgerPaginationNeitherRepeatsNorSkipsEntries(t *testing.T) {
	env := newTestEnv(t)
	wallet := env.openWallet("1000.00")
	for range 7 {
		env.placeBet(wallet.ID, "10.00")
	}

	var seen []string
	fetch := func(cursor string) ledgerPageResponse {
		path := "/wallets/" + wallet.ID + "/ledger?limit=3"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		response := env.do(http.MethodGet, path, "")
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, response.Code, response.Body)
		}
		page := decode[ledgerPageResponse](t, response)
		for _, item := range page.Items {
			seen = append(seen, item.ID)
		}
		return page
	}

	first := fetch("")
	if len(first.Items) != 3 || first.NextCursor == nil {
		t.Fatalf("first page = %d items, cursor %v", len(first.Items), first.NextCursor)
	}
	if first.Items[0].Direction != domain.LedgerCredit {
		t.Errorf("the oldest entry must be the opening credit, got %s", first.Items[0].Direction)
	}

	env.placeBet(wallet.ID, "10.00")

	page := first
	for page.NextCursor != nil {
		page = fetch(*page.NextCursor)
	}

	rows, err := env.pool.Query(context.Background(), "SELECT id::text FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY sequence", wallet.ID)
	if err != nil {
		t.Fatalf("read ledger: %v", err)
	}
	defer rows.Close()
	var stored []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		stored = append(stored, id)
	}

	if len(stored) != 9 {
		t.Fatalf("ledger has %d entries, want 9", len(stored))
	}
	if strings.Join(seen, ",") != strings.Join(stored, ",") {
		t.Errorf("pages returned\n%v\nwant\n%v", seen, stored)
	}

	balance := decode[walletResponse](t, env.do(http.MethodGet, "/wallets/"+wallet.ID, ""))
	if balance.Balance.String() != "920.00 BRL" || balance.Version != 9 {
		t.Errorf("wallet after 8 bets = %+v", balance)
	}
}

func TestErrorsMapToTheDocumentedStatus(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		retryAfter string
	}{
		{"transient storage failure", fmt.Errorf("%w: connection refused", application.ErrUnavailable), http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "1"},
		{"expired deadline", context.DeadlineExceeded, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "1"},
		{"unknown transaction", application.ErrTransactionNotFound, http.StatusNotFound, "TRANSACTION_NOT_FOUND", ""},
		{"unexpected error", errors.New(`pq: relation "wallets" does not exist`), http.StatusInternalServerError, "INTERNAL_ERROR", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			writeError(response, httptest.NewRequest(http.MethodGet, "/", nil), logger, tt.err)

			if response.Code != tt.status || response.Header().Get("Retry-After") != tt.retryAfter {
				t.Errorf("status = %d, Retry-After = %q", response.Code, response.Header().Get("Retry-After"))
			}
			body := decode[errorBody](t, response)
			if body.Error.Code != tt.code {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.code)
			}
			if tt.status == http.StatusInternalServerError && strings.Contains(response.Body.String(), "relation") {
				t.Errorf("internal detail leaked to the client: %s", response.Body)
			}
		})
	}
}

func TestMiddlewares(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chain := func(handler http.HandlerFunc) http.Handler {
		return correlationID(system.UUIDGenerator{})(recoverPanic(logger)(handler))
	}

	t.Run("a panic becomes a 500 with a correlation id", func(t *testing.T) {
		response := httptest.NewRecorder()
		chain(func(http.ResponseWriter, *http.Request) { panic("boom") }).
			ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

		if response.Code != http.StatusInternalServerError || response.Header().Get(correlationIDHeader) == "" {
			t.Errorf("status = %d, correlation id = %q", response.Code, response.Header().Get(correlationIDHeader))
		}
	})

	t.Run("an oversized correlation id is replaced", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodGet, "/", nil)
		request.Header.Set(correlationIDHeader, strings.Repeat("x", maxCorrelationIDLength+1))
		response := httptest.NewRecorder()
		var seen string
		chain(func(_ http.ResponseWriter, r *http.Request) { seen = correlationIDFrom(r.Context()) }).ServeHTTP(response, request)

		if len(seen) > maxCorrelationIDLength || seen != response.Header().Get(correlationIDHeader) {
			t.Errorf("correlation id = %q, header = %q", seen, response.Header().Get(correlationIDHeader))
		}
	})

	t.Run("the clock stores what PostgreSQL can hold", func(t *testing.T) {
		now := system.Clock{}.Now()
		if now.Location() != time.UTC || now.Nanosecond()%1000 != 0 {
			t.Errorf("Now() = %v, want UTC truncated to the microsecond", now)
		}
	})
}
