//go:build integration

package recovery

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/google/uuid"

	"github.com/rafaelcorreadev1/junglegaming/internal/config"
	"github.com/rafaelcorreadev1/junglegaming/test/e2e"
)

// §13 scenario 5.
func TestConsumerKilledAfterCommitBeforeDeletingTheMessage(t *testing.T) {
	s := newStack(t)
	crashing := s.start(t, "FAULT_INJECTION="+config.FaultExitAfterCommit)
	c := e2e.NewClient(t, []string{crashing.url})
	wallet := c.OpenWallet(t, "100.00")
	bet := e2e.Bet(wallet, "10.00")
	s.queue.Send(t, "msg-"+uuid.NewString(), bet.ExternalTransactionID, bet)
	crashing.waitFault(t)

	if debits := s.count(t, `SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1 AND direction = 'DEBIT'`, wallet.ID); debits != 1 {
		t.Fatalf("debits after the crash = %d, want 1: the crash must come after the commit", debits)
	}
	if depth := s.queueDepth(t); depth != 1 {
		t.Fatalf("messages left in the queue = %d, want 1: the crash must come before the delete", depth)
	}

	restarted := s.start(t)
	c = e2e.NewClient(t, []string{restarted.url})
	e2e.WaitForQueue(t, c, s.queue, wallet)
	if duplicates := c.Metric(t, restarted.url, "wagering_duplicates_total", "channel", "sqs"); duplicates != 1 {
		t.Errorf("redeliveries recognized by the inbox = %v, want 1", duplicates)
	}
	c.AssertFinalState(t, wallet.ID, "89.00", 2, 0)
}

