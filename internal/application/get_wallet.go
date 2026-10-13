package application

import (
	"context"

	"wagering-service/internal/domain"
)

type GetWallet struct {
	wallets WalletRepository
}

func NewGetWallet(wallets WalletRepository) *GetWallet {
	return &GetWallet{wallets: wallets}
}

func (uc *GetWallet) Execute(ctx context.Context, walletID string) (*domain.Wallet, error) {
	return uc.wallets.Get(ctx, walletID)
}
