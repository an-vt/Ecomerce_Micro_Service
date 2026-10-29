package http_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
)

const checkoutJSON = `"shop_order_ids":[
	{"shopId":"s1","shop_discounts":[{"codeId":"SALE10"}],"item_products":[{"productId":"p1","quantity":2}]},
	{"shopId":"s2","item_products":[{"productId":"p2","quantity":1}]}]`

const placeJSON = `{` + checkoutJSON + `,"shipping":{"name":"An","address":"1 Main St","city":"HCMC"},"payment":{"method":"card","token":"tok_ok"}}`

func placeAs(g *gw, token, key, body string) resp {
	g.t.Helper()
	return g.call("POST", "/v1/api/orders", body, "Authorization", "Bearer "+token, "Idempotency-Key", key)
}

func TestCheckoutAndOrdersAreForBuyersOnly(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	for _, rt := range []struct{ method, path, body string }{
		{"POST", "/v1/api/checkout/review", `{` + checkoutJSON + `}`},
		{"POST", "/v1/api/orders", placeJSON},
		{"GET", "/v1/api/orders", ""},
		{"GET", "/v1/api/orders/o1", ""},
	} {
		assert.Equal(t, 401, g.call(rt.method, rt.path, rt.body).Code, "%s %s without a token", rt.method, rt.path)
		assert.Equal(t, 403, g.as(shop, rt.method, rt.path, rt.body).Code, "%s %s as a shop", rt.method, rt.path)
	}
	assert.Equal(t, 200, g.as(g.token("u1", "USER", "user"), "GET", "/v1/api/orders", "").Code)
}

func TestReviewKeepsTheMonolithShape(t *testing.T) {
	g := newGateway(t)
	r := g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/checkout/review", `{`+checkoutJSON+`}`)
	require.Equal(t, 200, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)

	co := meta["checkout_order"].(map[string]any)
	assert.EqualValues(t, 250000, co["totalPrice"])
	assert.EqualValues(t, 20000, co["totalDiscount"])
	assert.EqualValues(t, 5000, co["feeShip"])
	assert.EqualValues(t, 235000, co["totalCheckout"])

	shops := meta["shop_order_ids_new"].([]any)
	require.Len(t, shops, 2)
	first := shops[0].(map[string]any)
	assert.EqualValues(t, 200000, first["priceRaw"])
	assert.EqualValues(t, 180000, first["priceApplyDiscount"])
	assert.Equal(t, "SALE10", first["shop_discounts"].([]any)[0].(map[string]any)["codeId"])
	assert.Equal(t, "Shirt", first["item_products"].([]any)[0].(map[string]any)["name"], "the name comes from the catalog")
	assert.Equal(t, []any{}, shops[1].(map[string]any)["shop_discounts"], "never null")

	g.order.mu.Lock()
	defer g.order.mu.Unlock()
	req := g.order.reviewReq
	require.Len(t, req.GetShopOrders(), 2)
	assert.Equal(t, "SALE10", req.GetShopOrders()[0].GetDiscountCode())
	assert.EqualValues(t, 2, req.GetShopOrders()[0].GetItems()[0].GetQuantity())
	assert.Equal(t, "u1", g.order.last.Get("x-account-id")[0], "the buyer is the token's account")
}

func TestCheckoutBodyIsValidated(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	for name, body := range map[string]string{
		"empty":          `{}`,
		"no shops":       `{"shop_order_ids":[]}`,
		"no items":       `{"shop_order_ids":[{"shopId":"s1","item_products":[]}]}`,
		"no shop id":     `{"shop_order_ids":[{"item_products":[{"productId":"p","quantity":1}]}]}`,
		"no quantity":    `{"shop_order_ids":[{"shopId":"s1","item_products":[{"productId":"p"}]}]}`,
		"two codes":      `{"shop_order_ids":[{"shopId":"s1","shop_discounts":[{"codeId":"A"},{"codeId":"B"}],"item_products":[{"productId":"p","quantity":1}]}]}`,
		"a client price": `{"shop_order_ids":[{"shopId":"s1","item_products":[{"productId":"p","quantity":1,"price":1}]}]}`,
	} {
		assert.Equal(t, 400, g.as(user, "POST", "/v1/api/checkout/review", body).Code, name)
	}
}

