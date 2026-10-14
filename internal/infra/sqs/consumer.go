package sqs

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"go.uber.org/fx"

	"wagering-service/internal/application"
	"wagering-service/internal/domain"
)

const (
	correlationIDAttribute = "correlationId"
	failureReasonAttribute = "failureReason"
	maxFailureReasonLength = 256
	// Calls made after handling (delete, release, dead-letter) run detached from shutdown, but not forever.
	sqsCallTimeout = 5 * time.Second
	// How long before the stop deadline in-flight messages are interrupted, leaving time to release them.
	releaseMargin = 3 * time.Second
)

type WagerHandler interface {
	Execute(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error)
}

type ConsumerOptions struct {
	QueueURL string
	DLQURL   string
	// AllowedProviders is checked even though the queue policy already limits who can send.
	AllowedProviders  []string
	BatchSize         int32
	WaitTime          time.Duration
	VisibilityTimeout time.Duration
	RetryBaseDelay    time.Duration
	RetryMaxDelay     time.Duration
	ReceiveErrorPause time.Duration
}

func DefaultConsumerOptions() ConsumerOptions {
	return ConsumerOptions{
		BatchSize:         10,
		WaitTime:          20 * time.Second,
		VisibilityTimeout: 30 * time.Second,
		RetryBaseDelay:    2 * time.Second,
		RetryMaxDelay:     5 * time.Minute,
		ReceiveErrorPause: 2 * time.Second,
	}
}

type Consumer struct {
	client  *sqs.Client
	handler WagerHandler
	opts    ConsumerOptions
	allowed map[string]bool
	ids     application.IDGenerator
	logger  *slog.Logger
}

func NewConsumer(client *sqs.Client, handler WagerHandler, opts ConsumerOptions, ids application.IDGenerator, logger *slog.Logger) (*Consumer, error) {
	switch {
	case opts.QueueURL == "" || opts.DLQURL == "":
		return nil, errors.New("wager queue and dead-letter queue URLs are required")
	case len(opts.AllowedProviders) == 0:
		return nil, errors.New("at least one provider must be allowed on the wager queue")
	case opts.BatchSize < 1 || opts.BatchSize > 10:
		return nil, errors.New("batch size must be between 1 and 10")
	case opts.WaitTime < 0 || opts.WaitTime > 20*time.Second:
		return nil, errors.New("wait time must be between 0 and 20s")
	case opts.VisibilityTimeout < time.Second || opts.RetryBaseDelay <= 0 || opts.RetryMaxDelay < opts.RetryBaseDelay:
		return nil, errors.New("visibility timeout and retry delays must be positive, with max delay not below base delay")
	}
	allowed := make(map[string]bool, len(opts.AllowedProviders))
	for _, provider := range opts.AllowedProviders {
		allowed[provider] = true
	}
	return &Consumer{client: client, handler: handler, opts: opts, allowed: allowed, ids: ids, logger: logger.With("worker", "wager-consumer")}, nil
}

