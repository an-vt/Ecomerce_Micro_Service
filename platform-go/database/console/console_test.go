package console_test

import (
	"bytes"
	"context"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/database/console"
	"github.com/an-vt/ecom/platform-go/database/migration"
)

type fakeMigrator struct {
	calls []string
	err   error
	rows  []migration.StatusRow
}

func (f *fakeMigrator) rec(s string) { f.calls = append(f.calls, s) }
func (f *fakeMigrator) Migrate(_ context.Context, pretend bool) ([]string, error) {
	if pretend {
		f.rec("migrate --pretend")
	} else {
		f.rec("migrate")
	}
	return []string{"2026_10_30_000010_create_accounts_table"}, f.err
}
func (f *fakeMigrator) Rollback(_ context.Context, step int) ([]string, error) {
	f.rec("rollback " + string(rune('0'+step)))
	return nil, f.err
}
func (f *fakeMigrator) Reset(context.Context) ([]string, error) { f.rec("reset"); return nil, f.err }
func (f *fakeMigrator) Refresh(context.Context) ([]string, []string, error) {
	f.rec("refresh")
	return []string{"b"}, []string{"a"}, f.err
}
func (f *fakeMigrator) Fresh(context.Context) ([]string, error) {
	f.rec("fresh")
	return []string{"a"}, f.err
}
func (f *fakeMigrator) Status(context.Context) ([]migration.StatusRow, error) {
	return f.rows, f.err
}

type fakeSeeder struct {
	classes []string
	err     error
}

func (f *fakeSeeder) Seed(_ context.Context, class string) ([]string, error) {
	f.classes = append(f.classes, class)
	return []string{"DatabaseSeeder"}, f.err
}

type run struct {
	code     int
	out, err string
}

func exec(t *testing.T, m console.Migrator, s console.Seeder, o console.Options, args ...string) run {
	t.Helper()
	var out, errb bytes.Buffer
	o.Out, o.Err = &out, &errb
	code := console.Run(context.Background(), args, m, s, o)
	return run{code, out.String(), errb.String()}
}

func TestMigrateCommandFamily(t *testing.T) {
	m := &fakeMigrator{}
	s := &fakeSeeder{}

	r := exec(t, m, s, console.Options{}, "migrate")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.out, "migrated  2026_10_30_000010_create_accounts_table")

	r = exec(t, m, s, console.Options{}, "migrate", "--pretend")
	assert.Contains(t, r.out, "pending  2026_10_30_000010_create_accounts_table")

	exec(t, m, s, console.Options{}, "migrate:rollback", "--step=2")
	exec(t, m, s, console.Options{}, "migrate:rollback")
	exec(t, m, s, console.Options{}, "migrate:reset")
	r = exec(t, m, s, console.Options{}, "migrate:refresh", "--seed")
	assert.Contains(t, r.out, "rolled back  b")
	assert.Contains(t, r.out, "seeded  DatabaseSeeder")
	r = exec(t, m, s, console.Options{}, "migrate:fresh", "--seed", "--class=RbacSeeder")
	assert.Equal(t, 0, r.code)

	assert.Equal(t, []string{"migrate", "migrate --pretend", "rollback 2", "rollback 1", "reset", "refresh", "fresh"}, m.calls)
	assert.Equal(t, []string{"", "RbacSeeder"}, s.classes)
}

func TestNothingToDoMessage(t *testing.T) {
	r := exec(t, &fakeMigrator{}, nil, console.Options{}, "migrate:reset")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.out, "Nothing to rolled back.")
}

func TestStatusTable(t *testing.T) {
	m := &fakeMigrator{rows: []migration.StatusRow{
		{Name: "2026_10_30_000010_a", Ran: true, Batch: 1}, {Name: "2026_10_30_000020_b"},
	}}
	r := exec(t, m, nil, console.Options{}, "migrate:status")
	require.Equal(t, 0, r.code)
	lines := strings.Split(strings.TrimSpace(r.out), "\n")
	require.Len(t, lines, 3)
	assert.Regexp(t, `^RAN\s+BATCH\s+MIGRATION$`, lines[0])
	assert.Regexp(t, `^Yes\s+1\s+2026_10_30_000010_a$`, lines[1])
	assert.Regexp(t, `^No\s+-\s+2026_10_30_000020_b$`, lines[2])
}

