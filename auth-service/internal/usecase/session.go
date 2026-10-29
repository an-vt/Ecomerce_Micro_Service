package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
)

// startSession creates a session for acc and signs its first access token. Call it inside the transaction that
// also holds the account change, so a failure leaves no orphan session.
func (d *Deps) startSession(ctx context.Context, acc *domain.Account) (Tokens, error) {
	secret, hash, err := d.Secrets.New()
	if err != nil {
		return Tokens{}, err
	}
	s := domain.NewSession(d.IDs.New(), acc.ID(), hash, d.Clock.Now(), d.Settings.RefreshTTL)
	if err := d.Sessions.Create(ctx, s); err != nil {
		return Tokens{}, err
	}
	access, _, err := d.Signer.Sign(acc.ID(), string(acc.Type()), acc.Email().String(), acc.Roles(), s.ID())
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{AccessToken: access, RefreshToken: secret, ExpiresIn: d.Signer.TTL(), SessionID: s.ID()}, nil
}
