package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	"github.com/an-vt/ecom/platform-go/errs"
	orderv1 "github.com/an-vt/ecom/proto/gen/go/order/v1"
)

// Order holds the checkout and order routes of buyers. The buyer is always the token's account; prices are never
// accepted from the client (order-service re-prices from the catalog).
type Order struct{ client orderv1.OrderServiceClient }

type itemInputJSON struct {
	ProductID string `json:"productId" binding:"required"`
	Quantity  int64  `json:"quantity" binding:"required"`
}

type shopOrderInputJSON struct {
	ShopID        string `json:"shopId" binding:"required"`
	ShopDiscounts []struct {
		CodeID string `json:"codeId" binding:"required"`
	} `json:"shop_discounts"`
	ItemProducts []itemInputJSON `json:"item_products" binding:"required,min=1,dive"`
}

type checkoutBody struct {
	ShopOrderIDs []shopOrderInputJSON `json:"shop_order_ids" binding:"required,min=1,dive"`
}

// shopOrdersOf maps the body; at most one discount code per shop (F-11).
func shopOrdersOf(in []shopOrderInputJSON) ([]*orderv1.ShopOrderInput, error) {
	out := make([]*orderv1.ShopOrderInput, len(in))
	for i, so := range in {
		if len(so.ShopDiscounts) > 1 {
			return nil, errs.Validation("at most one discount code per shop")
		}
		m := &orderv1.ShopOrderInput{ShopId: so.ShopID}
		if len(so.ShopDiscounts) == 1 {
			m.DiscountCode = so.ShopDiscounts[0].CodeID
		}
		for _, it := range so.ItemProducts {
			m.Items = append(m.Items, &orderv1.ItemInput{ProductId: it.ProductID, Quantity: it.Quantity})
		}
		out[i] = m
	}
	return out, nil
}

func (h *Order) review(c *gin.Context) {
	var in checkoutBody
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "shop_order_ids with shopId and item_products (productId, quantity) are required")
		return
	}
	orders, err := shopOrdersOf(in.ShopOrderIDs)
	if err != nil {
		fail(c, err)
		return
	}
	r, err := h.client.ReviewCheckout(c.Request.Context(), &orderv1.ReviewCheckoutRequest{ShopOrders: orders})
	if err != nil {
		fail(c, err)
		return
	}
	type item struct {
		ProductID string `json:"productId"`
		Name      string `json:"name"`
		Price     int64  `json:"price"`
		Quantity  int64  `json:"quantity"`
	}
	type discount struct {
		CodeID   string `json:"codeId"`
		Discount int64  `json:"discount"`
	}
	type shopOut struct {
		ShopID             string     `json:"shopId"`
		ShopDiscounts      []discount `json:"shop_discounts"`
		PriceRaw           int64      `json:"priceRaw"`
		PriceApplyDiscount int64      `json:"priceApplyDiscount"`
		ItemProducts       []item     `json:"item_products"`
	}
	shops := make([]shopOut, 0, len(r.GetShops()))
	for _, s := range r.GetShops() {
		so := shopOut{ShopID: s.GetShopId(), ShopDiscounts: []discount{}, PriceRaw: s.GetSubtotal(), PriceApplyDiscount: s.GetTotal(), ItemProducts: []item{}}
		if s.GetDiscountCode() != "" {
			so.ShopDiscounts = append(so.ShopDiscounts, discount{CodeID: s.GetDiscountCode(), Discount: s.GetDiscount()})
		}
		for _, it := range s.GetItems() {
			so.ItemProducts = append(so.ItemProducts, item{ProductID: it.GetProductId(), Name: it.GetName(), Price: it.GetPrice(), Quantity: it.GetQuantity()})
		}
		shops = append(shops, so)
	}
	ok(c, stdhttp.StatusOK, "Checkout review", gin.H{
		"shop_order_ids_new": shops,
		"checkout_order":     gin.H{"totalPrice": r.GetTotalPrice(), "feeShip": r.GetFeeShip(), "totalDiscount": r.GetTotalDiscount(), "totalCheckout": r.GetTotalCheckout()},
	})
}

