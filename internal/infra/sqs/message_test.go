package sqs

import (
	"errors"
	"strings"
	"testing"
)

const validBody = `{
  "messageId": "msg-123",
  "type": "WagerTransactionRequested",
  "occurredAt": "2026-09-08T12:00:00.000Z",
  "data": {
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "idempotencyKey": "provider-a:transaction-123",
    "playerId": "player-1",
    "walletId": "0192f291-27dd-7d3f-8071-5f8685deef37",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": { "amount": "25.00", "currency": "BRL" }
  }
}`

func TestDecodeWagerMessageMapsTheEnvelope(t *testing.T) {
	msg, err := decodeWagerMessage([]byte(validBody), "fallback-correlation")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	in := msg.Wager
	if msg.MessageID != "msg-123" || in.IdempotencyKey != "provider-a:transaction-123" || in.ProviderID != "provider-a" ||
		in.Kind != "BET" || in.Money.Amount() != "25.00" || in.CorrelationID != "fallback-correlation" || msg.PayloadHash == "" {
		t.Errorf("decoded %+v", msg)
	}

	withCorrelation := strings.Replace(validBody, `"type"`, `"correlationId": "corr-1", "type"`, 1)
	if msg, _ := decodeWagerMessage([]byte(withCorrelation), "fallback"); msg.Wager.CorrelationID != "corr-1" {
		t.Errorf("correlationId = %q, want the envelope's", msg.Wager.CorrelationID)
	}
}

func TestDecodeWagerMessageRejectsInvalidBodies(t *testing.T) {
	tests := map[string]string{
		"not json":          `{"messageId":`,
		"wrong type":        strings.Replace(validBody, "WagerTransactionRequested", "WalletOpened", 1),
		"missing messageId": strings.Replace(validBody, `"msg-123"`, `""`, 1),
		"missing data":      `{"messageId":"m","type":"WagerTransactionRequested","occurredAt":"2026-09-08T12:00:00Z"}`,
		"missing money":     strings.Replace(validBody, `"money": { "amount": "25.00", "currency": "BRL" }`, `"gameId2": "x"`, 1),
		"unknown field":     strings.Replace(validBody, `"kind"`, `"bonus": true, "kind"`, 1),
		"bad amount":        strings.Replace(validBody, `"25.00"`, `"25.001"`, 1),
		"missing occurred":  strings.Replace(validBody, `"occurredAt": "2026-09-08T12:00:00.000Z",`, ``, 1),
		"two objects":       validBody + validBody,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeWagerMessage([]byte(body), "c"); !errors.Is(err, errInvalidMessage) {
				t.Errorf("err = %v, want errInvalidMessage", err)
			}
		})
	}
}

func TestPayloadHashIgnoresFormattingButNotContent(t *testing.T) {
	compact := `{"type":"WagerTransactionRequested","messageId":"msg-123","occurredAt":"2026-09-08T12:00:00.000Z","data":{"money":{"currency":"BRL","amount":"25.00"},"kind":"BET","gameId":"fortune-chimp","roundId":"round-987","walletId":"0192f291-27dd-7d3f-8071-5f8685deef37","playerId":"player-1","idempotencyKey":"provider-a:transaction-123","externalTransactionId":"transaction-123","providerId":"provider-a"}}`
	original, _ := decodeWagerMessage([]byte(validBody), "c")
	reformatted, err := decodeWagerMessage([]byte(compact), "c")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if original.PayloadHash != reformatted.PayloadHash {
		t.Error("the same message with other whitespace and key order hashed differently")
	}

	changed, _ := decodeWagerMessage([]byte(strings.Replace(validBody, `"25.00"`, `"26.00"`, 1)), "c")
	if changed.PayloadHash == original.PayloadHash {
		t.Error("a different amount produced the same hash")
	}
}
