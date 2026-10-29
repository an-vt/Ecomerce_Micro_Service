package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/kafkax"
)

// SignUpShopInput is the request to register a seller.
type SignUpShopInput struct{ Name, Email, Password string }

// SignUpShop registers a seller (monolith AccessService.signUp) and logs them in.
type SignUpShop struct{ d *Deps }

// Execute validates, hashes the password outside the transaction (bcrypt is slow), then creates the account, its
// first session and the ShopRegistered event atomically.
func (u SignUpShop) Execute(ctx context.Context, in SignUpShopInput) (AuthResult, error) {
	email, err := domain.ParseEmail(in.Email)
	if err != nil {
		return AuthResult{}, err
	}
	if err := domain.ValidatePassword(in.Password); err != nil {
		return AuthResult{}, err
	}
	hash, err := u.d.Hasher.Hash(in.Password)
	if err != nil {
		return AuthResult{}, err
	}
	acc, err := domain.NewShop(u.d.IDs.New(), in.Name, email, hash, u.d.Clock.Now())
	if err != nil {
		return AuthResult{}, err
	}

	var tokens Tokens
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.d.Accounts.Create(ctx, acc); err != nil {
			return err
		}
		if err := u.d.emit(ctx, kafkax.TopicShopRegistered, domain.ShopRegistered{ShopID: acc.ID(), Name: acc.Name(), Email: acc.Email().String()}); err != nil {
			return err
		}
		tokens, err = u.d.startSession(ctx, acc)
		return err
	})
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Account: viewOf(acc), Tokens: tokens}, nil
}
