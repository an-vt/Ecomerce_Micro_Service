package http

import (
	"encoding/json"
	stdhttp "net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
)

// Catalog holds the product, SPU/SKU and comment routes. Request bodies keep the monolith's snake_case field names
// (product_name, product_price, …) so existing clients keep working.
type Catalog struct {
	client catalogv1.CatalogServiceClient
}

type productJSON struct {
	ID             string         `json:"_id"`
	ShopID         string         `json:"product_shop"`
	ShopName       string         `json:"shop_name,omitempty"`
	Name           string         `json:"product_name"`
	Description    string         `json:"product_description"`
	Slug           string         `json:"product_slug"`
	Price          int64          `json:"product_price"`
	Quantity       int64          `json:"product_quantity"`
	Type           string         `json:"product_type"`
	Thumb          string         `json:"product_thumb"`
	Attributes     map[string]any `json:"product_attributes"`
	RatingsAverage float64        `json:"product_ratingsAverage"`
	IsDraft        bool           `json:"isDraft"`
	IsPublished    bool           `json:"isPublished"`
	CreatedAt      string         `json:"createdAt"`
	UpdatedAt      string         `json:"updatedAt"`
}

func attrsOf(s string) map[string]any {
	m := map[string]any{}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

func productOf(p *catalogv1.Product) productJSON {
	return productJSON{ID: p.GetId(), ShopID: p.GetShopId(), ShopName: p.GetShopName(), Name: p.GetName(), Description: p.GetDescription(),
		Slug: p.GetSlug(), Price: p.GetPrice(), Quantity: p.GetQuantity(), Type: p.GetType(), Thumb: p.GetThumb(),
		Attributes: attrsOf(p.GetAttributesJson()), RatingsAverage: p.GetRatingsAverage(), IsDraft: p.GetIsDraft(),
		IsPublished: p.GetIsPublished(), CreatedAt: p.GetCreatedAt(), UpdatedAt: p.GetUpdatedAt()}
}

func listOf(r *catalogv1.ListProductsResponse) gin.H {
	items := make([]productJSON, 0, len(r.GetProducts()))
	for _, p := range r.GetProducts() {
		items = append(items, productOf(p))
	}
	return gin.H{"products": items, "total": r.GetTotal(), "page": r.GetPage(), "limit": r.GetLimit()}
}

func intQuery(c *gin.Context, name string) int {
	n, _ := strconv.Atoi(c.Query(name))
	return n
}

func rawAttrs(m map[string]any) string {
	if m == nil {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func (h *Catalog) list(c *gin.Context) {
	r, err := h.client.ListProducts(c.Request.Context(), &catalogv1.ListProductsRequest{Page: int32(intQuery(c, "page")), Limit: int32(intQuery(c, "limit")), Sort: c.Query("sort")})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Product list", listOf(r))
}

func (h *Catalog) search(c *gin.Context) {
	r, err := h.client.SearchProducts(c.Request.Context(), &catalogv1.SearchProductsRequest{Keyword: c.Param("keySearch"), Page: int32(intQuery(c, "page")), Limit: int32(intQuery(c, "limit"))})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Search result", listOf(r))
}

func (h *Catalog) get(c *gin.Context) {
	p, err := h.client.GetProduct(c.Request.Context(), &catalogv1.ProductIdRequest{ProductId: c.Param("productId")})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Product", productOf(p))
}

func (h *Catalog) create(c *gin.Context) {
	var in struct {
		Type        string         `json:"product_type" binding:"required"`
		Name        string         `json:"product_name" binding:"required"`
		Description string         `json:"product_description"`
		Price       int64          `json:"product_price"`
		Quantity    int64          `json:"product_quantity"`
		Thumb       string         `json:"product_thumb"`
		Attributes  map[string]any `json:"product_attributes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "product_type and product_name are required")
		return
	}
	p, err := h.client.CreateProduct(c.Request.Context(), &catalogv1.CreateProductRequest{Type: in.Type, Name: in.Name, Description: in.Description,
		Price: in.Price, Quantity: in.Quantity, Thumb: in.Thumb, AttributesJson: rawAttrs(in.Attributes)})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Created product", productOf(p))
}

func (h *Catalog) update(c *gin.Context) {
	var in struct {
		Name        *string        `json:"product_name"`
		Description *string        `json:"product_description"`
		Price       *int64         `json:"product_price"`
		Quantity    *int64         `json:"product_quantity"`
		Thumb       *string        `json:"product_thumb"`
		Attributes  map[string]any `json:"product_attributes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "the body is not a valid product update")
		return
	}
	req := &catalogv1.UpdateProductRequest{ProductId: c.Param("productId"), Name: in.Name, Description: in.Description, Price: in.Price, Quantity: in.Quantity, Thumb: in.Thumb}
	if in.Attributes != nil {
		a := rawAttrs(in.Attributes)
		req.AttributesJson = &a
	}
	p, err := h.client.UpdateProduct(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Updated product", productOf(p))
}

func (h *Catalog) setPublished(publish bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		req := &catalogv1.ProductIdRequest{ProductId: c.Param("id")}
		var p *catalogv1.Product
		var err error
		if publish {
			p, err = h.client.PublishProduct(c.Request.Context(), req)
		} else {
			p, err = h.client.UnpublishProduct(c.Request.Context(), req)
		}
		if err != nil {
			fail(c, err)
			return
		}
		msg := "Unpublished product"
		if publish {
			msg = "Published product"
		}
		ok(c, stdhttp.StatusOK, msg, productOf(p))
	}
}

func (h *Catalog) shopList(published bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		r, err := h.client.ListShopProducts(c.Request.Context(), &catalogv1.ListShopProductsRequest{Published: published, Page: int32(intQuery(c, "page")), Limit: int32(intQuery(c, "limit"))})
		if err != nil {
			fail(c, err)
			return
		}
		ok(c, stdhttp.StatusOK, "Shop products", listOf(r))
	}
}

// ---- SPU / SKU ---------------------------------------------------------------------------------------------------

type variationJSON struct {
	Name    string   `json:"name" binding:"required"`
	Options []string `json:"options"`
	Images  []string `json:"images"`
}

type skuJSON struct {
	ID        string  `json:"sku_id"`
	SpuID     string  `json:"product_id"`
	TierIdx   []int32 `json:"sku_tier_idx"`
	IsDefault bool    `json:"sku_default"`
	Slug      string  `json:"sku_slug"`
	Sort      int32   `json:"sku_sort"`
	Price     int64   `json:"sku_price"`
	Stock     int64   `json:"sku_stock"`
}

func skuOf(s *catalogv1.Sku) skuJSON {
	tiers := s.GetTierIdx()
	if tiers == nil {
		tiers = []int32{}
	}
	return skuJSON{ID: s.GetId(), SpuID: s.GetSpuId(), TierIdx: tiers, IsDefault: s.GetIsDefault(), Slug: s.GetSlug(), Sort: s.GetSort(), Price: s.GetPrice(), Stock: s.GetStock()}
}

func spuOf(r *catalogv1.SpuResponse) gin.H {
	s := r.GetSpu()
	vars := make([]variationJSON, 0, len(s.GetVariations()))
	for _, v := range s.GetVariations() {
		vars = append(vars, variationJSON{Name: v.GetName(), Options: v.GetOptions(), Images: v.GetImages()})
	}
	skus := make([]skuJSON, 0, len(r.GetSkus()))
	for _, k := range r.GetSkus() {
		skus = append(skus, skuOf(k))
	}
	return gin.H{"spu": gin.H{"product_id": s.GetId(), "product_shop": s.GetShopId(), "product_name": s.GetName(), "product_thumb": s.GetThumb(),
		"product_description": s.GetDescription(), "product_slug": s.GetSlug(), "product_price": s.GetPrice(), "product_category": s.GetCategories(),
		"product_quantity": s.GetQuantity(), "product_attributes": attrsOf(s.GetAttributesJson()), "product_ratingsAverage": s.GetRatingsAverage(),
		"product_variations": vars, "isDraft": s.GetIsDraft(), "isPublished": s.GetIsPublished()}, "skus": skus}
}

func (h *Catalog) createSpu(c *gin.Context) {
	var in struct {
		Name        string          `json:"product_name" binding:"required"`
		Thumb       string          `json:"product_thumb"`
		Description string          `json:"product_description"`
		Price       int64           `json:"product_price"`
		Categories  []string        `json:"product_category" binding:"required"`
		Quantity    int64           `json:"product_quantity"`
		Attributes  map[string]any  `json:"product_attributes"`
		Variations  []variationJSON `json:"product_variations" binding:"dive"`
		SkuList     []struct {
			TierIdx   []int32 `json:"sku_tier_idx"`
			IsDefault bool    `json:"sku_default"`
			Sort      int32   `json:"sku_sort"`
			Price     int64   `json:"sku_price"`
			Stock     int64   `json:"sku_stock"`
		} `json:"sku_list"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "product_name and product_category are required")
		return
	}
	req := &catalogv1.CreateSpuRequest{Name: in.Name, Thumb: in.Thumb, Description: in.Description, Price: in.Price, Categories: in.Categories,
		Quantity: in.Quantity, AttributesJson: rawAttrs(in.Attributes)}
	for _, v := range in.Variations {
		req.Variations = append(req.Variations, &catalogv1.Variation{Name: v.Name, Options: v.Options, Images: v.Images})
	}
	for _, k := range in.SkuList {
		req.Skus = append(req.Skus, &catalogv1.SkuInput{TierIdx: k.TierIdx, IsDefault: k.IsDefault, Sort: k.Sort, Price: k.Price, Stock: k.Stock})
	}
	r, err := h.client.CreateSpu(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Created spu", spuOf(r))
}

func (h *Catalog) getSpu(c *gin.Context) {
	id := c.Query("product_id")
	if id == "" {
		badRequest(c, "product_id is required")
		return
	}
	r, err := h.client.GetSpu(c.Request.Context(), &catalogv1.GetSpuRequest{SpuId: id})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Spu", spuOf(r))
}

func (h *Catalog) getSku(c *gin.Context) {
	skuID, spuID := c.Query("sku_id"), c.Query("product_id")
	if skuID == "" || spuID == "" {
		badRequest(c, "sku_id and product_id are required")
		return
	}
	s, err := h.client.GetSku(c.Request.Context(), &catalogv1.GetSkuRequest{SkuId: skuID, SpuId: spuID})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Sku", skuOf(s))
}

// ---- comments ----------------------------------------------------------------------------------------------------

type commentJSON struct {
	ID        string `json:"_id"`
	ProductID string `json:"comment_productId"`
	UserID    string `json:"comment_userId"`
	Content   string `json:"comment_content"`
	Left      int64  `json:"comment_left"`
	Right     int64  `json:"comment_right"`
	ParentID  string `json:"comment_parentId"`
	CreatedAt string `json:"createdAt"`
}

func commentOf(c *catalogv1.Comment) commentJSON {
	return commentJSON{ID: c.GetId(), ProductID: c.GetProductId(), UserID: c.GetUserId(), Content: c.GetContent(), Left: c.GetLeft(),
		Right: c.GetRight(), ParentID: c.GetParentId(), CreatedAt: c.GetCreatedAt()}
}

func (h *Catalog) listComments(c *gin.Context) {
	r, err := h.client.ListComments(c.Request.Context(), &catalogv1.ListCommentsRequest{ProductId: c.Query("productId"), ParentId: c.Query("parentId"),
		Limit: int32(intQuery(c, "limit")), Skip: int32(intQuery(c, "skip"))})
	if err != nil {
		fail(c, err)
		return
	}
	out := make([]commentJSON, 0, len(r.GetComments()))
	for _, cm := range r.GetComments() {
		out = append(out, commentOf(cm))
	}
	ok(c, stdhttp.StatusOK, "Comments", out)
}

func (h *Catalog) createComment(c *gin.Context) {
	var in struct {
		ProductID string `json:"productId" binding:"required"`
		Content   string `json:"content" binding:"required"`
		ParentID  string `json:"parentCommentId"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "productId and content are required")
		return
	}
	cm, err := h.client.CreateComment(c.Request.Context(), &catalogv1.CreateCommentRequest{ProductId: in.ProductID, Content: in.Content, ParentCommentId: in.ParentID})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Created comment", commentOf(cm))
}

func (h *Catalog) deleteComment(c *gin.Context) {
	if _, err := h.client.DeleteComment(c.Request.Context(), &catalogv1.DeleteCommentRequest{CommentId: c.Query("id"), ProductId: c.Query("productId")}); err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Deleted comment", nil)
}
