package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/inventory-service/internal/fake"
	transport "github.com/an-vt/ecom/inventory-service/internal/transport/kafka"
	"github.com/an-vt/ecom/inventory-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/saga"
)

type memGuard struct{ seen map[string]bool }

func (g *memGuard) TryRecord(_ context.Context, consumer, id string) (bool, error) {
	k := consumer + "/" + id
	if g.seen[k] {
		return false, nil
	}
	g.seen[k] = true
	return true, nil
}

type env struct {
	w      *fake.World
	uc     *usecase.Set
	handle func(event.Envelope) error
}

func setup() *env {
	w := fake.NewWorld()
	uc := usecase.NewSet(&usecase.Deps{Inventories: w.Inventories(), Reservations: w.Reservations(), Tx: w, Outbox: w,
		Clock: clock.NewFixed(time.Now()), IDs: idgen.ULID{}})
	h := transport.Handler(uc, w, &memGuard{seen: map[string]bool{}})
	return &env{w: w, uc: uc, handle: func(e event.Envelope) error { return h(context.Background(), e) }}
}

func msg(t *testing.T, id, typ string, payload any) event.Envelope {
	t.Helper()
	e, err := event.NewEnvelope(id, typ, "agg", "", time.Now(), payload)
	require.NoError(t, err)
	return e
}

func (e *env) stock(t *testing.T, p string) (int64, int64) {
	t.Helper()
	s, err := e.uc.GetStock.Execute(context.Background(), p)
	require.NoError(t, err)
	return s.Stock, s.Reserved
}

func TestCatalogEventsKeepStockInSync(t *testing.T) {
	e := setup()
	require.NoError(t, e.handle(msg(t, "m1", "catalog.product.created", map[string]any{"productId": "p1", "shopId": "s1", "quantity": 12})))
	s, _ := e.stock(t, "p1")
	assert.Equal(t, int64(12), s)

	require.NoError(t, e.handle(msg(t, "m2", "catalog.product.updated", map[string]any{"productId": "p1", "shopId": "s1", "quantity": 30})))
	s, _ = e.stock(t, "p1")
	assert.Equal(t, int64(30), s)
	require.NoError(t, e.handle(msg(t, "m3", "catalog.product.updated", map[string]any{"productId": "p1", "shopId": "s1", "price": 5})))
	s, _ = e.stock(t, "p1")
	assert.Equal(t, int64(30), s, "an update without a quantity leaves stock alone")
}

func TestSagaCommandsRunThroughTheInboxOnlyOnce(t *testing.T) {
	e := setup()
	require.NoError(t, e.handle(msg(t, "m1", "catalog.product.created", map[string]any{"productId": "p1", "shopId": "s1", "quantity": 10})))

	reserve := msg(t, "cmd-1", saga.CmdReserveStock, saga.ReserveStock{OrderID: "o1", Items: []saga.Item{{ProductID: "p1", Quantity: 4}}})
	require.NoError(t, e.handle(reserve))
	assert.ErrorIs(t, e.handle(reserve), inbox.ErrDuplicate, "the same message id is not processed twice")
	s, r := e.stock(t, "p1")
	assert.Equal(t, [2]int64{6, 4}, [2]int64{s, r})
	assert.Equal(t, [][2]string{{saga.ReplyStockReserved, "o1"}}, e.w.Replies(), "one reply")

	require.NoError(t, e.handle(msg(t, "cmd-2", saga.CmdCommitStock, saga.CommitStock{OrderID: "o1"})))
	s, r = e.stock(t, "p1")
	assert.Equal(t, [2]int64{6, 0}, [2]int64{s, r})

	require.NoError(t, e.handle(msg(t, "cmd-3", saga.CmdReserveStock, saga.ReserveStock{OrderID: "o2", Items: []saga.Item{{ProductID: "p1", Quantity: 2}}})))
	require.NoError(t, e.handle(msg(t, "cmd-4", saga.CmdReleaseStock, saga.ReleaseStock{OrderID: "o2"})))
	s, r = e.stock(t, "p1")
	assert.Equal(t, [2]int64{6, 0}, [2]int64{s, r})
}

func TestBadMessages(t *testing.T) {
	e := setup()
	for _, typ := range []string{"catalog.product.created", "catalog.product.updated", saga.CmdReserveStock, saga.CmdReleaseStock, saga.CmdCommitStock} {
		err := e.handle(msg(t, "bad-"+typ, typ, "not an object"))
		require.Error(t, err, typ)
		assert.True(t, errs.IsPermanent(err), typ+": an undecodable payload goes to the DLQ")
	}
	err := e.handle(msg(t, "x", saga.CmdCommitStock, saga.CommitStock{OrderID: "ghost"}))
	assert.True(t, errs.IsPermanent(err), "committing an order that holds nothing is not retried")
	assert.ErrorIs(t, e.handle(msg(t, "y", "something.else", map[string]string{})), kafkax.ErrIgnored)
	assert.ElementsMatch(t, []string{"catalog.product.created", "catalog.product.updated", "inventory.commands"}, transport.Topics)
}