func (h *Order) place(c *gin.Context) {
	var in struct {
		checkoutBody
		Shipping struct {
			Name    string `json:"name"`
			Phone   string `json:"phone"`
			Address string `json:"address" binding:"required"`
			City    string `json:"city"`
		} `json:"shipping" binding:"required"`
		Payment struct {
			Method string `json:"method" binding:"required"`
			Token  string `json:"token" binding:"required"`
		} `json:"payment" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "shop_order_ids, shipping.address and payment (method, token) are required")
		return
	}
	orders, err := shopOrdersOf(in.ShopOrderIDs)
	if err != nil {
		fail(c, err)
		return
	}
	r, err := h.client.PlaceOrder(c.Request.Context(), &orderv1.PlaceOrderRequest{ShopOrders: orders,
		Shipping:      &orderv1.Shipping{Name: in.Shipping.Name, Phone: in.Shipping.Phone, Address: in.Shipping.Address, City: in.Shipping.City},
		PaymentMethod: in.Payment.Method, PaymentToken: in.Payment.Token, IdempotencyKey: c.GetHeader("Idempotency-Key")})
	if err != nil {
		fail(c, err)
		return
	}
	// 202: the order is accepted; stock, discounts and payment happen in the background. The client polls GET /orders/:id.
	ok(c, stdhttp.StatusAccepted, "Order accepted", gin.H{"orderId": r.GetOrderId(), "status": r.GetStatus()})
}

type orderJSON struct {
	ID             string  `json:"_id"`
	UserID         string  `json:"order_userId"`
	Status         string  `json:"order_status"`
	Checkout       gin.H   `json:"order_checkout"`
	Shipping       gin.H   `json:"order_shipping"`
	Payment        gin.H   `json:"order_payment"`
	Products       []gin.H `json:"order_products"`
	Discounts      []gin.H `json:"order_discounts"`
	CancelReason   string  `json:"order_cancelReason,omitempty"`
	TrackingNumber string  `json:"order_trackingNumber"`
	CreatedAt      string  `json:"createdAt"`
	UpdatedAt      string  `json:"updatedAt"`
}

func orderOf(o *orderv1.Order) orderJSON {
	products := make([]gin.H, 0, len(o.GetItems()))
	for _, it := range o.GetItems() {
		products = append(products, gin.H{"productId": it.GetProductId(), "shopId": it.GetShopId(), "name": it.GetName(), "price": it.GetPrice(), "quantity": it.GetQuantity()})
	}
	discounts := make([]gin.H, 0, len(o.GetDiscounts()))
	for _, d := range o.GetDiscounts() {
		discounts = append(discounts, gin.H{"shopId": d.GetShopId(), "code": d.GetCode()})
	}
	sh := o.GetShipping()
	return orderJSON{ID: o.GetId(), UserID: o.GetUserId(), Status: o.GetStatus(),
		Checkout: gin.H{"totalPrice": o.GetTotalPrice(), "totalDiscount": o.GetTotalDiscount(), "feeShip": o.GetFeeShip(), "totalCheckout": o.GetTotalCheckout()},
		Shipping: gin.H{"name": sh.GetName(), "phone": sh.GetPhone(), "address": sh.GetAddress(), "city": sh.GetCity()},
		Payment:  gin.H{"method": o.GetPaymentMethod()}, Products: products, Discounts: discounts, CancelReason: o.GetCancelReason(),
		TrackingNumber: o.GetTrackingNumber(), CreatedAt: o.GetCreatedAt(), UpdatedAt: o.GetUpdatedAt()}
}

func (h *Order) get(c *gin.Context) {
	r, err := h.client.GetOrder(c.Request.Context(), &orderv1.GetOrderRequest{OrderId: c.Param("orderId")})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Order", orderOf(r))
}

func (h *Order) list(c *gin.Context) {
	page, limit := intQuery(c, "page"), intQuery(c, "limit")
	r, err := h.client.ListOrders(c.Request.Context(), &orderv1.ListOrdersRequest{Page: int32(page), Limit: int32(limit)})
	if err != nil {
		fail(c, err)
		return
	}
	orders := make([]orderJSON, 0, len(r.GetOrders()))
	for _, o := range r.GetOrders() {
		orders = append(orders, orderOf(o))
	}
	ok(c, stdhttp.StatusOK, "Orders", gin.H{"orders": orders, "total": r.GetTotal(), "page": max(page, 1), "limit": limit})
}
