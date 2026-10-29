//go:build integration

package seeders_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/an-vt/ecom/auth-service/internal/database/migrations"
	_ "github.com/an-vt/ecom/auth-service/internal/database/seeders"
	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/repository/postgres"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func seeded(t *testing.T, env map[string]string) (*pgdb.Store, func() ([]string, error)) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Setenv("DATABASE_URL", "postgres://unused")
	ctx := context.Background()
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "auth_seed"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = pgmigrate.NewRunner(store.Raw(), "auth_seed", nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store, func() ([]string, error) { return pgmigrate.Seeders.Run(ctx, store.Raw(), "") }
}

func TestDatabaseSeederIsIdempotent(t *testing.T) {
	store, seed := seeded(t, map[string]string{
		"SEED_API_KEY": "dev-key", "SEED_ADMIN_EMAIL": "root@ecom.test", "SEED_ADMIN_PASSWORD": "a-long-admin-password", "BCRYPT_COST": "4",
	})
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		started, err := seed()
		require.NoError(t, err, "run %d", i)
		assert.Equal(t, []string{"DatabaseSeeder"}, started)
	}

	repos := postgres.New(store)
	roles, err := repos.Rbac.ListRoles(ctx)
	require.NoError(t, err)
	names := []string{}
	for _, r := range roles {
		names = append(names, r.Name)
	}
	assert.Equal(t, []string{"admin", "shop", "user"}, names)
	grants, err := repos.Rbac.ActiveGrants(ctx)
	require.NoError(t, err)
	assert.Len(t, grants, 5)

	email, _ := domain.ParseEmail("root@ecom.test")
	admin, err := repos.Accounts.FindByEmail(ctx, principal.TypeUser, email)
	require.NoError(t, err)
	assert.Contains(t, admin.Roles(), "admin")
	assert.True(t, admin.HasPassword())
	assert.NoError(t, admin.CheckLogin())

	key, err := repos.ApiKeys.FindByHash(ctx, domain.HashToken("dev-key"))
	require.NoError(t, err)
	require.NotNil(t, key)
	assert.True(t, key.Allows(domain.PermissionAll))

	var accounts int
	require.NoError(t, store.Raw().NewSelect().TableExpr("accounts").ColumnExpr("count(*)").Scan(ctx, &accounts))
	assert.Equal(t, 1, accounts, "re-seeding never duplicates the admin")
}

func TestSeedersSkipWhatIsNotConfigured(t *testing.T) {
	store, seed := seeded(t, map[string]string{"SEED_API_KEY": "", "SEED_ADMIN_EMAIL": "", "SEED_ADMIN_PASSWORD": ""})
	_, err := seed()
	require.NoError(t, err)
	var accounts, keys int
	require.NoError(t, store.Raw().NewSelect().TableExpr("accounts").ColumnExpr("count(*)").Scan(context.Background(), &accounts))
	require.NoError(t, store.Raw().NewSelect().TableExpr("api_keys").ColumnExpr("count(*)").Scan(context.Background(), &keys))
	assert.Equal(t, 0, accounts, "no default admin without configuration")
	assert.Equal(t, 0, keys, "no default API key without configuration")
}

func TestAdminSeederNeverOverwritesAnExistingAccount(t *testing.T) {
	store, seed := seeded(t, map[string]string{"SEED_ADMIN_EMAIL": "root@ecom.test", "SEED_ADMIN_PASSWORD": "first-admin-password", "BCRYPT_COST": "4"})
	ctx := context.Background()
	_, err := seed()
	require.NoError(t, err)
	repos := postgres.New(store)
	email, _ := domain.ParseEmail("root@ecom.test")
	before, _ := repos.Accounts.FindByEmail(ctx, principal.TypeUser, email)

	t.Setenv("SEED_ADMIN_PASSWORD", "a-different-password")
	_, err = seed()
	require.NoError(t, err)
	after, _ := repos.Accounts.FindByEmail(ctx, principal.TypeUser, email)
	assert.Equal(t, before.PasswordHash(), after.PasswordHash(), "a changed env must not silently reset the operator's password")
}
