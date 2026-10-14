package application

import (
	"context"
	"errors"
	"fmt"
	"time"
)

const maxLastErrorLength = 1000

type OutboxPolicy struct {
	BaseDelay time.Duration
	MaxDelay  time.Duration
	// Lease is how long claimed events stay hidden from the other publishers.
	Lease        time.Duration
	Aggregates   int
	PerAggregate int
}

func DefaultOutboxPolicy() OutboxPolicy {
	return OutboxPolicy{
		BaseDelay:    time.Second,
		MaxDelay:     time.Minute,
		Lease:        30 * time.Second,
		Aggregates:   20,
		PerAggregate: 50,
	}
}

func (p OutboxPolicy) Validate() error {
	switch {
	case p.BaseDelay <= 0:
		return errors.New("outbox base delay must be positive")
	case p.MaxDelay < p.BaseDelay:
		return errors.New("outbox max delay must not be shorter than the base delay")
	case p.Lease <= 0:
		return errors.New("outbox lease must be positive")
	case p.Aggregates < 1 || p.PerAggregate < 1:
		return errors.New("outbox batch sizes must be at least 1")
	}
	return nil
}

type PublishOutbox struct {
	outbox    OutboxRepository
	publisher EventPublisher
	clock     Clock
	policy    OutboxPolicy
}

func NewPublishOutbox(outbox OutboxRepository, publisher EventPublisher, clock Clock, policy OutboxPolicy) *PublishOutbox {
	return &PublishOutbox{outbox: outbox, publisher: publisher, clock: clock, policy: policy}
}

// Execute returns how many events it claimed; a batch with events from every allowed aggregate means more may be due.
func (uc *PublishOutbox) Execute(ctx context.Context) (claimed int, full bool, err error) {
	now := uc.clock.Now()
	records, err := uc.outbox.ClaimDue(ctx, now, now.Add(uc.policy.Lease), uc.policy.Aggregates, uc.policy.PerAggregate)
	if err != nil {
		return 0, false, err
	}

	var errs []error
	groups := groupByAggregate(records)
	for _, group := range groups {
		if err := uc.publishInOrder(ctx, group); err != nil {
			errs = append(errs, err)
		}
	}
	return len(records), len(groups) == uc.policy.Aggregates, errors.Join(errs...)
}

// publishInOrder stops at the first failure: sending a later event of the same wallet first would break its order.
func (uc *PublishOutbox) publishInOrder(ctx context.Context, group []OutboxRecord) error {
	for i, record := range group {
		if err := uc.publisher.Publish(ctx, record.Message); err != nil {
			return uc.giveBack(ctx, record, group[i+1:], err)
		}
		// If this fails the event is published again after the lease, with the same ID.
		if err := uc.outbox.MarkPublished(ctx, record.Message.ID, uc.clock.Now()); err != nil {
			return fmt.Errorf("mark event %s published: %w", record.Message.ID, err)
		}
	}
	return nil
}

func (uc *PublishOutbox) giveBack(ctx context.Context, failed OutboxRecord, untried []OutboxRecord, cause error) error {
	errs := []error{fmt.Errorf("publish event %s: %w", failed.Message.ID, cause)}

	lastError := cause.Error()
	if len(lastError) > maxLastErrorLength {
		lastError = lastError[:maxLastErrorLength]
	}
	next := uc.clock.Now().Add(ExponentialDelay(uc.policy.BaseDelay, uc.policy.MaxDelay, failed.Attempts+1))
	// Detached so a shutdown in the middle still records the failure instead of waiting for the lease.
	bookkeeping := context.WithoutCancel(ctx)
	if err := uc.outbox.Reschedule(bookkeeping, failed.Message.ID, next, lastError); err != nil {
		errs = append(errs, err)
	}
	if len(untried) > 0 {
		ids := make([]string, len(untried))
		for i, record := range untried {
			ids[i] = record.Message.ID
		}
		if err := uc.outbox.Release(bookkeeping, ids); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// groupByAggregate keeps the claimed order inside each aggregate.
func groupByAggregate(records []OutboxRecord) [][]OutboxRecord {
	index := map[string]int{}
	var groups [][]OutboxRecord
	for _, record := range records {
		i, seen := index[record.Message.AggregateID]
		if !seen {
			i = len(groups)
			index[record.Message.AggregateID] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], record)
	}
	return groups
}
