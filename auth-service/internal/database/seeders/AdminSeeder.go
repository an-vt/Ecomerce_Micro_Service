package seeders

import (
	"context"
	"errors"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/auth-service/internal/config"
	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/security"
	platform "github.com/an-vt/ecom/platform-go/config"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
)

func init() { pgmigrate.RegisterSeeder(&AdminSeeder{}) }

var errNotPool = errors.New("seeders need the connection pool (*bun.DB)")

// AdminSeeder creates the first operator account from SEED_ADMIN_EMAIL / SEED_ADMIN_PASSWORD. The operator is a
// buyer-type account holding the admin role; an existing account is never modified.
type AdminSeeder struct{}

func (s *AdminSeeder) Name() string { return "AdminSeeder" }

func (s *AdminSeeder) Run(ctx context.Context, db bun.IDB, _ *seeder.Set[bun.IDB]) error {
	cfg, err := config.LoadConsole(platform.NewLoader())
	if err != nil {
		return err
	}
	if cfg.SeedAdminEmail == "" || cfg.SeedAdminPassword == "" {
		return nil // nothing configured: skip
	}
	repos, err := store(db)
	if err != nil {
		return err
	}
	email, err := domain.ParseEmail(cfg.SeedAdminEmail)
	if err != nil {
		return err
	}
	if _, err := repos.Accounts.FindByEmail(ctx, principal.TypeUser, email); err == nil {
		return nil
	} else if errs.KindOf(err) != errs.KindNotFound {
		return err
	}
	if err := domain.ValidatePassword(cfg.SeedAdminPassword); err != nil {
		return err
	}
	hasher, err := security.NewBcrypt(cfg.BcryptCost)
	if err != nil {
		return err
	}
	hash, err := hasher.Hash(cfg.SeedAdminPassword)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	admin := domain.RestoreAccount(domain.AccountSnapshot{ID: idgen.UUID{}.New(), Type: principal.TypeUser, Email: email, Name: "Administrator",
		PasswordHash: hash, Status: domain.StatusActive, Verified: true, Roles: []string{domain.RoleUser, "admin"}, CreatedAt: now, UpdatedAt: now})
	return repos.Accounts.Create(ctx, admin)
}
