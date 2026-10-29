// Package inventoryclient adapts inventory-service's gRPC API to the cart's Stock port.
package inventoryclient

import (
	"context"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
)

// Client implements domain.Stock (see catalogclient for how resilience is applied).
type Client struct {
	c inventoryv1.InventoryServiceClient
}

// New wraps a generated client.
func New(c inventoryv1.InventoryServiceClient) *Client { return &Client{c: c} }

// Available implements domain.Stock.
func (c *Client) Available(ctx context.Context, lines []domain.StockLine) (bool, error) {
	req := &inventoryv1.CheckStockRequest{}
	for _, l := range lines {
		req.Lines = append(req.Lines, &inventoryv1.StockLine{ProductId: l.ProductID, Quantity: l.Quantity})
	}
	r, err := c.c.CheckStock(ctx, req)
	if err != nil {
		return false, err
	}
	return r.GetOk(), nil
}
