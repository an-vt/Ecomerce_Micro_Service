package migration_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/database/migration"
)

// ---- in-memory store -----------------------------------------------------------------------------------------

type world struct {
	mu      sync.Mutex
	tables  map[string]bool
	records []migration.Record
	locked  bool
	freshed int
	failOn  string
}

type fakeStore struct{ w *world }

func newWorld() *world { return &world{tables: map[string]bool{}} }

func (s fakeStore) Lock(context.Context) (func(), error) {
	s.w.mu.Lock()
	defer s.w.mu.Unlock()
	if s.w.locked {
		return nil, errors.New("already locked")
	}
	s.w.locked = true
	return func() { s.w.mu.Lock(); s.w.locked = false; s.w.mu.Unlock() }, nil
}
func (s fakeStore) Prepare(context.Context) error { return nil }
func (s fakeStore) Applied(context.Context) ([]migration.Record, error) {
	return append([]migration.Record(nil), s.w.records...), nil
}
func (s fakeStore) Apply(ctx context.Context, m migration.Migration[*world], batch int) error {
	if s.w.failOn == m.Name() {
		return errors.New("boom")
	}
	if err := m.Up(ctx, s.w); err != nil {
		return err
	}
	s.w.records = append(s.w.records, migration.Record{Name: m.Name(), Batch: batch, RanAt: time.Now()})
	return nil
}
func (s fakeStore) Revert(ctx context.Context, m migration.Migration[*world]) error {
	if err := m.Down(ctx, s.w); err != nil {
		return err
	}
	for i, r := range s.w.records {
		if r.Name == m.Name() {
			s.w.records = append(s.w.records[:i], s.w.records[i+1:]...)
			break
		}
	}
	return nil
}
func (s fakeStore) Fresh(context.Context) error {
	s.w.tables = map[string]bool{}
	s.w.records = nil
	s.w.freshed++
	return nil
}

type createTable struct{ name, table string }

func (c createTable) Name() string { return c.name }
func (c createTable) Up(_ context.Context, w *world) error {
	if w.tables[c.table] {
		return fmt.Errorf("table %s exists", c.table)
	}
	w.tables[c.table] = true
	return nil
}
func (c createTable) Down(_ context.Context, w *world) error { delete(w.tables, c.table); return nil }

func newRunner(w *world, names ...[2]string) (*migration.Runner[*world], *migration.Set[*world]) {
	set := &migration.Set[*world]{}
	for _, n := range names {
		set.Register(createTable{name: n[0], table: n[1]})
	}
	return migration.NewRunner[*world](fakeStore{w}, set, nil), set
}

var (
	m1 = [2]string{"2026_10_30_000010_create_accounts_table", "accounts"}
	m2 = [2]string{"2026_10_30_000020_create_sessions_table", "sessions"}
	m3 = [2]string{"2026_11_02_093000_create_otps_table", "otps"}
)

// ---- tests -------------------------------------------------------------------------------------------------------

func TestMigrateRunsPendingInNameOrderInOneBatch(t *testing.T) {
	w := newWorld()
	r, _ := newRunner(w, m3, m1, m2) // registered out of order on purpose
	ran, err := r.Migrate(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{m1[0], m2[0], m3[0]}, ran)
	assert.Len(t, w.tables, 3)
	for _, rec := range w.records {
		assert.Equal(t, 1, rec.Batch)
	}

	ran, err = r.Migrate(context.Background(), false)
	require.NoError(t, err)
	assert.Empty(t, ran, "nothing pending: idempotent")
	assert.False(t, w.locked, "lock is released")
}

func TestLaterMigrationsGetTheNextBatch(t *testing.T) {
	w := newWorld()
	r1, _ := newRunner(w, m1)
	_, err := r1.Migrate(context.Background(), false)
	require.NoError(t, err)
	r2, _ := newRunner(w, m1, m2, m3)
	ran, err := r2.Migrate(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{m2[0], m3[0]}, ran)
	assert.Equal(t, 2, w.records[len(w.records)-1].Batch)
}

func TestPretendListsWithoutRunning(t *testing.T) {
	w := newWorld()
	r, _ := newRunner(w, m1, m2)
	ran, err := r.Migrate(context.Background(), true)
	require.NoError(t, err)
	assert.Equal(t, []string{m1[0], m2[0]}, ran)
	assert.Empty(t, w.tables)
	assert.Empty(t, w.records)
}

