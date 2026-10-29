package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
)

// Discount holds the discount routes. The shop of a code is always the caller (token), never a request field: the
// monolith let any shop edit any discount by id.
type Discount struct {
	client discountv1.DiscountServiceClient
}

type discountJSON struct {
	ID             string   `json:"_id"`
	ShopID         string   `json:"discount_shopId"`
	Code           string   `json:"discount_code"`
	Name           string   `json:"discount_name"`
	Description    string   `json:"discount_description"`
	Type           string   `json:"discount_type"`
	Value          int64    `json:"discount_value"`
	StartDate      string   `json:"discount_start_date"`
	EndDate        string   `json:"discount_end_date"`
	MaxUses        int64    `json:"discount_max_uses"`
	UsesCount      int64    `json:"discount_uses_count"`
	MaxUsesPerUser int64    `json:"discount_max_uses_per_user"`
	MinOrderValue  int64    `json:"discount_min_order_value"`
	IsActive       bool     `json:"discount_is_active"`
	AppliesTo      string   `json:"discount_applies_to"`
	ProductIDs     []string `json:"discount_product_ids"`
}

func discountOf(d *discountv1.Discount) discountJSON {
	ids := d.GetProductIds()
	if ids == nil {
		ids = []string{}
	}
	return discountJSON{ID: d.GetId(), ShopID: d.GetShopId(), Code: d.GetCode(), Name: d.GetName(), Description: d.GetDescription(),
		Type: d.GetType(), Value: d.GetValue(), StartDate: d.GetStartDate(), EndDate: d.GetEndDate(), MaxUses: d.GetMaxUses(),
		UsesCount: d.GetUsedCount(), MaxUsesPerUser: d.GetMaxUsesPerUser(), MinOrderValue: d.GetMinOrderValue(), IsActive: d.GetIsActive(),
		AppliesTo: d.GetAppliesTo(), ProductIDs: ids}
}

func (h *Discount) create(c *gin.Context) {
	var in struct {
		Code           string   `json:"code" binding:"required"`
		Name           string   `json:"name" binding:"required"`
		Description    string   `json:"description"`
		Type           string   `json:"type" binding:"required"`
		Value          int64    `json:"value" binding:"required"`
		StartDate      string   `json:"start_date" binding:"required"`
		EndDate        string   `json:"end_date" binding:"required"`
		MaxUses        int64    `json:"max_uses" binding:"required"`
		MaxUsesPerUser int64    `json:"max_uses_per_user"`
		MinOrderValue  int64    `json:"min_order_value"`
		AppliesTo      string   `json:"applies_to"`
		ProductIDs     []string `json:"product_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "code, name, type, value, start_date, end_date and max_uses are required")
		return
	}
	r, err := h.client.CreateDiscount(c.Request.Context(), &discountv1.CreateDiscountRequest{Code: in.Code, Name: in.Name, Description: in.Description,
		Type: in.Type, Value: in.Value, StartDate: in.StartDate, EndDate: in.EndDate, MaxUses: in.MaxUses, MaxUsesPerUser: in.MaxUsesPerUser,
		MinOrderValue: in.MinOrderValue, AppliesTo: in.AppliesTo, ProductIds: in.ProductIDs})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusCreated, "Discount created", discountOf(r))
}

func (h *Discount) update(c *gin.Context) {
	var in struct {
		Name           *string   `json:"name"`
		Description    *string   `json:"description"`
		EndDate        *string   `json:"end_date"`
		IsActive       *bool     `json:"is_active"`
		MaxUses        *int64    `json:"max_uses"`
		MaxUsesPerUser *int64    `json:"max_uses_per_user"`
		MinOrderValue  *int64    `json:"min_order_value"`
		AppliesTo      *string   `json:"applies_to"`
		ProductIDs     *[]string `json:"product_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "the body is not a valid discount update")
		return
	}
	req := &discountv1.UpdateDiscountRequest{DiscountId: c.Param("discountId"), Name: in.Name, Description: in.Description, EndDate: in.EndDate,
		IsActive: in.IsActive, MaxUses: in.MaxUses, MaxUsesPerUser: in.MaxUsesPerUser, MinOrderValue: in.MinOrderValue, AppliesTo: in.AppliesTo}
	if in.ProductIDs != nil { // present (even empty) means "replace the list"
		req.ProductIds, req.ReplaceProductIds = *in.ProductIDs, true
	}
	r, err := h.client.UpdateDiscount(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Discount updated", discountOf(r))
}

func (h *Discount) list(c *gin.Context) {
	page, limit := intQuery(c, "page"), intQuery(c, "limit")
	r, err := h.client.ListShopDiscounts(c.Request.Context(), &discountv1.ListShopDiscountsRequest{Page: int32(page), Limit: int32(limit)})
	if err != nil {
		fail(c, err)
		return
	}
	items := make([]discountJSON, 0, len(r.GetDiscounts()))
	for _, d := range r.GetDiscounts() {
		items = append(items, discountOf(d))
	}
	ok(c, stdhttp.StatusOK, "Shop discounts", gin.H{"discounts": items, "total": r.GetTotal(), "page": max(page, 1), "limit": limit})
}

func (h *Discount) productsForCode(c *gin.Context) {
	code, shopID := c.Query("code"), c.Query("shopId")
	if code == "" || shopID == "" {
		badRequest(c, "code and shopId are required")
		return
	}
	page, limit := intQuery(c, "page"), intQuery(c, "limit")
	r, err := h.client.ListProductsForCode(c.Request.Context(), &discountv1.ListProductsForCodeRequest{Code: code, ShopId: shopID, Page: int32(page), Limit: int32(limit)})
	if err != nil {
		fail(c, err)
		return
	}
	type product struct {
		ID    string `json:"_id"`
		Name  string `json:"product_name"`
		Price int64  `json:"product_price"`
		Thumb string `json:"product_thumb"`
	}
	items := make([]product, 0, len(r.GetProducts()))
	for _, p := range r.GetProducts() {
		items = append(items, product{ID: p.GetId(), Name: p.GetName(), Price: p.GetPrice(), Thumb: p.GetThumb()})
	}
	ok(c, stdhttp.StatusOK, "Products for the discount code", gin.H{"products": items, "total": r.GetTotal(), "page": max(page, 1), "limit": limit})
}

func (h *Discount) amount(c *gin.Context) {
	var in struct {
		Code     string `json:"code" binding:"required"`
		ShopID   string `json:"shopId" binding:"required"`
		Products []struct {
			ProductID string `json:"productId" binding:"required"`
			Price     int64  `json:"price"`
			Quantity  int64  `json:"quantity" binding:"required"`
		} `json:"products" binding:"required,min=1,dive"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "code, shopId and products with productId and quantity are required")
		return
	}
	req := &discountv1.CalculateDiscountRequest{Code: in.Code, ShopId: in.ShopID}
	for _, p := range in.Products {
		req.Lines = append(req.Lines, &discountv1.OrderLine{ProductId: p.ProductID, Price: p.Price, Quantity: p.Quantity})
	}
	r, err := h.client.CalculateDiscount(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Discount amount", gin.H{"totalOrder": r.GetSubtotal(), "discount": r.GetDiscount(), "totalPrice": r.GetTotal()})
}
