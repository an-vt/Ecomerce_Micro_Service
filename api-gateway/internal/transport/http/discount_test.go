package http_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
)

const createBody = `{"code":"sale10","name":"Ten","type":"percent","value":10,"start_date":"2026-10-30T00:00:00Z","end_date":"2026-12-30T00:00:00Z","max_uses":5,"max_uses_per_user":1,"min_order_value":1000,"applies_to":"specific","product_ids":["p1"]}`

func TestDiscountRoutesHaveTheRightGuards(t *testing.T) {
	g := newGateway(t)
	shop, user := g.token("shop1", "SHOP", "shop"), g.token("u1", "USER", "user")
	// management: shops only
	assert.Equal(t, 401, g.call("POST", "/v1/api/discount", createBody).Code)
	assert.Equal(t, 403, g.as(user, "POST", "/v1/api/discount", createBody).Code)
	assert.Equal(t, 403, g.as(user, "PATCH", "/v1/api/discount/d1", `{}`).Code)
	assert.Equal(t, 403, g.as(user, "GET", "/v1/api/discount", "").Code)
	// the amount preview: buyers only
	body := `{"code":"x","shopId":"s","products":[{"productId":"p","price":1,"quantity":1}]}`
	assert.Equal(t, 401, g.call("POST", "/v1/api/discount/amount", body).Code)
	assert.Equal(t, 403, g.as(shop, "POST", "/v1/api/discount/amount", body).Code)
	// browsing a code's products: public, API key only
	assert.Equal(t, 200, g.call("GET", "/v1/api/discount/list_product_code?code=X&shopId=s", "").Code)
	assert.Equal(t, 401, g.do(req{method: "GET", path: "/v1/api/discount/list_product_code?code=X&shopId=s"}).Code, "the API key is required")
}

func TestCreateDiscountSendsTheBodyAndReturns201(t *testing.T) {
	g := newGateway(t)
	r := g.as(g.token("shop1", "SHOP", "shop"), "POST", "/v1/api/discount", createBody)
	require.Equal(t, 201, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "sale10", meta["discount_code"])
	assert.Equal(t, "percent", meta["discount_type"])
	assert.Equal(t, true, meta["discount_is_active"])
	assert.Equal(t, []any{}, meta["discount_product_ids"], "never null")
	g.discount.mu.Lock()
	defer g.discount.mu.Unlock()
	assert.Equal(t, "2026-12-30T00:00:00Z", g.discount.createReq.GetEndDate())
	assert.Equal(t, []string{"p1"}, g.discount.createReq.GetProductIds())
	assert.EqualValues(t, 1, g.discount.createReq.GetMaxUsesPerUser())
	assert.Equal(t, "shop1", g.discount.last.Get("x-account-id")[0], "the shop is the token's account")
}

func TestCreateDiscountValidation(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/discount", `{}`).Code)
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/discount", `{"code":"x"}`).Code)
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/discount", `{"shopId":"other","code":"x","name":"n","type":"percent","value":1,"start_date":"a","end_date":"b","max_uses":1}`).Code,
		"a shop id in the body is rejected: the owner is the caller")
	g.discount.fail = errs.AlreadyExists("DISCOUNT_CODE_TAKEN", "taken")
	r := g.as(shop, "POST", "/v1/api/discount", createBody)
	assert.Equal(t, 409, r.Code)
	assert.Equal(t, "DISCOUNT_CODE_TAKEN", r.errCode())
}