func TestProductionGuards(t *testing.T) {
	prod := console.Options{Env: "production"}
	for _, cmd := range []string{"migrate", "migrate:rollback", "migrate:reset", "migrate:refresh", "db:seed"} {
		m, s := &fakeMigrator{}, &fakeSeeder{}
		r := exec(t, m, s, prod, cmd)
		assert.Equal(t, 1, r.code, cmd)
		assert.Contains(t, r.err, "--force", cmd)
		assert.Empty(t, m.calls, cmd+": nothing ran")
		assert.Empty(t, s.classes, cmd)
	}
	m := &fakeMigrator{}
	assert.Equal(t, 0, exec(t, m, nil, prod, "migrate", "--force").code)
	assert.Equal(t, []string{"migrate"}, m.calls)

	m = &fakeMigrator{}
	r := exec(t, m, nil, prod, "migrate:fresh", "--force")
	assert.Equal(t, 1, r.code, "fresh is never allowed in production, even with --force")
	assert.Contains(t, r.err, "destructive")
	assert.Empty(t, m.calls)

	assert.Equal(t, 0, exec(t, &fakeMigrator{}, nil, prod, "migrate:status").code, "read-only commands need no --force")
}

func TestErrorsGiveExitCode1(t *testing.T) {
	m := &fakeMigrator{err: errors.New("db unreachable")}
	r := exec(t, m, nil, console.Options{}, "migrate")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.err, "db unreachable")

	r = exec(t, &fakeMigrator{}, nil, console.Options{}, "db:seed")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.err, "no seeders")

	s := &fakeSeeder{err: errors.New("seed failed")}
	assert.Equal(t, 1, exec(t, &fakeMigrator{}, s, console.Options{}, "db:seed", "--class=X").code)
	assert.Equal(t, 1, exec(t, &fakeMigrator{err: errors.New("x")}, nil, console.Options{}, "migrate:status").code)
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"help"}, {"--help"}} {
		r := exec(t, nil, nil, console.Options{}, args...)
		assert.Equal(t, 0, r.code)
		assert.Contains(t, r.out, "make:migration")
	}
	r := exec(t, &fakeMigrator{}, nil, console.Options{}, "migrate:wat")
	assert.Equal(t, 2, r.code)
	assert.Contains(t, r.err, "unknown command")
	assert.Equal(t, 2, exec(t, &fakeMigrator{}, nil, console.Options{}, "migrate", "--nonsense").code)
}

// ---- generators -----------------------------------------------------------------------------------------------

func gen(t *testing.T, kind console.Kind, args ...string) (run, string) {
	t.Helper()
	dir := t.TempDir()
	o := console.Options{
		Kind: kind, MigrationsDir: filepath.Join(dir, "migrations"), SeedersDir: filepath.Join(dir, "seeders"),
		Now: func() time.Time { return time.Date(2026, 10, 30, 12, 30, 45, 0, time.UTC) },
	}
	return exec(t, nil, nil, o, args...), dir
}

func parseOK(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = parser.ParseFile(token.NewFileSet(), path, b, parser.AllErrors)
	require.NoError(t, err, "generated code must be valid Go:\n%s", b)
	return string(b)
}