func TestFailureStopsAndKeepsEarlierProgress(t *testing.T) {
	w := newWorld()
	w.failOn = m2[0]
	r, _ := newRunner(w, m1, m2, m3)
	ran, err := r.Migrate(context.Background(), false)
	require.Error(t, err)
	assert.ErrorContains(t, err, m2[0])
	assert.Equal(t, []string{m1[0]}, ran)
	assert.False(t, w.locked, "lock released after failure")

	w.failOn = ""
	ran, err = r.Migrate(context.Background(), false)
	require.NoError(t, err)
	assert.Equal(t, []string{m2[0], m3[0]}, ran, "re-running resumes where it stopped")
}

func TestRollbackLastBatchThenSteps(t *testing.T) {
	w := newWorld()
	rA, _ := newRunner(w, m1)
	_, _ = rA.Migrate(context.Background(), false) // batch 1
	rB, _ := newRunner(w, m1, m2, m3)
	_, _ = rB.Migrate(context.Background(), false) // batch 2: m2, m3

	back, err := rB.Rollback(context.Background(), 0)
	require.NoError(t, err)
	assert.Equal(t, []string{m3[0], m2[0]}, back, "newest first, last batch only")
	assert.Equal(t, map[string]bool{"accounts": true}, w.tables)

	_, _ = rB.Migrate(context.Background(), false) // batch 2 again
	back, err = rB.Rollback(context.Background(), 2)
	require.NoError(t, err)
	assert.Len(t, back, 3, "step=2 reverts both batches")
	assert.Empty(t, w.tables)

	back, err = rB.Rollback(context.Background(), 1)
	require.NoError(t, err)
	assert.Empty(t, back, "nothing to roll back")
}

func TestRollbackRefusesUnknownMigration(t *testing.T) {
	w := newWorld()
	rNew, _ := newRunner(w, m1, m2)
	_, _ = rNew.Migrate(context.Background(), false)
	rOld, _ := newRunner(w, m1) // an older binary that does not know m2
	_, err := rOld.Rollback(context.Background(), 1)
	require.Error(t, err)
	assert.ErrorContains(t, err, "not registered")
}

func TestResetRefreshFresh(t *testing.T) {
	w := newWorld()
	r, _ := newRunner(w, m1, m2)
	_, _ = r.Migrate(context.Background(), false)

	rolled, migrated, err := r.Refresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{m2[0], m1[0]}, rolled)
	assert.Equal(t, []string{m1[0], m2[0]}, migrated)

	back, err := r.Reset(context.Background())
	require.NoError(t, err)
	assert.Len(t, back, 2)
	assert.Empty(t, w.tables)

	w.tables["junk"] = true // not created by a migration: only Fresh removes it
	ran, err := r.Fresh(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{m1[0], m2[0]}, ran)
	assert.NotContains(t, w.tables, "junk")
	assert.Equal(t, 1, w.freshed)
}

func TestStatus(t *testing.T) {
	w := newWorld()
	rOld, _ := newRunner(w, m1, m2)
	_, _ = rOld.Migrate(context.Background(), false)
	r, _ := newRunner(w, m1, m3) // m2 applied but unknown to this binary; m3 pending
	rows, err := r.Status(context.Background())
	require.NoError(t, err)
	require.Len(t, rows, 3)
	assert.Equal(t, migration.StatusRow{Name: m1[0], Ran: true, Batch: 1}, rows[0])
	assert.Equal(t, migration.StatusRow{Name: m3[0], Ran: false}, rows[1])
	assert.Equal(t, migration.StatusRow{Name: m2[0] + " (not registered)", Ran: true, Batch: 1}, rows[2])
}

func TestConcurrentRunnersAreSerialisedByTheLock(t *testing.T) {
	w := newWorld()
	w.locked = true // another container holds the lock
	r, _ := newRunner(w, m1)
	_, err := r.Migrate(context.Background(), false)
	require.Error(t, err)
	assert.ErrorContains(t, err, "migration lock")
	_, err = r.Fresh(context.Background())
	assert.ErrorContains(t, err, "migration lock")
}

func TestRegisterValidatesNames(t *testing.T) {
	set := &migration.Set[*world]{}
	for _, bad := range []string{"create_users", "2026_10_30_create_users", "2026_10_30_000010_Create_Users", "2026_10_30_000010_", "20261030000010_x"} {
		assert.Panics(t, func() { set.Register(createTable{name: bad}) }, bad)
	}
	set.Register(createTable{name: m1[0]})
	assert.Panics(t, func() { set.Register(createTable{name: m1[0]}) }, "duplicate")
	assert.True(t, migration.NamePattern.MatchString("2026_10_30_000010_add_risk_level_to_merchants"))
}
