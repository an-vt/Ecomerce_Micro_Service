package domain

import (
	"context"

	"github.com/an-vt/ecom/platform-go/principal"
)

// All repositories take the transaction from ctx (platform-go/pgdb) and return domain errors:
// not found -> ErrAccountNotFound / ErrTokenInvalid, unique violations -> ErrEmailTaken / CodeRoleExists, …

// AccountRepository stores accounts.
type AccountRepository interface {
	Create(ctx context.Context, a *Account) error // ErrEmailTaken when (type, email) exists
	FindByID(ctx context.Context, id string) (*Account, error)
	FindByEmail(ctx context.Context, typ principal.Type, email Email) (*Account, error)
	Update(ctx context.Context, a *Account) error
}

// SessionRepository stores sessions.
type SessionRepository interface {
	Create(ctx context.Context, s *Session) error
	FindByID(ctx context.Context, id string) (*Session, error)
	FindByRefreshHash(ctx context.Context, hash string) (*Session, error)
	// FindByUsedHash finds the session a rotated-away token belonged to (reuse detection).
	FindByUsedHash(ctx context.Context, hash string) (*Session, error)
	// Rotate saves the rotated state only if the stored refresh hash is still oldHash and the session is not
	// revoked (compare-and-swap): of two concurrent refreshes with one token exactly one wins. The loser gets
	// ErrTokenInvalid.
	Rotate(ctx context.Context, s *Session, oldHash string) error
	// Revoke saves the revocation.
	Revoke(ctx context.Context, s *Session) error
}

// OtpRepository stores one-time tokens.
type OtpRepository interface {
	Create(ctx context.Context, o *Otp) error
	FindByTokenHash(ctx context.Context, hash string) (*Otp, error) // ErrTokenInvalid when unknown
	// MarkUsed saves the consumption only if the token is still unused (compare-and-swap).
	MarkUsed(ctx context.Context, o *Otp) error
}

// ApiKeyRepository stores API keys.
type ApiKeyRepository interface {
	FindByHash(ctx context.Context, hash string) (*ApiKey, error) // (nil, nil) when unknown
	Save(ctx context.Context, k *ApiKey) error                    // upsert by key hash
}

// RbacRepository stores roles, resources and grants.
type RbacRepository interface {
	CreateRole(ctx context.Context, r *Role) error // CodeRoleExists
	ListRoles(ctx context.Context) ([]Role, error)
	UpsertRole(ctx context.Context, r *Role) error         // used by seeders
	CreateResource(ctx context.Context, r *Resource) error // CodeResourceExists
	ListResources(ctx context.Context) ([]Resource, error)
	UpsertResource(ctx context.Context, r *Resource) error
	// MissingResources returns those of names that do not exist.
	MissingResources(ctx context.Context, names []string) ([]string, error)
	// ActiveGrants flattens the grants of all active roles.
	ActiveGrants(ctx context.Context) ([]RoleGrant, error)
}
