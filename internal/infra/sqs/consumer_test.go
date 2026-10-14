package sqs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/fx/fxtest"

	"github.com/rafaelcorreadev1/junglegaming/internal/application"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/lifecycle"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/observability"
	"github.com/rafaelcorreadev1/junglegaming/internal/infra/system"
	"github.com/rafaelcorreadev1/junglegaming/internal/testenv"
)

type queuePair struct {
	client *sqs.Client
	source string
	dlq    string
}

// newQueuePair creates a FIFO queue with its own dead-letter queue and redrive policy.
func newQueuePair(t *testing.T, maxReceiveCount int) queuePair {
	t.Helper()
	client, dlq := newTestQueue(t)
	ctx := context.Background()
	attrs, err := client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(dlq), AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
	})
	if err != nil {
		t.Fatalf("dlq arn: %v", err)
	}
	redrive := fmt.Sprintf(`{"deadLetterTargetArn":%q,"maxReceiveCount":"%d"}`, attrs.Attributes["QueueArn"], maxReceiveCount)
	created, err := client.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String(testenv.UniqueName("test-source-") + ".fifo"),
		Attributes: map[string]string{"FifoQueue": "true", "ContentBasedDeduplication": "false", "RedrivePolicy": redrive},
	})
	if err != nil {
		t.Fatalf("create source queue: %v", err)
	}
	t.Cleanup(func() {
		_, _ = client.DeleteQueue(context.Background(), &sqs.DeleteQueueInput{QueueUrl: created.QueueUrl})
	})
	return queuePair{client: client, source: *created.QueueUrl, dlq: dlq}
}

func (q queuePair) send(t *testing.T, group, dedup, body string) {
	t.Helper()
	_, err := q.client.SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl: aws.String(q.source), MessageBody: aws.String(body),
		MessageGroupId: aws.String(group), MessageDeduplicationId: aws.String(dedup),
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
}

type deadLetter struct {
	body, reason string
}

// drainDLQ reads and deletes what reached the dead-letter queue.
func (q queuePair) drainDLQ(t *testing.T) []deadLetter {
	t.Helper()
	ctx := context.Background()
	var letters []deadLetter
	for range 3 {
		out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(q.dlq), MaxNumberOfMessages: 10, WaitTimeSeconds: 1,
			MessageAttributeNames: []string{"All"},
		})
		if err != nil {
			t.Fatalf("receive dlq: %v", err)
		}
		for _, m := range out.Messages {
			reason := ""
			if attr, ok := m.MessageAttributes[failureReasonAttribute]; ok {
				reason = aws.ToString(attr.StringValue)
			}
			letters = append(letters, deadLetter{body: aws.ToString(m.Body), reason: reason})
			_, _ = q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{QueueUrl: aws.String(q.dlq), ReceiptHandle: m.ReceiptHandle})
		}
		if len(letters) > 0 {
			return letters
		}
	}
	return letters
}

func (q queuePair) visibleInSource(t *testing.T) int {
	t.Helper()
	out, err := q.client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(q.source),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatalf("attributes: %v", err)
	}
	var visible, hidden int
	fmt.Sscan(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)], &visible)
	fmt.Sscan(out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessagesNotVisible)], &hidden)
	return visible + hidden
}

type handlerFunc func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error)

func (f handlerFunc) Execute(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
	return f(ctx, msg)
}

func testOptions(q queuePair) ConsumerOptions {
	opts := DefaultConsumerOptions()
	opts.QueueURL, opts.DLQURL = q.source, q.dlq
	opts.AllowedProviders = []string{"provider-a"}
	opts.WaitTime = time.Second
	opts.VisibilityTimeout = 10 * time.Second
	opts.RetryBaseDelay, opts.RetryMaxDelay = time.Second, time.Second
	opts.ReceiveErrorPause = 100 * time.Millisecond
	return opts
}

func newTestConsumer(t *testing.T, q queuePair, opts ConsumerOptions, handler WagerHandler) *Consumer {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	// No outbox: these tests never scrape, so the outbox age gauge is never evaluated.
	consumer, err := NewConsumer(q.client, handler, opts, system.UUIDGenerator{}, observability.NewMetrics(observability.NewRegistry(), nil, logger), logger)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	return consumer
}

// poll runs one receive and handles the batch, as one turn of Run.
func poll(t *testing.T, c *Consumer) int {
	t.Helper()
	messages, err := c.receive(context.Background())
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	c.handleBatch(context.Background(), messages)
	return len(messages)
}

