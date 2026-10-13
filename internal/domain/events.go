package domain

import (
	"fmt"
	"time"
)

const (
	EventWagerTransactionProcessed        = "WagerTransactionProcessed"
	EventWagerTransactionRejected         = "WagerTransactionRejected"
	EventWagerTransactionPendingReference = "WagerTransactionPendingReference"
	EventWalletBalanceChanged             = "WalletBalanceChanged"

	eventVersion = 1
)

// EventEnvelope is embedded in every event, so its fields sit at the top level of the JSON next to "data".
type EventEnvelope struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
}

func (e EventEnvelope) Envelope() EventEnvelope { return e }

type Event interface {
	Envelope() EventEnvelope
}

// EventMeta is what the caller chooses; type, version and aggregate are set by each constructor.
type EventMeta struct {
	EventID       string
	CorrelationID string
	CausationID   string
	OccurredAt    time.Time
}

// The wallet is the aggregate of every event, which keeps the events of one wallet in order on a FIFO queue.
func newEnvelope(meta EventMeta, eventType, walletID string) (EventEnvelope, error) {
	switch {
	case meta.EventID == "":
		return EventEnvelope{}, invalid("eventId", "is required")
	case meta.CorrelationID == "":
		return EventEnvelope{}, invalid("correlationId", "is required")
	case meta.OccurredAt.IsZero():
		return EventEnvelope{}, invalid("occurredAt", "is required")
	}
	return EventEnvelope{
		EventID:       meta.EventID,
		EventType:     eventType,
		AggregateID:   walletID,
		CorrelationID: meta.CorrelationID,
		CausationID:   meta.CausationID,
		OccurredAt:    meta.OccurredAt.UTC(),
		Version:       eventVersion,
	}, nil
}

// TransactionSnapshot leaves the external metadata out of the JSON for an internal OPENING.
type TransactionSnapshot struct {
	TransactionID                  string          `json:"transactionId"`
	WalletID                       string          `json:"walletId"`
	PlayerID                       string          `json:"playerId"`
	Kind                           TransactionKind `json:"kind"`
	Money                          Money           `json:"money"`
	ProviderID                     string          `json:"providerId,omitempty"`
	ExternalTransactionID          string          `json:"externalTransactionId,omitempty"`
	RoundID                        string          `json:"roundId,omitempty"`
	GameID                         string          `json:"gameId,omitempty"`
	ReferenceExternalTransactionID string          `json:"referenceExternalTransactionId,omitempty"`
}

func snapshotOf(tx *WagerTransaction) TransactionSnapshot {
	return TransactionSnapshot{
		TransactionID:                  tx.id,
		WalletID:                       tx.walletID,
		PlayerID:                       tx.playerID,
		Kind:                           tx.kind,
		Money:                          tx.money,
		ProviderID:                     tx.providerID,
		ExternalTransactionID:          tx.externalTransactionID,
		RoundID:                        tx.roundID,
		GameID:                         tx.gameID,
		ReferenceExternalTransactionID: tx.referenceExternalTransactionID,
	}
}

func requireStatus(tx *WagerTransaction, want TransactionStatus, eventType string) error {
	if tx.status != want {
		return fmt.Errorf("%w: %s requires a %s transaction, got %s", ErrInvalidTransition, eventType, want, tx.status)
	}
	return nil
}

type WagerTransactionProcessedData struct {
	TransactionSnapshot
	Balance Money `json:"balance"`
}

type WagerTransactionProcessed struct {
	EventEnvelope
	Data WagerTransactionProcessedData `json:"data"`
}

func NewWagerTransactionProcessed(meta EventMeta, tx *WagerTransaction) (WagerTransactionProcessed, error) {
	if err := requireStatus(tx, StatusProcessed, EventWagerTransactionProcessed); err != nil {
		return WagerTransactionProcessed{}, err
	}
	envelope, err := newEnvelope(meta, EventWagerTransactionProcessed, tx.walletID)
	if err != nil {
		return WagerTransactionProcessed{}, err
	}
	return WagerTransactionProcessed{
		EventEnvelope: envelope,
		Data:          WagerTransactionProcessedData{TransactionSnapshot: snapshotOf(tx), Balance: tx.resultBalance},
	}, nil
}

