package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// RefreshInput carries the opaque refresh token.
type RefreshInput struct{ RefreshToken string }

// Refresh rotates a refresh token (monolith handleRefetchTokenV2, docs/07 §2.1).
type Refresh struct{ d *Deps }

// Execute exchanges the refresh token for a new pair. Presenting a token that was already rotated away means it
// leaked: the whole session is revoked (in its own transaction, so the revocation outlives the error returned) and
// its access tokens are denylisted.
func (u Refresh) Execute(ctx context.Context, in RefreshInput) (AuthResult, error) {
	if in.RefreshToken == "" {
		return AuthResult{}, domain.ErrTokenInvalid()
	}
	hash := domain.HashToken(in.RefreshToken)

	sess, err := u.d.Sessions.FindByRefreshHash(ctx, hash)
	if err != nil {
		if errs.KindOf(err) != errs.KindNotFound && errs.CodeOf(err) != domain.CodeTokenInvalid {
			return AuthResult{}, err
		}
		return AuthResult{}, u.detectReuse(ctx, hash)
	}
	now := u.d.Clock.Now()
	if !sess.IsActive(now) {
		return AuthResult{}, domain.ErrTokenInvalid()
	}
	acc, err := u.d.Accounts.FindByID(ctx, sess.AccountID())
	if err != nil {
		return AuthResult{}, domain.ErrTokenInvalid()
	}
	if !acc.IsActive() {
		return AuthResult{}, errs.PermissionDenied(domain.CodeAccountInactive, "this account cannot sign in")
	}

	secret, newHash, err := u.d.Secrets.New()
	if err != nil {
		return AuthResult{}, err
	}
	oldHash, err := sess.Rotate(newHash, now, u.d.Settings.RefreshTTL)
	if err != nil {
		return AuthResult{}, err
	}
	// Compare-and-swap: of two concurrent refreshes with the same token exactly one wins.
	if err := u.d.Sessions.Rotate(ctx, sess, oldHash); err != nil {
		return AuthResult{}, err
	}
	access, _, err := u.d.Signer.Sign(acc.ID(), string(acc.Type()), acc.Email().String(), acc.Roles(), sess.ID())
	if err != nil {
		return AuthResult{}, err
	}
	return AuthResult{Account: viewOf(acc), Tokens: Tokens{AccessToken: access, RefreshToken: secret, ExpiresIn: u.d.Signer.TTL(), SessionID: sess.ID()}}, nil
}

// detectReuse distinguishes an unknown token from a replayed one and punishes the latter.
func (u Refresh) detectReuse(ctx context.Context, hash string) error {
	used, err := u.d.Sessions.FindByUsedHash(ctx, hash)
	if err != nil {
		if errs.KindOf(err) == errs.KindNotFound || errs.CodeOf(err) == domain.CodeTokenInvalid {
			return domain.ErrTokenInvalid()
		}
		return err
	}
	u.d.security().RefreshReuseDetected() // the detection is the signal, whether or not the revocation below succeeds
	used.Revoke(domain.RevokeReuseDetected, u.d.Clock.Now())
	if err := u.d.Tx.WithinTx(ctx, func(ctx context.Context) error { return u.d.Sessions.Revoke(ctx, used) }); err != nil {
		return err
	}
	if err := u.d.Denylist.Revoke(ctx, used.ID(), u.d.Signer.TTL()); err != nil {
		return err
	}
	return domain.ErrTokenReused()
}
