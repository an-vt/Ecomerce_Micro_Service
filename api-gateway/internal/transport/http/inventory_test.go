package http_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
)

func TestStockIsPublicToRead(t *testing.T) {
	g := newGateway(t)
	r := g.call("GET", "/v1/api/inventory/p1", "")
	require.Equal(t, 200, r.Code)
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "p1", meta["inven_productId"], "monolith field names")
	assert.EqualValues(t, 7, meta["inven_stock"])
	assert.EqualValues(t, 2, meta["inven_reserved"])
	assert.Equal(t, 401, g.do(req{method: "GET", path: "/v1/api/inventory/p1"}).Code, "the API key is still required")
}

func TestAddStockIsForShopsOnlyAndSendsTheCaller(t *testing.T) {
	g := newGateway(t)
	body := `{"productId":"p1","stock":5,"location":"Hall 2"}`
	assert.Equal(t, 401, g.call("POST", "/v1/api/inventory", body).Code)
	assert.Equal(t, 403, g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/inventory", body).Code)

	r := g.as(g.token("shop1", "SHOP", "shop"), "POST", "/v1/api/inventory", body)
	require.Equal(t, 200, r.Code, r.Body.String())
	assert.EqualValues(t, 15, r.json()["metadata"].(map[string]any)["inven_stock"])
	g.inventory.mu.Lock()
	assert.Equal(t, "shop1", g.inventory.last.Get("x-account-id")[0], "ownership is checked by the service against the token's shop")
	g.inventory.mu.Unlock()

	shop := g.token("shop1", "SHOP", "shop")
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/inventory", `{"productId":"p1"}`).Code, "stock is required")
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/inventory", `{"stock":5}`).Code, "productId is required")

	g.inventory.addErr = errs.PermissionDenied("NOT_OWNER", "this product belongs to another shop")
	r = g.as(shop, "POST", "/v1/api/inventory", body)
	assert.Equal(t, 403, r.Code)
	assert.Equal(t, "NOT_OWNER", r.errCode())
}
