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

	"github.com/an-vt/ecom/payment-service/internal/client/provider"
	_ "github.com/an-vt/ecom/payment-service/internal/database/migrations"
	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/payment-service/internal/repository/postgres"
	"github.com/an-vt/ecom/payment-service/internal/repotest"
	"github.com/an-vt/ecom/payment-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/breaker"
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
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "payment_test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	_, err = pgmigrate.NewRunner(store.Raw(), "payment_test", nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

func TestPostgresSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) domain.PaymentRepository { return postgres.New(openMigrated(t)) })
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := pgmigrate.NewRunner(store.Raw(), "payment_test", nil)
	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 2)
	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 2)
	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 2, "up -> down -> up")
}

func TestConstraintsAreTheBackstop(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	_, err := store.Raw().ExecContext(ctx, `INSERT INTO payments (id, order_id, currency, status, amount) VALUES ('00000000-0000-4000-8000-000000000001', 'o', 'VND', 'PAID', 1)`)
	assert.Error(t, err, "unknown status")
	_, err = store.Raw().ExecContext(ctx, `INSERT INTO payments (id, order_id, currency, status, amount) VALUES ('00000000-0000-4000-8000-000000000002', 'o', 'VND', 'PENDING', -1)`)
	assert.Error(t, err, "negative amount")
}

type env struct {
	uc    *usecase.Set
	store *pgdb.Store
	mock  *provider.Mock
}

func newEnv(t *testing.T, timeout time.Duration, minRequests uint32) env {
	t.Helper()
	store := openMigrated(t)
	mock := provider.NewMock()
	reg := breaker.NewRegistry(breaker.Settings{ErrorRatio: 0.5, MinRequests: minRequests, ResetTimeout: time.Minute, IsFailure: provider.IsFailure})
	uc := usecase.NewSet(&usecase.Deps{Payments: postgres.New(store), Provider: provider.NewGuard(mock, reg, timeout), Tx: pgdb.NewTxManager(store),
		Outbox: pgoutbox.NewWriter(store), Clock: clock.NewFixed(time.Now()), IDs: idgen.UUID{}, Currency: "VND"})
	return env{uc: uc, store: store, mock: mock}
}

func (e env) replies(t *testing.T, typ string) int {
	t.Helper()
	var n int
	require.NoError(t, e.store.Raw().QueryRow(`SELECT count(*) FROM outbox_messages WHERE type = ?`, typ).Scan(&n))
	return n
}

func charge(order, token string) saga.ProcessPayment {
	return saga.ProcessPayment{OrderID: order, UserID: "alice", Amount: 150000, Method: "card", Token: token}
}

// Acceptance #5 at full strength: many copies of one command racing on real transactions make exactly one charge.
func TestRacingCopiesOfOneCommandChargeOnce(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, 3*time.Second, 100)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, e.uc.ProcessPayment.Execute(ctx, charge("order-race", provider.TokenOK)))
		}()
	}
	wg.Wait()

	var rows int
	require.NoError(t, e.store.Raw().QueryRow(`SELECT count(*) FROM payments WHERE order_id = 'order-race'`).Scan(&rows))
	assert.Equal(t, 1, rows, "one payment row")
	assert.Equal(t, 10, e.replies(t, saga.ReplyPaymentCompleted), "every copy was answered")
	assert.Equal(t, 1, e.mock.Calls(), "and the provider was asked once: the others waited for the first and read its outcome")
}

// A refund and a charge for one order arriving together end in a consistent state, in either order.
func TestRefundAndChargeRacingNeverLeaveAChargeStanding(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, 3*time.Second, 100)
	for i := 0; i < 8; i++ {
		order := fmt.Sprintf("order-%d", i)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = e.uc.ProcessPayment.Execute(ctx, charge(order, provider.TokenOK)) }()
		go func() { defer wg.Done(); _ = e.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: order}) }()
		wg.Wait()

		// whatever interleaving happened, a final refund (the saga keeps re-sending it) leaves nothing standing
		require.NoError(t, e.uc.RefundPayment.Execute(ctx, saga.RefundPayment{OrderID: order}))
		var status string
		require.NoError(t, e.store.Raw().QueryRow(`SELECT status FROM payments WHERE order_id = ?`, order).Scan(&status))
		assert.Contains(t, []string{"REFUNDED", "CANCELLED"}, status, order)
	}
}

// Acceptance #3 end to end: timeouts open the circuit, then the provider is not called and the reply says why.
func TestProviderTimeoutsOpenTheCircuit(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, 30*time.Millisecond, 3)

	for i := 0; i < 3; i++ {
		require.NoError(t, e.uc.ProcessPayment.Execute(ctx, charge(fmt.Sprintf("slow-%d", i), provider.TokenTimeout)))
	}
	callsBefore := e.mock.Calls()
	start := time.Now()
	require.NoError(t, e.uc.ProcessPayment.Execute(ctx, charge("after-open", provider.TokenOK)))
	assert.Less(t, time.Since(start), 25*time.Millisecond, "an open circuit answers without waiting for the provider")
	assert.Equal(t, callsBefore, e.mock.Calls(), "the provider was not called")
	assert.Equal(t, 4, e.replies(t, saga.ReplyPaymentFailed))

	var reason string
	require.NoError(t, e.store.Raw().QueryRow(`SELECT failure_reason FROM payments WHERE order_id = 'after-open'`).Scan(&reason))
	assert.Equal(t, saga.ReasonProviderUnavailable, reason)
}
