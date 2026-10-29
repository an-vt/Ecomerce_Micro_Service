package http_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
)

func TestCartIsForBuyersOnly(t *testing.T) {
	g := newGateway(t)
	assert.Equal(t, 401, g.call("GET", "/v1/api/cart", "").Code, "no token")
	assert.Equal(t, 403, g.as(g.token("shop1", "SHOP", "shop"), "GET", "/v1/api/cart", "").Code, "shops have no cart")
	assert.Equal(t, 200, g.as(g.token("u1", "USER", "user"), "GET", "/v1/api/cart", "").Code)
	assert.Equal(t, 401, g.do(req{method: "GET", path: "/v1/api/cart"}).Code, "the API key is required too")
}

func TestAddToCartKeepsTheMonolithBodyAndNeverSendsAUserID(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	r := g.as(user, "POST", "/v1/api/cart", `{"product":{"productId":"p1","shopId":"s1","quantity":2}}`)
	require.Equal(t, 200, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "active", meta["cart_state"])
	assert.EqualValues(t, 2000, meta["cart_total"])
	assert.Equal(t, "p1", meta["cart_products"].([]any)[0].(map[string]any)["productId"])
	g.cart.mu.Lock()
	assert.Equal(t, "p1", g.cart.addReq.GetProductId())
	assert.Equal(t, int64(2), g.cart.addReq.GetQuantity())
	assert.Equal(t, "u1", g.cart.last.Get("x-account-id")[0], "the buyer is the token's account")
	g.cart.mu.Unlock()

	// a userId in the body is not even a field of the request: unknown fields are rejected
	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/cart", `{"userId":"someone-else","product":{"productId":"p1","quantity":1}}`).Code)
	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/cart", `{"product":{"quantity":1}}`).Code)
	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/cart", `{}`).Code)
}

func TestUpdateCartFlattensShopOrderIDs(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	body := `{"shop_order_ids":[
		{"shopId":"s1","item_products":[{"productId":"p1","quantity":5,"old_quantity":2},{"productId":"p3","shopId":"s9","quantity":0}]},
		{"shopId":"s2","item_products":[{"productId":"p2","quantity":1}]}]}`
	r := g.as(user, "POST", "/v1/api/cart/update", body)
	require.Equal(t, 200, r.Code, r.Body.String())
	g.cart.mu.Lock()
	defer g.cart.mu.Unlock()
	lines := g.cart.updateReq.GetLines()
	require.Len(t, lines, 3)
	assert.Equal(t, "s1", lines[0].GetShopId(), "a line without its own shop inherits the group's")
	assert.Equal(t, int64(2), lines[0].GetOldQuantity())
	assert.Equal(t, "s9", lines[1].GetShopId(), "an explicit shop wins")
	assert.Equal(t, int64(0), lines[1].GetQuantity(), "quantity 0 reaches the service as a removal")
	assert.Equal(t, "s2", lines[2].GetShopId())
	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/cart/update", `{}`).Code)
}

func TestRemoveFromCartAndServiceErrors(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	assert.Equal(t, 200, g.as(user, "DELETE", "/v1/api/cart", `{"productId":"p1"}`).Code)
	assert.Equal(t, 400, g.as(user, "DELETE", "/v1/api/cart", `{}`).Code)

	g.cart.fail = errs.FailedPrecondition("INSUFFICIENT_STOCK", "not enough stock for the requested quantity")
	r := g.as(user, "POST", "/v1/api/cart", `{"product":{"productId":"p1","quantity":99}}`)
	assert.Equal(t, 422, r.Code)
	assert.Equal(t, "INSUFFICIENT_STOCK", r.errCode())
	g.cart.fail = errs.Aborted("CART_CHANGED", "the cart changed since you loaded it")
	r = g.as(user, "POST", "/v1/api/cart/update", `{"shop_order_ids":[{"shopId":"s","item_products":[{"productId":"p1","quantity":1}]}]}`)
	assert.Equal(t, 409, r.Code)
	assert.Equal(t, "CART_CHANGED", r.errCode())
}
