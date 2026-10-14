package application

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// payloadHash is the SHA-256 of the business fields as canonical JSON.
// encoding/json writes map keys in sorted order, which is what makes the output canonical.
// The idempotency key and transport metadata stay out, so HTTP and SQS hash the same operation alike.
func payloadHash(in ProcessWagerInput) (string, error) {
	fields := map[string]any{
		"providerId":            in.ProviderID,
		"externalTransactionId": in.ExternalTransactionID,
		"playerId":              in.PlayerID,
		"walletId":              in.WalletID,
		"roundId":               in.RoundID,
		"gameId":                in.GameID,
		"kind":                  in.Kind,
		// Amount() is the normalized form: "25", "25.0" and "25.00" all hash as "25.00".
		"money": map[string]string{"amount": in.Money.Amount(), "currency": in.Money.Currency()},
	}
	if in.ReferenceExternalTransactionID != "" {
		fields["referenceExternalTransactionId"] = in.ReferenceExternalTransactionID
	}

	canonical, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}
