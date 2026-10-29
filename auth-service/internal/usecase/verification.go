package usecase

import (
	"context"
	"net/url"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/principal"
)

// RequestEmailVerificationInput is the address to verify.
type RequestEmailVerificationInput struct{ Email string }

// RequestEmailVerification starts the buyer registration (monolith UserService.newUser).
type RequestEmailVerification struct{ d *Deps }

// Execute stores a single-use token (hashed) and asks notification-service, through the outbox, to mail the link.
// The token only ever exists in the event and in the mail.
func (u RequestEmailVerification) Execute(ctx context.Context, in RequestEmailVerificationInput) error {
	email, err := domain.ParseEmail(in.Email)
	if err != nil {
		return err
	}
	if _, err := u.d.Accounts.FindByEmail(ctx, principal.TypeUser, email); err == nil {
		return domain.ErrEmailTaken()
	} else if errs.KindOf(err) != errs.KindNotFound {
		return err
	}
	secret, hash, err := u.d.Secrets.New()
	if err != nil {
		return err
	}
	otp := domain.NewOtp(u.d.IDs.New(), email, hash, u.d.Clock.Now(), u.d.Settings.OTPTTL)
	link := u.d.Settings.VerifyLinkBase + "?token=" + url.QueryEscape(secret)

	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.d.Otps.Create(ctx, otp); err != nil {
			return err
		}
		return u.d.emit(ctx, kafkax.TopicEmailVerification, domain.EmailVerificationRequested{
			OtpID: otp.ID(), Email: email.String(), Link: link, ExpiresAt: otp.ExpiresAt()})
	})
}

// VerifyEmailInput carries the token from the mailed link.
type VerifyEmailInput struct{ Token string }

// VerifyEmail completes the buyer registration (monolith UserService.checkLoginEmailTokenService).
type VerifyEmail struct{ d *Deps }

// Execute consumes the token, creates the buyer (without password) if new, and starts a session. Token
// consumption is a compare-and-swap, so a link clicked twice at once creates one account and one session.
func (u VerifyEmail) Execute(ctx context.Context, in VerifyEmailInput) (AuthResult, error) {
	if in.Token == "" {
		return AuthResult{}, domain.ErrTokenInvalid()
	}
	var result AuthResult
	err := u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		otp, err := u.d.Otps.FindByTokenHash(ctx, domain.HashToken(in.Token))
		if err != nil {
			return err
		}
		if err := otp.Consume(u.d.Clock.Now()); err != nil {
			return err
		}
		if err := u.d.Otps.MarkUsed(ctx, otp); err != nil {
			return err
		}

		acc, err := u.d.Accounts.FindByEmail(ctx, principal.TypeUser, otp.Email())
		switch {
		case err == nil:
		case errs.KindOf(err) == errs.KindNotFound:
			acc = domain.NewUser(u.d.IDs.New(), otp.Email(), u.d.Clock.Now())
			if err := u.d.Accounts.Create(ctx, acc); err != nil {
				return err
			}
			if err := u.d.emit(ctx, kafkax.TopicUserRegistered, domain.UserRegistered{UserID: acc.ID(), Email: acc.Email().String()}); err != nil {
				return err
			}
		default:
			return err
		}
		tokens, err := u.d.startSession(ctx, acc)
		if err != nil {
			return err
		}
		result = AuthResult{Account: viewOf(acc), Tokens: tokens}
		return nil
	})
	if err != nil {
		return AuthResult{}, err
	}
	return result, nil
}
