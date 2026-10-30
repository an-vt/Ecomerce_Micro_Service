//go:build e2e

package e2e_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
	"github.com/an-vt/ecom/platform-go/event"
)

func orderBody(shopID, pid string, qty int, code, token string) map[string]any {
	so := map[string]any{"shopId": shopID, "item_products": []map[string]any{{"productId": pid, "quantity": qty}}}
	if code != "" {
		so["shop_discounts"] = []map[string]any{{"codeId": code}}
	}
	return map[string]any{"shop_order_ids": []map[string]any{so}, "shipping": map[string]any{"name": "An", "address": "1 Main St", "city": "HCMC"},
		"payment": map[string]any{"method": "card", "token": token}}
}

func placeOrder(t *testing.T, c *e2ekit.Client, buyer, key string, body map[string]any) *e2ekit.Response {
	t.Helper()
	return c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/orders", Token: buyer, Body: body, Headers: map[string]string{"Idempotency-Key": key}})
}

// waitOrder polls GET /orders/:id (what a client does after the 202) until the order reaches want.
func waitOrder(t *testing.T, c *e2ekit.Client, buyer, id, want string) *e2ekit.Response {
	t.Helper()
	var last *e2ekit.Response
	require.Eventually(t, func() bool {
		last = c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/orders/" + id, Token: buyer})
		return last.Status == 200 && last.String("metadata.order_status") == want
	}, 60*time.Second, 300*time.Millisecond, "order %s should become %s", id, want)
	return last
}

func discountUses(t *testing.T, c *e2ekit.Client, shop shopSession, discountID string) float64 {
	t.Helper()
	r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/discount?limit=100", Token: shop.token})
	require.Equal(t, 200, r.Status)
	for _, d := range r.Get("metadata.discounts").([]any) {
		if m := d.(map[string]any); m["_id"] == discountID {
			return m["discount_uses_count"].(float64)
		}
	}
	t.Fatalf("discount %s not listed", discountID)
	return -1
}

func cartLines(t *testing.T, c *e2ekit.Client, buyer string) []any {
	t.Helper()
	r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: buyer})
	require.Equal(t, 200, r.Status)
	lines, _ := r.Get("metadata.cart_products").([]any)
	return lines
}

// The whole system: gateway -> order -> (inventory, discount, payment over Kafka) -> order, plus the cart and the
// discount counter reacting to the outcome. Nothing is mocked.
func TestCheckoutHappyPath(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 5, 100000)
	code := uniqueCode("CHK")
	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shop.token, Body: discountBody(code, func(b map[string]any) { b["min_order_value"] = 0 })})
	require.Equal(t, 201, created.Status, string(created.Body))
	discountID := created.String("metadata._id")
	buyer := signUpBuyer(t, c)
	userID := userIDOf(t, c, buyer)

	// the buyer fills the cart, then reviews: names and prices come from the catalog, the discount is previewed
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: buyer, Body: map[string]any{"product": map[string]any{"productId": pid, "quantity": 2}}}).Status)
	review := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/checkout/review", Token: buyer, Body: map[string]any{"shop_order_ids": []map[string]any{
		{"shopId": shop.id, "shop_discounts": []map[string]any{{"codeId": code}}, "item_products": []map[string]any{{"productId": pid, "quantity": 2}}}}}})
	require.Equal(t, 200, review.Status, string(review.Body))
	assert.EqualValues(t, 200000, review.Get("metadata.checkout_order.totalPrice"))
	assert.EqualValues(t, 20000, review.Get("metadata.checkout_order.totalDiscount"))
	assert.EqualValues(t, 180000, review.Get("metadata.checkout_order.totalCheckout"))

	// place the order: accepted at once, finished in the background
	key := fmt.Sprintf("e2e-%d", time.Now().UnixNano())
	body := orderBody(shop.id, pid, 2, code, "tok_ok")
	placed := placeOrder(t, c, buyer, key, body)
	require.Equal(t, 202, placed.Status, string(placed.Body))
	orderID := placed.String("metadata.orderId")
	require.NotEmpty(t, orderID)
	assert.Equal(t, "PENDING", placed.String("metadata.status"))

	// a retry with the same key is the same order; the same key with another request is refused
	again := placeOrder(t, c, buyer, key, body)
	assert.Equal(t, 202, again.Status)
	assert.Equal(t, orderID, again.String("metadata.orderId"))
	other := placeOrder(t, c, buyer, key, orderBody(shop.id, pid, 3, code, "tok_ok"))
	assert.Equal(t, 409, other.Status)
	assert.Equal(t, "IDEMPOTENCY_CONFLICT", other.Code())

	got := waitOrder(t, c, buyer, orderID, "CONFIRMED")
	assert.EqualValues(t, 200000, got.Get("metadata.order_checkout.totalPrice"))
	assert.EqualValues(t, 20000, got.Get("metadata.order_checkout.totalDiscount"), "from what discount-service really applied")
	assert.EqualValues(t, 180000, got.Get("metadata.order_checkout.totalCheckout"))
	assert.Equal(t, userID, got.String("metadata.order_userId"))
	assert.NotContains(t, string(got.Body), "tok_ok", "the payment token is never returned")

	// every participant did its part
	waitForStock(t, c, pid, 3, 0) // 5 on the shelf, 2 sold: reserved went back to 0 when the hold was committed
	assert.EqualValues(t, 1, discountUses(t, c, shop, discountID), "the code was redeemed")
	require.Eventually(t, func() bool { return len(cartLines(t, c, buyer)) == 0 }, 30*time.Second, 300*time.Millisecond, "the bought units left the cart")
	e2ekit.WaitForEvent(t, "order.order.confirmed", func(e event.Envelope) bool { return e.AggregateID == orderID })

	// the order is listed, only for its owner
	list := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/orders", Token: buyer})
	require.Equal(t, 200, list.Status)
	assert.EqualValues(t, 1, list.Get("metadata.total"))
	stranger := signUpBuyer(t, c)
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/orders/" + orderID, Token: stranger}).Status)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/orders/" + orderID, Token: shop.token}).Status)
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/orders/not-an-id", Token: buyer}).Status)
}

