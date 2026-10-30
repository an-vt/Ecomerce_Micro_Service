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
	"github.com/an-vt/ecom/platform-go/saga"
)

func discountBody(code string, mut func(map[string]any)) map[string]any {
	b := map[string]any{"code": code, "name": "E2E " + code, "type": "percent", "value": 10,
		"start_date": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "end_date": time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339),
		"max_uses": 5, "max_uses_per_user": 1, "min_order_value": 1000}
	if mut != nil {
		mut(b)
	}
	return b
}

func uniqueCode(prefix string) string { return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano()%1e9) }

func TestDiscountManagementThroughTheGateway(t *testing.T) {
	c := e2ekit.NewClient(t)
	shopA, shopB := signUpShop(t, c), signUpShop(t, c)
	pid := publishedProduct(t, c, shopA, 10, 100000)
	code := uniqueCode("E2E")

	// only shops create codes
	buyer := signUpBuyer(t, c)
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Body: discountBody(code, nil)}).Status)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: buyer, Body: discountBody(code, nil)}).Status)

	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shopA.token, Body: discountBody(code, func(b map[string]any) {
		b["applies_to"], b["product_ids"] = "specific", []string{pid}
	})})
	require.Equal(t, 201, created.Status, string(created.Body))
	id := created.String("metadata._id")
	assert.Equal(t, shopA.id, created.String("metadata.discount_shopId"), "the owner is the caller")
	assert.Equal(t, code, created.String("metadata.discount_code"))

	// the same shop cannot reuse the code; a code on someone else's product is refused
	dup := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shopA.token, Body: discountBody(code, nil)})
	assert.Equal(t, 409, dup.Status)
	assert.Equal(t, "DISCOUNT_CODE_TAKEN", dup.Code())
	foreign := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shopB.token, Body: discountBody(uniqueCode("FOR"), func(b map[string]any) {
		b["applies_to"], b["product_ids"] = "specific", []string{pid}
	})})
	assert.Equal(t, 400, foreign.Status)
	assert.Equal(t, "UNKNOWN_PRODUCTS", foreign.Code())
	badDates := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shopA.token, Body: discountBody(uniqueCode("BAD"), func(b map[string]any) {
		b["end_date"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	})})
	assert.Equal(t, 400, badDates.Status)

	// the announcement reaches Kafka (notification-service will turn it into PROMOTION-001)
	e2ekit.WaitForEvent(t, "discount.discount.created", func(e event.Envelope) bool { return e.AggregateID == id })

	// shop B can neither change nor see shop A's code
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/discount/" + id, Token: shopB.token, Body: map[string]any{"name": "stolen"}}).Status)
	list := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/discount", Token: shopB.token})
	require.Equal(t, 200, list.Status)
	assert.EqualValues(t, 0, list.Get("metadata.total"))

	// shop A updates and lists
	upd := c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/discount/" + id, Token: shopA.token, Body: map[string]any{"name": "Renamed", "max_uses": 9}})
	require.Equal(t, 200, upd.Status, string(upd.Body))
	assert.Equal(t, "Renamed", upd.String("metadata.discount_name"))
	assert.EqualValues(t, 9, upd.Get("metadata.discount_max_uses"))
	assert.Equal(t, 400, c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/discount/" + id, Token: shopA.token, Body: map[string]any{"code": "CHANGED"}}).Status)
	mine := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/discount", Token: shopA.token})
	assert.EqualValues(t, 1, mine.Get("metadata.total"))

	// the products of a code are public (API key only)
	products := c.Do(t, e2ekit.Req{Method: "GET", Path: fmt.Sprintf("/v1/api/discount/list_product_code?code=%s&shopId=%s", code, shopA.id)})
	require.Equal(t, 200, products.Status, string(products.Body))
	ps := products.Get("metadata.products").([]any)
	require.Len(t, ps, 1)
	assert.Equal(t, pid, ps[0].(map[string]any)["_id"])
	assert.EqualValues(t, 100000, ps[0].(map[string]any)["product_price"])
	missing := c.Do(t, e2ekit.Req{Method: "GET", Path: fmt.Sprintf("/v1/api/discount/list_product_code?code=NOPE&shopId=%s", shopA.id)})
	assert.Equal(t, 404, missing.Status)

	// the preview: buyers only, covers only the code's products, honours the minimum
	amount := func(token string, lines ...map[string]any) *e2ekit.Response {
		return c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount/amount", Token: token, Body: map[string]any{"code": code, "shopId": shopA.id, "products": lines}})
	}
	got := amount(buyer, map[string]any{"productId": pid, "price": 100000, "quantity": 2}, map[string]any{"productId": "not-covered", "price": 5000, "quantity": 1})
	require.Equal(t, 200, got.Status, string(got.Body))
	assert.EqualValues(t, 205000, got.Get("metadata.totalOrder"))
	assert.EqualValues(t, 20000, got.Get("metadata.discount"), "10% of the covered product only")
	assert.EqualValues(t, 185000, got.Get("metadata.totalPrice"))
	assert.Equal(t, 403, amount(shopA.token, map[string]any{"productId": pid, "price": 1, "quantity": 1}).Status)
	below := amount(buyer, map[string]any{"productId": pid, "price": 100, "quantity": 5})
	assert.Equal(t, 422, below.Status)
	assert.Equal(t, "DISCOUNT_MIN_ORDER", below.Code())

	// deactivated codes cannot be previewed
	off := c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/discount/" + id, Token: shopA.token, Body: map[string]any{"is_active": false}})
	require.Equal(t, 200, off.Status)
	inactive := amount(buyer, map[string]any{"productId": pid, "price": 100000, "quantity": 1})
	assert.Equal(t, 422, inactive.Status)
	assert.Equal(t, "DISCOUNT_INACTIVE", inactive.Code())
}

