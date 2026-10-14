package migrations_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	uniqueViolation     = "23505"
	checkViolation      = "23514"
	foreignKeyViolation = "23503"
	restrictViolation   = "23001"
)

var testNow = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)

// connect isolates each test in its own schema, so tests never see each other's tables.
func connect(t *testing.T) *pgx.Conn {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	schema := fmt.Sprintf("migrations_test_%d", time.Now().UnixNano())
	mustExec(t, conn, fmt.Sprintf("CREATE SCHEMA %s", schema))
	mustExec(t, conn, fmt.Sprintf("SET search_path TO %s", schema))

	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, fmt.Sprintf("DROP SCHEMA %s CASCADE", schema))
		_ = conn.Close(ctx)
	})
	return conn
}

func mustExec(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := conn.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("exec %q: %v", sql, err)
	}
}

func migrate(t *testing.T, conn *pgx.Conn, direction string) {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}

	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), "."+direction+".sql") {
			files = append(files, entry.Name())
		}
	}
	if len(files) == 0 {
		t.Fatalf("no %s migrations found", direction)
	}
	slices.Sort(files)
	if direction == "down" {
		slices.Reverse(files)
	}

	for _, file := range files {
		sql, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		// The simple protocol is the one that accepts several statements in one call.
		if _, err := conn.PgConn().Exec(context.Background(), string(sql)).ReadAll(); err != nil {
			t.Fatalf("apply %s: %v", file, err)
		}
	}
}

