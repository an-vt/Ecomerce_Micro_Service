//go:build integration

package pgoutbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/an-vt/ecom/platform-go/database/migration"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/testkit"
)

type env struct {
	store  *pgdb.Store
	tx     *pgdb.TxManager
	writer *pgoutbox.Writer
	relayS *pgoutbox.Store
	prod   *kafkax.Producer
	topic  string
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "t"})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	set := &migration.Set[bun.IDB]{}
	set.Register(pgoutbox.CreateTables("2026_10_30_000090_create_outbox_inbox_tables"))
	_, err = pgmigrate.NewRunner(store.Raw(), "t", set).Migrate(ctx, false)
	require.NoError(t, err)
	_, err = store.Raw().Exec(`CREATE TABLE orders (id TEXT PRIMARY KEY)`)
	require.NoError(t, err)

	prod, err := kafkax.NewProducer(testkit.KafkaBrokers(t))
	require.NoError(t, err)
	t.Cleanup(prod.Close)
	topic := "it.pgoutbox." + testkit.Suffix(t)
	testkit.CreateTopics(t, topic)
	return &env{store: store, tx: pgdb.NewTxManager(store), writer: pgoutbox.NewWriter(store), relayS: pgoutbox.NewStore(store, "svc"), prod: prod, topic: topic}
}

func (e *env) msg(t *testing.T, ctx context.Context, id, key string) outbox.Message {
	t.Helper()
	env, err := event.NewEnvelope(id, "OrderPlaced", key, key, time.Now(), map[string]string{"id": id})
	require.NoError(t, err)
	m, err := outbox.New(ctx, e.topic, env)
	require.NoError(t, err)
	return m
}

func (e *env) outboxRows(t *testing.T, status string) int {
	t.Helper()
	var n int
	require.NoError(t, e.store.Raw().NewSelect().TableExpr("outbox_messages").ColumnExpr("count(*)").Where("status = ?", status).Scan(context.Background(), &n))
	return n
}

func TestRollbackLeavesNeitherStateNorOutboxRow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	err := e.tx.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := e.store.DB(ctx).ExecContext(ctx, `INSERT INTO orders VALUES ('o1')`); err != nil {
			return err
		}
		if err := e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1")); err != nil {
			return err
		}
		return errors.New("business rule failed after the outbox write")
	})
	require.Error(t, err)
	assert.Equal(t, 0, e.outboxRows(t, "PENDING"), "no event for a change that never happened")
	var orders int
	require.NoError(t, e.store.Raw().NewSelect().TableExpr("orders").ColumnExpr("count(*)").Scan(ctx, &orders))
	assert.Equal(t, 0, orders)
}

func TestCommitThenRelayPublishesWithTraceContext(t *testing.T) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample())))
	_, _ = otelx.Init(context.Background(), otelx.Config{})
	e := setup(t)

	ctx, span := otelx.Tracer("test").Start(context.Background(), "PlaceOrder")
	defer span.End()
	require.NoError(t, e.tx.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := e.store.DB(ctx).ExecContext(ctx, `INSERT INTO orders VALUES ('o1')`); err != nil {
			return err
		}
		return e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1"))
	}))
	assert.Equal(t, 1, e.outboxRows(t, "PENDING"))

	n, err := outbox.NewRelay(e.relayS, e.prod, outbox.RelayOptions{}).RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, 0, e.outboxRows(t, "PENDING"))
	assert.Equal(t, 1, e.outboxRows(t, "SENT"))

	recs := testkit.ConsumeN(t, e.topic, 1, 60*time.Second)
	assert.Equal(t, "o1", string(recs[0].Key))
	parsed, err := event.Parse(recs[0].Value)
	require.NoError(t, err)
	assert.Equal(t, "m1", parsed.MessageID)
	assert.Contains(t, testkit.Header(recs[0], "traceparent"), span.SpanContext().TraceID().String(), "the consumer continues the producing request's trace")
}

func TestTwoRelaysPublishEachMessageOnceAndKeepPerKeyOrder(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const perKey, keys = 20, 3
	for k := 0; k < keys; k++ {
		var msgs []outbox.Message
		for i := 0; i < perKey; i++ {
			msgs = append(msgs, e.msg(t, ctx, fmt.Sprintf("k%d-%02d", k, i), fmt.Sprintf("agg-%d", k)))
		}
		require.NoError(t, e.writer.Add(ctx, msgs...))
	}

	// two relays with their own stores/leases (as two replicas of the service would have)
	var wg sync.WaitGroup
	for r := 0; r < 2; r++ {
		relay := outbox.NewRelay(pgoutbox.NewStore(e.store, "svc"), e.prod, outbox.RelayOptions{Batch: 7})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				_, _ = relay.RunOnce(ctx)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, 0, e.outboxRows(t, "PENDING"))

	recs := testkit.ConsumeN(t, e.topic, perKey*keys, 60*time.Second)
	seen := map[string]bool{}
	last := map[string]string{}
	for _, r := range recs {
		env, err := event.Parse(r.Value)
		require.NoError(t, err)
		assert.False(t, seen[env.MessageID], "published twice: %s", env.MessageID)
		seen[env.MessageID] = true
		if prev, ok := last[string(r.Key)]; ok {
			assert.Less(t, prev, env.MessageID, "per-aggregate order must be preserved")
		}
		last[string(r.Key)] = env.MessageID
	}
	assert.Len(t, seen, perKey*keys)
	extra, err := e.relayS.Claim(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, extra.Messages())
	_ = extra.Close(ctx)
}

