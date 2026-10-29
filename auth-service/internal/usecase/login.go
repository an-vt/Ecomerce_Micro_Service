package usecase

import (
	"context"
	"sync"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/principal"
)

// LoginInput identifies the account type being signed in.
type LoginInput struct {
	Type     principal.Type
	Email    string
	Password string
}

// Login starts a session for a shop or a buyer (monolith AccessService.login).
type Login struct {
	d         *Deps
	dummyOnce sync.Once
	dummy     string
}

// dummyHash is compared against when the account does not exist, so that an unknown email costs the same time as
// a wrong password (no account enumeration through timing).
func (u *Login) dummyHash() string {
	u.dummyOnce.Do(func() { u.dummy, _ = u.d.Hasher.Hash("not-a-real-password") })
	return u.dummy
}

// Execute fails with the single error INVALID_CREDENTIALS for an unknown email, a wrong password and an account
// that has no password yet. Inactive accounts are told so only after the password was right.
func (u *Login) Execute(ctx context.Context, in LoginInput) (AuthResult, error) {
	if in.Type != principal.TypeShop && in.Type != principal.TypeUser {
		return AuthResult{}, errs.Validation("account type must be SHOP or USER")
	}
	email, err := domain.ParseEmail(in.Email)
	if err != nil {
		_ = u.d.Hasher.Compare(u.dummyHash(), in.Password)
		return AuthResult{}, domain.ErrInvalidCredentials()
	}
	acc, err := u.d.Accounts.FindByEmail(ctx, in.Type, email)
	if err != nil {
		if errs.KindOf(err) != errs.KindNotFound {
			return AuthResult{}, err
		}
		_ = u.d.Hasher.Compare(u.dummyHash(), in.Password)
		return AuthResult{}, domain.ErrInvalidCredentials()
	}
	if !acc.HasPassword() {
		_ = u.d.Hasher.Compare(u.dummyHash(), in.Password)
		return AuthResult{}, domain.ErrInvalidCredentials()
	}
	if err := u.d.Hasher.Compare(acc.PasswordHash(), in.Password); err != nil {
		return AuthResult{}, domain.ErrInvalidCredentials()
	}
	if err := acc.CheckLogin(); err != nil {
		return AuthResult{}, err
	}

	var tokens Tokens
	if err := u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		var terr error
		tokens, terr = u.d.startSession(ctx, acc)
		return terr
	}); err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Account: viewOf(acc), Tokens: tokens}, nil
}
