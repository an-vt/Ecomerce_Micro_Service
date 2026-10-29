//go:build integration

package pgmigrate_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/testkit"
)

// sqlMigration is a migration defined by two statements.
type sqlMigration struct {
	name, up, down string
	noTx           bool
}

func (m sqlMigration) Name() string { return m.name }
func (m sqlMigration) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, m.up)
	return err
}
func (m sqlMigration) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, m.down)
	return err
}
func (m sqlMigration) NoTransaction() bool { return m.noTx }

// failingMigration creates a table and then fails: the table must not survive.
type failingMigration struct{}

func (failingMigration) Name() string { return "2026_10_30_000099_create_broken_table" }
func (failingMigration) Up(ctx context.Context, db bun.IDB) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE broken (id INT)`); err != nil {
		return err
	}
	return errors.New("migration bug after the DDL")
}
func (failingMigration) Down(context.Context, bun.IDB) error { return nil }

var (
	accounts = sqlMigration{name: "2026_10_30_000010_create_accounts_table",
		up: `CREATE TABLE accounts (id INT PRIMARY KEY, email TEXT)`, down: `DROP TABLE accounts`}
	sessions = sqlMigration{name: "2026_10_30_000020_create_sessions_table",
		up: `CREATE TABLE sessions (id INT PRIMARY KEY)`, down: `DROP TABLE sessions`}
	emailIdx = sqlMigration{name: "2026_10_31_000010_index_accounts_email", noTx: true,
		up: `CREATE INDEX CONCURRENTLY idx_accounts_email ON accounts (email)`, down: `DROP INDEX idx_accounts_email`}
)

func setup(t *testing.T, ms ...migration.Migration[bun.IDB]) (*pgdb.Store, *migration.Runner[bun.IDB]) {
	t.Helper()
	store, err := pgdb.Open(context.Background(), pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "t"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	set := &migration.Set[bun.IDB]{}
	for _, m := range ms {
		set.Register(m)
	}
	return store, pgmigrate.NewRunner(store.Raw(), "t", set)
}

func tableExists(t *testing.T, s *pgdb.Store, name string) bool {
	t.Helper()
	var ok bool
	require.NoError(t, s.Raw().NewSelect().ColumnExpr("to_regclass(?) IS NOT NULL", "public."+name).Scan(context.Background(), &ok))
	return ok
}

func TestMigrateRollbackMigrate(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, accounts, sessions)

	ran, err := r.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, []string{accounts.name, sessions.name}, ran)
	assert.True(t, tableExists(t, s, "accounts"))
	assert.True(t, tableExists(t, s, "sessions"))

	rows, err := r.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, []migration.StatusRow{{Name: accounts.name, Ran: true, Batch: 1}, {Name: sessions.name, Ran: true, Batch: 1}}, rows)

	back, err := r.Rollback(ctx, 1)
	require.NoError(t, err)
	assert.Equal(t, []string{sessions.name, accounts.name}, back)
	assert.False(t, tableExists(t, s, "accounts"))
	assert.True(t, tableExists(t, s, "migrations"), "the tracking table stays")

	ran, err = r.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 2, "up -> down -> up")
}

func TestFailedMigrationIsAtomic(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, accounts, failingMigration{})
	_, err := r.Migrate(ctx, false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "migration bug")

	assert.True(t, tableExists(t, s, "accounts"), "earlier migration stays applied")
	assert.False(t, tableExists(t, s, "broken"), "PostgreSQL DDL rolled back with the failed migration")
	rows, err := r.Status(ctx)
	require.NoError(t, err)
	assert.True(t, rows[0].Ran)
	assert.False(t, rows[1].Ran, "no tracking row for the failed migration")
}

func TestTwoRunnersStartingTogetherApplyEachMigrationOnce(t *testing.T) {
	ctx := context.Background()
	slow := sqlMigration{name: "2026_10_30_000030_slow_table",
		up: `SELECT pg_sleep(0.5); CREATE TABLE slow_t (id INT)`, down: `DROP TABLE slow_t`}
	s, r1 := setup(t, accounts, slow)
	set := &migration.Set[bun.IDB]{}
	set.Register(accounts)
	set.Register(slow)
	r2 := pgmigrate.NewRunner(s.Raw(), "t", set)

	var wg sync.WaitGroup
	results := make([][]string, 2)
	errs := make([]error, 2)
	for i, r := range []*migration.Runner[bun.IDB]{r1, r2} {
		wg.Add(1)
		go func() { defer wg.Done(); results[i], errs[i] = r.Migrate(ctx, false) }()
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	assert.Equal(t, 2, len(results[0])+len(results[1]), "each migration ran exactly once across both runners")
	assert.True(t, tableExists(t, s, "slow_t"))
}

func TestNoTransactionMigration(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, accounts, emailIdx)
	_, err := r.Migrate(ctx, false)
	require.NoError(t, err, "CREATE INDEX CONCURRENTLY cannot run inside a transaction")
	var n int
	require.NoError(t, s.Raw().NewSelect().ColumnExpr("count(*) FROM pg_indexes WHERE indexname = 'idx_accounts_email'").Scan(ctx, &n))
	assert.Equal(t, 1, n)

	_, err = r.Rollback(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, s.Raw().NewSelect().ColumnExpr("count(*) FROM pg_indexes WHERE indexname = 'idx_accounts_email'").Scan(ctx, &n))
	assert.Equal(t, 0, n)
}

func TestFreshDropsEverythingAndMigratesAgain(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, accounts)
	_, err := r.Migrate(ctx, false)
	require.NoError(t, err)
	_, err = s.Raw().ExecContext(ctx, `CREATE TABLE leftover (id INT)`)
	require.NoError(t, err)

	ran, err := r.Fresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{accounts.name}, ran)
	assert.False(t, tableExists(t, s, "leftover"))
	assert.True(t, tableExists(t, s, "accounts"))
}

func TestPackageRegistryAndPretend(t *testing.T) {
	ctx := context.Background()
	s, r := setup(t, accounts)
	ran, err := r.Migrate(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, []string{accounts.name}, ran)
	assert.False(t, tableExists(t, s, "accounts"), "--pretend changes nothing")

	pgmigrate.Register(sqlMigration{name: "2026_10_30_000001_registry_probe", up: `SELECT 1`, down: `SELECT 1`})
	assert.Len(t, pgmigrate.Migrations.Sorted(), 1)
}