func TestPlaceOrderReturns202AndPassesTheIdempotencyKey(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	r := placeAs(g, user, "key-123", placeJSON)
	require.Equal(t, 202, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "o1", meta["orderId"])
	assert.Equal(t, "PENDING", meta["status"])

	g.order.mu.Lock()
	defer g.order.mu.Unlock()
	p := g.order.placeReq
	assert.Equal(t, "key-123", p.GetIdempotencyKey())
	assert.Equal(t, "card", p.GetPaymentMethod())
	assert.Equal(t, "tok_ok", p.GetPaymentToken())
	assert.Equal(t, "1 Main St", p.GetShipping().GetAddress())
	assert.Equal(t, "SALE10", p.GetShopOrders()[0].GetDiscountCode())
}

func TestPlaceOrderValidationAndErrors(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	for name, body := range map[string]string{
		"no shipping": `{` + checkoutJSON + `,"payment":{"method":"card","token":"t"}}`,
		"no address":  `{` + checkoutJSON + `,"shipping":{"name":"An"},"payment":{"method":"card","token":"t"}}`,
		"no payment":  `{` + checkoutJSON + `,"shipping":{"address":"x"}}`,
		"no token":    `{` + checkoutJSON + `,"shipping":{"address":"x"},"payment":{"method":"card"}}`,
		"no items":    `{"shipping":{"address":"x"},"payment":{"method":"card","token":"t"}}`,
	} {
		assert.Equal(t, 400, placeAs(g, user, "k", body).Code, name)
	}

	g.order.fail = errs.InvalidArgument("IDEMPOTENCY_KEY_REQUIRED", "an Idempotency-Key is required")
	r := g.as(user, "POST", "/v1/api/orders", placeJSON) // no header
	assert.Equal(t, 400, r.Code)
	assert.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", r.errCode())
	g.order.fail = errs.Aborted("IDEMPOTENCY_CONFLICT", "reused with a different request")
	r = placeAs(g, user, "k", placeJSON)
	assert.Equal(t, 409, r.Code)
	assert.Equal(t, "IDEMPOTENCY_CONFLICT", r.errCode())
}

func TestGetAndListOrders(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	r := g.as(user, "GET", "/v1/api/orders/o42", "")
	require.Equal(t, 200, r.Code, r.Body.String())
	o := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "o42", o["_id"])
	assert.Equal(t, "CANCELLED", o["order_status"])
	assert.Equal(t, "CARD_DECLINED", o["order_cancelReason"])
	assert.EqualValues(t, 235000, o["order_checkout"].(map[string]any)["totalCheckout"])
	assert.Equal(t, "card", o["order_payment"].(map[string]any)["method"])
	assert.NotContains(t, r.Body.String(), "token", "the payment token is never in a response")
	g.order.mu.Lock()
	assert.Equal(t, "o42", g.order.getReq.GetOrderId())
	g.order.mu.Unlock()

	l := g.as(user, "GET", "/v1/api/orders?page=2&limit=5", "")
	require.Equal(t, 200, l.Code)
	meta := l.json()["metadata"].(map[string]any)
	assert.EqualValues(t, 1, meta["total"])
	assert.EqualValues(t, 2, meta["page"])
	g.order.mu.Lock()
	assert.EqualValues(t, 5, g.order.listReq.GetLimit())
	g.order.mu.Unlock()

	g.order.fail = errs.NotFound("ORDER_NOT_FOUND", "no such order")
	assert.Equal(t, 404, g.as(user, "GET", "/v1/api/orders/other", "").Code)
	assert.Equal(t, 404, g.as(user, "GET", "/v1/api/orders", "").Code, "errors of the service are passed through on the list too")
}
