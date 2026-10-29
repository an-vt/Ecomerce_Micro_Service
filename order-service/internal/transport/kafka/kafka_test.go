package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/fake"
	transport "github.com/an-vt/ecom/order-service/internal/transport/kafka"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

type memGuard struct{ seen map[string]bool }

func (g *memGuard) TryRecord(_ context.Context, c, id string) (bool, error) {
	k := c + "/" + id
	if g.seen[k] {
		return false, nil
	}
	g.seen[k] = true
	return true, nil
}

var now = time.Now()

func setup(t *testing.T) (kafkax.Handler, *fake.World, string) {
	t.Helper()
	w := fake.NewWorld()
	cat := &fake.Catalog{Products_: map[string]domain.Product{"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 1000, IsPublished: true}}}
	uc := usecase.NewSet(&usecase.Deps{Orders: w.Orders(), Sagas: w.Sagas(), Keys: w.Keys(), Catalog: cat, Discounts: &fake.Discounts{}, Tx: w, Outbox: w,
		Clock: clock.NewFixed(now), IDs: idgen.UUID{}})
	ctx := principal.With(context.Background(), principal.Principal{ID: "alice", Type: principal.TypeUser})
	p, err := uc.PlaceOrder.Execute(ctx, usecase.PlaceInput{ShopOrders: []usecase.ShopOrder{{ShopID: "s1", Items: []usecase.ItemInput{{ProductID: "p1", Quantity: 1}}}},
		Shipping: domain.Shipping{Address: "x"}, PaymentMethod: "card", PaymentToken: "tok_ok", IdempotencyKey: "k"})
	require.NoError(t, err)
	return transport.Handler(uc, w, &memGuard{seen: map[string]bool{}}), w, p.OrderID
}

func TestEveryReplyTypeReachesTheSaga(t *testing.T) {
	h, w, id := setup(t)
	send := func(msgID string, m saga.Message) error {
		e, err := saga.Envelope(msgID, now, m)
		require.NoError(t, err)
		return h(context.Background(), e)
	}

	require.NoError(t, send("m1", saga.StockReserved{OrderID: id}))
	require.NoError(t, send("m2", saga.PaymentCompleted{OrderID: id, PaymentID: "pay"}))
	require.NoError(t, send("m3", saga.StockCommitted{OrderID: id}))
	o, _ := w.Order(id)
	assert.Equal(t, domain.StatusConfirmed, o.Status)

	// the failure and compensation replies, on a second order
	h2, w2, id2 := setup(t)
	send2 := func(msgID string, m saga.Message) {
		e, _ := saga.Envelope(msgID, now, m)
		require.NoError(t, h2(context.Background(), e))
	}
	send2("a1", saga.StockReserved{OrderID: id2})
	send2("a2", saga.PaymentFailed{OrderID: id2, Reason: saga.ReasonCardDeclined})
	send2("a3", saga.StockReleased{OrderID: id2})
	o2, _ := w2.Order(id2)
	assert.Equal(t, domain.StatusCancelled, o2.Status)

	// the rest decode and are routed; whether the saga accepts them is its business (here they are stale or unknown)
	for i, m := range []saga.Message{saga.StockReservationFailed{OrderID: id2}, saga.DiscountsApplied{OrderID: id2}, saga.DiscountsRejected{OrderID: id2},
		saga.DiscountsReleased{OrderID: id2}, saga.PaymentRefunded{OrderID: id2}} {
		send2("z"+string(rune('a'+i)), m)
	}
}

func TestADuplicateMessageIsNotHandledTwice(t *testing.T) {
	h, w, id := setup(t)
	e, _ := saga.Envelope("same", now, saga.StockReserved{OrderID: id})
	require.NoError(t, h(context.Background(), e))
	before := len(w.Types())
	assert.ErrorIs(t, h(context.Background(), e), inbox.ErrDuplicate)
	assert.Equal(t, before, len(w.Types()))
}

func TestMalformedAndForeignMessages(t *testing.T) {
	h, _, id := setup(t)
	broken, _ := saga.Envelope("b1", now, saga.StockReserved{OrderID: id})
	broken.Payload = []byte(`"nope"`)
	assert.True(t, errs.IsPermanent(h(context.Background(), broken)), "to the dead-letter topic, not into a retry loop")

	unknown, _ := saga.Envelope("u1", now, saga.StockReserved{OrderID: "no-such-order"})
	assert.True(t, errs.IsPermanent(h(context.Background(), unknown)), "a reply for an order we never had")

	other, _ := saga.Envelope("o1", now, saga.ProcessPayment{OrderID: id})
	assert.ErrorIs(t, h(context.Background(), other), kafkax.ErrIgnored)
	assert.Equal(t, []string{saga.TopicReplies}, transport.Topics)
}
