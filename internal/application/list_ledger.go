package application

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

const (
	DefaultLedgerPageSize = 50
	MaxLedgerPageSize     = 200

	cursorPrefix = "v1:"
)

type ListLedgerInput struct {
	WalletID string
	Cursor   string
	// Limit of zero means the default page size.
	Limit int
}

type LedgerPage struct {
	Entries []domain.LedgerEntry
	// NextCursor is empty on the last page.
	NextCursor string
}

type ListLedger struct {
	wallets WalletRepository
	ledger  LedgerRepository
}

func NewListLedger(wallets WalletRepository, ledger LedgerRepository) *ListLedger {
	return &ListLedger{wallets: wallets, ledger: ledger}
}

func (uc *ListLedger) Execute(ctx context.Context, in ListLedgerInput) (LedgerPage, error) {
	limit := in.Limit
	switch {
	case limit == 0:
		limit = DefaultLedgerPageSize
	case limit < 0 || limit > MaxLedgerPageSize:
		return LedgerPage{}, &domain.ValidationError{Field: "limit", Reason: fmt.Sprintf("must be between 1 and %d", MaxLedgerPageSize)}
	}
	afterSequence, err := decodeCursor(in.Cursor)
	if err != nil {
		return LedgerPage{}, err
	}

	// Without this, an unknown wallet would look like a wallet with an empty ledger.
	if _, err := uc.wallets.Get(ctx, in.WalletID); err != nil {
		return LedgerPage{}, err
	}

	// One extra record tells whether another page exists without a second query.
	records, err := uc.ledger.ListByWallet(ctx, in.WalletID, afterSequence, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}

	page := LedgerPage{}
	if len(records) > limit {
		records = records[:limit]
		page.NextCursor = encodeCursor(records[limit-1].Sequence)
	}
	page.Entries = make([]domain.LedgerEntry, len(records))
	for i, record := range records {
		page.Entries[i] = record.Entry
	}
	return page, nil
}

func encodeCursor(sequence int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(sequence, 10)))
}

func decodeCursor(cursor string) (int64, error) {
	if cursor == "" {
		return 0, nil
	}
	invalid := &domain.ValidationError{Field: "cursor", Reason: "is not a cursor returned by this API"}

	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, invalid
	}
	value, found := strings.CutPrefix(string(raw), cursorPrefix)
	if !found {
		return 0, invalid
	}
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 {
		return 0, invalid
	}
	return sequence, nil
}
