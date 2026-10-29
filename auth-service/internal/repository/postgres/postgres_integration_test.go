//go:build integration

package postgres_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/an-vt/ecom/auth-service/internal/database/migrations"
	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/repository/postgres"
	"github.com/an-vt/ecom/auth-service/internal/repotest"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func openMigrated(t *testing.T) *pgdb.Store {
	t.Helper()
	ctx := context.Background()
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "auth_test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = pgmigrate.NewRunner(store.Raw(), "auth_test", nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

// The PostgreSQL repositories must satisfy exactly the contract the in-memory fakes satisfy.
func TestPostgresSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		r := postgres.New(openMigrated(t))
		return repotest.Repos{Accounts: r.Accounts, Sessions: r.Sessions, Otps: r.Otps, ApiKeys: r.ApiKeys, Rbac: r.Rbac}
	})
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := pgmigrate.NewRunner(store.Raw(), "auth_test", nil)

	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 8)
	for _, r := range rows {
		assert.True(t, r.Ran, r.Name)
	}

	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 8)
	var tables int
	require.NoError(t, store.Raw().NewSelect().ColumnExpr("count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name <> 'migrations'").Scan(ctx, &tables))
	assert.Equal(t, 0, tables, "down removes everything the migrations created")

	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 8, "up -> down -> up")
}

func TestOnlyOneOfTwoConcurrentRefreshesWins(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	r := postgres.New(store)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

	email, _ := domain.ParseEmail("a@example.com")
	acc, _ := domain.NewShop("00000000-0000-4000-8000-000000000001", "S", email, "hash", now)
	require.NoError(t, r.Accounts.Create(ctx, acc))
	require.NoError(t, r.Sessions.Create(ctx, domain.NewSession("00000000-0000-4000-8000-0000000000a1", acc.ID(), "h1", now, time.Hour)))

	const racers = 8
	var wins, losses atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := r.Sessions.FindByRefreshHash(ctx, "h1")
			if err != nil {
				losses.Add(1)
				return
			}
			old, err := s.Rotate("h-new-"+time.Now().Format("150405.000000000"), now.Add(time.Minute), time.Hour)
			require.NoError(t, err)
			<-start
			if err := r.Sessions.Rotate(ctx, s, old); err == nil {
				wins.Add(1)
			} else {
				assert.Equal(t, domain.CodeTokenInvalid, errs.CodeOf(err))
				losses.Add(1)
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	close(start)
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load(), "exactly one refresh succeeds")
	assert.Equal(t, int32(racers-1), losses.Load())
}

func TestOnlyOneOfTwoConcurrentVerificationsConsumesTheToken(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	r := postgres.New(store)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	email, _ := domain.ParseEmail("b@example.com")
	require.NoError(t, r.Otps.Create(ctx, domain.NewOtp("00000000-0000-4000-8000-0000000000b1", email, "otp-hash", now, time.Hour)))

	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			o, err := r.Otps.FindByTokenHash(ctx, "otp-hash")
			require.NoError(t, err)
			if o.Consume(now.Add(time.Minute)) != nil { // someone already finished: a loss
				return
			}
			if r.Otps.MarkUsed(ctx, o) == nil {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), wins.Load())
}

func TestConcurrentSignUpsOfOneEmailCreateOneAccount(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	r := postgres.New(store)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	email, _ := domain.ParseEmail("race@example.com")

	var ok, taken atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a, _ := domain.NewShop("00000000-0000-4000-8000-0000000001"+string(rune('0'+i))+"0", "S", email, "h", now)
			err := r.Accounts.Create(ctx, a)
			switch errs.CodeOf(err) {
			case domain.CodeEmailTaken:
				taken.Add(1)
			default:
				if err == nil {
					ok.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(1), ok.Load())
	assert.Equal(t, int32(7), taken.Load(), "the unique index, not application code, decides")
}

func TestRepositoriesJoinTheSurroundingTransaction(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	r := postgres.New(store)
	tx := pgdb.NewTxManager(store)
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	email, _ := domain.ParseEmail("tx@example.com")
	acc, _ := domain.NewShop("00000000-0000-4000-8000-000000000001", "S", email, "h", now)

	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.Accounts.Create(ctx, acc); err != nil {
			return err
		}
		return r.Sessions.Create(ctx, domain.NewSession("00000000-0000-4000-8000-0000000000a1", acc.ID(), "h1", now, time.Hour))
	})
	require.NoError(t, err)

	acc2, _ := domain.NewShop("00000000-0000-4000-8000-000000000002", "S2", mustEmail(t, "tx2@example.com"), "h", now)
	err = tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := r.Accounts.Create(ctx, acc2); err != nil {
			return err
		}
		// the second statement violates the foreign key: the account insert must roll back with it
		return r.Sessions.Create(ctx, domain.NewSession("00000000-0000-4000-8000-0000000000a2", "00000000-0000-4000-8000-0000000000ff", "h2", now, time.Hour))
	})
	require.Error(t, err)
	_, err = r.Accounts.FindByID(ctx, acc2.ID())
	assert.Equal(t, domain.CodeAccountNotFound, errs.CodeOf(err), "all or nothing")
}

func mustEmail(t *testing.T, s string) domain.Email {
	t.Helper()
	e, err := domain.ParseEmail(s)
	require.NoError(t, err)
	return e
}
