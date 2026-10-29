package kafka_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/cart-service/internal/fake"
	transport "github.com/an-vt/ecom/cart-service/internal/transport/kafka"
	"github.com/an-vt/ecom/cart-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/principal"
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

type passTx struct{}

func (passTx) WithinTx(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }

func TestConfirmedOrderClearsBoughtUnitsExactlyOnce(t *testing.T) {
	carts := fake.NewCarts()
	uc := usecase.NewSet(&usecase.Deps{Carts: carts, Clock: clock.NewFixed(time.Now()), IDs: idgen.ULID{},
		Catalog: &fake.Catalog{Products_: map[string]domain.ProductInfo{"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 100, IsPublished: true}}},
		Stock:   &fake.Stock{Have: map[string]int64{"p1": 100}}})
	ctx := principal.With(context.Background(), principal.Principal{ID: "u1", Type: principal.TypeUser})
	_, err := uc.AddToCart.Execute(ctx, "p1", 10)
	require.NoError(t, err)

	h := transport.Handler(uc, passTx{}, &memGuard{seen: map[string]bool{}})
	env, err := event.NewEnvelope("m1", "order.order.confirmed", "o1", "", time.Now(), map[string]any{
		"orderId": "o1", "userId": "u1", "items": []map[string]any{{"productId": "p1", "quantity": 4}}})
	require.NoError(t, err)
	require.NoError(t, h(context.Background(), env))
	assert.ErrorIs(t, h(context.Background(), env), inbox.ErrDuplicate)

	v, _ := uc.GetCart.Execute(ctx)
	require.Len(t, v.Items, 1)
	assert.Equal(t, int64(6), v.Items[0].Quantity, "10 - 4, taken out once although the event was delivered twice")

	bad, _ := event.NewEnvelope("m2", "order.order.confirmed", "o", "", time.Now(), "nope")
	err = h(context.Background(), bad)
	assert.True(t, errs.IsPermanent(err))
	other, _ := event.NewEnvelope("m3", "order.order.cancelled", "o", "", time.Now(), map[string]any{})
	assert.ErrorIs(t, h(context.Background(), other), transportIgnored())
	assert.Equal(t, []string{"order.order.confirmed"}, transport.Topics)
}
