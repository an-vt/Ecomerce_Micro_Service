//go:build integration

package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/an-vt/ecom/order-service/internal/database/migrations"
	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/fake"
	"github.com/an-vt/ecom/order-service/internal/repository/postgres"
	"github.com/an-vt/ecom/order-service/internal/repotest"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func openMigrated(t *testing.T) *pgdb.Store {
	t.Helper()
	ctx := context.Background()
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "order_test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = pgmigrate.NewRunner(store.Raw(), "order_test", nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

func TestPostgresSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		r := postgres.New(openMigrated(t))
		return repotest.Repos{Orders: r.Orders, Sagas: r.Sagas, Keys: r.Keys}
	})
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := pgmigrate.NewRunner(store.Raw(), "order_test", nil)
	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 5)
	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 5)
	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 5, "up -> down -> up")
}

func TestConstraintsAreTheBackstop(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	_, err := store.Raw().ExecContext(ctx, `INSERT INTO orders (id, user_id, status, total_price, total_checkout, shipping, payment_method, payment_token)
		VALUES ('00000000-0000-4000-8000-000000000001', 'u', 'SHIPPED', 1, 1, '{}', 'card', 't')`)
	assert.Error(t, err, "unknown status")
	_, err = store.Raw().ExecContext(ctx, `INSERT INTO orders (id, user_id, status, total_price, total_discount, total_checkout, shipping, payment_method, payment_token)
		VALUES ('00000000-0000-4000-8000-000000000002', 'u', 'PENDING', 100, 200, 1, '{}', 'card', 't')`)
	assert.Error(t, err, "a discount above the total")
	_, err = store.Raw().ExecContext(ctx, `INSERT INTO saga_instances (id, state, has_discounts, step_deadline_at) VALUES ('00000000-0000-4000-8000-0000000000ff', 'RESERVING_STOCK', false, now())`)
	assert.Error(t, err, "a saga needs its order")
}

type env struct {
	uc    *usecase.Set
	store *pgdb.Store
	clk   *clock.Fixed
	repos postgres.Repositories
}

func newEnv(t *testing.T) env {
	t.Helper()
	store := openMigrated(t)
	r := postgres.New(store)
	clk := clock.NewFixed(time.Now())
	cat := &fake.Catalog{Products_: map[string]domain.Product{
		"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 100000, IsPublished: true},
		"p2": {ID: "p2", ShopID: "s2", Name: "Hat", Price: 50000, IsPublished: true}}}
	uc := usecase.NewSet(&usecase.Deps{Orders: r.Orders, Sagas: r.Sagas, Keys: r.Keys, Catalog: cat, Discounts: &fake.Discounts{}, Tx: pgdb.NewTxManager(store),
		Outbox: pgoutbox.NewWriter(store), Clock: clk, IDs: idgen.UUID{}, FeeShip: 5000, Timeouts: domain.Timeouts{Step: 30 * time.Second, Compensation: 30 * time.Second}})
	return env{uc: uc, store: store, clk: clk, repos: r}
}

func buyer(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeUser})
}

func place(key string) usecase.PlaceInput {
	return usecase.PlaceInput{ShopOrders: []usecase.ShopOrder{
		{ShopID: "s1", Items: []usecase.ItemInput{{ProductID: "p1", Quantity: 2}}},
		{ShopID: "s2", Items: []usecase.ItemInput{{ProductID: "p2", Quantity: 1}}}},
		Shipping: domain.Shipping{Name: "An", Address: "1 Main St"}, PaymentMethod: "card", PaymentToken: "tok_ok", IdempotencyKey: key}
}

func (e env) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, e.store.Raw().QueryRow(query, args...).Scan(&n))
	return n
}

// Acceptance #2 under real concurrency: many requests with one Idempotency-Key create one order.
func TestConcurrentRequestsWithOneKeyCreateOneOrder(t *testing.T) {
	e := newEnv(t)
	const n = 12
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := e.uc.PlaceOrder.Execute(buyer("alice"), place("same-key"))
			if assert.NoError(t, err) {
				ids[i] = p.OrderID
			}
		}(i)
	}
	wg.Wait()

	for _, id := range ids {
		assert.Equal(t, ids[0], id, "every request got the same order")
	}
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM orders`))
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM saga_instances`))
	assert.Equal(t, 1, e.count(t, `SELECT count(*) FROM outbox_messages WHERE type = ?`, saga.CmdReserveStock), "and one ReserveStock")
	assert.Equal(t, 2, e.count(t, `SELECT count(*) FROM order_items`))
}

// Acceptance #4: two sweeper replicas over the same database handle each overdue saga exactly once.
func TestTwoSweepersHandleEachTimeoutOnce(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	const orders = 15
	for i := 0; i < orders; i++ {
		_, err := e.uc.PlaceOrder.Execute(buyer(fmt.Sprintf("user-%d", i)), place("key"))
		require.NoError(t, err)
	}
	e.clk.Advance(time.Minute) // every saga is now overdue

	var wg sync.WaitGroup
	handled := make([]int, 2)
	for replica := 0; replica < 2; replica++ {
		wg.Add(1)
		go func(replica int) {
			defer wg.Done()
			n, err := e.uc.SweepSagaTimeouts.Execute(ctx, 100)
			assert.NoError(t, err)
			handled[replica] = n
		}(replica)
	}
	wg.Wait()

	assert.Equal(t, orders, handled[0]+handled[1], "every timeout handled, none twice")
	assert.Equal(t, orders, e.count(t, `SELECT count(*) FROM outbox_messages WHERE type = ?`, saga.CmdReleaseStock), "one ReleaseStock per order")
	assert.Equal(t, orders, e.count(t, `SELECT count(*) FROM orders WHERE status = 'CANCELLING'`))
	assert.Equal(t, 0, e.count(t, `SELECT count(*) FROM saga_instances WHERE state = 'RESERVING_STOCK'`))
}

// A reply racing the sweeper on the same order is serialised by the row locks: one of them wins, the other sees the
// result of the first and is stale or a no-op, and the order ends in a state the guard allows.
func TestAReplyRacingTheSweeperLeavesAConsistentOrder(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	for i := 0; i < 6; i++ {
		p, err := e.uc.PlaceOrder.Execute(buyer(fmt.Sprintf("u%d", i)), place(fmt.Sprintf("k%d", i)))
		require.NoError(t, err)
		e.clk.Advance(time.Minute)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = e.uc.SweepSagaTimeouts.Execute(ctx, 10) }()
		go func() { defer wg.Done(); _ = e.uc.HandleSagaReply.Execute(ctx, saga.StockReserved{OrderID: p.OrderID}) }()
		wg.Wait()

		var status, state string
		require.NoError(t, e.store.Raw().QueryRow(`SELECT o.status, s.state FROM orders o JOIN saga_instances s ON s.id = o.id WHERE o.id = ?`, p.OrderID).Scan(&status, &state))
		switch state {
		case "COMPENSATING":
			assert.Equal(t, "CANCELLING", status, "the sweeper won: the late StockReserved was ignored")
		case "PROCESSING_PAYMENT":
			assert.Equal(t, "PAYMENT_PROCESSING", status, "the reply won: the sweeper found a fresh deadline")
		default:
			t.Fatalf("unexpected saga state %s with order %s", state, status)
		}
	}
}
