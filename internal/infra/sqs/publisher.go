package sqs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/config"
	"wagering-service/internal/infra/observability"
)

type ClientConfig struct {
	Region string
	// Endpoint points the client at LocalStack; empty means the real AWS endpoint.
	Endpoint string
	// HTTPClient is optional; the SDK default is used when nil.
	HTTPClient *http.Client
}

var Module = fx.Module("sqs",
	fx.Provide(
		newClient,
		fx.Annotate(newPublisher, fx.As(new(application.EventPublisher))),
		newConsumer,
	),
	fx.Invoke(checkQueuesOnStart),
)

// checkQueuesOnStart refuses to start when a queue is missing, instead of failing on the first message.
func checkQueuesOnStart(lc fx.Lifecycle, client *sqs.Client, cfg config.Config) {
	probe := NewProbe(client, cfg)
	lc.Append(fx.StartHook(func(ctx context.Context) error {
		if err := probe.Ping(ctx); err != nil {
			return fmt.Errorf("check sqs queues: %w", err)
		}
		return nil
	}))
}

func newClient(lc fx.Lifecycle, cfg config.Config) (*sqs.Client, error) {
	// A transport of our own, so stopping can close its idle keep-alive connections and their goroutines.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	lc.Append(fx.StopHook(transport.CloseIdleConnections))
	return NewClient(context.Background(), ClientConfig{
		Region: cfg.AWSRegion, Endpoint: cfg.SQSEndpoint, HTTPClient: &http.Client{Transport: transport},
	})
}

func newPublisher(client *sqs.Client, cfg config.Config) (*Publisher, error) {
	return NewPublisher(client, cfg.OutboxQueueURL)
}

func newConsumer(
	client *sqs.Client,
	handler *application.HandleWagerMessage,
	cfg config.Config,
	ids application.IDGenerator,
	metrics *observability.Metrics,
	logger *slog.Logger,
) (*Consumer, error) {
	opts := DefaultConsumerOptions()
	opts.QueueURL = cfg.WagerQueueURL
	opts.DLQURL = cfg.WagerDLQURL
	opts.AllowedProviders = cfg.WagerQueueProviders
	return NewConsumer(client, handler, opts, ids, metrics, logger)
}

func NewClient(ctx context.Context, cfg ClientConfig) (*sqs.Client, error) {
	// Credentials come from the default chain (AWS_ACCESS_KEY_ID and friends, profiles, instance roles).
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.Region)}
	if cfg.HTTPClient != nil {
		opts = append(opts, awsconfig.WithHTTPClient(cfg.HTTPClient))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
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
