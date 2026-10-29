package usecase

import (
	"context"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// LogoutInput names the caller and the session to end.
type LogoutInput struct{ AccountID, SessionID string }

// Logout ends a session (monolith AccessService.logout, which crashed reading a delete result).
type Logout struct{ d *Deps }

// Execute revokes the session and denylists its access tokens. It is idempotent: an unknown or already revoked
// session is success. A caller can only end their own sessions.
func (u Logout) Execute(ctx context.Context, in LogoutInput) error {
	sess, err := u.d.Sessions.FindByID(ctx, in.SessionID)
	if err != nil {
		if errs.KindOf(err) == errs.KindNotFound {
			return nil
		}
		return err
	}
	if sess.AccountID() != in.AccountID {
		return errs.PermissionDenied("FORBIDDEN", "this session belongs to another account")
	}
	if !sess.Revoked() {
		sess.Revoke(domain.RevokeLogout, u.d.Clock.Now())
		if err := u.d.Tx.WithinTx(ctx, func(ctx context.Context) error { return u.d.Sessions.Revoke(ctx, sess) }); err != nil {
			return err
		}
	}
	return u.d.Denylist.Revoke(ctx, sess.ID(), u.d.Signer.TTL())
}
