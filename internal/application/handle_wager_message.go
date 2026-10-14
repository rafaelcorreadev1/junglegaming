package application

import (
	"context"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

const WagerConsumerName = "wager-transactions-consumer"

// WagerMessage is a WagerTransactionRequested already decoded from the queue.
type WagerMessage struct {
	MessageID string
	// PayloadHash identifies the body, so a redelivery can be told apart from a different message under the same id.
	PayloadHash string
	Wager       ProcessWagerInput
}

type HandleWagerMessageResult struct {
	ProcessWagerResult
	// AlreadyHandled is true when an earlier delivery was committed; Transaction is then nil.
	AlreadyHandled bool
}

// HandleWagerMessage runs the same use case as HTTP and records the message in the inbox in the same SQL transaction.
type HandleWagerMessage struct {
	tx      TxManager
	inbox   InboxRepository
	process *ProcessWagerTransaction
	clock   Clock
}

func NewHandleWagerMessage(tx TxManager, inbox InboxRepository, process *ProcessWagerTransaction, clock Clock) *HandleWagerMessage {
	return &HandleWagerMessage{tx: tx, inbox: inbox, process: process, clock: clock}
}

func (h *HandleWagerMessage) Execute(ctx context.Context, msg WagerMessage) (HandleWagerMessageResult, error) {
	switch {
	case msg.MessageID == "":
		return HandleWagerMessageResult{}, &domain.ValidationError{Field: "messageId", Reason: "is required"}
	case msg.PayloadHash == "":
		return HandleWagerMessageResult{}, &domain.ValidationError{Field: "payloadHash", Reason: "is required"}
	}
	hash, err := prepareProcessInput(msg.Wager)
	if err != nil {
		return HandleWagerMessageResult{}, err
	}

	var result HandleWagerMessageResult
	err = withinTxRetryingRaces(ctx, h.tx, func(ctx context.Context) error {
		result = HandleWagerMessageResult{}
		stored, first, err := h.inbox.Receive(ctx, WagerConsumerName, msg.MessageID, msg.PayloadHash, h.clock.Now())
		if err != nil {
			return err
		}
		if !first {
			if stored.PayloadHash != msg.PayloadHash {
				return ErrMessageConflict
			}
			// Receive and Complete share this transaction, so a visible record that is not completed is corrupt state.
			if !stored.Completed {
				return ErrInvariantViolation
			}
			result.AlreadyHandled = true
			return nil
		}

		result.ProcessWagerResult, err = h.process.process(ctx, msg.Wager, hash)
		if err != nil {
			return err
		}
		return h.inbox.Complete(ctx, WagerConsumerName, msg.MessageID, h.clock.Now())
	})
	if err != nil {
		return HandleWagerMessageResult{}, err
	}
	return result, nil
}