func body(messageID, externalID, provider string) string {
	b := strings.Replace(validBody, `"msg-123"`, fmt.Sprintf("%q", messageID), 1)
	b = strings.Replace(b, `"transaction-123"`, fmt.Sprintf("%q", externalID), 1)
	return strings.Replace(b, `"providerId": "provider-a"`, fmt.Sprintf(`"providerId": %q`, provider), 1)
}

// succeed answers like the real handler for a message it had already handled: no error and no transaction.
func succeed(context.Context, application.WagerMessage) (application.HandleWagerMessageResult, error) {
	return application.HandleWagerMessageResult{AlreadyHandled: true}, nil
}

func TestConsumerDeletesAHandledMessage(t *testing.T) {
	q := newQueuePair(t, 5)
	var got []application.WagerMessage
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		got = append(got, msg)
		return succeed(ctx, msg)
	}))
	q.send(t, "wallet-1", "d1", body("msg-1", "tx-1", "provider-a"))

	if n := poll(t, c); n != 1 {
		t.Fatalf("received %d messages, want 1", n)
	}
	if len(got) != 1 || got[0].MessageID != "msg-1" || got[0].Wager.IdempotencyKey != "provider-a:transaction-123" {
		t.Errorf("handler got %+v", got)
	}
	if n := q.visibleInSource(t); n != 0 {
		t.Errorf("%d messages left in the queue, want 0", n)
	}
}

func TestConsumerDeadLettersWhatARetryCannotFix(t *testing.T) {
	tests := map[string]struct {
		body    string
		err     error
		reason  string
		handled bool
	}{
		"invalid json":         {body: `{"messageId":`, reason: "invalid message"},
		"provider not allowed": {body: body("msg-1", "tx-1", "provider-z"), reason: "provider is not allowed"},
		"conflicting reuse":    {body: body("msg-1", "tx-1", "provider-a"), err: application.ErrIdempotencyConflict, reason: "idempotency key reused", handled: true},
		"message id conflict":  {body: body("msg-1", "tx-1", "provider-a"), err: application.ErrMessageConflict, reason: "different body", handled: true},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			q := newQueuePair(t, 5)
			calls := 0
			c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(context.Context, application.WagerMessage) (application.HandleWagerMessageResult, error) {
				calls++
				return application.HandleWagerMessageResult{}, tc.err
			}))
			q.send(t, "wallet-1", "d1", tc.body)

			poll(t, c)

			if (calls == 1) != tc.handled {
				t.Errorf("handler called %d times, handled = %v", calls, tc.handled)
			}
			letters := q.drainDLQ(t)
			if len(letters) != 1 || letters[0].body != tc.body || !strings.Contains(letters[0].reason, tc.reason) {
				t.Fatalf("dlq = %+v, want the original body with a reason containing %q", letters, tc.reason)
			}
			if n := q.visibleInSource(t); n != 0 {
				t.Errorf("%d messages left in the source queue, want 0", n)
			}
		})
	}
}

func TestConsumerRetriesATransientFailureAfterABackoff(t *testing.T) {
	q := newQueuePair(t, 5)
	opts := testOptions(q)
	// Longer than the 1s long poll below, so seeing the message there would mean it skipped its backoff.
	opts.RetryBaseDelay, opts.RetryMaxDelay = 3*time.Second, 3*time.Second
	calls := 0
	c := newTestConsumer(t, q, opts, handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		calls++
		if calls == 1 {
			return application.HandleWagerMessageResult{}, fmt.Errorf("%w: connection refused", application.ErrUnavailable)
		}
		return succeed(ctx, msg)
	}))
	q.send(t, "wallet-1", "d1", body("msg-1", "tx-1", "provider-a"))

	poll(t, c)
	if n := q.visibleInSource(t); n != 1 {
		t.Fatalf("%d messages in the queue after a transient failure, want 1", n)
	}
	if n := poll(t, c); n != 0 {
		t.Errorf("message came back before its backoff (%d received)", n)
	}

	deadline := time.Now().Add(10 * time.Second)
	for calls < 2 && time.Now().Before(deadline) {
		poll(t, c)
	}
	if calls != 2 {
		t.Fatalf("handler called %d times, want 2", calls)
	}
	if n := q.visibleInSource(t); n != 0 {
		t.Errorf("%d messages left after the retry succeeded, want 0", n)
	}
	if letters := q.drainDLQ(t); len(letters) != 0 {
		t.Errorf("dlq = %+v, want empty", letters)
	}
}

