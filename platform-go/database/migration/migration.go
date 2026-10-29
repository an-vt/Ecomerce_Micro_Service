// Package migration is the Laravel-style migration engine (docs/03-project-structure.md §4).
//
// It is generic over the thing a migration works on (T): *bun.DB-like for PostgreSQL, *mongo.Database for
// MongoDB. The driver-specific parts live in pgmigrate and mongomigrate; this package holds the rules:
// ordered names, batches, status, rollback by batch, reset / refresh / fresh.
package migration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"time"
)

// NamePattern is the Laravel file-name format: YYYY_MM_DD_HHMMSS_snake_case_name.
var NamePattern = regexp.MustCompile(`^\d{4}_\d{2}_\d{2}_\d{6}_[a-z0-9]+(_[a-z0-9]+)*$`)

// Migration is one schema change.
type Migration[T any] interface {
	Name() string
	Up(ctx context.Context, db T) error
	Down(ctx context.Context, db T) error
}

// NoTransaction can be implemented by migrations that cannot run inside a transaction
// (for example CREATE INDEX CONCURRENTLY).
type NoTransaction interface{ NoTransaction() bool }

// Record is a row of the tracking table.
type Record struct {
	Name  string
	Batch int
	RanAt time.Time
}

// Store is the driver-specific half: tracking table, locking and atomic apply / revert.
type Store[T any] interface {
	// Lock serialises concurrent runners (two containers starting together). The returned func releases it.
	Lock(ctx context.Context) (unlock func(), err error)
	// Prepare creates the tracking table if needed.
	Prepare(ctx context.Context) error
	Applied(ctx context.Context) ([]Record, error)
	// Apply runs m.Up and records it in the given batch — atomically when the store supports transactions
	// and m does not opt out.
	Apply(ctx context.Context, m Migration[T], batch int) error
	// Revert runs m.Down and removes the record.
	Revert(ctx context.Context, m Migration[T]) error
	// Fresh drops everything (schema / collections), including the tracking table.
	Fresh(ctx context.Context) error
}

// Set is an ordered, validated collection of migrations (the package registry of a service).
type Set[T any] struct{ items []Migration[T] }

// Register adds m; it panics on an invalid or duplicate name so that mistakes fail at start-up, not in production.
func (s *Set[T]) Register(m Migration[T]) {
	if !NamePattern.MatchString(m.Name()) {
		panic(fmt.Sprintf("migration: invalid name %q (want YYYY_MM_DD_HHMMSS_snake_case)", m.Name()))
	}
	for _, e := range s.items {
		if e.Name() == m.Name() {
			panic(fmt.Sprintf("migration: duplicate name %q", m.Name()))
		}
	}
	s.items = append(s.items, m)
}