// The saga participant over real Kafka: order-service does not exist yet in this phase, so the test plays its part.
func TestDiscountSagaStepOverKafka(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	code := uniqueCode("SAGA")
	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/discount", Token: shop.token, Body: discountBody(code, func(b map[string]any) {
		b["max_uses"], b["max_uses_per_user"] = 1, 1
	})})
	require.Equal(t, 201, created.Status, string(created.Body))
	id := created.String("metadata._id")

	userA := userIDOf(t, c, signUpBuyer(t, c))
	userB := userIDOf(t, c, signUpBuyer(t, c))
	items := []saga.Item{{ProductID: "p", ShopID: shop.id, Quantity: 2, Price: 50000}}
	uses := func() float64 {
		r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/discount", Token: shop.token})
		for _, d := range r.Get("metadata.discounts").([]any) {
			if m := d.(map[string]any); m["_id"] == id {
				return m["discount_uses_count"].(float64)
			}
		}
		return -1
	}

	orderA := fmt.Sprintf("e2e-disc-a-%d", time.Now().UnixNano())
	cmd := saga.ApplyDiscounts{OrderID: orderA, UserID: userA, Discounts: []saga.DiscountRequest{{ShopID: shop.id, Code: code}}, Items: items}
	e2ekit.SendSagaCommand(t, saga.TopicDiscountCommands, cmd)
	var applied saga.DiscountsApplied
	e2ekit.WaitForReply(t, orderA, saga.ReplyDiscountsApplied, &applied)
	assert.EqualValues(t, 10000, applied.TotalDiscount, "10% of 2 x 50000")
	assert.EqualValues(t, 1, uses())

	// a redelivered command (new message id, same order) is answered again and counted once
	e2ekit.SendSagaCommand(t, saga.TopicDiscountCommands, cmd)
	time.Sleep(2 * time.Second)
	assert.EqualValues(t, 1, uses())

	// the last use is gone: another buyer is turned down with a reply, not an error
	orderB := fmt.Sprintf("e2e-disc-b-%d", time.Now().UnixNano())
	e2ekit.SendSagaCommand(t, saga.TopicDiscountCommands, saga.ApplyDiscounts{OrderID: orderB, UserID: userB, Discounts: cmd.Discounts, Items: items})
	var rej saga.DiscountsRejected
	e2ekit.WaitForReply(t, orderB, saga.ReplyDiscountsRejected, &rej)
	assert.Equal(t, "DISCOUNT_USED_UP", rej.Reason)
	assert.Equal(t, code, rej.Code)

	// compensating order A gives the use back (once, however often the command arrives) and B can now take it
	for i := 0; i < 2; i++ {
		e2ekit.SendSagaCommand(t, saga.TopicDiscountCommands, saga.ReleaseDiscounts{OrderID: orderA})
	}
	e2ekit.WaitForReply(t, orderA, saga.ReplyDiscountsReleased, nil)
	require.Eventually(t, func() bool { return uses() == 0 }, 15*time.Second, 200*time.Millisecond)
	orderC := fmt.Sprintf("e2e-disc-c-%d", time.Now().UnixNano())
	e2ekit.SendSagaCommand(t, saga.TopicDiscountCommands, saga.ApplyDiscounts{OrderID: orderC, UserID: userB, Discounts: cmd.Discounts, Items: items})
	e2ekit.WaitForReply(t, orderC, saga.ReplyDiscountsApplied, nil)
	assert.EqualValues(t, 1, uses())
}

func userIDOf(t *testing.T, c *e2ekit.Client, token string) string {
	t.Helper()
	r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/cart", Token: token})
	require.Equal(t, 200, r.Status, string(r.Body))
	return r.String("metadata.cart_userId")
}