func TestRedrivePolicyDeadLettersAMessageThatKeepsFailing(t *testing.T) {
	q := newQueuePair(t, 2)
	calls := 0
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(context.Context, application.WagerMessage) (application.HandleWagerMessageResult, error) {
		calls++
		return application.HandleWagerMessageResult{}, application.ErrUnavailable
	}))
	msgBody := body("msg-1", "tx-1", "provider-a")
	q.send(t, "wallet-1", "d1", msgBody)

	deadline := time.Now().Add(15 * time.Second)
	for q.visibleInSource(t) > 0 && time.Now().Before(deadline) {
		poll(t, c)
	}

	if calls != 2 {
		t.Errorf("handler called %d times, want maxReceiveCount = 2", calls)
	}
	letters := q.drainDLQ(t)
	if len(letters) != 1 || letters[0].body != msgBody {
		t.Fatalf("dlq = %+v, want the message moved by the redrive policy", letters)
	}
}

func TestConsumerKeepsAWalletsMessagesInOrderWhileOthersProceed(t *testing.T) {
	q := newQueuePair(t, 5)
	var mu sync.Mutex
	var seen []string
	failedOnce := false
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, msg.MessageID)
		if msg.MessageID == "a1" && !failedOnce {
			failedOnce = true
			return application.HandleWagerMessageResult{}, application.ErrUnavailable
		}
		return succeed(ctx, msg)
	}))
	q.send(t, "wallet-a", "a1", body("a1", "tx-a1", "provider-a"))
	q.send(t, "wallet-a", "a2", body("a2", "tx-a2", "provider-a"))
	q.send(t, "wallet-b", "b1", body("b1", "tx-b1", "provider-a"))

	poll(t, c)
	mu.Lock()
	first := strings.Join(seen, ",")
	mu.Unlock()
	if strings.Contains(first, "a2") || !strings.Contains(first, "b1") {
		t.Fatalf("first poll handled %s: a2 must wait for a1, b1 must not", first)
	}

	deadline := time.Now().Add(10 * time.Second)
	for q.visibleInSource(t) > 0 && time.Now().Before(deadline) {
		poll(t, c)
	}
	mu.Lock()
	defer mu.Unlock()
	var walletA []string
	for _, id := range seen {
		if strings.HasPrefix(id, "a") {
			walletA = append(walletA, id)
		}
	}
	if strings.Join(walletA, ",") != "a1,a1,a2" {
		t.Errorf("wallet-a handled as %v, want a1 (failed), a1, a2", walletA)
	}
}

func TestStopReleasesAMessageThatCannotFinishInTime(t *testing.T) {
	q := newQueuePair(t, 5)
	started := make(chan struct{}, 1)
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(ctx context.Context, _ application.WagerMessage) (application.HandleWagerMessageResult, error) {
		started <- struct{}{}
		<-ctx.Done()
		return application.HandleWagerMessageResult{}, ctx.Err()
	}))
	q.send(t, "wallet-1", "d1", body("msg-1", "tx-1", "provider-a"))

	lc := fxtest.NewLifecycle(t)
	c.Attach(lc)
	lc.RequireStart()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the consumer never handled the message")
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), lifecycle.ReleaseMargin+time.Second)
	defer cancel()
	if err := lc.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}

	// Released, not left hidden for the 10s visibility timeout: it is received again right away.
	out, err := q.client.ReceiveMessage(context.Background(), &sqs.ReceiveMessageInput{
		QueueUrl: aws.String(q.source), WaitTimeSeconds: 1,
		MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameApproximateReceiveCount},
	})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(out.Messages) != 1 || receiveCount(out.Messages[0]) != 2 {
		t.Fatalf("got %d messages after stop, want the released message on its second receive", len(out.Messages))
	}
}

func TestStopFinishesWorkThatEndsInTime(t *testing.T) {
	q := newQueuePair(t, 5)
	started := make(chan struct{}, 1)
	c := newTestConsumer(t, q, testOptions(q), handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		started <- struct{}{}
		time.Sleep(500 * time.Millisecond)
		return succeed(ctx, msg)
	}))
	q.send(t, "wallet-1", "d1", body("msg-1", "tx-1", "provider-a"))

	lc := fxtest.NewLifecycle(t)
	c.Attach(lc)
	lc.RequireStart()
	<-started
	stopCtx, cancel := context.WithTimeout(context.Background(), lifecycle.ReleaseMargin+5*time.Second)
	defer cancel()
	if err := lc.Stop(stopCtx); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if n := q.visibleInSource(t); n != 0 {
		t.Errorf("%d messages left: the message in progress should have finished and been deleted", n)
	}
}

