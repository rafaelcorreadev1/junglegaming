//go:build integration

package e2e

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/google/uuid"

	"github.com/rafaelcorreadev1/junglegaming/internal/infra/sqs"
	"github.com/rafaelcorreadev1/junglegaming/internal/testenv"
)

// Queue sends wager messages the way a provider would, straight to the queue the service consumes.
type Queue struct {
	Client *awssqs.Client
	URL    string
}

func NewQueue(t testing.TB) *Queue {
	t.Helper()
	endpoint := testenv.SQSEndpoint(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	client, err := sqs.NewClient(context.Background(), sqs.ClientConfig{Region: "us-east-1", Endpoint: endpoint})
	if err != nil {
		t.Fatalf("sqs client: %v", err)
	}
	return &Queue{
		Client: client,
		URL:    testenv.Lookup(t, "TEST_WAGER_QUEUE_URL", "http://localhost:4566/000000000000/wager-transactions.fifo"),
	}
}

// Send uses a new deduplication id on every call, so SQS never drops a repeat: only the service's inbox can.
// It returns the id SQS gave the message, which proves each call was a separate delivery.
func (q *Queue) Send(t testing.TB, messageID, idempotencyKey string, w Wager) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"messageId":  messageID,
		"type":       "WagerTransactionRequested",
		"occurredAt": time.Now().UTC().Format(time.RFC3339),
		"data": map[string]any{
			"providerId": w.ProviderID, "externalTransactionId": w.ExternalTransactionID, "idempotencyKey": idempotencyKey,
			"playerId": w.PlayerID, "walletId": w.WalletID, "roundId": w.RoundID, "gameId": w.GameID,
			"kind": w.Kind, "money": w.Money, "referenceExternalTransactionId": w.ReferenceExternalTransactionID,
		},
	})
	if err != nil {
		t.Fatalf("encode message: %v", err)
	}
	sent, err := q.Client.SendMessage(context.Background(), &awssqs.SendMessageInput{
		QueueUrl:               aws.String(q.URL),
		MessageBody:            aws.String(string(body)),
		MessageGroupId:         aws.String(w.WalletID),
		MessageDeduplicationId: aws.String(uuid.NewString()),
	})
	if err != nil {
		t.Fatalf("send message: %v", err)
	}
	return *sent.MessageId
}
