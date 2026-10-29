// Package seeder is the Laravel-style seeder registry (docs/03-project-structure.md §4): `db:seed` runs the
// DatabaseSeeder (which calls the others in the order it chooses) or one class with --class=X.
package seeder

import (
	"context"
	"fmt"
	"sort"
)

// DatabaseSeeder is the name of the entry-point seeder.
const DatabaseSeeder = "DatabaseSeeder"

// Seeder fills a database with reference or demo data. Seeders must be idempotent (upserts), because `db:seed`
// can be run any number of times, including against a database that already holds data.
type Seeder[T any] interface {
	Name() string
	Run(ctx context.Context, db T, set *Set[T]) error
}

// Set is the registry of a service.
type Set[T any] struct{ items map[string]Seeder[T] }

// Register adds s; it panics on a duplicate name.
func (s *Set[T]) Register(sd Seeder[T]) {
	if s.items == nil {
		s.items = map[string]Seeder[T]{}
	}
	if _, dup := s.items[sd.Name()]; dup {
		panic(fmt.Sprintf("seeder: duplicate name %q", sd.Name()))
	}
	s.items[sd.Name()] = sd
}

// Names lists the registered seeders, sorted.
func (s *Set[T]) Names() []string {
	out := make([]string, 0, len(s.items))
	for n := range s.items {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Call runs the named seeders in the given order (used from DatabaseSeeder.Run).
func (s *Set[T]) Call(ctx context.Context, db T, names ...string) error {
	for _, n := range names {
		sd, ok := s.items[n]
		if !ok {
			return fmt.Errorf("seeder %q is not registered", n)
		}
		if err := sd.Run(ctx, db, s); err != nil {
			return fmt.Errorf("seeder %s: %w", n, err)
		}
	}
	return nil
}

// Run executes class, or — when class is empty — DatabaseSeeder if registered, else every seeder in name order.
// It returns the names of the seeders that were started directly.
func (s *Set[T]) Run(ctx context.Context, db T, class string) ([]string, error) {
	switch {
	case class != "":
		return []string{class}, s.Call(ctx, db, class)
	case s.items[DatabaseSeeder] != nil:
		return []string{DatabaseSeeder}, s.Call(ctx, db, DatabaseSeeder)
	}
	names := s.Names()
	return names, s.Call(ctx, db, names...)
}
