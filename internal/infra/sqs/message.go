package sqs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
)

const wagerRequestedType = "WagerTransactionRequested"

var errInvalidMessage = errors.New("invalid message")

type wagerEnvelope struct {
	MessageID     string            `json:"messageId"`
	Type          string            `json:"type"`
	OccurredAt    time.Time         `json:"occurredAt"`
	CorrelationID string            `json:"correlationId"`
	Data          *wagerRequestData `json:"data"`
}

type wagerRequestData struct {
	ProviderID                     string        `json:"providerId"`
	ExternalTransactionID          string        `json:"externalTransactionId"`
	IdempotencyKey                 string        `json:"idempotencyKey"`
	PlayerID                       string        `json:"playerId"`
	WalletID                       string        `json:"walletId"`
	RoundID                        string        `json:"roundId"`
	GameID                         string        `json:"gameId"`
	Kind                           string        `json:"kind"`
	Money                          *domain.Money `json:"money"`
	ReferenceExternalTransactionID string        `json:"referenceExternalTransactionId"`
}

func invalidMessage(reason string) error {
	return fmt.Errorf("%w: %s", errInvalidMessage, reason)
}

// decodeWagerMessage checks the envelope only; the use case validates the operation itself, as it does for HTTP.
func decodeWagerMessage(body []byte, fallbackCorrelationID string) (application.WagerMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var envelope wagerEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return application.WagerMessage{}, invalidMessage("body is not a valid WagerTransactionRequested: " + err.Error())
	}
	if decoder.More() {
		return application.WagerMessage{}, invalidMessage("body must contain a single JSON object")
	}

	switch {
	case envelope.MessageID == "":
		return application.WagerMessage{}, invalidMessage("messageId is required")
	case envelope.Type != wagerRequestedType:
		return application.WagerMessage{}, invalidMessage(fmt.Sprintf("type must be %s", wagerRequestedType))
	case envelope.OccurredAt.IsZero():
		return application.WagerMessage{}, invalidMessage("occurredAt is required")
	case envelope.Data == nil:
		return application.WagerMessage{}, invalidMessage("data is required")
	case envelope.Data.Money == nil:
		return application.WagerMessage{}, invalidMessage("data.money is required")
	}

	hash, err := canonicalHash(body)
	if err != nil {
		return application.WagerMessage{}, invalidMessage(err.Error())
	}
	correlationID := envelope.CorrelationID
	if correlationID == "" {
		correlationID = fallbackCorrelationID
	}

	data := envelope.Data
	return application.WagerMessage{
		MessageID:   envelope.MessageID,
		PayloadHash: hash,
		Wager: application.ProcessWagerInput{
			ProviderID:                     data.ProviderID,
			ExternalTransactionID:          data.ExternalTransactionID,
			IdempotencyKey:                 data.IdempotencyKey,
			PlayerID:                       data.PlayerID,
			WalletID:                       data.WalletID,
			RoundID:                        data.RoundID,
			GameID:                         data.GameID,
			Kind:                           data.Kind,
			Money:                          *data.Money,
			ReferenceExternalTransactionID: data.ReferenceExternalTransactionID,
			CorrelationID:                  correlationID,
		},
	}, nil
}

// canonicalHash ignores whitespace and key order, so a producer that re-serializes the same message is not taken for a conflict.
func canonicalHash(body []byte) (string, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
