// Package pgdb opens PostgreSQL through bun and provides the transaction manager the use cases depend on
// (docs/03-project-structure.md §2.2). The transaction travels in the context, so repositories call Store.DB(ctx)
// and automatically take part in the surrounding WithinTx.
package pgdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
	"github.com/uptrace/bun/extra/bunotel"

	"github.com/an-vt/ecom/platform-go/retry"
)

// Config for Open.
type Config struct {
	DSN             string // postgres://user:pass@host:5432/db?sslmode=disable
	MaxOpen         int    // default 20
	MaxIdle         int    // default 5
	ConnMaxLifetime time.Duration
	DBName          string // label for traces
}

// Store owns the connection pool.
type Store struct{ db *bun.DB }

// Open connects, configures the pool, installs tracing and pings.
func Open(ctx context.Context, c Config) (*Store, error) {
	sqldb := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(c.DSN)))
	if c.MaxOpen <= 0 {
		c.MaxOpen = 20
	}
	if c.MaxIdle <= 0 {
		c.MaxIdle = 5
	}
	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = 30 * time.Minute
	}
	sqldb.SetMaxOpenConns(c.MaxOpen)
	sqldb.SetMaxIdleConns(c.MaxIdle)
	sqldb.SetConnMaxLifetime(c.ConnMaxLifetime)

	db := bun.NewDB(sqldb, pgdialect.New()).WithQueryHook(bunotel.NewQueryHook(bunotel.WithDBName(c.DBName)))
	s := &Store{db: db}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.Ping(pctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("postgres: %w", err)
	}
	return s, nil
}

// NewStore wraps an existing *bun.DB (tests).
func NewStore(db *bun.DB) *Store { return &Store{db: db} }

// Raw returns the underlying *bun.DB (migrations, health checks, shutdown).
func (s *Store) Raw() *bun.DB { return s.db }

// Close closes the pool.
func (s *Store) Close() error { return s.db.Close() }

// Ping is usable as an httpx.Check.
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

type txKey struct{}

// DB returns the transaction carried by ctx, or the pool when there is none. Repositories always use this.
func (s *Store) DB(ctx context.Context) bun.IDB {
	if tx, ok := ctx.Value(txKey{}).(bun.Tx); ok {
		return tx
	}
	return s.db
}

// InTx reports whether ctx carries a transaction.
func InTx(ctx context.Context) bool {
	_, ok := ctx.Value(txKey{}).(bun.Tx)
	return ok
}

// TxManager runs functions inside a database transaction.
type TxManager struct {
	store   *Store
	retries int
	policy  retry.Policy
}

// NewTxManager creates a manager that retries serialization failures and deadlocks up to 3 times.
func NewTxManager(s *Store) *TxManager {
	return &TxManager{store: s, retries: 3, policy: retry.Policy{Base: 20 * time.Millisecond, Max: 500 * time.Millisecond}}
}

// WithinTx runs fn in a transaction: commit when it returns nil, roll back on error or panic. A call made while
// ctx already carries a transaction joins it (no savepoint, no retry — the outermost call owns both).
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	p := m.policy
	p.MaxAttempts = m.retries
	p.Retryable = IsRetryable
	return retry.Do(ctx, p, func(ctx context.Context) error {
		return m.store.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
			return fn(context.WithValue(ctx, txKey{}, tx))
		})
	})
}

// SQLSTATE codes we care about.
const (
	StateUniqueViolation  = "23505"
	StateForeignKey       = "23503"
	StateCheckViolation   = "23514"
	StateSerialization    = "40001"
	StateDeadlockDetected = "40P01"
)

// SQLState returns the SQLSTATE of a PostgreSQL error, or "".
func SQLState(err error) string {
	var pe pgdriver.Error
	if errors.As(err, &pe) {
		return pe.Field('C')
	}
	return ""
}

// ConstraintName returns the violated constraint's name, or "".
func ConstraintName(err error) string {
	var pe pgdriver.Error
	if errors.As(err, &pe) {
		return pe.Field('n')
	}
	return ""
}

// IsUniqueViolation reports a unique constraint violation (optionally for a specific constraint name).
func IsUniqueViolation(err error, constraint ...string) bool {
	if SQLState(err) != StateUniqueViolation {
		return false
	}
	return len(constraint) == 0 || ConstraintName(err) == constraint[0]
}

// IsCheckViolation reports a CHECK constraint violation.
func IsCheckViolation(err error) bool { return SQLState(err) == StateCheckViolation }

// IsRetryable reports serialization failures and deadlocks, which succeed when the transaction is re-run.
func IsRetryable(err error) bool {
	s := SQLState(err)
	return s == StateSerialization || s == StateDeadlockDetected
}
