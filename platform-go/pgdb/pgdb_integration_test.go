//go:build integration

package pgdb_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func open(t *testing.T) (*pgdb.Store, *pgdb.TxManager) {
	t.Helper()
	s, err := pgdb.Open(context.Background(), pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "test"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	_, err = s.Raw().Exec(`CREATE TABLE items (id INT PRIMARY KEY, name TEXT UNIQUE, qty INT NOT NULL CHECK (qty >= 0))`)
	require.NoError(t, err)
	return s, pgdb.NewTxManager(s)
}

func count(t *testing.T, s *pgdb.Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.Raw().NewSelect().TableExpr("items").ColumnExpr("count(*)").Scan(context.Background(), &n))
	return n
}

func TestCommitAndRollback(t *testing.T) {
	s, tx := open(t)
	ctx := context.Background()

	require.NoError(t, tx.WithinTx(ctx, func(ctx context.Context) error {
		assert.True(t, pgdb.InTx(ctx))
		_, err := s.DB(ctx).ExecContext(ctx, `INSERT INTO items VALUES (1, 'a', 1)`)
		return err
	}))
	assert.Equal(t, 1, count(t, s))

	boom := errors.New("boom")
	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		_, _ = s.DB(ctx).ExecContext(ctx, `INSERT INTO items VALUES (2, 'b', 1)`)
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, 1, count(t, s), "rolled back")

	assert.Panics(t, func() {
		_ = tx.WithinTx(ctx, func(ctx context.Context) error {
			_, _ = s.DB(ctx).ExecContext(ctx, `INSERT INTO items VALUES (3, 'c', 1)`)
			panic("handler bug")
		})
	})
	assert.Equal(t, 1, count(t, s), "rolled back on panic")
	assert.False(t, pgdb.InTx(ctx))
}

func TestNestedCallJoinsTheOuterTransaction(t *testing.T) {
	s, tx := open(t)
	ctx := context.Background()
	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := tx.WithinTx(ctx, func(ctx context.Context) error {
			_, err := s.DB(ctx).ExecContext(ctx, `INSERT INTO items VALUES (1, 'a', 1)`)
			return err
		}); err != nil {
			return err
		}
		return errors.New("outer fails after inner succeeded")
	})
	require.Error(t, err)
	assert.Equal(t, 0, count(t, s), "inner work is rolled back with the outer transaction")
}

func TestErrorClassification(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	_, err := s.Raw().ExecContext(ctx, `INSERT INTO items VALUES (1, 'a', 1)`)
	require.NoError(t, err)

	_, err = s.Raw().ExecContext(ctx, `INSERT INTO items VALUES (2, 'a', 1)`)
	require.Error(t, err)
	assert.True(t, pgdb.IsUniqueViolation(err))
	assert.True(t, pgdb.IsUniqueViolation(err, "items_name_key"))
	assert.False(t, pgdb.IsUniqueViolation(err, "other"))
	assert.Equal(t, "items_name_key", pgdb.ConstraintName(err))
	assert.False(t, pgdb.IsRetryable(err))

	_, err = s.Raw().ExecContext(ctx, `UPDATE items SET qty = -1 WHERE id = 1`)
	assert.True(t, pgdb.IsCheckViolation(err))
	assert.Equal(t, pgdb.StateCheckViolation, pgdb.SQLState(err))
	assert.Equal(t, "", pgdb.SQLState(errors.New("plain")))
	assert.Equal(t, "", pgdb.ConstraintName(nil))
}

func TestDeadlockIsRetried(t *testing.T) {
	s, tx := open(t)
	ctx := context.Background()
	_, err := s.Raw().ExecContext(ctx, `INSERT INTO items VALUES (1,'a',0),(2,'b',0)`)
	require.NoError(t, err)

	var attempts atomic.Int32
	ready := make(chan struct{}, 2) // each transaction signals once it holds its first row lock
	proceed := make(chan struct{})  // released when both do, which forces the lock cycle
	var wg sync.WaitGroup
	run := func(first, second int) {
		defer wg.Done()
		waited := false
		err := tx.WithinTx(ctx, func(ctx context.Context) error {
			attempts.Add(1)
			db := s.DB(ctx)
			if _, err := db.ExecContext(ctx, `UPDATE items SET qty = qty + 1 WHERE id = ?`, first); err != nil {
				return err
			}
			if !waited { // only the first attempt takes part in the handshake; a retry runs straight through
				waited = true
				ready <- struct{}{}
				<-proceed
			}
			_, err := db.ExecContext(ctx, `UPDATE items SET qty = qty + 1 WHERE id = ?`, second)
			return err
		})
		assert.NoError(t, err, "a deadlock victim is re-run and succeeds")
	}
	wg.Add(2)
	go run(1, 2)
	go run(2, 1)
	<-ready
	<-ready
	close(proceed)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("transactions did not finish")
	}

	var total int
	require.NoError(t, s.Raw().NewSelect().TableExpr("items").ColumnExpr("sum(qty)").Scan(ctx, &total))
	assert.Equal(t, 4, total, "both transactions applied both increments exactly once")
	assert.GreaterOrEqual(t, attempts.Load(), int32(3), "at least one transaction was retried")
}

func TestPingAndRaw(t *testing.T) {
	s, _ := open(t)
	assert.NoError(t, s.Ping(context.Background()))
	assert.Equal(t, s.Raw(), s.DB(context.Background()))
}

func TestOpenFailsFastOnBadDSN(t *testing.T) {
	_, err := pgdb.Open(context.Background(), pgdb.Config{DSN: "postgres://nobody:x@127.0.0.1:1/none?sslmode=disable"})
	assert.Error(t, err)
}
