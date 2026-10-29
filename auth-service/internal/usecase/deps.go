package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// Settings are the tunables of the use cases (docs/11 §3.2).
type Settings struct {
	RefreshTTL     time.Duration // session lifetime, 7 days
	OTPTTL         time.Duration // email verification link lifetime, 5 minutes
	VerifyLinkBase string        // e.g. http://localhost:8088/v1/api/user/welcome_back
}

// Deps is everything the use cases need.
type Deps struct {
	Accounts domain.AccountRepository
	Sessions domain.SessionRepository
	Otps     domain.OtpRepository
	ApiKeys  domain.ApiKeyRepository
	Rbac     domain.RbacRepository

	Hasher   PasswordHasher
	Signer   AccessTokenSigner
	Secrets  SecretGenerator
	Denylist Denylist
	Tx       TxManager
	Outbox   Outbox

	Clock    clock.Clock
	IDs      idgen.Generator
	Settings Settings
}

// emit records a domain event in the outbox of the current transaction.
func (d *Deps) emit(ctx context.Context, topic string, ev event.Event) error {
	env, err := event.FromEvent(d.IDs.New(), d.Clock.Now(), ev)
	if err != nil {
		return err
	}
	msg, err := outbox.New(ctx, topic, env)
	if err != nil {
		return err
	}
	return d.Outbox.Add(ctx, msg)
}