func TestMakeMigrationCreateInferredFromName(t *testing.T) {
	r, dir := gen(t, console.KindPostgres, "make:migration", "create_accounts_table")
	require.Equal(t, 0, r.code, r.err)
	path := filepath.Join(dir, "migrations", "2026_10_30_123045_create_accounts_table.go")
	assert.Contains(t, r.out, path)
	src := parseOK(t, path)
	assert.Contains(t, src, `return "2026_10_30_123045_create_accounts_table"`)
	assert.Contains(t, src, "type createAccountsTable struct{}")
	assert.Contains(t, src, "pgmigrate.Register(&createAccountsTable{})")
	assert.Contains(t, src, "CREATE TABLE accounts")
	assert.Contains(t, src, "DROP TABLE IF EXISTS accounts")
	assert.True(t, migration.NamePattern.MatchString("2026_10_30_123045_create_accounts_table"))
}

func TestMakeMigrationTableAndBlankAndFlagOrder(t *testing.T) {
	r, dir := gen(t, console.KindPostgres, "make:migration", "--table=merchants", "add_risk_level_to_merchants")
	require.Equal(t, 0, r.code, r.err)
	src := parseOK(t, filepath.Join(dir, "migrations", "2026_10_30_123045_add_risk_level_to_merchants.go"))
	assert.Contains(t, src, "ALTER TABLE merchants ADD COLUMN")

	r, dir = gen(t, console.KindPostgres, "make:migration", "seed_reference_rows")
	require.Equal(t, 0, r.code, r.err)
	src = parseOK(t, filepath.Join(dir, "migrations", "2026_10_30_123045_seed_reference_rows.go"))
	assert.Contains(t, src, "TODO: write the change")

	r, dir = gen(t, console.KindPostgres, "make:migration", "make_orders", "--create=orders")
	require.Equal(t, 0, r.code, r.err)
	assert.Contains(t, parseOK(t, filepath.Join(dir, "migrations", "2026_10_30_123045_make_orders.go")), "CREATE TABLE orders")
}

func TestMakeMigrationMongo(t *testing.T) {
	r, dir := gen(t, console.KindMongo, "make:migration", "create_products_collection", "--create=products")
	require.Equal(t, 0, r.code, r.err)
	src := parseOK(t, filepath.Join(dir, "migrations", "2026_10_30_123045_create_products_collection.go"))
	assert.Contains(t, src, "mongomigrate.Register")
	assert.Contains(t, src, `CreateCollection(ctx, "products")`)
}

func TestMakeMigrationRejectsBadInput(t *testing.T) {
	for _, args := range [][]string{
		{"make:migration"},
		{"make:migration", "CreateUsers"},
		{"make:migration", "create users"},
		{"make:migration", "a", "b"},
		{"make:migration", "x", "--create=a", "--table=b"},
	} {
		r, _ := gen(t, console.KindPostgres, args...)
		assert.Equal(t, 2, r.code, strings.Join(args, " "))
	}
}

func TestMakeMigrationRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	o := console.Options{MigrationsDir: dir, Now: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }}
	assert.Equal(t, 0, exec(t, nil, nil, o, "make:migration", "create_a_table").code)
	r := exec(t, nil, nil, o, "make:migration", "create_a_table")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.err, "already exists")
}

func TestMakeSeeder(t *testing.T) {
	r, dir := gen(t, console.KindPostgres, "make:seeder", "RbacSeeder")
	require.Equal(t, 0, r.code, r.err)
	src := parseOK(t, filepath.Join(dir, "seeders", "RbacSeeder.go"))
	assert.Contains(t, src, "pgmigrate.RegisterSeeder(&RbacSeeder{})")
	assert.Contains(t, src, `return "RbacSeeder"`)

	r, dir = gen(t, console.KindMongo, "make:seeder", "TemplateSeeder")
	require.Equal(t, 0, r.code, r.err)
	assert.Contains(t, parseOK(t, filepath.Join(dir, "seeders", "TemplateSeeder.go")), "mongomigrate.RegisterSeeder")

	for _, bad := range []string{"rbac", "Rbac", "rbacSeeder", "Rbac Seeder"} {
		r, _ = gen(t, console.KindPostgres, "make:seeder", bad)
		assert.Equal(t, 2, r.code, bad)
	}
}