func TestNewConsumerValidatesOptions(t *testing.T) {
	valid := DefaultConsumerOptions()
	valid.QueueURL, valid.DLQURL, valid.AllowedProviders = "q", "d", []string{"provider-a"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewConsumer(nil, nil, valid, system.UUIDGenerator{}, nil, logger); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}

	broken := map[string]func(*ConsumerOptions){
		"no dlq":       func(o *ConsumerOptions) { o.DLQURL = "" },
		"no providers": func(o *ConsumerOptions) { o.AllowedProviders = nil },
		"batch of 11":  func(o *ConsumerOptions) { o.BatchSize = 11 },
		"long wait":    func(o *ConsumerOptions) { o.WaitTime = 21 * time.Second },
		"max < base":   func(o *ConsumerOptions) { o.RetryMaxDelay = time.Millisecond },
	}
	for name, breakIt := range broken {
		opts := valid
		breakIt(&opts)
		if _, err := NewConsumer(nil, nil, opts, system.UUIDGenerator{}, nil, logger); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestPermanentErrorsAreTheOnesARetryCannotFix(t *testing.T) {
	if !permanent(fmt.Errorf("wrapped: %w", application.ErrWalletNotFound)) {
		t.Error("a missing wallet should be permanent")
	}
	for _, err := range []error{application.ErrUnavailable, application.ErrConcurrentUpdate, errors.New("unexpected")} {
		if permanent(err) {
			t.Errorf("%v should be retried", err)
		}
	}
}

func TestRunKeepsTryingWhileTheQueueIsUnreachableAndStopsWhenCancelled(t *testing.T) {
	q := newQueuePair(t, 5)
	opts := testOptions(q)
	opts.QueueURL = q.source + "-missing"
	c := newTestConsumer(t, q, opts, handlerFunc(succeed))

	pollCtx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Run(pollCtx, context.Background())
	}()
	time.Sleep(3 * opts.ReceiveErrorPause)
	stop()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
}

func counterValue(t *testing.T, registry *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
	metrics:
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if labels[label.GetName()] != label.GetValue() {
					continue metrics
				}
			}
			return metric.GetCounter().GetValue()
		}
	}
	return 0
}

type emptyOutbox struct{ application.OutboxRepository }

func (emptyOutbox) OldestPending(context.Context) (time.Time, bool, error) {
	return time.Time{}, false, nil
}

func TestConsumerMetrics(t *testing.T) {
	q := newQueuePair(t, 5)
	registry := observability.NewRegistry()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	calls := 0
	handler := handlerFunc(func(ctx context.Context, msg application.WagerMessage) (application.HandleWagerMessageResult, error) {
		calls++
		if calls == 1 {
			return application.HandleWagerMessageResult{}, application.ErrUnavailable
		}
		return succeed(ctx, msg)
	})
	opts := testOptions(q)
	opts.RetryBaseDelay, opts.RetryMaxDelay = time.Second, time.Second
	c, err := NewConsumer(q.client, handler, opts, system.UUIDGenerator{}, observability.NewMetrics(registry, emptyOutbox{}, logger), logger)
	if err != nil {
		t.Fatalf("NewConsumer: %v", err)
	}
	q.send(t, "wallet-1", "bad", `{"messageId":`)
	q.send(t, "wallet-2", "good", body("msg-1", "tx-1", "provider-a"))

	deadline := time.Now().Add(10 * time.Second)
	for q.visibleInSource(t) > 0 && time.Now().Before(deadline) {
		poll(t, c)
	}

	for _, check := range []struct {
		name   string
		labels map[string]string
		want   float64
	}{
		{"wagering_dlq_messages_total", map[string]string{"reason": "invalid_message"}, 1},
		{"wagering_retries_total", map[string]string{"component": "sqs_consumer"}, 1},
		{"wagering_duplicates_total", map[string]string{"channel": "sqs", "kind": "inbox"}, 1},
	} {
		if got := counterValue(t, registry, check.name, check.labels); got != check.want {
			t.Errorf("%s%v = %v, want %v", check.name, check.labels, got, check.want)
		}
	}
}

func TestProbeChecksEveryQueue(t *testing.T) {
	q := newQueuePair(t, 5)
	ok := &Probe{client: q.client, queueURLs: []string{q.source, q.dlq}}
	if err := ok.Ping(context.Background()); err != nil {
		t.Errorf("Ping with existing queues: %v", err)
	}
	missing := &Probe{client: q.client, queueURLs: []string{q.source, q.source + "-missing"}}
	if err := missing.Ping(context.Background()); err == nil {
		t.Error("Ping must fail when one of the queues does not exist")
	}
}
