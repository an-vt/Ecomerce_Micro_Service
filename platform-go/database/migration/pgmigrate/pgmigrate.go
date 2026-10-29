// Package pgmigrate is the PostgreSQL half of the migration engine: bun-based store, advisory lock and the
// registry that migration files register into from init() (docs/03 §4).
package pgmigrate

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration"
)

// Migration is a PostgreSQL migration: Up / Down receive a bun.IDB (a transaction unless the migration opts out).
type Migration = migration.Migration[bun.IDB]

// Migrations is the registry of the service; files call Register from init().
var Migrations = &migration.Set[bun.IDB]{}

// Register adds a migration to the service registry.
func Register(m Migration) { Migrations.Register(m) }

const table = "migrations"

// Store implements migration.Store[bun.IDB].
type Store struct {
	db     *bun.DB
	dbName string
}

// NewStore creates the store; dbName only seeds the advisory-lock key.
func NewStore(db *bun.DB, dbName string) *Store { return &Store{db: db, dbName: dbName} }

// NewRunner builds a runner over the service registry.
func NewRunner(db *bun.DB, dbName string, set *migration.Set[bun.IDB]) *migration.Runner[bun.IDB] {
	if set == nil {
		set = Migrations
	}
	return migration.NewRunner[bun.IDB](NewStore(db, dbName), set, nil)
}

func (s *Store) lockKey() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s.dbName + ":migrations"))
	return int64(h.Sum64() >> 1)
}

// Lock takes a session-level advisory lock on a dedicated connection and waits for it (two containers starting
// together migrate one after the other).
func (s *Store) Lock(ctx context.Context) (func(), error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock(?)", s.lockKey()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return func() {
		uctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = conn.ExecContext(uctx, "SELECT pg_advisory_unlock(?)", s.lockKey())
		_ = conn.Close()
	}, nil
}

// Prepare creates the tracking table.
func (s *Store) Prepare(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+table+` (
		id        BIGSERIAL PRIMARY KEY,
		migration VARCHAR(255) NOT NULL UNIQUE,
		batch     INT NOT NULL,
		ran_at    TIMESTAMPTZ NOT NULL DEFAULT now()
	)`)
	return err
}

// Applied lists the tracking table.
func (s *Store) Applied(ctx context.Context) ([]migration.Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT migration, batch, ran_at FROM `+table+` ORDER BY migration`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []migration.Record
	for rows.Next() {
		var r migration.Record
		if err := rows.Scan(&r.Name, &r.Batch, &r.RanAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func optsOut(m Migration) bool {
	n, ok := m.(migration.NoTransaction)
	return ok && n.NoTransaction()
}

// Apply runs Up and inserts the tracking row in the same transaction: the migration fully applies or not at all.
func (s *Store) Apply(ctx context.Context, m Migration, batch int) error {
	record := func(ctx context.Context, db bun.IDB) error {
		_, err := db.ExecContext(ctx, `INSERT INTO `+table+` (migration, batch) VALUES (?, ?)`, m.Name(), batch)
		return err
	}
	if optsOut(m) {
		if err := m.Up(ctx, s.db); err != nil {
			return err
		}
		return record(ctx, s.db)
	}
	return s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		if err := m.Up(ctx, tx); err != nil {
			return err
		}
		return record(ctx, tx)
	})
}

// Revert runs Down and deletes the tracking row in the same transaction.
func (s *Store) Revert(ctx context.Context, m Migration) error {
	forget := func(ctx context.Context, db bun.IDB) error {
		_, err := db.ExecContext(ctx, `DELETE FROM `+table+` WHERE migration = ?`, m.Name())
		return err
	}
	if optsOut(m) {
		if err := m.Down(ctx, s.db); err != nil {
			return err
		}
		return forget(ctx, s.db)
	}
	return s.db.RunInTx(ctx, &sql.TxOptions{}, func(ctx context.Context, tx bun.Tx) error {
		if err := m.Down(ctx, tx); err != nil {
			return err
		}
		return forget(ctx, tx)
	})
}

// Fresh drops and recreates the public schema (all tables, types, functions) — local development only.
func (s *Store) Fresh(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `DROP SCHEMA public CASCADE`); err != nil {
		return fmt.Errorf("drop schema: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `CREATE SCHEMA public`)
	return err
}