// §13 scenario 6, first half: the event reached the queue but the publisher died before marking it.
func TestPublisherKilledAfterSendingRepublishesTheSameEvent(t *testing.T) {
	s := newStack(t)
	var ids []string
	for _, aggregate := range s.insertEvents(t, 1, 3) {
		ids = aggregate
	}

	crashing := s.start(t, "FAULT_INJECTION="+config.FaultExitAfterPublish)
	crashing.waitFault(t)
	if published := s.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NOT NULL`); published != 0 {
		t.Fatalf("%d events marked as published, want none: the crash must come before marking", published)
	}
	if first := s.receiveEvents(t, 1); first[0].id != ids[0] {
		t.Fatalf("event in the queue = %s, want the first one %s", first[0].id, ids[0])
	}

	restarted := s.start(t)
	c := e2e.NewClient(t, []string{restarted.url})
	e2e.Eventually(t, 30*time.Second, "every event marked as published", func() bool {
		return s.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})
	// Three sends: the first event again, once its lease expired, and the two it never reached.
	if sent := c.Metric(t, restarted.url, "wagering_outbox_published_total"); sent != 3 {
		t.Errorf("events sent by the restarted instance = %v, want 3", sent)
	}
	// SQS drops the repeat because the deduplication id is the event id; a consumer outside the window would get the same eventId.
	rest := s.receiveEvents(t, 2)
	for i, e := range rest {
		if e.id != ids[i+1] {
			t.Errorf("event %d in the queue = %s, want %s", i+1, e.id, ids[i+1])
		}
	}
}

// §13 scenario 6, second half: two instances publish from the same outbox at once.
func TestCompetingPublishersLoseNothingAndKeepEachAggregateInOrder(t *testing.T) {
	s := newStack(t)
	a, b := s.start(t), s.start(t)
	c := e2e.NewClient(t, []string{a.url, b.url})

	const aggregates, perAggregate = 50, 4
	stored := s.insertEvents(t, aggregates, perAggregate)
	e2e.Eventually(t, 60*time.Second, "every event marked as published", func() bool {
		return s.count(t, `SELECT count(*) FROM outbox_events WHERE published_at IS NULL`) == 0
	})

	received := map[string][]string{}
	for _, e := range s.receiveEvents(t, aggregates*perAggregate) {
		received[e.group] = append(received[e.group], e.id)
	}
	for aggregate, ids := range stored {
		if fmt.Sprint(received[aggregate]) != fmt.Sprint(ids) {
			t.Errorf("aggregate %s received %v, want %v", aggregate, received[aggregate], ids)
		}
	}

	for _, p := range []*process{a, b} {
		sent := c.Metric(t, p.url, "wagering_outbox_published_total")
		t.Logf("%s sent %v events", p.url, sent)
		if sent == 0 {
			t.Errorf("%s sent nothing, so the publishers did not compete", p.url)
		}
	}
}

// §13 scenario 7.
func TestReversalArrivingBeforeItsReference(t *testing.T) {
	s := newStack(t)
	c := e2e.NewClient(t, []string{s.start(t).url})

	t.Run("resolved when the reference arrives", func(t *testing.T) {
		wallet := c.OpenWallet(t, "100.00")
		bet := e2e.Bet(wallet, "10.00")
		refund := e2e.Reversal("REFUND", bet)
		if status, r := c.Process(t, refund); status != http.StatusAccepted || r.Status != "PENDING_REFERENCE" {
			t.Fatalf("refund before its bet = %d %+v, want 202 PENDING_REFERENCE", status, r)
		}
		if status, r := c.Process(t, bet); status != http.StatusOK {
			t.Fatalf("bet = %d %+v", status, r)
		}
		waitForStatus(t, c, refund, "PROCESSED", "")
		c.AssertFinalState(t, wallet.ID, "100.00", 1, 1)
	})

	t.Run("rejected when the reference never arrives", func(t *testing.T) {
		wallet := c.OpenWallet(t, "100.00")
		rollback := e2e.Reversal("ROLLBACK", e2e.Bet(wallet, "10.00"))
		if status, r := c.Process(t, rollback); status != http.StatusAccepted || r.Status != "PENDING_REFERENCE" {
			t.Fatalf("rollback of a missing bet = %d %+v, want 202 PENDING_REFERENCE", status, r)
		}
		waitForStatus(t, c, rollback, "REJECTED", "REFERENCE_NOT_FOUND")
		c.AssertFinalState(t, wallet.ID, "100.00", 0, 0)
	})
}

// §13 scenario 8: kill -9 in the middle of a load, with a reversal waiting for its reference.
func TestRestartKeepsIdempotencyPendingWorkAndConsistency(t *testing.T) {
	s := newStack(t)
	// Enough attempts for the pending refund to outlive the restart.
	const patience = "REFERENCE_MAX_ATTEMPTS=100"
	first := s.start(t, patience)
	c := e2e.NewClient(t, []string{first.url})

	wallet := c.OpenWallet(t, "100.00")
	bet := e2e.Bet(wallet, "10.00")
	_, placed := c.Process(t, bet)
	late := e2e.Bet(wallet, "5.00")
	refund := e2e.Reversal("REFUND", late)
	if status, r := c.Process(t, refund); status != http.StatusAccepted {
		t.Fatalf("refund = %d %+v, want 202", status, r)
	}
	const queued = 100
	for range queued {
		b := e2e.Bet(wallet, "0.50")
		s.queue.Send(t, "msg-"+uuid.NewString(), b.ExternalTransactionID, b)
	}
	e2e.Eventually(t, 30*time.Second, "the first queued bets", func() bool {
		return s.count(t, `SELECT count(*) FROM inbox_messages`) >= 10
	})
	first.kill()
	atKill := s.count(t, `SELECT count(*) FROM inbox_messages`)
	t.Logf("killed after %d of %d queued bets", atKill, queued)
	if atKill == queued {
		t.Fatal("every message was handled before the kill, so the restart proves nothing")
	}

	second := s.start(t, patience)
	c = e2e.NewClient(t, []string{second.url})
	status, replay := c.Process(t, bet)
	if status != http.StatusOK || !replay.IdempotentReplay || replay.TransactionID != placed.TransactionID || replay.Balance.Amount != placed.Balance.Amount {
		t.Errorf("bet resent after the restart = %d %+v, want a replay of %+v", status, replay, placed)
	}
	if tx, _ := c.TransactionByExternalID(t, refund); tx.Status != "PENDING_REFERENCE" {
		t.Errorf("refund after the restart = %s, want still PENDING_REFERENCE", tx.Status)
	}
	if status, r := c.Process(t, late); status != http.StatusOK {
		t.Fatalf("late bet = %d %+v", status, r)
	}
	waitForStatus(t, c, refund, "PROCESSED", "")
	e2e.WaitForQueue(t, c, s.queue, wallet)

	// 100 − 10 − 100×0.50 − 5 + 5 − 1 (marker)
	c.AssertFinalState(t, wallet.ID, "39.00", 1+queued+1+1, 1)
}

func waitForStatus(t *testing.T, c *e2e.Client, w e2e.Wager, status, failureCode string) {
	t.Helper()
	var last e2e.Transaction
	e2e.Eventually(t, 30*time.Second, w.Kind+" to become "+status, func() bool {
		last, _ = c.TransactionByExternalID(t, w)
		return last.Status == status
	})
	if last.FailureCode != failureCode {
		t.Errorf("%s failure code = %q, want %q", w.Kind, last.FailureCode, failureCode)
	}
}

// insertEvents writes events straight into the outbox and returns their ids per aggregate, in order.
func (s *stack) insertEvents(t *testing.T, aggregates, perAggregate int) map[string][]string {
	t.Helper()
	stored := map[string][]string{}
	for range aggregates {
		aggregate := uuid.NewString()
		for i := range perAggregate {
			id := uuid.NewString()
			_, err := s.pool.Exec(context.Background(),
				`INSERT INTO outbox_events (id, aggregate_id, event_type, payload, occurred_at, next_attempt_at)
				 VALUES ($1, $2, 'TestEvent', $3, now(), now())`,
				id, aggregate, fmt.Sprintf(`{"eventId":%q,"n":%d}`, id, i))
			if err != nil {
				t.Fatalf("insert event: %v", err)
			}
			stored[aggregate] = append(stored[aggregate], id)
		}
	}
	return stored
}

type receivedEvent struct{ id, group string }

// receiveEvents reads and deletes want events, then checks for a while that no extra one arrives.
func (s *stack) receiveEvents(t *testing.T, want int) []receivedEvent {
	t.Helper()
	var events []receivedEvent
	deadline := time.Now().Add(60 * time.Second)
	quietPolls := 0
	for quietPolls < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("received %d events, want %d", len(events), want)
		}
		out, err := s.queue.Client.ReceiveMessage(context.Background(), &awssqs.ReceiveMessageInput{
			QueueUrl:                    aws.String(s.events),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageAttributeNames:       []string{"eventId"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameMessageGroupId},
		})
		if err != nil {
			t.Fatalf("receive events: %v", err)
		}
		for _, m := range out.Messages {
			events = append(events, receivedEvent{
				id:    aws.ToString(m.MessageAttributes["eventId"].StringValue),
				group: m.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
			})
			_, _ = s.queue.Client.DeleteMessage(context.Background(), &awssqs.DeleteMessageInput{QueueUrl: aws.String(s.events), ReceiptHandle: m.ReceiptHandle})
		}
		if len(out.Messages) == 0 && len(events) >= want {
			quietPolls++
		}
	}
	if len(events) != want {
		t.Fatalf("received %d events, want exactly %d: %v", len(events), want, events)
	}
	return events
}

func (s *stack) queueDepth(t *testing.T) int {
	t.Helper()
	out, err := s.queue.Client.GetQueueAttributes(context.Background(), &awssqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(s.queue.URL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages, types.QueueAttributeNameApproximateNumberOfMessagesNotVisible},
	})
	if err != nil {
		t.Fatalf("queue attributes: %v", err)
	}
	total := 0
	for _, value := range out.Attributes {
		n, _ := strconv.Atoi(value)
		total += n
	}
	return total
}