// Sorted returns the migrations in run order.
func (s *Set[T]) Sorted() []Migration[T] {
	out := append([]Migration[T](nil), s.items...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Runner executes a Set against a Store.
type Runner[T any] struct {
	store Store[T]
	set   *Set[T]
	log   *slog.Logger
}

// NewRunner creates a Runner.
func NewRunner[T any](store Store[T], set *Set[T], log *slog.Logger) *Runner[T] {
	if log == nil {
		log = slog.Default()
	}
	return &Runner[T]{store: store, set: set, log: log}
}

// StatusRow is one line of migrate:status.
type StatusRow struct {
	Name  string
	Ran   bool
	Batch int
}

func (r *Runner[T]) begin(ctx context.Context) (unlock func(), applied []Record, err error) {
	unlock, err = r.store.Lock(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("migration lock: %w", err)
	}
	if err = r.store.Prepare(ctx); err == nil {
		applied, err = r.store.Applied(ctx)
	}
	if err != nil {
		unlock()
		return nil, nil, err
	}
	return unlock, applied, nil
}

// Migrate applies every pending migration in one new batch. With pretend it only lists them.
func (r *Runner[T]) Migrate(ctx context.Context, pretend bool) ([]string, error) {
	unlock, applied, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	done := map[string]bool{}
	batch := 0
	for _, a := range applied {
		done[a.Name] = true
		if a.Batch > batch {
			batch = a.Batch
		}
	}
	batch++

	var ran []string
	for _, m := range r.set.Sorted() {
		if done[m.Name()] {
			continue
		}
		if pretend {
			ran = append(ran, m.Name())
			continue
		}
		r.log.InfoContext(ctx, "migrating", "migration", m.Name(), "batch", batch)
		if err := r.store.Apply(ctx, m, batch); err != nil {
			return ran, fmt.Errorf("migrate %s: %w", m.Name(), err)
		}
		ran = append(ran, m.Name())
	}
	return ran, nil
}

// Rollback reverts the last `step` batches (step <= 0 means 1), newest migration first.
func (r *Runner[T]) Rollback(ctx context.Context, step int) ([]string, error) {
	if step <= 0 {
		step = 1
	}
	unlock, applied, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	maxBatch := 0
	for _, a := range applied {
		if a.Batch > maxBatch {
			maxBatch = a.Batch
		}
	}
	return r.revert(ctx, applied, func(a Record) bool { return a.Batch > maxBatch-step })
}

// Reset reverts every migration.
func (r *Runner[T]) Reset(ctx context.Context) ([]string, error) {
	unlock, applied, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return r.revert(ctx, applied, func(Record) bool { return true })
}

func (r *Runner[T]) revert(ctx context.Context, applied []Record, pick func(Record) bool) ([]string, error) {
	byName := map[string]Migration[T]{}
	for _, m := range r.set.Sorted() {
		byName[m.Name()] = m
	}
	var todo []Record
	for _, a := range applied {
		if pick(a) {
			todo = append(todo, a)
		}
	}
	sort.Slice(todo, func(i, j int) bool { return todo[i].Name > todo[j].Name }) // newest first

	var out []string
	for _, a := range todo {
		m, ok := byName[a.Name]
		if !ok {
			return out, fmt.Errorf("rollback %s: migration is recorded as applied but not registered in this binary", a.Name)
		}
		r.log.InfoContext(ctx, "rolling back", "migration", a.Name, "batch", a.Batch)
		if err := r.store.Revert(ctx, m); err != nil {
			return out, fmt.Errorf("rollback %s: %w", a.Name, err)
		}
		out = append(out, a.Name)
	}
	return out, nil
}

// Refresh = Reset + Migrate.
func (r *Runner[T]) Refresh(ctx context.Context) (rolledBack, migrated []string, err error) {
	if rolledBack, err = r.Reset(ctx); err != nil {
		return rolledBack, nil, err
	}
	migrated, err = r.Migrate(ctx, false)
	return rolledBack, migrated, err
}

// Fresh drops everything and migrates from scratch. Callers must refuse it in production.
func (r *Runner[T]) Fresh(ctx context.Context) ([]string, error) {
	unlock, err := r.store.Lock(ctx)
	if err != nil {
		return nil, fmt.Errorf("migration lock: %w", err)
	}
	ferr := r.store.Fresh(ctx)
	unlock()
	if ferr != nil {
		return nil, fmt.Errorf("fresh: %w", ferr)
	}
	return r.Migrate(ctx, false)
}

// Status lists every registered migration with its state, plus applied ones that are not registered.
func (r *Runner[T]) Status(ctx context.Context) ([]StatusRow, error) {
	unlock, applied, err := r.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	rec := map[string]Record{}
	for _, a := range applied {
		rec[a.Name] = a
	}
	var rows []StatusRow
	for _, m := range r.set.Sorted() {
		a, ok := rec[m.Name()]
		rows = append(rows, StatusRow{Name: m.Name(), Ran: ok, Batch: a.Batch})
		delete(rec, m.Name())
	}
	var orphans []string
	for n := range rec {
		orphans = append(orphans, n)
	}
	sort.Strings(orphans)
	for _, n := range orphans {
		rows = append(rows, StatusRow{Name: n + " (not registered)", Ran: true, Batch: rec[n].Batch})
	}
	return rows, nil
}

// ErrProduction is returned by destructive commands in production.
var ErrProduction = errors.New("refused: this command is destructive and APP_ENV=production")
