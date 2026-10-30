package http_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
)

func TestPublicCatalogRoutesNeedOnlyTheAPIKey(t *testing.T) {
	g := newGateway(t)

	r := g.call("GET", "/v1/api/product?page=2&limit=5&sort=price_asc", "")
	require.Equal(t, 200, r.Code, r.Body.String())
	prods := r.json()["metadata"].(map[string]any)["products"].([]any)
	assert.Equal(t, "Shirt", prods[0].(map[string]any)["product_name"], "monolith field names are kept")
	assert.Equal(t, "M", prods[0].(map[string]any)["product_attributes"].(map[string]any)["size"], "attributes are real JSON, not an escaped string")
	assert.EqualValues(t, 2, r.json()["metadata"].(map[string]any)["page"])

	assert.Equal(t, "found-red", g.call("GET", "/v1/api/product/search/red", "").json()["metadata"].(map[string]any)["products"].([]any)[0].(map[string]any)["_id"])
	assert.Equal(t, "p9", g.call("GET", "/v1/api/product/p9", "").json()["metadata"].(map[string]any)["_id"])
	assert.Equal(t, 200, g.call("GET", "/v1/api/product/spu/get_spu_info?product_id=spu-7", "").Code)
	assert.Equal(t, 200, g.call("GET", "/v1/api/product/sku/select_variation?sku_id=s.1&product_id=s", "").Code)
	assert.Equal(t, 200, g.call("GET", "/v1/api/comment/list?productId=p1&parentId=c0&limit=5", "").Code)
	assert.Equal(t, 401, g.do(req{method: "GET", path: "/v1/api/product"}).Code, "no API key, no catalog")
}

func TestStaticProductPathsAreNotSwallowedByTheIdRoute(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	assert.Equal(t, "draft", g.as(shop, "GET", "/v1/api/product/drafts/all", "").json()["metadata"].(map[string]any)["products"].([]any)[0].(map[string]any)["_id"])
	assert.Equal(t, "published", g.as(shop, "GET", "/v1/api/product/published/all", "").json()["metadata"].(map[string]any)["products"].([]any)[0].(map[string]any)["_id"])
}

func TestMissingParametersAreRejectedBeforeReachingTheService(t *testing.T) {
	g := newGateway(t)
	assert.Equal(t, 400, g.call("GET", "/v1/api/product/spu/get_spu_info", "").Code)
	assert.Equal(t, 400, g.call("GET", "/v1/api/product/sku/select_variation?sku_id=x", "").Code)
	assert.Equal(t, 400, g.call("GET", "/v1/api/product/sku/select_variation?product_id=x", "").Code)
}

func TestShopRoutesRequireAShopToken(t *testing.T) {
	g := newGateway(t)
	body := `{"product_type":"Clothing","product_name":"Shirt","product_price":150000,"product_quantity":5,"product_attributes":{"brand":"A","size":"M","material":"c"}}`

	assert.Equal(t, 401, g.call("POST", "/v1/api/product", body).Code, "no token")
	r := g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/product", body)
	assert.Equal(t, 403, r.Code, "buyers cannot create products")

	created := g.as(g.token("shop1", "SHOP", "shop"), "POST", "/v1/api/product", body)
	require.Equal(t, 201, created.Code, created.Body.String())
	meta := created.json()["metadata"].(map[string]any)
	assert.Equal(t, "p-new", meta["_id"])
	assert.Equal(t, true, meta["isDraft"])
	assert.Equal(t, "shop1", g.catalogMD("x-account-id"), "the caller's identity travels with the call; the body has no say in who owns the product")
	assert.Equal(t, "SHOP", g.catalogMD("x-account-type"))

	for _, route := range []struct{ method, path string }{
		{"PATCH", "/v1/api/product/p1"}, {"POST", "/v1/api/product/publish/p1"}, {"POST", "/v1/api/product/unpublish/p1"},
		{"GET", "/v1/api/product/drafts/all"}, {"GET", "/v1/api/product/published/all"}, {"POST", "/v1/api/product/spu/new"},
	} {
		assert.Equal(t, 403, g.as(g.token("u1", "USER", "user"), route.method, route.path, `{}`).Code, route.method+" "+route.path)
	}
}

