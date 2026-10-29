package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	cartv1 "github.com/an-vt/ecom/proto/gen/go/cart/v1"
)

// Cart holds the buyer's cart routes. The cart is always the caller's own: no user id is read from the request
// (the monolith took it from the body, so anyone could edit anyone's cart).
type Cart struct{ client cartv1.CartServiceClient }

type cartItemJSON struct {
	ProductID string `json:"productId"`
	ShopID    string `json:"shopId"`
	Name      string `json:"name"`
	Price     int64  `json:"price"`
	Quantity  int64  `json:"quantity"`
}

type cartJSON struct {
	ID           string         `json:"_id"`
	UserID       string         `json:"cart_userId"`
	State        string         `json:"cart_state"`
	Products     []cartItemJSON `json:"cart_products"`
	CountProduct int64          `json:"cart_count_product"`
	Total        int64          `json:"cart_total"`
	UpdatedAt    string         `json:"ModifiedOn"`
}

func cartOf(c *cartv1.Cart) cartJSON {
	items := make([]cartItemJSON, 0, len(c.GetItems()))
	for _, it := range c.GetItems() {
		items = append(items, cartItemJSON{ProductID: it.GetProductId(), ShopID: it.GetShopId(), Name: it.GetName(), Price: it.GetPrice(), Quantity: it.GetQuantity()})
	}
	return cartJSON{ID: c.GetId(), UserID: c.GetUserId(), State: c.GetState(), Products: items, CountProduct: c.GetCountProduct(), Total: c.GetTotal(), UpdatedAt: c.GetUpdatedAt()}
}

func (h *Cart) add(c *gin.Context) {
	var in struct {
		Product struct {
			ProductID string `json:"productId" binding:"required"`
			ShopID    string `json:"shopId"` // accepted for compatibility with the monolith's clients; the shop comes from the catalog
			Quantity  int64  `json:"quantity" binding:"required"`
		} `json:"product" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "product.productId and product.quantity are required")
		return
	}
	r, err := h.client.AddToCart(c.Request.Context(), &cartv1.AddToCartRequest{ProductId: in.Product.ProductID, Quantity: in.Product.Quantity})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Added to cart", cartOf(r))
}

func (h *Cart) update(c *gin.Context) {
	var in struct {
		ShopOrderIDs []struct {
			ShopID       string `json:"shopId"`
			ItemProducts []struct {
				ProductID   string `json:"productId" binding:"required"`
				ShopID      string `json:"shopId"`
				Quantity    int64  `json:"quantity"`
				OldQuantity int64  `json:"old_quantity"`
			} `json:"item_products" binding:"required,dive"`
		} `json:"shop_order_ids" binding:"required,dive"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "shop_order_ids with item_products are required")
		return
	}
	req := &cartv1.UpdateCartRequest{}
	for _, so := range in.ShopOrderIDs {
		for _, it := range so.ItemProducts {
			shop := it.ShopID
			if shop == "" {
				shop = so.ShopID
			}
			req.Lines = append(req.Lines, &cartv1.UpdateLine{ProductId: it.ProductID, ShopId: shop, Quantity: it.Quantity, OldQuantity: it.OldQuantity})
		}
	}
	r, err := h.client.UpdateCart(c.Request.Context(), req)
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Cart updated", cartOf(r))
}

func (h *Cart) remove(c *gin.Context) {
	var in struct {
		ProductID string `json:"productId" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "productId is required")
		return
	}
	r, err := h.client.RemoveItem(c.Request.Context(), &cartv1.RemoveItemRequest{ProductId: in.ProductID})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Removed from cart", cartOf(r))
}

func (h *Cart) get(c *gin.Context) {
	r, err := h.client.GetCart(c.Request.Context(), &cartv1.GetCartRequest{})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Cart", cartOf(r))
}