// Attach runs the consumer between the application's start and stop.
func (c *Consumer) Attach(lc fx.Lifecycle) {
	pollCtx, stopPolling := context.WithCancel(context.Background())
	workCtx, abortWork := context.WithCancel(context.Background())
	done := make(chan struct{})

	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() {
				defer close(done)
				c.Run(pollCtx, workCtx)
			}()
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			stopPolling()
			graceCtx := stopCtx
			if deadline, ok := stopCtx.Deadline(); ok {
				var cancel context.CancelFunc
				graceCtx, cancel = context.WithDeadline(stopCtx, deadline.Add(-releaseMargin))
				defer cancel()
			}
			select {
			case <-done:
				return nil
			case <-graceCtx.Done():
			}

			// Out of time: interrupted messages roll back and are made visible again for another instance.
			abortWork()
			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

// Run receives until pollCtx is cancelled; workCtx bounds the handling of messages already received.
func (c *Consumer) Run(pollCtx, workCtx context.Context) {
	for pollCtx.Err() == nil {
		messages, err := c.receive(pollCtx)
		if err != nil {
			if pollCtx.Err() != nil {
				return
			}
			c.logger.ErrorContext(pollCtx, "receive from wager queue failed", "error", err)
			select {
			case <-pollCtx.Done():
			case <-time.After(c.opts.ReceiveErrorPause):
			}
			continue
		}
		c.handleBatch(workCtx, messages)
	}
}

func (c *Consumer) receive(ctx context.Context) ([]types.Message, error) {
	out, err := c.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              aws.String(c.opts.QueueURL),
		MaxNumberOfMessages:   c.opts.BatchSize,
		WaitTimeSeconds:       int32(c.opts.WaitTime / time.Second),
		VisibilityTimeout:     int32(c.opts.VisibilityTimeout / time.Second),
		MessageAttributeNames: []string{correlationIDAttribute},
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{
			types.MessageSystemAttributeNameApproximateReceiveCount,
			types.MessageSystemAttributeNameMessageGroupId,
		},
	})
	if err != nil {
		return nil, err
	}
	return out.Messages, nil
}

// handleBatch runs wallets in parallel and each wallet's messages in order.
func (c *Consumer) handleBatch(ctx context.Context, messages []types.Message) {
	var groups [][]types.Message
	index := map[string]int{}
	for _, m := range messages {
		group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
		i, ok := index[group]
		if !ok {
			i = len(groups)
			index[group] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], m)
	}

	var wg sync.WaitGroup
	for _, group := range groups {
		wg.Go(func() {
			for i, m := range group {
				if retryIn, done := c.handle(ctx, m); !done {
					// The rest come back with the failed one, not before it: a LocalStack FIFO queue, unlike SQS, may hand
					// out a visible later message while an earlier one of the group is still hidden.
					for _, rest := range group[i+1:] {
						c.changeVisibility(ctx, rest, retryIn)
					}
					return
				}
			}
		})
	}
	wg.Wait()
}

// handle reports whether the message left the queue, processed or dead-lettered, and otherwise when it comes back.
func (c *Consumer) handle(ctx context.Context, m types.Message) (retryIn time.Duration, done bool) {
	logger := c.logger.With("sqsMessageId", aws.ToString(m.MessageId))

	correlationID := c.ids.NewID()
	if attribute, ok := m.MessageAttributes[correlationIDAttribute]; ok && aws.ToString(attribute.StringValue) != "" {
		correlationID = aws.ToString(attribute.StringValue)
	}
	msg, err := decodeWagerMessage([]byte(aws.ToString(m.Body)), correlationID)
	if err != nil {
		return c.deadLetter(ctx, logger, m, err)
	}
	logger = logger.With("messageId", msg.MessageID, "providerId", msg.Wager.ProviderID,
		"walletId", msg.Wager.WalletID, "correlationId", msg.Wager.CorrelationID)
	if !c.allowed[msg.Wager.ProviderID] {
		return c.deadLetter(ctx, logger, m, invalidMessage("provider is not allowed on this queue"))
	}

	result, err := c.handler.Execute(ctx, msg)
	switch {
	case err == nil:
		attrs := []any{"alreadyHandled", result.AlreadyHandled}
		if result.Transaction != nil {
			attrs = append(attrs, "transactionId", result.Transaction.ID(), "status", result.Transaction.Status(), "replay", result.Replay)
		}
		logger.InfoContext(ctx, "wager message handled", attrs...)
		c.delete(ctx, logger, m)
		return 0, true
	case ctx.Err() != nil:
		logger.WarnContext(ctx, "wager message interrupted by shutdown; releasing it", "error", err)
		c.changeVisibility(ctx, m, 0)
		return 0, false
	case permanent(err):
		return c.deadLetter(ctx, logger, m, err)
	default:
		delay := c.retryDelay(m)
		logger.WarnContext(ctx, "wager message failed; it will be redelivered", "error", err, "retryIn", delay.String())
		c.changeVisibility(ctx, m, delay)
		return delay, false
	}
}