func (g *gw) catalogMD(key string) string {
	g.catalog.mu.Lock()
	defer g.catalog.mu.Unlock()
	if v := g.catalog.last.Get(key); len(v) > 0 {
		return v[0]
	}
	return ""
}

func TestCreateProductValidationAndServiceErrors(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/product", `{"product_name":"x"}`).Code, "product_type is required")
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/product", `{"product_type":"Clothing","product_name":"x","unknown":1}`).Code, "unknown fields are rejected")

	g.catalog.create = func(*catalogv1.CreateProductRequest) (*catalogv1.Product, error) {
		return nil, errs.InvalidArgument("INVALID_ATTRIBUTES", "Clothing products need the attributes: size")
	}
	r := g.as(shop, "POST", "/v1/api/product", `{"product_type":"Clothing","product_name":"x"}`)
	assert.Equal(t, 400, r.Code)
	assert.Equal(t, "INVALID_ATTRIBUTES", r.errCode())

	g.catalog.create = func(*catalogv1.CreateProductRequest) (*catalogv1.Product, error) {
		return nil, errs.AlreadyExists("PRODUCT_NAME_TAKEN", "taken")
	}
	assert.Equal(t, 409, g.as(shop, "POST", "/v1/api/product", `{"product_type":"Clothing","product_name":"x"}`).Code)
}

func TestUpdateSendsOnlyWhatTheClientSent(t *testing.T) {
	g := newGateway(t)
	var got *catalogv1.UpdateProductRequest
	g.catalog.update = func(in *catalogv1.UpdateProductRequest) (*catalogv1.Product, error) {
		got = in
		return &catalogv1.Product{Id: in.GetProductId()}, nil
	}
	r := g.as(g.token("shop1", "SHOP", "shop"), "PATCH", "/v1/api/product/p1", `{"product_price":99,"product_attributes":{"size":"L"}}`)
	require.Equal(t, 200, r.Code, r.Body.String())
	require.NotNil(t, got)
	assert.Equal(t, "p1", got.GetProductId())
	require.NotNil(t, got.Price)
	assert.Equal(t, int64(99), *got.Price)
	assert.Nil(t, got.Name, "fields that were not sent stay untouched")
	assert.Nil(t, got.Quantity)
	assert.JSONEq(t, `{"size":"L"}`, got.GetAttributesJson())

	_ = g.as(g.token("shop1", "SHOP", "shop"), "PATCH", "/v1/api/product/p1", `{"product_price":5}`)
	assert.Nil(t, got.AttributesJson, "no attributes sent, none merged")
	assert.Equal(t, 400, g.as(g.token("shop1", "SHOP", "shop"), "PATCH", "/v1/api/product/p1", `nope`).Code)
}

func TestPublishUnpublishAndServiceNotFound(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	assert.Equal(t, true, g.as(shop, "POST", "/v1/api/product/publish/p1", "").json()["metadata"].(map[string]any)["isPublished"])
	assert.Equal(t, false, g.as(shop, "POST", "/v1/api/product/unpublish/p1", "").json()["metadata"].(map[string]any)["isPublished"])

	g.catalog.get = func(*catalogv1.ProductIdRequest) (*catalogv1.Product, error) {
		return nil, errs.NotFound("PRODUCT_NOT_FOUND", "product not found")
	}
	r := g.call("GET", "/v1/api/product/ghost", "")
	assert.Equal(t, 404, r.Code)
	assert.Equal(t, "PRODUCT_NOT_FOUND", r.errCode())
}

func TestSpuCreation(t *testing.T) {
	g := newGateway(t)
	shop := g.token("shop1", "SHOP", "shop")
	body := `{"product_name":"T-Shirt","product_category":["clothes"],"product_variations":[{"name":"color","options":["red","blue"]}],
		"sku_list":[{"sku_tier_idx":[0],"sku_price":100,"sku_stock":5},{"sku_tier_idx":[1],"sku_price":110,"sku_stock":2}]}`
	r := g.as(shop, "POST", "/v1/api/product/spu/new", body)
	require.Equal(t, 201, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "spu-1", meta["spu"].(map[string]any)["product_id"])
	skus := meta["skus"].([]any)
	require.Len(t, skus, 2)
	assert.EqualValues(t, 110, skus[1].(map[string]any)["sku_price"])
	assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/product/spu/new", `{"product_name":"x"}`).Code, "a category is required")
}

