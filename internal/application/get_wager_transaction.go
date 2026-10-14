package application

import (
	"context"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
)

type GetWagerTransaction struct {
	wagers WagerTransactionRepository
}

func NewGetWagerTransaction(wagers WagerTransactionRepository) *GetWagerTransaction {
	return &GetWagerTransaction{wagers: wagers}
}

func (uc *GetWagerTransaction) ByID(ctx context.Context, transactionID string) (*domain.WagerTransaction, error) {
	return uc.wagers.Get(ctx, transactionID)
}

func (uc *GetWagerTransaction) ByExternalID(ctx context.Context, providerID, externalTransactionID string) (*domain.WagerTransaction, error) {
	return uc.wagers.GetByExternalID(ctx, providerID, externalTransactionID)
}
