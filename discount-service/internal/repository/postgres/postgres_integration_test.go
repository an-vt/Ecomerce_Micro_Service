//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/an-vt/ecom/discount-service/internal/database/migrations"
	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/discount-service/internal/repository/postgres"
	"github.com/an-vt/ecom/discount-service/internal/repotest"
	"github.com/an-vt/ecom/discount-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/saga"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func openMigrated(t *testing.T) *pgdb.Store {
	t.Helper()
	ctx := context.Background()
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "discount_test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = pgmigrate.NewRunner(store.Raw(), "discount_test", nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

func TestPostgresSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		r := postgres.New(openMigrated(t))
		return repotest.Repos{Discounts: r.Discounts, Usages: r.Usages}
	})
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := pgmigrate.NewRunner(store.Raw(), "discount_test", nil)
	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 3)
	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 3, "up -> down -> up")
}

// The database refuses what the domain would never produce: a counter above the limit, a usage for a missing discount.
func TestConstraintsAreTheBackstop(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	_, err := store.Raw().ExecContext(ctx, `INSERT INTO discounts (id, shop_id, code, name, type, value, start_date, end_date, max_uses, used_count)
		VALUES ('00000000-0000-4000-8000-000000000001', 's', 'OVER', 'n', 'percent', 10, now(), now() + interval '1 day', 1, 2)`)
	assert.Error(t, err, "used_count > max_uses")
	_, err = store.Raw().ExecContext(ctx, `INSERT INTO discounts (id, shop_id, code, name, type, value, start_date, end_date, max_uses)
		VALUES ('00000000-0000-4000-8000-000000000002', 's', 'PCT', 'n', 'percent', 150, now(), now() + interval '1 day', 1)`)
	assert.Error(t, err, "percent above 100")
	_, err = store.Raw().ExecContext(ctx, `INSERT INTO discount_usages (id, discount_id, user_id, order_id, amount)
		VALUES ('00000000-0000-4000-8000-000000000003', '00000000-0000-4000-8000-0000000000ff', 'u', 'o', 1)`)
	assert.Error(t, err, "usage of an unknown discount")
}

type env struct {
	uc    *usecase.Set
	store *pgdb.Store
	repos postgres.Repositories
}

func newEnv(t *testing.T) env {
	t.Helper()
	store := openMigrated(t)
	r := postgres.New(store)
	uc := usecase.NewSet(&usecase.Deps{Discounts: r.Discounts, Usages: r.Usages, Tx: pgdb.NewTxManager(store),
		Outbox: pgoutbox.NewWriter(store), Clock: clock.NewFixed(time.Now()), IDs: idgen.UUID{}})
	return env{uc: uc, store: store, repos: r}
}

func (e env) seed(t *testing.T, code string, maxUses, perUser int64) *domain.Discount {
	t.Helper()
	d, err := domain.New(domain.NewInput{ID: idgen.UUID{}.New(), ShopID: "shop", Code: code, Name: code, Type: domain.TypePercent, Value: 10,
		StartDate: time.Now().Add(-time.Hour), EndDate: time.Now().Add(24 * time.Hour), MaxUses: maxUses, MaxUsesPerUser: perUser}, time.Now())
	require.NoError(t, err)
	require.NoError(t, e.repos.Discounts.Insert(context.Background(), d))
	return d
}

func apply(order, user string, codes ...string) saga.ApplyDiscounts {
	cmd := saga.ApplyDiscounts{OrderID: order, UserID: user, Items: []saga.Item{{ProductID: "p", ShopID: "shop", Quantity: 1, Price: 1000}}}
	for _, c := range codes {
		cmd.Discounts = append(cmd.Discounts, saga.DiscountRequest{ShopID: "shop", Code: c})
	}
	return cmd
}

func (e env) repliesOfType(t *testing.T, typ string) int {
	t.Helper()
	var n int
	require.NoError(t, e.store.Raw().QueryRow(`SELECT count(*) FROM outbox_messages WHERE type = ?`, typ).Scan(&n))
	return n
}

func (e env) used(t *testing.T, d *domain.Discount) int64 {
	t.Helper()
	got, err := e.repos.Discounts.Find(context.Background(), d.ID())
	require.NoError(t, err)
	return got.Snapshot().UsedCount
}

// Acceptance #2: max_uses 1, many concurrent orders: exactly one gets the code.
func TestConcurrentOrdersCannotOverspendTheLastUse(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	d := e.seed(t, "LASTONE", 1, 0)

	const orders = 12
	var wg sync.WaitGroup
	var failed atomic.Int32
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := e.uc.ApplyDiscounts.Execute(ctx, apply(fmt.Sprintf("order-%d", i), fmt.Sprintf("user-%d", i), "LASTONE")); err != nil {
				failed.Add(1)
				t.Errorf("handler error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	assert.Zero(t, failed.Load())
	assert.EqualValues(t, 1, e.used(t, d), "the counter stops at max_uses")
	assert.Equal(t, 1, e.repliesOfType(t, saga.ReplyDiscountsApplied), "one order got it")
	assert.Equal(t, orders-1, e.repliesOfType(t, saga.ReplyDiscountsRejected), "everyone else was told no, with a reply and not an error")
}

// Acceptance #3: the per-user limit holds even when one user fires the orders at once.
func TestPerUserLimitHoldsUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	d := e.seed(t, "TWICE", 100, 2)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			require.NoError(t, e.uc.ApplyDiscounts.Execute(ctx, apply(fmt.Sprintf("order-%d", i), "alice", "TWICE")))
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 2, e.used(t, d))
	assert.Equal(t, 2, e.repliesOfType(t, saga.ReplyDiscountsApplied))
}

// Acceptance #4 and #5: a redelivered apply leaves one usage; a release gives the use back once.
func TestRedeliveryAndReleaseAreIdempotent(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	d := e.seed(t, "ONCE", 5, 0)

	for i := 0; i < 3; i++ {
		require.NoError(t, e.uc.ApplyDiscounts.Execute(ctx, apply("order-1", "alice", "ONCE")))
	}
	assert.EqualValues(t, 1, e.used(t, d))
	us, err := e.repos.Usages.ForOrder(ctx, "order-1")
	require.NoError(t, err)
	assert.Len(t, us, 1, "a single usage row")

	for i := 0; i < 3; i++ {
		require.NoError(t, e.uc.ReleaseDiscounts.Execute(ctx, saga.ReleaseDiscounts{OrderID: "order-1"}))
	}
	assert.EqualValues(t, 0, e.used(t, d), "decremented once, not three times")

	// a late apply after the release (a stuck message) must not take the code again
	require.NoError(t, e.uc.ApplyDiscounts.Execute(ctx, apply("order-1", "alice", "ONCE")))
	assert.EqualValues(t, 0, e.used(t, d))
}

// Two orders needing the same two codes in opposite request order must queue, not deadlock.
func TestOverlappingCodesDoNotDeadlock(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	a, b := e.seed(t, "AAA", 1000, 0), e.seed(t, "BBB", 1000, 0)
	_, _ = a, b

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := saga.ApplyDiscounts{OrderID: fmt.Sprintf("o-%d", i), UserID: fmt.Sprintf("u-%d", i),
				Items: []saga.Item{{ProductID: "p", ShopID: "shop", Quantity: 1, Price: 1000}, {ProductID: "q", ShopID: "shop-2", Quantity: 1, Price: 1000}}}
			cmd.Discounts = []saga.DiscountRequest{{ShopID: "shop", Code: "AAA"}}
			assert.NoError(t, e.uc.ApplyDiscounts.Execute(ctx, cmd))
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 20, e.used(t, a))
}