// permanent lists the failures a redelivery cannot fix; anything else is retried until the redrive policy gives up.
func permanent(err error) bool {
	var validation *domain.ValidationError
	if errors.As(err, &validation) {
		return true
	}
	for _, target := range []error{
		errInvalidMessage,
		domain.ErrInvalidAmount, domain.ErrInvalidCurrency, domain.ErrInvalidMoney, domain.ErrMoneyOverflow,
		application.ErrWalletNotFound,
		application.ErrIdempotencyConflict,
		application.ErrExternalTransactionConflict,
		application.ErrMessageConflict,
		application.ErrInvariantViolation,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

func (c *Consumer) retryDelay(m types.Message) time.Duration {
	return application.ExponentialDelay(c.opts.RetryBaseDelay, c.opts.RetryMaxDelay, receiveCount(m))
}

func receiveCount(m types.Message) int {
	count, err := strconv.Atoi(m.Attributes[string(types.MessageSystemAttributeNameApproximateReceiveCount)])
	if err != nil || count < 1 {
		return 1
	}
	return count
}

func (c *Consumer) deadLetter(ctx context.Context, logger *slog.Logger, m types.Message, cause error) (time.Duration, bool) {
	callCtx, cancel := detached(ctx)
	defer cancel()

	reason := cause.Error()
	if len(reason) > maxFailureReasonLength {
		reason = reason[:maxFailureReasonLength]
	}
	group := m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)]
	if group == "" {
		group = "unknown"
	}
	// The SQS message id repeats on redelivery, so the DLQ drops a second copy if deleting from the source fails below.
	_, err := c.client.SendMessage(callCtx, &sqs.SendMessageInput{
		QueueUrl:               aws.String(c.opts.DLQURL),
		MessageBody:            m.Body,
		MessageGroupId:         aws.String(group),
		MessageDeduplicationId: m.MessageId,
		MessageAttributes: map[string]types.MessageAttributeValue{
			failureReasonAttribute: {DataType: aws.String("String"), StringValue: aws.String(reason)},
		},
	})
	if err != nil {
		logger.ErrorContext(ctx, "moving wager message to the dead-letter queue failed", "error", err, "reason", reason)
		delay := c.retryDelay(m)
		c.changeVisibility(ctx, m, delay)
		return delay, false
	}
	logger.ErrorContext(ctx, "wager message moved to the dead-letter queue", "reason", reason)
	c.delete(ctx, logger, m)
	return 0, true
}

func (c *Consumer) delete(ctx context.Context, logger *slog.Logger, m types.Message) {
	callCtx, cancel := detached(ctx)
	defer cancel()
	_, err := c.client.DeleteMessage(callCtx, &sqs.DeleteMessageInput{QueueUrl: aws.String(c.opts.QueueURL), ReceiptHandle: m.ReceiptHandle})
	if err != nil {
		// Safe to leave: the redelivery finds the message in the inbox.
		logger.WarnContext(ctx, "deleting wager message failed", "error", err)
	}
}

func (c *Consumer) changeVisibility(ctx context.Context, m types.Message, delay time.Duration) {
	callCtx, cancel := detached(ctx)
	defer cancel()
	_, err := c.client.ChangeMessageVisibility(callCtx, &sqs.ChangeMessageVisibilityInput{
		QueueUrl:          aws.String(c.opts.QueueURL),
		ReceiptHandle:     m.ReceiptHandle,
		VisibilityTimeout: int32(delay / time.Second),
	})
	if err != nil {
		// The message still comes back once its current visibility timeout ends.
		c.logger.WarnContext(ctx, "changing wager message visibility failed", "error", err, "sqsMessageId", aws.ToString(m.MessageId))
	}
}

func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), sqsCallTimeout)
}
