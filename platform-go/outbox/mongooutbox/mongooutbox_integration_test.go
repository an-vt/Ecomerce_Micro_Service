//go:build integration

package mongooutbox_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/platform-go/database/migration"
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/mongox"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/mongooutbox"
	"github.com/an-vt/ecom/platform-go/testkit"
)

type env struct {
	store  *mongox.Store
	tx     *mongox.TxManager
	writer *mongooutbox.Writer
	relayS *mongooutbox.Store
	prod   *kafkax.Producer
	topic  string
}

func setup(t *testing.T) *env {
	t.Helper()
	ctx := context.Background()
	uri, name := testkit.MongoURI(t)
	store, err := mongox.Open(ctx, mongox.Config{URI: uri, Database: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DB().Drop(ctx); _ = store.Close(ctx) })

	set := &migration.Set[*mongo.Database]{}
	set.Register(mongooutbox.CreateCollections("2026_10_30_000090_create_outbox_inbox_collections"))
	_, err = mongomigrate.NewRunner(store.DB(), set).Migrate(ctx, false)
	require.NoError(t, err)
	require.NoError(t, store.DB().CreateCollection(ctx, "orders"))

	prod, err := kafkax.NewProducer(testkit.KafkaBrokers(t))
	require.NoError(t, err)
	t.Cleanup(prod.Close)
	topic := "it.mongooutbox." + testkit.Suffix(t)
	testkit.CreateTopics(t, topic)
	return &env{store: store, tx: mongox.NewTxManager(store), writer: mongooutbox.NewWriter(store.DB()),
		relayS: mongooutbox.NewStore(store.DB()), prod: prod, topic: topic}
}

func (e *env) msg(t *testing.T, ctx context.Context, id, key string) outbox.Message {
	t.Helper()
	ev, err := event.NewEnvelope(id, "ProductCreated", key, key, time.Now(), map[string]string{"id": id})
	require.NoError(t, err)
	m, err := outbox.New(ctx, e.topic, ev)
	require.NoError(t, err)
	return m
}

func (e *env) count(t *testing.T, coll string, filter bson.M) int64 {
	t.Helper()
	n, err := e.store.DB().Collection(coll).CountDocuments(context.Background(), filter)
	require.NoError(t, err)
	return n
}

func TestRollbackLeavesNeitherStateNorOutboxRow(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	err := e.tx.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := e.store.DB().Collection("orders").InsertOne(ctx, bson.M{"_id": "o1"}); err != nil {
			return err
		}
		if err := e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1")); err != nil {
			return err
		}
		return errors.New("business rule failed after the outbox write")
	})
	require.Error(t, err)
	assert.Equal(t, int64(0), e.count(t, "outbox_messages", bson.M{}))
	assert.Equal(t, int64(0), e.count(t, "orders", bson.M{}))
}

func TestCommitThenRelayPublishes(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.tx.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := e.store.DB().Collection("orders").InsertOne(ctx, bson.M{"_id": "o1"}); err != nil {
			return err
		}
		return e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1"))
	}))
	assert.Equal(t, int64(1), e.count(t, "outbox_messages", bson.M{"status": "PENDING"}))

	n, err := outbox.NewRelay(e.relayS, e.prod, outbox.RelayOptions{}).RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, int64(0), e.count(t, "outbox_messages", bson.M{"status": "PENDING"}))
	assert.Equal(t, int64(1), e.count(t, "outbox_messages", bson.M{"status": "SENT"}))

	recs := testkit.ConsumeN(t, e.topic, 1, 60*time.Second)
	assert.Equal(t, "o1", string(recs[0].Key))
	parsed, err := event.Parse(recs[0].Value)
	require.NoError(t, err)
	assert.Equal(t, "m1", parsed.MessageID)
}

