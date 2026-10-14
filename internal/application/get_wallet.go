package application

import (
	"context"

	"github.com/rafaelcorreadev1/junglegaming/internal/domain"
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
