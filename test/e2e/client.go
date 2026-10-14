//go:build integration

// Package e2e drives the running service from the outside, with real Keycloak tokens, the way a client would.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"wagering-service/internal/testenv"
)

const (
	ProviderA          = "provider-a"
	ProviderB          = "provider-b"
	ProviderAShortLife = "provider-a-short-lived"
	WalletService      = "wallet-service"
)

type Money struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

func BRL(amount string) Money { return Money{Amount: amount, Currency: "BRL"} }

type Wager struct {
	ProviderID                     string `json:"providerId"`
	ExternalTransactionID          string `json:"externalTransactionId"`
	PlayerID                       string `json:"playerId"`
	WalletID                       string `json:"walletId"`
	RoundID                        string `json:"roundId"`
	GameID                         string `json:"gameId"`
	Kind                           string `json:"kind"`
	Money                          Money  `json:"money"`
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
}

type WagerResult struct {
	TransactionID    string `json:"transactionId"`
	Status           string `json:"status"`
	FailureCode      string `json:"failureCode"`
	Balance          *Money `json:"balance"`
	IdempotentReplay bool   `json:"idempotentReplay"`
}

type Transaction struct {
	TransactionID string `json:"transactionId"`
	Kind          string `json:"kind"`
	Status        string `json:"status"`
	FailureCode   string `json:"failureCode"`
	WalletID      string `json:"walletId"`
	ProviderID    string `json:"providerId"`
}

type Wallet struct {
	ID       string `json:"id"`
	PlayerID string `json:"playerId"`
	Balance  Money  `json:"balance"`
}

type LedgerEntry struct {
	TransactionID string `json:"transactionId"`
	Direction     string `json:"direction"`
	Money         Money  `json:"money"`
}

type Reconciliation struct {
	StoredBalance     Money `json:"storedBalance"`
	CalculatedBalance Money `json:"calculatedBalance"`
	Consistent        bool  `json:"consistent"`
	CheckedEntries    int   `json:"checkedEntries"`
}

type Response struct {
	Status   int
	Body     []byte
	Instance string
}

// Client spreads requests over the given instances in turn.
type Client struct {
	Instances []string
	keycloak  string
	http      *http.Client
	next      atomic.Uint64
	tokens    sync.Map
}

// NewClient waits until every instance reports ready, so a test never starts against a service still booting.
func NewClient(t *testing.T, instances []string) *Client {
	t.Helper()
	c := &Client{
		Instances: instances,
		keycloak:  testenv.Lookup(t, "TEST_KEYCLOAK_URL", "http://localhost:8081"),
		http:      &http.Client{Timeout: 30 * time.Second},
	}
	for _, instance := range instances {
		Eventually(t, 90*time.Second, instance+" ready", func() bool {
			response, err := c.http.Get(instance + "/health/ready")
			if err != nil {
				return false
			}
			_ = response.Body.Close()
			return response.StatusCode == http.StatusOK
		})
	}
	return c
}

// Token is cached per client: Keycloak tokens last five minutes, longer than any test.
func (c *Client) Token(t testing.TB, clientID string) string {
	t.Helper()
	if token, ok := c.tokens.Load(clientID); ok {
		return token.(string)
	}
	token := c.FreshToken(t, clientID)
	c.tokens.Store(clientID, token)
	return token
}

func (c *Client) FreshToken(t testing.TB, clientID string) string {
	t.Helper()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {clientID}, "client_secret": {clientID + "-secret"}}
	response, err := c.http.PostForm(c.keycloak+"/realms/wagering/protocol/openid-connect/token", form)
	if err != nil {
		t.Fatalf("token for %s: %v", clientID, err)
	}
	defer response.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil || body.AccessToken == "" {
		t.Fatalf("token for %s: status %d, %v", clientID, response.StatusCode, err)
	}
	return body.AccessToken
}

type Request struct {
	Token          string
	Method         string
	Path           string
	IdempotencyKey string
	Body           any
}

func (c *Client) Send(t testing.TB, r Request) Response {
	t.Helper()
	instance := c.Instances[int(c.next.Add(1)-1)%len(c.Instances)]
	return c.SendTo(t, instance, r)
}

