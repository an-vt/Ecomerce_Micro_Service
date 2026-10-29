package http

import (
	stdhttp "net/http"

	"github.com/gin-gonic/gin"

	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
)

// Inventory holds the stock routes.
type Inventory struct {
	client inventoryv1.InventoryServiceClient
}

type stockJSON struct {
	ProductID string `json:"inven_productId"`
	ShopID    string `json:"inven_shopId"`
	Location  string `json:"inven_location"`
	Stock     int64  `json:"inven_stock"`
	Reserved  int64  `json:"inven_reserved"`
}

func stockOf(s *inventoryv1.Stock) stockJSON {
	return stockJSON{ProductID: s.GetProductId(), ShopID: s.GetShopId(), Location: s.GetLocation(), Stock: s.GetStock(), Reserved: s.GetReserved()}
}

func (h *Inventory) addStock(c *gin.Context) {
	var in struct {
		ProductID string `json:"productId" binding:"required"`
		Stock     int64  `json:"stock" binding:"required"`
		Location  string `json:"location"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		badRequest(c, "productId and stock are required")
		return
	}
	s, err := h.client.AddStock(c.Request.Context(), &inventoryv1.AddStockRequest{ProductId: in.ProductID, Stock: in.Stock, Location: in.Location})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Stock added", stockOf(s))
}

func (h *Inventory) get(c *gin.Context) {
	s, err := h.client.GetStock(c.Request.Context(), &inventoryv1.GetStockRequest{ProductId: c.Param("productId")})
	if err != nil {
		fail(c, err)
		return
	}
	ok(c, stdhttp.StatusOK, "Stock", stockOf(s))
}
