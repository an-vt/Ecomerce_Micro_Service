//go:build e2e

package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
)

// publishedProduct creates a product with stock, publishes it and waits until inventory knows its stock.
func publishedProduct(t *testing.T, c *e2ekit.Client, shop shopSession, qty int, price int) string {
	t.Helper()
	r := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{
		"product_type": "Clothing", "product_name": fmt.Sprintf("E2E Cart Shirt %d", time.Now().UnixNano()), "product_price": price, "product_quantity": qty,
		"product_attributes": map[string]any{"brand": "b", "size": "M", "material": "c"}}})
	require.Equal(t, 201, r.Status, string(r.Body))
	id := r.String("metadata._id")
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/publish/" + id, Token: shop.token}).Status)
	waitForStock(t, c, id, float64(qty), 0)
	return id
}

func TestCartThroughTheGateway(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 5, 150000)
	alice, bob := signUpBuyer(t, c), signUpBuyer(t, c)

	// authentication: shops and anonymous callers have no cart
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart"}).Status)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: shop.token}).Status)

	empty := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: alice})
	require.Equal(t, 200, empty.Status)
	assert.Empty(t, empty.Get("metadata.cart_products"))

	// add: name and price are snapshotted from the catalog, stock is checked against inventory
	add := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: alice, Body: map[string]any{"product": map[string]any{"productId": pid, "shopId": shop.id, "quantity": 2}}})
	require.Equal(t, 200, add.Status, string(add.Body))
	assert.EqualValues(t, 300000, add.Get("metadata.cart_total"))
	line := add.Get("metadata.cart_products").([]any)[0].(map[string]any)
	assert.EqualValues(t, 150000, line["price"])
	assert.Equal(t, shop.id, line["shopId"])

	more := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: alice, Body: map[string]any{"product": map[string]any{"productId": pid, "quantity": 1}}})
	assert.EqualValues(t, 3, more.Get("metadata.cart_products").([]any)[0].(map[string]any)["quantity"], "adding again merges the line")

	// stock: 3 in the cart + 3 more is more than the 5 on the shelf
	tooMany := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: alice, Body: map[string]any{"product": map[string]any{"productId": pid, "quantity": 3}}})
	assert.Equal(t, 422, tooMany.Status)
	assert.Equal(t, "INSUFFICIENT_STOCK", tooMany.Code())

	// unknown and unpublished products cannot be added
	ghost := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: alice, Body: map[string]any{"product": map[string]any{"productId": "ghost", "quantity": 1}}})
	assert.Equal(t, 404, ghost.Status)
	assert.Equal(t, "PRODUCT_NOT_AVAILABLE", ghost.Code())

	// update with absolute quantities; a stale old_quantity is a conflict
	upd := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart/update", Token: alice, Body: map[string]any{"shop_order_ids": []map[string]any{
		{"shopId": shop.id, "item_products": []map[string]any{{"productId": pid, "quantity": 4, "old_quantity": 3}}}}}})
	require.Equal(t, 200, upd.Status, string(upd.Body))
	assert.EqualValues(t, 600000, upd.Get("metadata.cart_total"))
	stale := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart/update", Token: alice, Body: map[string]any{"shop_order_ids": []map[string]any{
		{"shopId": shop.id, "item_products": []map[string]any{{"productId": pid, "quantity": 1, "old_quantity": 3}}}}}})
	assert.Equal(t, 409, stale.Status)
	assert.Equal(t, "CART_CHANGED", stale.Code())
	wrongShop := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart/update", Token: alice, Body: map[string]any{"shop_order_ids": []map[string]any{
		{"shopId": "not-this-shop", "item_products": []map[string]any{{"productId": pid, "quantity": 1}}}}}})
	assert.Equal(t, 400, wrongShop.Status)
	assert.Equal(t, "SHOP_MISMATCH", wrongShop.Code())

	// carts are private to their buyer
	bobCart := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: bob})
	assert.Empty(t, bobCart.Get("metadata.cart_products"))
	assert.Equal(t, 400, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: bob, Body: map[string]any{"userId": "alice", "product": map[string]any{"productId": pid, "quantity": 1}}}).Status, "no user id is accepted")

	// remove, twice
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "DELETE", Path: "/v1/api/cart", Token: alice, Body: map[string]any{"productId": pid}}).Status)
	again := c.Do(t, e2ekit.Req{Method: "DELETE", Path: "/v1/api/cart", Token: alice, Body: map[string]any{"productId": pid}})
	assert.Equal(t, 200, again.Status)
	assert.Empty(t, again.Get("metadata.cart_products"))
}

func TestConfirmedOrderEmptiesTheBoughtLinesExactlyOnce(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 50, 1000)

	buyer := signUpBuyer(t, c)
	add := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: buyer, Body: map[string]any{"product": map[string]any{"productId": pid, "quantity": 10}}})
	require.Equal(t, 200, add.Status, string(add.Body))
	userID := add.String("metadata.cart_userId")
	require.NotEmpty(t, userID)

	// order-service does not exist yet in this phase: play its part and publish order.order.confirmed (twice)
	evt := map[string]any{"orderId": "e2e-order", "userId": userID, "items": []map[string]any{{"productId": pid, "quantity": 4}}, "totalCheckout": 4000}
	e2ekit.PublishEvent(t, "order.order.confirmed", "e2e-order", "order.order.confirmed", evt)
	require.Eventually(t, func() bool {
		r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: buyer})
		ps, _ := r.Get("metadata.cart_products").([]any)
		return len(ps) == 1 && ps[0].(map[string]any)["quantity"] == float64(6)
	}, 20*time.Second, 200*time.Millisecond, "ordered units leave the cart")

	// a second, separate event for another order takes 6 more: that proves events are applied, not just the first
	e2ekit.PublishEvent(t, "order.order.confirmed", "e2e-order-2", "order.order.confirmed", map[string]any{
		"orderId": "e2e-order-2", "userId": userID, "items": []map[string]any{{"productId": pid, "quantity": 6}}})
	require.Eventually(t, func() bool {
		r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: buyer})
		ps, _ := r.Get("metadata.cart_products").([]any)
		return len(ps) == 0
	}, 20*time.Second, 200*time.Millisecond, "the line disappears when nothing is left")
}
