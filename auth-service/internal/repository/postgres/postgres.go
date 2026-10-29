// Package postgres implements the auth repositories with bun (docs/04-data-model.md §1). Every method takes its
// connection from pgdb.Store.DB(ctx), so it joins the surrounding transaction when there is one.
package postgres

import (
	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

// Repositories bundles the PostgreSQL implementations.
type Repositories struct {
	Accounts domain.AccountRepository
	Sessions domain.SessionRepository
	Otps     domain.OtpRepository
	ApiKeys  domain.ApiKeyRepository
	Rbac     domain.RbacRepository
}

// New creates all repositories over one store.
func New(s *pgdb.Store) Repositories {
	return Repositories{
		Accounts: &accountRepo{s: s},
		Sessions: &sessionRepo{s: s},
		Otps:     &otpRepo{s: s},
		ApiKeys:  &apiKeyRepo{s: s},
		Rbac:     &rbacRepo{s: s, tx: pgdb.NewTxManager(s)},
	}
}