func TestOnlyTheLeaseHolderRelaysAndOrderIsKept(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	const perKey, keys = 15, 3
	for k := 0; k < keys; k++ {
		var msgs []outbox.Message
		for i := 0; i < perKey; i++ {
			msgs = append(msgs, e.msg(t, ctx, fmt.Sprintf("k%d-%02d", k, i), fmt.Sprintf("agg-%d", k)))
		}
		require.NoError(t, e.writer.Add(ctx, msgs...))
	}

	holder := mongooutbox.NewStore(e.store.DB())
	rival := mongooutbox.NewStore(e.store.DB())
	batch, err := holder.Claim(ctx, 1)
	require.NoError(t, err)
	assert.Len(t, batch.Messages(), 1, "the first store takes the lease")
	_ = batch.Close(ctx)
	rb, err := rival.Claim(ctx, 100)
	require.NoError(t, err)
	assert.Empty(t, rb.Messages(), "a second relay does nothing while the lease is live")
	_ = rb.Close(ctx)

	var wg sync.WaitGroup
	for _, s := range []*mongooutbox.Store{holder, rival} {
		relay := outbox.NewRelay(s, e.prod, outbox.RelayOptions{Batch: 6})
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 15; i++ {
				_, _ = relay.RunOnce(ctx)
			}
		}()
	}
	wg.Wait()
	assert.Equal(t, int64(0), e.count(t, "outbox_messages", bson.M{"status": "PENDING"}))

	recs := testkit.ConsumeN(t, e.topic, perKey*keys, 60*time.Second)
	seen, last := map[string]bool{}, map[string]string{}
	for _, r := range recs {
		ev, err := event.Parse(r.Value)
		require.NoError(t, err)
		assert.False(t, seen[ev.MessageID], "published twice: %s", ev.MessageID)
		seen[ev.MessageID] = true
		if prev, ok := last[string(r.Key)]; ok {
			assert.Less(t, prev, ev.MessageID)
		}
		last[string(r.Key)] = ev.MessageID
	}
	assert.Len(t, seen, perKey*keys)
}

func TestExpiredLeaseIsTakenOver(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1")))
	_, err := e.store.DB().Collection("outbox_lease").InsertOne(ctx, bson.M{"_id": "relay", "owner": "crashed", "expires_at": time.Now().Add(-time.Minute)})
	require.NoError(t, err)

	n, err := outbox.NewRelay(e.relayS, e.prod, outbox.RelayOptions{}).RunOnce(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "a crashed relay's lease expires and the next relay continues")
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
	assert.Equal(t, int64(0), deleted)
	deleted, err = e.relayS.Purge(ctx, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

func TestFailedPublishStaysPending(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	require.NoError(t, e.writer.Add(ctx, e.msg(t, ctx, "m1", "o1")))
	broken, err := kafkax.NewProducer([]string{"127.0.0.1:1"})
	require.NoError(t, err)
	defer broken.Close()
	short, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	n, err := outbox.NewRelay(e.relayS, broken, outbox.RelayOptions{}).RunOnce(short)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Equal(t, int64(1), e.count(t, "outbox_messages", bson.M{"status": "PENDING", "attempts": 1}))
}

func TestInboxHandlesEachMessageOnceAndRollsBackWithTheHandler(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	guard := mongooutbox.NewGuard(e.store.DB())
	insert := func(id string) func(context.Context) error {
		return func(ctx context.Context) error {
			_, err := e.store.DB().Collection("orders").InsertOne(ctx, bson.M{"_id": id})
			return err
		}
	}

	require.NoError(t, inbox.Handle(ctx, e.tx, guard, "cart", "m1", insert("o1")))
	err := inbox.Handle(ctx, e.tx, guard, "cart", "m1", insert("o1-again"))
	assert.ErrorIs(t, err, inbox.ErrDuplicate)
	require.NoError(t, inbox.Handle(ctx, e.tx, guard, "notification", "m1", insert("o2")))

	err = inbox.Handle(ctx, e.tx, guard, "cart", "m2", func(ctx context.Context) error {
		_ = insert("half-done")(ctx)
		return errors.New("handler failed")
	})
	require.Error(t, err)
	require.NoError(t, inbox.Handle(ctx, e.tx, guard, "cart", "m2", insert("o3")), "the failed attempt left no inbox record")
	assert.Equal(t, int64(3), e.count(t, "orders", bson.M{}))
	assert.Equal(t, int64(0), e.count(t, "orders", bson.M{"_id": "half-done"}))
}

func TestMigrationIsIdempotentAndReversible(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	m := mongooutbox.CreateCollections("2026_10_30_000091_again")
	require.NoError(t, m.Up(ctx, e.store.DB()), "running Up twice must not fail (collections already exist)")
	require.NoError(t, m.Down(ctx, e.store.DB()))
	names, err := e.store.DB().ListCollectionNames(ctx, bson.M{})
	require.NoError(t, err)
	assert.NotContains(t, names, "outbox_messages")
}
