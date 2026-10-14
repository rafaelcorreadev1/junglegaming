package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"wagering-service/internal/application"
)

func logLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var line map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &line); err != nil {
		t.Fatalf("log is not one JSON line: %q (%v)", buf.String(), err)
	}
	buf.Reset()
	return line
}

func TestContextFieldsAreAddedToEveryLog(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(NewContextHandler(slog.NewJSONHandler(&buf, nil)))

	logger.InfoContext(context.Background(), "no fields")
	if line := logLine(t, &buf); line["correlationId"] != nil {
		t.Errorf("unexpected field in %v", line)
	}

	ctx := WithFields(context.Background(), "correlationId", "corr-1")
	inner := WithFields(ctx, "walletId", "wallet-1")
	// Added after the inner context was derived, as a handler does deep inside a request.
	AddFields(ctx, "providerId", "provider-a")
	AddFields(inner, "transactionId", "tx-1")

	logger.With("worker", "w").InfoContext(inner, "inner", "extra", 1)
	line := logLine(t, &buf)
	for key, want := range map[string]any{"correlationId": "corr-1", "walletId": "wallet-1", "transactionId": "tx-1", "worker": "w", "extra": float64(1)} {
		if line[key] != want {
			t.Errorf("%s = %v, want %v (line %v)", key, line[key], want, line)
		}
	}
	if line["providerId"] != nil {
		t.Errorf("a field added to the parent after the copy leaked into the child: %v", line)
	}

	logger.InfoContext(ctx, "outer")
	if line := logLine(t, &buf); line["providerId"] != "provider-a" || line["transactionId"] != nil {
		t.Errorf("outer line = %v", line)
	}

	AddFields(ctx, "correlationId", "corr-2")
	logger.InfoContext(ctx, "replaced")
	if line := logLine(t, &buf); line["correlationId"] != "corr-2" {
		t.Errorf("a repeated key must replace the old value: %v", line)
	}
	AddFields(context.Background(), "ignored", true)
}

func TestConflictsAreCountedByReason(t *testing.T) {
	metrics := NewMetrics(NewRegistry(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	errs := []error{
		fmt.Errorf("%w: unique", application.ErrDuplicateIdempotencyKey),
		application.ErrAlreadyReversed,
		application.ErrConcurrentUpdate,
		fmt.Errorf("%w: %w: 40P01", application.ErrUnavailable, application.ErrTransactionConflict),
		application.ErrWalletNotFound,
		nil,
	}
	tx := InstrumentTxManager(fakeTx{}, metrics)
	for _, err := range errs {
		_ = tx.WithinTx(context.Background(), func(context.Context) error { return err })
	}

	for reason, want := range map[string]float64{"duplicate_race": 2, "concurrent_update": 1, "deadlock_or_serialization": 1} {
		if got := testutil.ToFloat64(metrics.conflicts.WithLabelValues(reason)); got != want {
			t.Errorf("conflicts{%s} = %v, want %v", reason, got, want)
		}
	}
}

type fakeTx struct{}

func (fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (fakeTx) WithinSnapshot(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

type failingPublisher struct{ err error }

func (p failingPublisher) Publish(context.Context, application.OutboxMessage) error { return p.err }

func TestPublicationsCountAsSentOrRetried(t *testing.T) {
	metrics := NewMetrics(NewRegistry(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	_ = InstrumentPublisher(failingPublisher{}, metrics).Publish(context.Background(), application.OutboxMessage{})
	_ = InstrumentPublisher(failingPublisher{errors.New("sqs down")}, metrics).Publish(context.Background(), application.OutboxMessage{})

	if got := testutil.ToFloat64(metrics.retries.WithLabelValues("outbox_publisher")); got != 1 {
		t.Errorf("outbox retries = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.published); got != 1 {
		t.Errorf("published = %v, want 1", got)
	}
}

type fakeOutbox struct {
	application.OutboxRepository
	oldest  time.Time
	pending bool
	err     error
}

func (o fakeOutbox) OldestPending(context.Context) (time.Time, bool, error) {
	return o.oldest, o.pending, o.err
}

func TestOutboxAgeGauge(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cases := []struct {
		name   string
		outbox fakeOutbox
		check  func(float64) bool
	}{
		{"nothing pending", fakeOutbox{}, func(v float64) bool { return v == 0 }},
		{"a minute old", fakeOutbox{oldest: time.Now().Add(-time.Minute), pending: true}, func(v float64) bool { return v >= 60 && v < 70 }},
		{"query failed", fakeOutbox{err: errors.New("db down")}, math.IsNaN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oldestPendingAge(tc.outbox, logger); !tc.check(got) {
				t.Errorf("age = %v", got)
			}
		})
	}

	registry := NewRegistry()
	NewMetrics(registry, fakeOutbox{oldest: time.Now().Add(-time.Minute), pending: true}, logger)
	if n, err := testutil.GatherAndCount(registry, "wagering_outbox_oldest_pending_age_seconds"); err != nil || n != 1 {
		t.Errorf("gauge series = %d, %v; want it registered", n, err)
	}
}
