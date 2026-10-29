//go:build e2e

package e2e_test

import (
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
	"github.com/an-vt/ecom/platform-go/event"
)

type shopSession struct {
	token, id, name string
}

func signUpShop(t *testing.T, c *e2ekit.Client) shopSession {
	t.Helper()
	name := fmt.Sprintf("E2E Shop %d", time.Now().UnixNano()%1_000_000)
	r := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/signup", Body: map[string]string{"name": name, "email": e2ekit.UniqueEmail("cs"), "password": "correct-horse-battery"}})
	require.Equal(t, 201, r.Status, string(r.Body))
	return shopSession{token: r.String("metadata.tokens.accessToken"), id: r.String("metadata.account.id"), name: name}
}

func signUpBuyer(t *testing.T, c *e2ekit.Client) string {
	t.Helper()
	email := e2ekit.UniqueEmail("cb")
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/new_user", Body: map[string]string{"email": email}}).Status)
	ev := e2ekit.WaitForEvent(t, "auth.email-verification.requested", func(e event.Envelope) bool {
		var p struct{ Email string }
		return e.Decode(&p) == nil && p.Email == email
	})
	var p struct{ Link string }
	require.NoError(t, ev.Decode(&p))
	u, err := url.Parse(p.Link)
	require.NoError(t, err)
	r := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/user/welcome_back?token=" + url.QueryEscape(u.Query().Get("token"))})
	require.Equal(t, 200, r.Status, string(r.Body))
	return r.String("metadata.tokens.accessToken")
}

func TestProductCatalogThroughTheGateway(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	productName := fmt.Sprintf("E2E Cotton Shirt %d", time.Now().UnixNano())

	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{
		"product_type": "Clothing", "product_name": productName, "product_description": "soft cotton", "product_price": 150000, "product_quantity": 25,
		"product_attributes": map[string]any{"brand": "Acme", "size": "M", "material": "cotton", "care": map[string]any{"wash": "cold", "iron": "low"}}}})
	require.Equal(t, 201, created.Status, string(created.Body))
	id := created.String("metadata._id")
	assert.Equal(t, shop.id, created.String("metadata.product_shop"), "the owner is the token's shop")
	assert.Equal(t, true, created.Get("metadata.isDraft"))

	// validation by product type
	bad := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{"product_type": "Clothing", "product_name": productName + " x", "product_attributes": map[string]any{"brand": "x"}}})
	assert.Equal(t, 400, bad.Status)
	assert.Equal(t, "INVALID_ATTRIBUTES", bad.Code())
	dup := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{"product_type": "Clothing", "product_name": productName, "product_attributes": map[string]any{"brand": "a", "size": "b", "material": "c"}}})
	assert.Equal(t, 409, dup.Status)

	// the outbox relay published catalog.product.created
	ev := e2ekit.WaitForEvent(t, "catalog.product.created", func(e event.Envelope) bool { return e.AggregateID == id })
	var payload struct {
		ShopID   string
		Quantity int64
	}
	require.NoError(t, ev.Decode(&payload))
	assert.Equal(t, shop.id, payload.ShopID)
	assert.EqualValues(t, 25, payload.Quantity)

	// drafts are private
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/" + id}).Status)
	drafts := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/drafts/all", Token: shop.token})
	require.Equal(t, 200, drafts.Status)
	assert.EqualValues(t, 1, drafts.Get("metadata.total"))

	// another shop cannot touch it
	other := signUpShop(t, c)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/product/" + id, Token: other.token, Body: map[string]any{"product_price": 1}}).Status)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/publish/" + id, Token: other.token}).Status)

	// partial update merges nested attributes and tells inventory about stock changes
	upd := c.Do(t, e2ekit.Req{Method: "PATCH", Path: "/v1/api/product/" + id, Token: shop.token, Body: map[string]any{
		"product_price": 99000, "product_quantity": 40, "product_attributes": map[string]any{"size": "L", "care": map[string]any{"wash": "warm"}}}})
	require.Equal(t, 200, upd.Status, string(upd.Body))
	assert.EqualValues(t, 99000, upd.Get("metadata.product_price"))
	assert.Equal(t, "L", upd.String("metadata.product_attributes.size"))
	assert.Equal(t, "Acme", upd.String("metadata.product_attributes.brand"))
	assert.Equal(t, "low", upd.String("metadata.product_attributes.care.iron"), "nested attributes are merged, not replaced")
	e2ekit.WaitForEvent(t, "catalog.product.updated", func(e event.Envelope) bool {
		var p struct{ Quantity *int64 }
		return e.AggregateID == id && e.Decode(&p) == nil && p.Quantity != nil && *p.Quantity == 40
	})

	// publish: visible to everyone, with the shop name that arrived from auth over Kafka
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/publish/" + id, Token: shop.token}).Status)
	var seen *e2ekit.Response
	require.Eventually(t, func() bool {
		seen = c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/" + id})
		return seen.Status == 200 && seen.String("metadata.shop_name") == shop.name
	}, 30*time.Second, 300*time.Millisecond, "catalog builds its shop projection from auth.shop.registered")

	list := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product?limit=100&sort=ctime"})
	assert.Equal(t, 200, list.Status)
	found := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/search/cotton"})
	require.Equal(t, 200, found.Status)
	assert.GreaterOrEqual(t, found.Get("metadata.total"), float64(1))

	// unpublish hides it again
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/unpublish/" + id, Token: shop.token}).Status)
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/" + id}).Status)

	// buyers cannot manage products
	buyer := signUpBuyer(t, c)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: buyer, Body: map[string]any{"product_type": "Clothing", "product_name": "nope"}}).Status)
}