func TestUpdateDiscountOnlySendsWhatWasGiven(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	r := g.as(shop, "PATCH", "/v1/api/discount/d7", `{"name":"New","is_active":false}`)
	require.Equal(t, 200, r.Code, r.Body.String())
	g.discount.mu.Lock()
	req := g.discount.updateReq
	g.discount.mu.Unlock()
	assert.Equal(t, "d7", req.GetDiscountId())
	assert.Equal(t, "New", req.GetName())
	assert.False(t, req.GetIsActive())
	assert.Nil(t, req.MaxUses, "absent stays absent")
	assert.False(t, req.GetReplaceProductIds())

	r = g.as(shop, "PATCH", "/v1/api/discount/d7", `{"product_ids":[]}`)
	require.Equal(t, 200, r.Code)
	g.discount.mu.Lock()
	assert.True(t, g.discount.updateReq.GetReplaceProductIds(), "an empty list is still a replacement")
	g.discount.mu.Unlock()
	r = g.as(shop, "PATCH", "/v1/api/discount/d7", `{"product_ids":["a","b"]}`)
	require.Equal(t, 200, r.Code)
	assert.Equal(t, []any{"a", "b"}, r.json()["metadata"].(map[string]any)["discount_product_ids"])

	assert.Equal(t, 400, g.as(shop, "PATCH", "/v1/api/discount/d7", `{"code":"CHANGED"}`).Code, "the code cannot be changed")
	g.discount.fail = errs.PermissionDenied("NOT_OWNER", "not yours")
	assert.Equal(t, 403, g.as(shop, "PATCH", "/v1/api/discount/d7", `{"name":"x"}`).Code)
}

func TestListShopDiscounts(t *testing.T) {
	g := newGateway(t)
	r := g.as(g.token("shop1", "SHOP", "shop"), "GET", "/v1/api/discount?page=2&limit=5", "")
	require.Equal(t, 200, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.EqualValues(t, 1, meta["total"])
	assert.EqualValues(t, 2, meta["page"])
	assert.EqualValues(t, 5, meta["limit"])
	assert.EqualValues(t, 2, meta["discounts"].([]any)[0].(map[string]any)["discount_uses_count"])
	g.discount.mu.Lock()
	defer g.discount.mu.Unlock()
	assert.EqualValues(t, 2, g.discount.listReq.GetPage())
}

func TestProductsForCode(t *testing.T) {
	g := newGateway(t)
	r := g.call("GET", "/v1/api/discount/list_product_code?code=SALE10&shopId=shop1&limit=3", "")
	require.Equal(t, 200, r.Code, r.Body.String())
	p := r.json()["metadata"].(map[string]any)["products"].([]any)[0].(map[string]any)
	assert.Equal(t, "Shirt", p["product_name"])
	assert.EqualValues(t, 1000, p["product_price"])
	assert.Equal(t, 400, g.call("GET", "/v1/api/discount/list_product_code?code=SALE10", "").Code, "shopId is required")
	assert.Equal(t, 400, g.call("GET", "/v1/api/discount/list_product_code?shopId=s", "").Code, "code is required")
	g.discount.fail = errs.NotFound("DISCOUNT_NOT_FOUND", "no such code")
	assert.Equal(t, 404, g.call("GET", "/v1/api/discount/list_product_code?code=NOPE&shopId=s", "").Code)
}

func TestDiscountAmountKeepsTheMonolithShape(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	r := g.as(user, "POST", "/v1/api/discount/amount", `{"code":"SALE10","shopId":"shop1","products":[{"productId":"p1","price":100000,"quantity":2}]}`)
	require.Equal(t, 200, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.EqualValues(t, 200000, meta["totalOrder"])
	assert.EqualValues(t, 20000, meta["discount"])
	assert.EqualValues(t, 180000, meta["totalPrice"])
	g.discount.mu.Lock()
	assert.Equal(t, "u1", g.discount.last.Get("x-account-id")[0], "the buyer's own uses are counted")
	assert.Equal(t, int64(100000), g.discount.calcReq.GetLines()[0].GetPrice())
	g.discount.mu.Unlock()

	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/discount/amount", `{"code":"SALE10","shopId":"s","products":[]}`).Code, "an order needs products")
	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/discount/amount", `{"code":"SALE10"}`).Code)
	g.discount.fail = errs.FailedPrecondition("DISCOUNT_EXPIRED", "expired")
	r = g.as(user, "POST", "/v1/api/discount/amount", `{"code":"SALE10","shopId":"s","products":[{"productId":"p","price":1,"quantity":1}]}`)
	assert.Equal(t, 422, r.Code)
	assert.Equal(t, "DISCOUNT_EXPIRED", r.errCode())
}