func (c *Client) SendTo(t testing.TB, instance string, r Request) Response {
	t.Helper()
	var body io.Reader
	if r.Body != nil {
		raw, err := json.Marshal(r.Body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		body = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(context.Background(), r.Method, instance+r.Path, body)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if r.Token != "" {
		request.Header.Set("Authorization", "Bearer "+r.Token)
	}
	if r.IdempotencyKey != "" {
		request.Header.Set("Idempotency-Key", r.IdempotencyKey)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		t.Fatalf("%s %s on %s: %v", r.Method, r.Path, instance, err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return Response{Status: response.StatusCode, Body: raw, Instance: instance}
}

func Decode[T any](t testing.TB, r Response) T {
	t.Helper()
	var value T
	if err := json.Unmarshal(r.Body, &value); err != nil {
		t.Fatalf("decode %T from %s: %v", value, r.Body, err)
	}
	return value
}

func (c *Client) OpenWallet(t testing.TB, amount string) Wallet {
	t.Helper()
	response := c.Send(t, Request{
		Token: c.Token(t, WalletService), Method: http.MethodPost, Path: "/wallets",
		Body: map[string]any{"playerId": "player-" + uuid.NewString(), "initialBalance": BRL(amount)},
	})
	if response.Status != http.StatusCreated {
		t.Fatalf("open wallet: %d %s", response.Status, response.Body)
	}
	return Decode[Wallet](t, response)
}

// Bet builds a BET from provider-a with ids of its own; the idempotency key is the external id.
func Bet(wallet Wallet, amount string) Wager {
	return Wager{
		ProviderID: ProviderA, ExternalTransactionID: "bet-" + uuid.NewString(), PlayerID: wallet.PlayerID, WalletID: wallet.ID,
		RoundID: "round-1", GameID: "game-1", Kind: "BET", Money: BRL(amount),
	}
}

func Reversal(kind string, target Wager) Wager {
	w := target
	w.Kind = kind
	w.ExternalTransactionID = strings.ToLower(kind) + "-" + uuid.NewString()
	w.ReferenceExternalTransactionID = target.ExternalTransactionID
	return w
}

func (c *Client) Process(t testing.TB, w Wager) (int, WagerResult) {
	t.Helper()
	return c.ProcessAs(t, c.Token(t, w.ProviderID), w.ExternalTransactionID, w)
}

func (c *Client) ProcessAs(t testing.TB, token, idempotencyKey string, w Wager) (int, WagerResult) {
	t.Helper()
	response := c.Send(t, Request{Token: token, Method: http.MethodPost, Path: "/wagering/transactions", IdempotencyKey: idempotencyKey, Body: w})
	if response.Status >= 500 || response.Status == http.StatusUnauthorized || response.Status == http.StatusForbidden {
		t.Fatalf("process %s: %d %s", w.ExternalTransactionID, response.Status, response.Body)
	}
	return response.Status, Decode[WagerResult](t, response)
}

// TransactionByExternalID returns false while the operation has not arrived.
func (c *Client) TransactionByExternalID(t testing.TB, w Wager) (Transaction, bool) {
	t.Helper()
	response := c.Send(t, Request{Token: c.Token(t, WalletService), Method: http.MethodGet,
		Path: fmt.Sprintf("/providers/%s/wagering/transactions/%s", w.ProviderID, w.ExternalTransactionID)})
	switch response.Status {
	case http.StatusOK:
		return Decode[Transaction](t, response), true
	case http.StatusNotFound:
		return Transaction{}, false
	}
	t.Fatalf("get transaction: %d %s", response.Status, response.Body)
	return Transaction{}, false
}

func (c *Client) Wallet(t testing.TB, id string) Wallet {
	t.Helper()
	response := c.Send(t, Request{Token: c.Token(t, WalletService), Method: http.MethodGet, Path: "/wallets/" + id})
	if response.Status != http.StatusOK {
		t.Fatalf("get wallet: %d %s", response.Status, response.Body)
	}
	return Decode[Wallet](t, response)
}

func (c *Client) Ledger(t testing.TB, walletID string) []LedgerEntry {
	t.Helper()
	var entries []LedgerEntry
	cursor := ""
	for {
		path := "/wallets/" + walletID + "/ledger?limit=100"
		if cursor != "" {
			path += "&cursor=" + url.QueryEscape(cursor)
		}
		response := c.Send(t, Request{Token: c.Token(t, WalletService), Method: http.MethodGet, Path: path})
		if response.Status != http.StatusOK {
			t.Fatalf("ledger: %d %s", response.Status, response.Body)
		}
		page := Decode[struct {
			Items      []LedgerEntry `json:"items"`
			NextCursor *string       `json:"nextCursor"`
		}](t, response)
		entries = append(entries, page.Items...)
		if page.NextCursor == nil {
			return entries
		}
		cursor = *page.NextCursor
	}
}

func (c *Client) Reconcile(t testing.TB, walletID string) Reconciliation {
	t.Helper()
	response := c.Send(t, Request{Token: c.Token(t, WalletService), Method: http.MethodPost, Path: "/wallets/" + walletID + "/reconciliation"})
	if response.Status != http.StatusOK {
		t.Fatalf("reconcile: %d %s", response.Status, response.Body)
	}
	return Decode[Reconciliation](t, response)
}

// Movements counts ledger entries by direction, leaving out the opening credit.
func Movements(entries []LedgerEntry) (debits, credits int) {
	for i, e := range entries {
		if i == 0 && e.Direction == "CREDIT" {
			continue
		}
		if e.Direction == "DEBIT" {
			debits++
		} else {
			credits++
		}
	}
	return debits, credits
}

// AssertFinalState is the check every financial scenario ends with: the balance, the ledger movements and stored = credits − debits.
func (c *Client) AssertFinalState(t testing.TB, walletID, balance string, debits, credits int) {
	t.Helper()
	if got := c.Wallet(t, walletID).Balance.Amount; got != balance {
		t.Errorf("balance = %s, want %s", got, balance)
	}
	gotDebits, gotCredits := Movements(c.Ledger(t, walletID))
	if gotDebits != debits || gotCredits != credits {
		t.Errorf("ledger movements = %d debits, %d credits; want %d, %d", gotDebits, gotCredits, debits, credits)
	}
	if r := c.Reconcile(t, walletID); !r.Consistent || r.StoredBalance != r.CalculatedBalance {
		t.Errorf("reconciliation = %+v, want stored balance equal to the ledger", r)
	}
}

func Eventually(t testing.TB, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