func TestSpuSkuAndCommentsThroughTheGateway(t *testing.T) {
	c := e2ekit.NewClient(t)
	shop := signUpShop(t, c)
	name := fmt.Sprintf("E2E T-Shirt %d", time.Now().UnixNano())

	spu := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/spu/new", Token: shop.token, Body: map[string]any{
		"product_name": name, "product_category": []string{"clothes"}, "product_price": 100, "product_quantity": 7,
		"product_variations": []map[string]any{{"name": "color", "options": []string{"red", "blue"}}},
		"sku_list":           []map[string]any{{"sku_tier_idx": []int{0}, "sku_price": 100, "sku_stock": 5}, {"sku_tier_idx": []int{1}, "sku_price": 110, "sku_stock": 2}}}})
	require.Equal(t, 201, spu.Status, string(spu.Body))
	spuID := spu.String("metadata.spu.product_id")
	skus := spu.Get("metadata.skus").([]any)
	require.Len(t, skus, 2)
	skuID := skus[1].(map[string]any)["sku_id"].(string)

	bad := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product/spu/new", Token: shop.token, Body: map[string]any{
		"product_name": name + " bad", "product_category": []string{"c"}, "product_variations": []map[string]any{{"name": "color", "options": []string{"red"}}},
		"sku_list": []map[string]any{{"sku_tier_idx": []int{5}}}}})
	assert.Equal(t, 400, bad.Status)
	assert.Equal(t, "INVALID_SKU", bad.Code())

	got := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/spu/get_spu_info?product_id=" + spuID})
	require.Equal(t, 200, got.Status)
	assert.Len(t, got.Get("metadata.skus"), 2)

	// the SKU lookup is cached, and so are unknown ids (the second ghost lookup does not reach MongoDB)
	for i := 0; i < 3; i++ {
		sku := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/sku/select_variation?sku_id=" + skuID + "&product_id=" + spuID})
		require.Equal(t, 200, sku.Status)
		assert.EqualValues(t, 110, sku.Get("metadata.sku_price"))
	}
	for i := 0; i < 2; i++ {
		ghost := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/product/sku/select_variation?sku_id=ghost&product_id=" + spuID})
		assert.Equal(t, 404, ghost.Status)
		assert.Equal(t, "SKU_NOT_FOUND", ghost.Code())
	}

	// comments need a published product; nested threads and cascading delete
	p := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/product", Token: shop.token, Body: map[string]any{
		"product_type": "Furniture", "product_name": name + " chair", "product_attributes": map[string]any{"brand": "b", "size": "L", "material": "oak"}}})
	require.Equal(t, 201, p.Status, string(p.Body))
	pid := p.String("metadata._id")
	buyerA, buyerB := signUpBuyer(t, c), signUpBuyer(t, c)
	root := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/comment", Token: buyerA, Body: map[string]any{"productId": pid, "content": "lovely chair"}})
	require.Equal(t, 201, root.Status, string(root.Body))
	rootID := root.String("metadata._id")
	reply := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/comment", Token: buyerB, Body: map[string]any{"productId": pid, "content": "agreed", "parentCommentId": rootID}})
	require.Equal(t, 201, reply.Status, string(reply.Body))

	kids := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/comment/list?productId=" + pid + "&parentId=" + rootID})
	assert.Len(t, kids.Get("metadata"), 1)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "DELETE", Path: "/v1/api/comment?id=" + rootID + "&productId=" + pid, Token: buyerB}).Status, "only the author deletes")
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "DELETE", Path: "/v1/api/comment?id=" + rootID + "&productId=" + pid, Token: buyerA}).Status)
	top := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/comment/list?productId=" + pid})
	assert.Empty(t, top.Get("metadata"))
	gone := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/comment/list?productId=" + pid + "&parentId=" + rootID})
	assert.Empty(t, gone.Get("metadata"), "the reply went with its thread")
	assert.Equal(t, 404, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/comment", Token: buyerA, Body: map[string]any{"productId": "no-such-product", "content": "x"}}).Status)
}