type WagerTransactionRejectedData struct {
	TransactionSnapshot
	FailureCode FailureCode `json:"failureCode"`
	// Balance is absent when the rejection was decided without reading the wallet.
	Balance *Money `json:"balance,omitempty"`
}

type WagerTransactionRejected struct {
	EventEnvelope
	Data WagerTransactionRejectedData `json:"data"`
}

func NewWagerTransactionRejected(meta EventMeta, tx *WagerTransaction) (WagerTransactionRejected, error) {
	if err := requireStatus(tx, StatusRejected, EventWagerTransactionRejected); err != nil {
		return WagerTransactionRejected{}, err
	}
	envelope, err := newEnvelope(meta, EventWagerTransactionRejected, tx.walletID)
	if err != nil {
		return WagerTransactionRejected{}, err
	}
	data := WagerTransactionRejectedData{TransactionSnapshot: snapshotOf(tx), FailureCode: tx.failureCode}
	if balance, observed := tx.ResultBalance(); observed {
		data.Balance = &balance
	}
	return WagerTransactionRejected{EventEnvelope: envelope, Data: data}, nil
}

type WagerTransactionPendingReferenceData struct {
	TransactionSnapshot
}

type WagerTransactionPendingReference struct {
	EventEnvelope
	Data WagerTransactionPendingReferenceData `json:"data"`
}

func NewWagerTransactionPendingReference(meta EventMeta, tx *WagerTransaction) (WagerTransactionPendingReference, error) {
	if err := requireStatus(tx, StatusPendingReference, EventWagerTransactionPendingReference); err != nil {
		return WagerTransactionPendingReference{}, err
	}
	envelope, err := newEnvelope(meta, EventWagerTransactionPendingReference, tx.walletID)
	if err != nil {
		return WagerTransactionPendingReference{}, err
	}
	return WagerTransactionPendingReference{
		EventEnvelope: envelope,
		Data:          WagerTransactionPendingReferenceData{TransactionSnapshot: snapshotOf(tx)},
	}, nil
}

type WalletBalanceChangedData struct {
	WalletID      string          `json:"walletId"`
	TransactionID string          `json:"transactionId"`
	Direction     LedgerDirection `json:"direction"`
	Money         Money           `json:"money"`
	BalanceBefore Money           `json:"balanceBefore"`
	BalanceAfter  Money           `json:"balanceAfter"`
	WalletVersion int64           `json:"walletVersion"`
}

type WalletBalanceChanged struct {
	EventEnvelope
	Data WalletBalanceChangedData `json:"data"`
}

// NewWalletBalanceChanged expects the wallet right after the movement that produced entry.
func NewWalletBalanceChanged(meta EventMeta, wallet *Wallet, entry LedgerEntry) (WalletBalanceChanged, error) {
	if entry.walletID != wallet.id {
		return WalletBalanceChanged{}, ErrWalletMismatch
	}
	if !entry.balanceAfter.Equal(wallet.balance) {
		return WalletBalanceChanged{}, invalid("entry", "does not describe the current wallet balance")
	}
	envelope, err := newEnvelope(meta, EventWalletBalanceChanged, wallet.id)
	if err != nil {
		return WalletBalanceChanged{}, err
	}
	return WalletBalanceChanged{
		EventEnvelope: envelope,
		Data: WalletBalanceChangedData{
			WalletID:      wallet.id,
			TransactionID: entry.transactionID,
			Direction:     entry.direction,
			Money:         entry.amount,
			BalanceBefore: entry.balanceBefore,
			BalanceAfter:  entry.balanceAfter,
			WalletVersion: wallet.version,
		},
	}, nil
}
