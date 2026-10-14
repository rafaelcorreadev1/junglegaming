package sqs

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
)

type ClientConfig struct {
	Region string
	// Endpoint points the client at LocalStack; empty means the real AWS endpoint.
	Endpoint string
}

var Module = fx.Module("sqs",
	fx.Provide(
		newClient,
		fx.Annotate(newPublisher, fx.As(new(application.EventPublisher))),
	),
)

func newClient(cfg config.Config) (*sqs.Client, error) {
	return NewClient(context.Background(), ClientConfig{Region: cfg.AWSRegion, Endpoint: cfg.SQSEndpoint})
}

func newPublisher(client *sqs.Client, cfg config.Config) (*Publisher, error) {
	return NewPublisher(client, cfg.OutboxQueueURL)
}

func NewClient(ctx context.Context, cfg ClientConfig) (*sqs.Client, error) {
	// Credentials come from the default chain (AWS_ACCESS_KEY_ID and friends, profiles, instance roles).
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return sqs.NewFromConfig(awsCfg, func(o *sqs.Options) {
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	}), nil
}

type Publisher struct {
	client   *sqs.Client
	queueURL string
}

func NewPublisher(client *sqs.Client, queueURL string) (*Publisher, error) {
	if queueURL == "" {
		return nil, errors.New("outbox queue URL is required")
	}
	return &Publisher{client: client, queueURL: queueURL}, nil
}

// Publish sends to a FIFO queue: the group keeps one wallet's events in order, and the deduplication ID
// drops a resend of the same event within SQS's five-minute window.
func (p *Publisher) Publish(ctx context.Context, message application.OutboxMessage) error {
	_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(p.queueURL),
		MessageBody:            aws.String(string(message.Payload)),
		MessageGroupId:         aws.String(message.AggregateID),
		MessageDeduplicationId: aws.String(message.ID),
		MessageAttributes: map[string]types.MessageAttributeValue{
			"eventType": {DataType: aws.String("String"), StringValue: aws.String(message.EventType)},
			"eventId":   {DataType: aws.String("String"), StringValue: aws.String(message.ID)},
		},
	})
	if err != nil {
		return fmt.Errorf("send event to sqs: %w", err)
	}
	return nil
}