func TestCommentRoutes(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	assert.Equal(t, 401, g.call("POST", "/v1/api/comment", `{"productId":"p1","content":"hi"}`).Code)
	r := g.as(user, "POST", "/v1/api/comment", `{"productId":"p1","content":"hi","parentCommentId":"c0"}`)
	require.Equal(t, 201, r.Code)
	assert.Equal(t, "c0", r.json()["metadata"].(map[string]any)["comment_parentId"])
	assert.Equal(t, 400, g.as(user, "POST", "/v1/api/comment", `{"content":"hi"}`).Code)
	assert.Equal(t, 200, g.as(user, "DELETE", "/v1/api/comment?id=c1&productId=p1", "").Code)
	assert.Equal(t, 401, g.call("DELETE", "/v1/api/comment?id=c1&productId=p1", "").Code)
}

func TestUploadURLIsForShopsAndKeepsTheDeclaredFile(t *testing.T) {
	g := newGateway(t)
	body := `{"contentType":"image/png","size":2048}`
	assert.Equal(t, 401, g.call("POST", "/v1/api/product/upload-url", body).Code)
	assert.Equal(t, 403, g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/product/upload-url", body).Code, "buyers do not upload product images")

	var got *catalogv1.CreateUploadUrlRequest
	g.catalog.upload = func(in *catalogv1.CreateUploadUrlRequest) (*catalogv1.UploadUrl, error) {
		got = in
		return &catalogv1.UploadUrl{UploadUrl: "http://store.test/b/k?sig=1", Method: "PUT", Headers: map[string]string{"Content-Type": "image/png"},
			PublicUrl: "http://store.test/b/k", Key: "products/s1/k.png", ExpiresAt: "2026-10-30T12:05:00Z"}, nil
	}
	r := g.as(g.token("s1", "SHOP", "shop"), "POST", "/v1/api/product/upload-url", body)
	require.Equal(t, 200, r.Code, r.Body.String())
	meta := r.json()["metadata"].(map[string]any)
	assert.Equal(t, "http://store.test/b/k?sig=1", meta["uploadUrl"])
	assert.Equal(t, "PUT", meta["method"])
	assert.Equal(t, map[string]any{"Content-Type": "image/png"}, meta["headers"])
	assert.Equal(t, "http://store.test/b/k", meta["publicUrl"])
	assert.Equal(t, "image/png", got.GetContentType())
	assert.EqualValues(t, 2048, got.GetSize())
}

func TestUploadURLValidationAndErrors(t *testing.T) {
	g := newGateway(t)
	shop := g.token("s1", "SHOP", "shop")
	for name, body := range map[string]string{"empty": `{}`, "no size": `{"contentType":"image/png"}`, "no type": `{"size":10}`, "file name": `{"contentType":"image/png","size":10,"fileName":"../../etc/passwd"}`} {
		assert.Equal(t, 400, g.as(shop, "POST", "/v1/api/product/upload-url", body).Code, name)
	}
	g.catalog.upload = func(*catalogv1.CreateUploadUrlRequest) (*catalogv1.UploadUrl, error) {
		return nil, errs.InvalidArgument("UNSUPPORTED_MEDIA_TYPE", "only images")
	}
	r := g.as(shop, "POST", "/v1/api/product/upload-url", `{"contentType":"text/html","size":10}`)
	assert.Equal(t, 400, r.Code)
	assert.Equal(t, "UNSUPPORTED_MEDIA_TYPE", r.errCode())
	g.catalog.upload = func(*catalogv1.CreateUploadUrlRequest) (*catalogv1.UploadUrl, error) {
		return nil, errs.FailedPrecondition("MEDIA_NOT_CONFIGURED", "image upload is not enabled")
	}
	r = g.as(shop, "POST", "/v1/api/product/upload-url", `{"contentType":"image/png","size":10}`)
	assert.Equal(t, 422, r.Code)
	assert.Equal(t, "MEDIA_NOT_CONFIGURED", r.errCode())
}
