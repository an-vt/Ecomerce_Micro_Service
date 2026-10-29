package seeders

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/auth-service/internal/config"
	"github.com/an-vt/ecom/auth-service/internal/domain"
	platform "github.com/an-vt/ecom/platform-go/config"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
	"github.com/an-vt/ecom/platform-go/idgen"
)

func init() { pgmigrate.RegisterSeeder(&ApiKeySeeder{}) }

// ApiKeySeeder stores the development API key from SEED_API_KEY (hashed; the monolith committed real keys in
// .http files). Without the variable nothing is created, so production never gets a default key.
type ApiKeySeeder struct{}

func (s *ApiKeySeeder) Name() string { return "ApiKeySeeder" }

func (s *ApiKeySeeder) Run(ctx context.Context, db bun.IDB, _ *seeder.Set[bun.IDB]) error {
	cfg, err := config.LoadConsole(platform.NewLoader())
	if err != nil {
		return err
	}
	if cfg.SeedAPIKey == "" {
		return nil
	}
	repos, err := store(db)
	if err != nil {
		return err
	}
	return repos.ApiKeys.Save(ctx, &domain.ApiKey{ID: idgen.UUID{}.New(), Name: "seeded development key",
		KeyHash: domain.HashToken(cfg.SeedAPIKey), Permissions: []string{domain.PermissionAll}, Active: true})
}
