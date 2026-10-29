package seeder_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/database/seeder"
)

type log struct{ calls []string }

type simple struct {
	name string
	err  error
}

func (s simple) Name() string { return s.name }
func (s simple) Run(_ context.Context, l *log, _ *seeder.Set[*log]) error {
	l.calls = append(l.calls, s.name)
	return s.err
}

type database struct{ order []string }

func (database) Name() string { return seeder.DatabaseSeeder }
func (d database) Run(ctx context.Context, l *log, set *seeder.Set[*log]) error {
	return set.Call(ctx, l, d.order...)
}

func TestRunWithoutClassUsesDatabaseSeederOrder(t *testing.T) {
	set := &seeder.Set[*log]{}
	set.Register(simple{name: "AdminSeeder"})
	set.Register(simple{name: "RbacSeeder"})
	set.Register(database{order: []string{"RbacSeeder", "AdminSeeder"}})

	l := &log{}
	started, err := set.Run(context.Background(), l, "")
	require.NoError(t, err)
	assert.Equal(t, []string{seeder.DatabaseSeeder}, started)
	assert.Equal(t, []string{"RbacSeeder", "AdminSeeder"}, l.calls, "DatabaseSeeder decides the order")
}

func TestRunWithoutDatabaseSeederRunsAllSorted(t *testing.T) {
	set := &seeder.Set[*log]{}
	set.Register(simple{name: "ZSeeder"})
	set.Register(simple{name: "ASeeder"})
	l := &log{}
	started, err := set.Run(context.Background(), l, "")
	require.NoError(t, err)
	assert.Equal(t, []string{"ASeeder", "ZSeeder"}, started)
	assert.Equal(t, []string{"ASeeder", "ZSeeder"}, l.calls)
	assert.Equal(t, []string{"ASeeder", "ZSeeder"}, set.Names())
}

func TestRunOneClass(t *testing.T) {
	set := &seeder.Set[*log]{}
	set.Register(simple{name: "ASeeder"})
	set.Register(simple{name: "BSeeder"})
	l := &log{}
	_, err := set.Run(context.Background(), l, "BSeeder")
	require.NoError(t, err)
	assert.Equal(t, []string{"BSeeder"}, l.calls)
}

func TestErrors(t *testing.T) {
	set := &seeder.Set[*log]{}
	boom := errors.New("boom")
	set.Register(simple{name: "BadSeeder", err: boom})

	_, err := set.Run(context.Background(), &log{}, "BadSeeder")
	assert.ErrorIs(t, err, boom)
	assert.ErrorContains(t, err, "BadSeeder")

	_, err = set.Run(context.Background(), &log{}, "MissingSeeder")
	assert.ErrorContains(t, err, "not registered")

	assert.Panics(t, func() { set.Register(simple{name: "BadSeeder"}) })

	empty := &seeder.Set[*log]{}
	started, err := empty.Run(context.Background(), &log{}, "")
	assert.NoError(t, err)
	assert.Empty(t, started)
}