func TestFailedPublishIsRetriedLater(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1")))

	broken, err := kafkax.NewProducer([]string{"127.0.0.1:1"})
	require.NoError(t, err)
	defer broken.Close()
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	n, err := outbox.NewRelay(e.relayS, broken, outbox.RelayOptions{}).RunOnce(short)
	require.NoError(t, err, "a failing broker is not a relay error: the message just stays pending")
	assert.Equal(t, 0, n)
	assert.Equal(t, 1, e.outboxRows(t, "PENDING"))
	var attempts int
	require.NoError(t, e.store.Raw().NewSelect().TableExpr("outbox_messages").ColumnExpr("attempts").Scan(ctx, &attempts))
	assert.Equal(t, 1, attempts)

	// not due yet (backoff): the healthy relay finds nothing now...
	n, err = outbox.NewRelay(e.relayS, e.prod, outbox.RelayOptions{}).RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	// ...and publishes once the delay has passed
	_, err = e.store.Raw().ExecContext(ctx, `UPDATE outbox_messages SET available_at = now() - interval '1 second'`)
	require.NoError(t, err)
	n, err = outbox.NewRelay(e.relayS, e.prod, outbox.RelayOptions{}).RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestStatsAndPurge(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	pending, oldest, err := e.relayS.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), pending)
	assert.Equal(t, time.Duration(0), oldest)

	require.NoError(t, e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1")))
	require.NoError(t, e.writer.Add(ctx))
	time.Sleep(50 * time.Millisecond)
	pending, oldest, err = e.relayS.Stats(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), pending)
	assert.Greater(t, oldest, 10*time.Millisecond)

	_, err = outbox.NewRelay(e.relayS, e.prod, outbox.RelayOptions{}).RunOnce(ctx)
	require.NoError(t, err)
	deleted, err := e.relayS.Purge(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(0), deleted, "recently sent rows are kept")
	deleted, err = e.relayS.Purge(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestInboxHandlesEachMessageOnceAndRollsBackWithTheHandler(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	guard := pgoutbox.NewGuard(e.store)
	insert := func(id string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := e.store.DB(ctx).ExecContext(ctx, `INSERT INTO orders VALUES (?)`, id)
			return err
		}
	}

	require.NoError(t, inbox.Handle(ctx, e.tx, guard, "inventory", "m1", insert("o1")))
	err := inbox.Handle(ctx, e.tx, guard, "inventory", "m1", insert("o1-again"))
	assert.ErrorIs(t, err, inbox.ErrDuplicate)
	require.NoError(t, inbox.Handle(ctx, e.tx, guard, "notification", "m1", insert("o2")), "each consumer has its own record")

	// a handler that fails must not leave an inbox record behind, or the retry would be skipped
	err = inbox.Handle(ctx, e.tx, guard, "inventory", "m2", func(ctx context.Context) error {
		_ = insert("half-done")(ctx)
		return errors.New("handler failed")
	})
	require.Error(t, err)
	require.NoError(t, inbox.Handle(ctx, e.tx, guard, "inventory", "m2", insert("o3")), "the retry runs: the failed attempt left nothing")

	var ids []string
	require.NoError(t, e.store.Raw().NewSelect().TableExpr("orders").Column("id").Order("id").Scan(ctx, &ids))
	assert.Equal(t, []string{"o1", "o2", "o3"}, ids)
}

func TestMigrationDownDropsTables(t *testing.T) {
	ctx := context.Background()
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "t"})
	require.NoError(t, err)
	defer store.Close()
	set := &migration.Set[bun.IDB]{}
	set.Register(pgoutbox.CreateTables("2026_10_30_000090_create_outbox_inbox_tables"))
	r := pgmigrate.NewRunner(store.Raw(), "t", set)
	_, err = r.Migrate(ctx, false)
	require.NoError(t, err)
	_, err = r.Rollback(ctx, 1)
	require.NoError(t, err)
	var ok bool
	require.NoError(t, store.Raw().NewSelect().ColumnExpr("to_regclass('public.outbox_messages') IS NULL").Scan(ctx, &ok))
	assert.True(t, ok)
}
