package sqs

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"wagering-service/internal/application"
)

// newTestQueue creates a FIFO queue of its own in LocalStack; skipped when TEST_SQS_ENDPOINT is unset.
func newTestQueue(t *testing.T) (*sqs.Client, string) {
	t.Helper()
	endpoint := os.Getenv("TEST_SQS_ENDPOINT")
	if endpoint == "" {
		t.Skip("TEST_SQS_ENDPOINT not set")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	ctx := context.Background()
	client, err := NewClient(ctx, ClientConfig{Region: "us-east-1", Endpoint: endpoint})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(fmt.Sprintf("test-%d.fifo", time.Now().UnixNano())),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false"},
	})
	if err != nil {
		t.Fatalf("create queue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
	})
	return client, *created.QueueUrl
}

func TestPublishSendsToTheFIFOQueueWithGroupAndDeduplication(t *testing.T) {
	client, queueURL := newTestQueue(t)
	publisher, err := NewPublisher(client, queueURL)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	ctx := context.Background()
	message := func(id string) application.OutboxMessage {
		return application.OutboxMessage{
			ID: id, AggregateID: "wallet-1", EventType: "WalletBalanceChanged",
			Payload: []byte(`{"eventId":"` + id + `"}`), OccurredAt: time.Now(),
		}
	}

	for _, id := range []string{"event-1", "event-2", "event-1"} {
		if err := publisher.Publish(ctx, message(id)); err != nil {
			t.Fatalf("publish %s: %v", id, err)
		}
	}

	received, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(queueURL), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
		MessageAttributeNames:       []string{"All"},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}

	var bodies []string
	for _, m := range received.Messages {
		bodies = append(bodies, *m.Body)
		if group := m.Attributes["MessageGroupId"]; group != "wallet-1" {
			t.Errorf("MessageGroupId = %q, want the aggregate id", group)
		}
		eventID := *m.MessageAttributes["eventId"].StringValue
		if dedup := m.Attributes["MessageDeduplicationId"]; dedup != eventID {
			t.Errorf("MessageDeduplicationId = %q, want the event id %q", dedup, eventID)
		}
		if eventType := *m.MessageAttributes["eventType"].StringValue; eventType != "WalletBalanceChanged" {
			t.Errorf("eventType attribute = %q", eventType)
		}
	}
	want := []string{`{"eventId":"event-1"}`, `{"eventId":"event-2"}`}
	if fmt.Sprint(bodies) != fmt.Sprint(want) {
		t.Errorf("received %v, want %v: in order, and the resend of event-1 dropped by deduplication", bodies, want)
	}
}

func TestNewPublisherRequiresAQueue(t *testing.T) {
	if _, err := NewPublisher(nil, ""); err == nil {
		t.Error("expected an error for an empty queue URL")
	}
}
