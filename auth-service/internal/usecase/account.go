package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
)

// SetPasswordInput is the caller's own account and the new password.
type SetPasswordInput struct{ AccountID, Password string }

// SetPassword lets a verified buyer (or anyone) set their password.
type SetPassword struct{ d *Deps }

// Execute validates and hashes the password, then saves it.
func (u SetPassword) Execute(ctx context.Context, in SetPasswordInput) error {
	if err := domain.ValidatePassword(in.Password); err != nil {
		return err
	}
	hash, err := u.d.Hasher.Hash(in.Password)
	if err != nil {
		return err
	}
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		acc, err := u.d.Accounts.FindByID(ctx, in.AccountID)
		if err != nil {
			return err
		}
		if err := acc.SetPasswordHash(hash, u.d.Clock.Now()); err != nil {
			return err
		}
		return u.d.Accounts.Update(ctx, acc)
	})
}

// GetAccount loads an account.
type GetAccount struct{ d *Deps }

// Execute returns the account view.
func (u GetAccount) Execute(ctx context.Context, accountID string) (AccountView, error) {
	acc, err := u.d.Accounts.FindByID(ctx, accountID)
	if err != nil {
		return AccountView{}, err
	}
	return viewOf(acc), nil
}
