package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func item(p string, q, price int64) domain.Item {
	return domain.Item{ProductID: p, ShopID: "s1", Name: "N " + p, Price: 0 + moneyOf(price), Quantity: q}
}

func cart(t *testing.T) *domain.Cart {
	t.Helper()
	c, err := domain.NewCart("c1", "u1", now)
	require.NoError(t, err)
	return c
}

func TestNewCart(t *testing.T) {
	c := cart(t)
	assert.True(t, c.IsEmpty())
	assert.Equal(t, "u1", c.UserID())
	assert.Equal(t, "c1", c.ID())
	assert.Equal(t, int64(0), int64(c.Total()))
	_, err := domain.NewCart("c", "", now)
	assert.Error(t, err)
	assert.Equal(t, c.Snapshot(), domain.RestoreCart(c.Snapshot()).Snapshot())
}

func TestAddMergesQuantitiesAndRefreshesThePriceSnapshot(t *testing.T) {
	c := cart(t)
	require.NoError(t, c.Add(item("p1", 2, 1000), now))
	require.NoError(t, c.Add(item("p2", 1, 500), now))
	require.NoError(t, c.Add(item("p1", 3, 1200), now))

	assert.Equal(t, 2, c.Lines())
	assert.Equal(t, int64(5), c.QuantityOf("p1"))
	assert.Equal(t, int64(1200), int64(c.Items()[0].Price), "the buyer pays today's price for the whole line")
	assert.Equal(t, int64(5*1200+500), int64(c.Total()))
}

func TestAddRejections(t *testing.T) {
	c := cart(t)
	for name, it := range map[string]domain.Item{
		"no product":     {ShopID: "s", Quantity: 1},
		"no shop":        {ProductID: "p", Quantity: 1},
		"zero quantity":  item("p", 0, 1),
		"negative":       item("p", -1, 1),
		"too many":       item("p", 1001, 1),
		"negative price": {ProductID: "p", ShopID: "s", Quantity: 1, Price: -1},
	} {
		assert.Error(t, c.Add(it, now), name)
	}
	require.NoError(t, c.Add(item("p", 600, 1), now))
	err := c.Add(item("p", 600, 1), now)
	assert.Equal(t, domain.CodeInvalidQuantity, errs.CodeOf(err), "a line holds at most 1000 units")
	assert.Equal(t, int64(600), c.QuantityOf("p"), "a rejected add changes nothing")
	assert.True(t, c.IsEmpty() == false)
}

func TestCartSizeLimit(t *testing.T) {
	c := cart(t)
	for i := 0; i < domain.MaxLines; i++ {
		require.NoError(t, c.Add(item(strings.Repeat("p", 1)+string(rune('A'+i/26))+string(rune('a'+i%26)), 1, 1), now))
	}
	err := c.Add(item("one-too-many", 1, 1), now)
	assert.Equal(t, domain.CodeCartTooBig, errs.CodeOf(err))
	require.NoError(t, c.Add(item("pAa", 1, 1), now), "adding to an existing line is still fine")
}

func TestSetQuantity(t *testing.T) {
	c := cart(t)
	require.NoError(t, c.Add(item("p1", 2, 1000), now))

	require.NoError(t, c.SetQuantity(item("p1", 5, 1100), 2, now))
	assert.Equal(t, int64(5), c.QuantityOf("p1"), "an absolute value, not a difference")
	assert.Equal(t, int64(1100), int64(c.Items()[0].Price))

	err := c.SetQuantity(item("p1", 7, 1100), 2, now)
	assert.Equal(t, domain.CodeCartChanged, errs.CodeOf(err), "the client's old quantity was stale")
	assert.Equal(t, errs.KindAborted, errs.KindOf(err))
	assert.Equal(t, int64(5), c.QuantityOf("p1"))

	require.NoError(t, c.SetQuantity(item("p1", 6, 1100), 0, now), "old_quantity 0 means 'not checked'")
	require.NoError(t, c.SetQuantity(item("p9", 2, 10), 0, now), "setting a line that is not there adds it")
	assert.Equal(t, 2, c.Lines())

	require.NoError(t, c.SetQuantity(domain.Item{ProductID: "p1"}, 0, now), "quantity 0 removes the line (the monolith's empty branch)")
	assert.Equal(t, int64(0), c.QuantityOf("p1"))
	require.NoError(t, c.SetQuantity(domain.Item{ProductID: "ghost"}, 0, now), "removing what is not there is fine")
	assert.Error(t, c.SetQuantity(item("p2", -1, 1), 0, now))
	assert.Error(t, c.SetQuantity(item("p2", 1001, 1), 0, now))
}

func TestRemoveAndConsume(t *testing.T) {
	c := cart(t)
	require.NoError(t, c.Add(item("p1", 5, 100), now))
	require.NoError(t, c.Add(item("p2", 2, 100), now))

	c.Consume("p1", 2, now)
	assert.Equal(t, int64(3), c.QuantityOf("p1"), "ordered units are taken out of the line")
	c.Consume("p1", 3, now)
	assert.Equal(t, int64(0), c.QuantityOf("p1"), "the line goes when nothing is left")
	c.Consume("p2", 10, now)
	assert.True(t, c.IsEmpty(), "ordering more than the cart held just empties the line")
	c.Consume("ghost", 1, now)
	c.Consume("p2", 0, now)
	c.Remove("ghost", now)
}

func TestItemsAreCopies(t *testing.T) {
	c := cart(t)
	require.NoError(t, c.Add(item("p1", 1, 1), now))
	items := c.Items()
	items[0].Quantity = 99
	assert.Equal(t, int64(1), c.QuantityOf("p1"))
	snap := c.Snapshot()
	snap.Items[0].Quantity = 99
	assert.Equal(t, int64(1), c.QuantityOf("p1"))
}