func countObjects(t *testing.T, conn *pgx.Conn) int {
	t.Helper()
	var count int
	err := conn.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		         WHERE n.nspname = current_schema())
		     + (SELECT count(*) FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
		         WHERE n.nspname = current_schema())`).Scan(&count)
	if err != nil {
		t.Fatalf("count objects: %v", err)
	}
	return count
}

func id(n int) string {
	return fmt.Sprintf("00000000-0000-7000-8000-%012d", n)
}

type row map[string]any

func (r row) with(overrides row) row {
	merged := row{}
	for column, value := range r {
		merged[column] = value
	}
	for column, value := range overrides {
		merged[column] = value
	}
	return merged
}

func insert(conn *pgx.Conn, table string, r row) error {
	columns := make([]string, 0, len(r))
	for column := range r {
		columns = append(columns, column)
	}
	slices.Sort(columns)

	placeholders := make([]string, len(columns))
	args := make([]any, len(columns))
	for i, column := range columns {
		placeholders[i] = fmt.Sprintf("$%d", i+1)
		args[i] = r[column]
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), strings.Join(placeholders, ", "))
	_, err := conn.Exec(context.Background(), sql, args...)
	return err
}

func walletRow(n int, playerID string) row {
	return row{
		"id": id(n), "player_id": playerID, "currency": "BRL", "balance_minor": int64(10000),
		"version": int64(1), "created_at": testNow, "updated_at": testNow,
	}
}

func transactionRow(n, walletN int, kind string) row {
	return row{
		"id": id(n), "kind": kind, "status": "PROCESSED", "wallet_id": id(walletN), "player_id": "player-1",
		"amount_minor": int64(2500), "currency": "BRL",
		"result_balance_minor": int64(7500), "result_balance_currency": "BRL",
		"provider_id": "provider-a", "external_transaction_id": fmt.Sprintf("ext-%d", n),
		"idempotency_key": fmt.Sprintf("key-%d", n), "payload_hash": "hash",
		"round_id": "round-1", "game_id": "game-1",
		"created_at": testNow, "updated_at": testNow,
	}
}

func openingRow(n, walletN int) row {
	return row{
		"id": id(n), "kind": "OPENING", "status": "PROCESSED", "wallet_id": id(walletN), "player_id": "player-1",
		"amount_minor": int64(10000), "currency": "BRL",
		"result_balance_minor": int64(10000), "result_balance_currency": "BRL",
		"created_at": testNow, "updated_at": testNow,
	}
}

func ledgerRow(n, walletN, transactionN int) row {
	return row{
		"id": id(n), "wallet_id": id(walletN), "transaction_id": id(transactionN), "direction": "DEBIT",
		"amount_minor": int64(2500), "currency": "BRL",
		"balance_before_minor": int64(10000), "balance_after_minor": int64(7500), "created_at": testNow,
	}
}

func assertViolation(t *testing.T, err error, code, constraint string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("error = %v, want a PostgreSQL error with SQLSTATE %s", err, code)
	}
	if pgErr.Code != code {
		t.Errorf("SQLSTATE = %s (%s), want %s", pgErr.Code, pgErr.Message, code)
	}
	if constraint != "" && pgErr.ConstraintName != constraint {
		t.Errorf("constraint = %q, want %q", pgErr.ConstraintName, constraint)
	}
}

func TestMigrationsApplyRevertAndApplyAgain(t *testing.T) {
	conn := connect(t)

	migrate(t, conn, "up")
	if countObjects(t, conn) == 0 {
		t.Fatal("up created nothing")
	}

	migrate(t, conn, "down")
	if left := countObjects(t, conn); left != 0 {
		t.Fatalf("down left %d objects behind", left)
	}

	migrate(t, conn, "up")
}

func TestSchemaEnforcesFinancialInvariants(t *testing.T) {
	conn := connect(t)
	migrate(t, conn, "up")

	const (
		wallet, otherWallet, usdWallet = 1, 2, 3
		opening, bet, refund, otherBet = 10, 11, 12, 13
		betEntry                       = 20
	)

	seed := []struct {
		table string
		row   row
	}{
		{"wallets", walletRow(wallet, "player-1")},
		{"wallets", walletRow(otherWallet, "player-2")},
		{"wallets", walletRow(usdWallet, "player-1").with(row{"currency": "USD"})},
		{"wager_transactions", openingRow(opening, wallet)},
		{"wager_transactions", transactionRow(bet, wallet, "BET")},
		{"wager_transactions", transactionRow(otherBet, otherWallet, "BET")},
		{"wager_transactions", transactionRow(refund, wallet, "REFUND").with(row{
			"reference_external_transaction_id": "ext-11", "reference_transaction_id": id(bet),
		})},
		{"wallet_ledger_entries", ledgerRow(betEntry, wallet, bet)},
		{"inbox_messages", row{
			"consumer_name": "wager-consumer", "message_id": "msg-1", "payload_hash": "hash", "received_at": testNow,
		}},
	}
	for _, s := range seed {
		if err := insert(conn, s.table, s.row); err != nil {
			t.Fatalf("seed %s: %v", s.table, err)
		}
	}

	exec := func(sql string, args ...any) func() error {
		return func() error {
			_, err := conn.Exec(context.Background(), sql, args...)
			return err
		}
	}
	insertRow := func(table string, r row) func() error {
		return func() error { return insert(conn, table, r) }
	}

	rejected := []struct {
		name       string
		run        func() error
		code       string
		constraint string
	}{
		{
			name:       "negative balance",
			run:        exec("UPDATE wallets SET balance_minor = -1 WHERE id = $1", id(wallet)),
			code:       checkViolation,
			constraint: "wallets_balance_non_negative",
		},
		{
			name:       "second wallet for the same player and currency",
			run:        insertRow("wallets", walletRow(4, "player-1")),
			code:       uniqueViolation,
			constraint: "wallets_player_currency_key",
		},
		{
			name: "same external transaction twice",
			run: insertRow("wager_transactions", transactionRow(30, wallet, "BET").with(row{
				"external_transaction_id": "ext-11",
			})),
			code:       uniqueViolation,
			constraint: "wager_transactions_external_key",
		},
		{
			name: "same idempotency key twice",
			run: insertRow("wager_transactions", transactionRow(31, wallet, "BET").with(row{
				"idempotency_key": "key-11",
			})),
			code:       uniqueViolation,
			constraint: "wager_transactions_idempotency_key",
		},
		{
			name:       "second opening credit for the wallet",
			run:        insertRow("wager_transactions", openingRow(32, wallet)),
			code:       uniqueViolation,
			constraint: "wager_transactions_one_opening_per_wallet",
		},
		{
			name:       "opening carrying external metadata",
			run:        insertRow("wager_transactions", openingRow(33, otherWallet).with(row{"provider_id": "provider-a"})),
			code:       checkViolation,
			constraint: "wager_transactions_origin",
		},
		{
			name:       "external transaction without provider",
			run:        insertRow("wager_transactions", transactionRow(34, wallet, "BET").with(row{"provider_id": nil})),
			code:       checkViolation,
			constraint: "wager_transactions_origin",
		},
		{
			name:       "bet of zero",
			run:        insertRow("wager_transactions", transactionRow(35, wallet, "BET").with(row{"amount_minor": int64(0)})),
			code:       checkViolation,
			constraint: "wager_transactions_amount_by_kind",
		},
		{
			name:       "loss with an amount",
			run:        insertRow("wager_transactions", transactionRow(36, wallet, "LOSS")),
			code:       checkViolation,
			constraint: "wager_transactions_amount_by_kind",
		},
		{
			name:       "refund without reference",
			run:        insertRow("wager_transactions", transactionRow(37, wallet, "REFUND")),
			code:       checkViolation,
			constraint: "wager_transactions_reference_by_kind",
		},
		{
			name:       "unknown status",
			run:        insertRow("wager_transactions", transactionRow(38, wallet, "BET").with(row{"status": "DONE"})),
			code:       checkViolation,
			constraint: "wager_transactions_status_known",
		},
		{
			name:       "rejection without failure code",
			run:        insertRow("wager_transactions", transactionRow(39, wallet, "BET").with(row{"status": "REJECTED"})),
			code:       checkViolation,
			constraint: "wager_transactions_failure_code_by_status",
		},
		{
			name: "result balance without its currency",
			run: insertRow("wager_transactions", transactionRow(41, wallet, "BET").with(row{
				"result_balance_currency": nil,
			})),
			code:       checkViolation,
			constraint: "wager_transactions_result_balance",
		},
		{
			name: "second processed reversal of the same transaction",
			run: insertRow("wager_transactions", transactionRow(40, wallet, "ROLLBACK").with(row{
				"reference_external_transaction_id": "ext-11", "reference_transaction_id": id(bet),
			})),
			code:       uniqueViolation,
			constraint: "wager_transactions_one_processed_reversal",
		},
		{
			name: "ledger update",
			run:  exec("UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1", id(betEntry)),
			code: restrictViolation,
		},
		{
			name: "ledger delete",
			run:  exec("DELETE FROM wallet_ledger_entries WHERE id = $1", id(betEntry)),
			code: restrictViolation,
		},
		{
			name: "ledger truncate",
			run:  exec("TRUNCATE wallet_ledger_entries"),
			code: restrictViolation,
		},
		{
			name:       "second ledger entry for the same transaction",
			run:        insertRow("wallet_ledger_entries", ledgerRow(50, wallet, bet)),
			code:       uniqueViolation,
			constraint: "wallet_ledger_entries_wallet_transaction_key",
		},
		{
			name: "ledger entry whose balances do not add up",
			run: insertRow("wallet_ledger_entries", ledgerRow(51, wallet, refund).with(row{
				"balance_after_minor": int64(9999),
			})),
			code:       checkViolation,
			constraint: "wallet_ledger_entries_balance_equation",
		},
		{
			name:       "ledger entry in a currency other than the wallet's",
			run:        insertRow("wallet_ledger_entries", ledgerRow(52, wallet, refund).with(row{"currency": "USD"})),
			code:       foreignKeyViolation,
			constraint: "wallet_ledger_entries_wallet_currency_fkey",
		},
		{
			name:       "ledger entry for a transaction of another wallet",
			run:        insertRow("wallet_ledger_entries", ledgerRow(53, wallet, otherBet)),
			code:       foreignKeyViolation,
			constraint: "wallet_ledger_entries_transaction_wallet_fkey",
		},
		{
			name: "waiting transaction with no next attempt",
			run: insertRow("wager_transactions", transactionRow(54, wallet, "REFUND").with(row{
				"status": "PENDING_REFERENCE", "result_balance_minor": nil, "result_balance_currency": nil,
				"reference_external_transaction_id": "ext-not-yet",
			})),
			code:       checkViolation,
			constraint: "wager_transactions_next_attempt_only_while_waiting",
		},
		{
			name:       "finished transaction still scheduled for another attempt",
			run:        insertRow("wager_transactions", transactionRow(55, wallet, "BET").with(row{"next_reference_attempt_at": testNow})),
			code:       checkViolation,
			constraint: "wager_transactions_next_attempt_only_while_waiting",
		},
		{
			name:       "negative reference attempts",
			run:        insertRow("wager_transactions", transactionRow(56, wallet, "BET").with(row{"reference_attempts": -1})),
			code:       checkViolation,
			constraint: "wager_transactions_reference_attempts_non_negative",
		},
		{
			name: "same message twice for the same consumer",
			run: insertRow("inbox_messages", row{
				"consumer_name": "wager-consumer", "message_id": "msg-1", "payload_hash": "other", "received_at": testNow,
			}),
			code:       uniqueViolation,
			constraint: "inbox_messages_pkey",
		},
	}

	for _, tt := range rejected {
		t.Run("rejects "+tt.name, func(t *testing.T) {
			assertViolation(t, tt.run(), tt.code, tt.constraint)
		})
	}

	accepted := []struct {
		name string
		run  func() error
	}{
		{
			name: "a rejected reversal next to the processed one",
			run: insertRow("wager_transactions", transactionRow(60, wallet, "ROLLBACK").with(row{
				"status": "REJECTED", "failure_code": "ALREADY_REVERSED",
				"reference_external_transaction_id": "ext-11", "reference_transaction_id": id(bet),
			})),
		},
		{
			name: "a reversal still waiting for its reference",
			run: insertRow("wager_transactions", transactionRow(61, wallet, "REFUND").with(row{
				"status": "PENDING_REFERENCE", "result_balance_minor": nil, "result_balance_currency": nil,
				"reference_external_transaction_id": "ext-not-yet",
				"reference_attempts":                1, "next_reference_attempt_at": testNow,
			})),
		},
		{
			name: "the same external id from another provider",
			run: insertRow("wager_transactions", transactionRow(62, wallet, "BET").with(row{
				"provider_id": "provider-b", "external_transaction_id": "ext-11", "idempotency_key": "key-11",
			})),
		},
		{
			name: "the same message for another consumer",
			run: insertRow("inbox_messages", row{
				"consumer_name": "other-consumer", "message_id": "msg-1", "payload_hash": "hash", "received_at": testNow,
			}),
		},
		{
			name: "a loss of zero",
			run:  insertRow("wager_transactions", transactionRow(63, wallet, "LOSS").with(row{"amount_minor": int64(0)})),
		},
	}

	for _, tt := range accepted {
		t.Run("accepts "+tt.name, func(t *testing.T) {
			if err := tt.run(); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}

	var balance int64
	if err := conn.QueryRow(context.Background(), "SELECT balance_minor FROM wallets WHERE id = $1", id(wallet)).Scan(&balance); err != nil {
		t.Fatalf("read balance: %v", err)
	}
	if balance != 10000 {
		t.Errorf("balance = %d, want 10000", balance)
	}
}
