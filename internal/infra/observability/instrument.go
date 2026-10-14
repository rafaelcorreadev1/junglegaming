package observability

import (
	"context"
	"errors"

	"wagering-service/internal/application"
)

// InstrumentTxManager counts the transactions that ended by losing a race; it is applied with fx.Decorate.
func InstrumentTxManager(next application.TxManager, metrics *Metrics) application.TxManager {
	return &instrumentedTxManager{next: next, metrics: metrics}
}

type instrumentedTxManager struct {
	next    application.TxManager
	metrics *Metrics
}

func (t *instrumentedTxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	err := t.next.WithinTx(ctx, fn)
	if reason, ok := conflictReason(err); ok {
		t.metrics.Conflict(reason)
	}
	return err
}

func (t *instrumentedTxManager) WithinSnapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	return t.next.WithinSnapshot(ctx, fn)
}

func conflictReason(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, application.ErrDuplicateIdempotencyKey),
		errors.Is(err, application.ErrDuplicateExternalTransaction),
		errors.Is(err, application.ErrAlreadyReversed):
		return "duplicate_race", true
	case errors.Is(err, application.ErrConcurrentUpdate):
		return "concurrent_update", true
	case errors.Is(err, application.ErrTransactionConflict):
		return "deadlock_or_serialization", true
	}
	return "", false
}

// InstrumentPublisher counts failed publications, each of which the outbox schedules again.
func InstrumentPublisher(next application.EventPublisher, metrics *Metrics) application.EventPublisher {
	return &instrumentedPublisher{next: next, metrics: metrics}
}

type instrumentedPublisher struct {
	next    application.EventPublisher
	metrics *Metrics
}

func (p *instrumentedPublisher) Publish(ctx context.Context, message application.OutboxMessage) error {
	err := p.next.Publish(ctx, message)
	if err != nil {
		p.metrics.Retry("outbox_publisher")
	}
	return err
}