// A declined card: stock and the discount go back, nothing is charged, the cart is untouched, and the buyer is told why.
func TestCheckoutWithADeclinedCardIsCompensated(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 5, 100000)
	code := uniqueCode("DEC")
	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shop.token, Body: discountBody(code, func(b map[string]any) { b["min_order_value"] = 0 })})
	require.Equal(t, 201, created.Status)
	discountID := created.String("metadata._id")
	buyer := signUpBuyer(t, c)
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/cart", Token: buyer, Body: map[string]any{"product": map[string]any{"productId": pid, "quantity": 2}}}).Status)

	placed := placeOrder(t, c, buyer, fmt.Sprintf("e2e-%d", time.Now().UnixNano()), orderBody(shop.id, pid, 2, code, "tok_fail"))
	require.Equal(t, 202, placed.Status, string(placed.Body))
	orderID := placed.String("metadata.orderId")

	got := waitOrder(t, c, buyer, orderID, "CANCELLED")
	assert.Equal(t, "CARD_DECLINED", got.String("metadata.order_cancelReason"))

	waitForStock(t, c, pid, 5, 0) // the hold was released
	require.Eventually(t, func() bool { return discountUses(t, c, shop, discountID) == 0 }, 20*time.Second, 300*time.Millisecond, "the code was given back")
	assert.Len(t, cartLines(t, c, buyer), 1, "a cancelled order does not empty the cart")
	e2ekit.WaitForEvent(t, "order.order.cancelled", func(e event.Envelope) bool { return e.AggregateID == orderID })
}

// The shelf is shorter than the order: inventory refuses, there is nothing to undo, and the order says why.
func TestCheckoutWithoutEnoughStockIsCancelled(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 1, 100000)
	buyer := signUpBuyer(t, c)

	placed := placeOrder(t, c, buyer, fmt.Sprintf("e2e-%d", time.Now().UnixNano()), orderBody(shop.id, pid, 3, "", "tok_ok"))
	require.Equal(t, 202, placed.Status, string(placed.Body))
	got := waitOrder(t, c, buyer, placed.String("metadata.orderId"), "CANCELLED")
	assert.Equal(t, "INSUFFICIENT_STOCK", got.String("metadata.order_cancelReason"))
	waitForStock(t, c, pid, 1, 0)
}

func TestCheckoutRefusals(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	pid := publishedProduct(t, c, shop, 5, 100000)
	buyer := signUpBuyer(t, c)
	key := func() string { return fmt.Sprintf("e2e-%d", time.Now().UnixNano()) }

	missing := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/orders", Token: buyer, Body: orderBody(shop.id, pid, 1, "", "tok_ok")})
	assert.Equal(t, 400, missing.Status)
	assert.Equal(t, "IDEMPOTENCY_KEY_REQUIRED", missing.Code())

	assert.Equal(t, 404, placeOrder(t, c, buyer, key(), orderBody(shop.id, "ghost", 1, "", "tok_ok")).Status, "an unknown product")
	assert.Equal(t, 400, placeOrder(t, c, buyer, key(), orderBody("another-shop", pid, 1, "", "tok_ok")).Status, "a product listed under the wrong shop")
	noCode := placeOrder(t, c, buyer, key(), orderBody(shop.id, pid, 1, "NOSUCHCODE", "tok_ok"))
	assert.Equal(t, 404, noCode.Status)
	assert.Equal(t, "DISCOUNT_NOT_FOUND", noCode.Code())
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/orders", Token: shop.token, Body: orderBody(shop.id, pid, 1, "", "tok_ok"), Headers: map[string]string{"Idempotency-Key": key()}}).Status, "shops do not buy")
	waitForStock(t, c, pid, 5, 0)
}
