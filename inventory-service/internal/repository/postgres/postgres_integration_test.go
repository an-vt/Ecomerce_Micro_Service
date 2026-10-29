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

	_ "github.com/an-vt/ecom/inventory-service/internal/database/migrations"
	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/inventory-service/internal/repository/postgres"
	"github.com/an-vt/ecom/inventory-service/internal/repotest"
	"github.com/an-vt/ecom/inventory-service/internal/usecase"
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
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "inventory_test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = pgmigrate.NewRunner(store.Raw(), "inventory_test", nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

func TestPostgresSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		r := postgres.New(openMigrated(t))
		return repotest.Repos{Inventories: r.Inventories, Reservations: r.Reservations}
	})
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := pgmigrate.NewRunner(store.Raw(), "inventory_test", nil)
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

func TestDatabaseConstraintsAreTheLastLineOfDefence(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	_, err := store.Raw().ExecContext(ctx, `INSERT INTO inventories (id, product_id, shop_id, stock) VALUES (gen_random_uuid(), 'p', 's', 5)`)
	require.NoError(t, err)
	_, err = store.Raw().ExecContext(ctx, `UPDATE inventories SET stock = stock - 6 WHERE product_id = 'p'`)
	require.Error(t, err)
	assert.True(t, pgdb.IsCheckViolation(err), "a bug in the application can never drive stock below zero")
	_, err = store.Raw().ExecContext(ctx, `UPDATE inventories SET reserved = -1 WHERE product_id = 'p'`)
	assert.True(t, pgdb.IsCheckViolation(err))
}

func newSet(store *pgdb.Store, clk clock.Clock) *usecase.Set {
	r := postgres.New(store)
	return usecase.NewSet(&usecase.Deps{Inventories: r.Inventories, Reservations: r.Reservations, Tx: pgdb.NewTxManager(store),
		Outbox: pgoutbox.NewWriter(store), Clock: clk, IDs: idgen.UUID{}, ReservationTTL: 15 * time.Minute})
}

func stockOf(t *testing.T, store *pgdb.Store, product string) (int64, int64) {
	t.Helper()
	var s, r int64
	require.NoError(t, store.Raw().NewSelect().TableExpr("inventories").ColumnExpr("stock, reserved").Where("product_id = ?", product).Scan(context.Background(), &s, &r))
	return s, r
}

// The acceptance test of the phase: 50 orders race for 10 units. Exactly 10 get stock, none oversells.
func TestConcurrentReservationsNeverOversell(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	set := newSet(store, clock.System{})
	require.NoError(t, set.CreateInventory.Execute(ctx, usecase.ProductCreated{ProductID: "hot", ShopID: "s", Quantity: 10}))

	const orders = 50
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < orders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			require.NoError(t, set.ReserveStock.Execute(ctx, saga.ReserveStock{OrderID: fmt.Sprintf("order-%02d", i), Items: []saga.Item{{ProductID: "hot", Quantity: 1}}}))
		}()
	}
	close(start)
	wg.Wait()

	var reserved, failed int
	require.NoError(t, store.Raw().NewSelect().TableExpr("outbox_messages").ColumnExpr("count(*)").Where("type = ?", saga.ReplyStockReserved).Scan(ctx, &reserved))
	require.NoError(t, store.Raw().NewSelect().TableExpr("outbox_messages").ColumnExpr("count(*)").Where("type = ?", saga.ReplyStockReservationFailed).Scan(ctx, &failed))
	assert.Equal(t, 10, reserved, "exactly the available units were handed out")
	assert.Equal(t, orders-10, failed)
	s, r := stockOf(t, store, "hot")
	assert.Equal(t, [2]int64{0, 10}, [2]int64{s, r})
	var holds int
	require.NoError(t, store.Raw().NewSelect().TableExpr("reservations").ColumnExpr("count(*)").Scan(ctx, &holds))
	assert.Equal(t, 10, holds)
}

// Two orders that need the same two products in opposite order must not deadlock.
func TestOrdersOverlappingProductsDoNotDeadlock(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	set := newSet(store, clock.System{})
	for _, p := range []string{"a", "b"} {
		require.NoError(t, set.CreateInventory.Execute(ctx, usecase.ProductCreated{ProductID: p, ShopID: "s", Quantity: 1000}))
	}
	var wg sync.WaitGroup
	var failures atomic.Int32
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			items := []saga.Item{{ProductID: "a", Quantity: 1}, {ProductID: "b", Quantity: 1}}
			if i%2 == 1 {
				items = []saga.Item{{ProductID: "b", Quantity: 1}, {ProductID: "a", Quantity: 1}}
			}
			if err := set.ReserveStock.Execute(ctx, saga.ReserveStock{OrderID: fmt.Sprintf("o-%02d", i), Items: items}); err != nil {
				failures.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int32(0), failures.Load())
	sa, ra := stockOf(t, store, "a")
	sb, rb := stockOf(t, store, "b")
	assert.Equal(t, [4]int64{960, 40, 960, 40}, [4]int64{sa, ra, sb, rb})
}

func TestReservationFlowWithRealTransactions(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	clk := clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))
	set := newSet(store, clk)
	require.NoError(t, set.CreateInventory.Execute(ctx, usecase.ProductCreated{ProductID: "a", ShopID: "s", Quantity: 10}))

	cmd := saga.ReserveStock{OrderID: "o1", Items: []saga.Item{{ProductID: "a", Quantity: 4}}}
	require.NoError(t, set.ReserveStock.Execute(ctx, cmd))
	require.NoError(t, set.ReserveStock.Execute(ctx, cmd), "a redelivered command is a no-op")
	s, r := stockOf(t, store, "a")
	assert.Equal(t, [2]int64{6, 4}, [2]int64{s, r})

	require.NoError(t, set.CommitStock.Execute(ctx, saga.CommitStock{OrderID: "o1"}))
	s, r = stockOf(t, store, "a")
	assert.Equal(t, [2]int64{6, 0}, [2]int64{s, r})

	require.NoError(t, set.ReserveStock.Execute(ctx, saga.ReserveStock{OrderID: "o2", Items: []saga.Item{{ProductID: "a", Quantity: 3}}}))
	clk.Advance(20 * time.Minute)
	n, err := set.ExpireReservations.Execute(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	s, r = stockOf(t, store, "a")
	assert.Equal(t, [2]int64{6, 0}, [2]int64{s, r}, "the overdue hold went back to the shelf")
}

func TestReplyAndStockChangeCommitTogether(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	set := newSet(store, clock.System{})
	require.NoError(t, set.CreateInventory.Execute(ctx, usecase.ProductCreated{ProductID: "a", ShopID: "s", Quantity: 3}))
	// break the outbox: the reserve must roll back with it
	_, err := store.Raw().ExecContext(ctx, `ALTER TABLE outbox_messages RENAME TO outbox_messages_gone`)
	require.NoError(t, err)
	err = set.ReserveStock.Execute(ctx, saga.ReserveStock{OrderID: "o1", Items: []saga.Item{{ProductID: "a", Quantity: 2}}})
	require.Error(t, err)
	s, r := stockOf(t, store, "a")
	assert.Equal(t, [2]int64{3, 0}, [2]int64{s, r}, "no reply, no hold")
	var holds int
	require.NoError(t, store.Raw().NewSelect().TableExpr("reservations").ColumnExpr("count(*)").Scan(ctx, &holds))
	assert.Equal(t, 0, holds)
	_ = domain.CodeInsufficientStock
}
