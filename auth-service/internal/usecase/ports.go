// Package usecase holds the application logic of auth-service. It depends on the domain and on the small
// interfaces below; concrete adapters (bcrypt, PostgreSQL, Redis, …) are wired in internal/app.
package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// PasswordHasher hashes and checks passwords (bcrypt).
type PasswordHasher interface {
	Hash(plain string) (string, error)
	// Compare returns nil when plain matches hash.
	Compare(hash, plain string) error
}

// AccessTokenSigner issues RS256 access tokens and publishes the verification keys.
type AccessTokenSigner interface {
	Sign(accountID, accountType, email string, roles []string, sessionID string) (token string, expiresAt time.Time, err error)
	JWKS() jwtx.JWKS
	TTL() time.Duration
}

// SecretGenerator creates opaque random secrets (refresh tokens, one-time tokens) and their storage hash.
type SecretGenerator interface {
	New() (secret, hash string, err error)
}

// Denylist marks session ids revoked until their access tokens would have expired.
type Denylist interface {
	Revoke(ctx context.Context, sessionID string, ttl time.Duration) error
}

// TxManager runs fn in one database transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Outbox adds messages in the transaction of ctx.
type Outbox interface {
	Add(ctx context.Context, msgs ...outbox.Message) error
}
