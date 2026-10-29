// Package discountclient adapts discount-service's gRPC API to the order Discounts port.
package discountclient

import (
	"context"

	"github.com/an-vt/ecom/order-service/internal/domain"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
)

// Client implements domain.Discounts. The caller's identity travels in ctx (grpcx forwards the principal), so
// discount-service counts the buyer's own earlier uses of a code.
type Client struct {
	c discountv1.DiscountServiceClient
}

// New wraps a generated client.
func New(c discountv1.DiscountServiceClient) *Client { return &Client{c: c} }

// Preview implements domain.Discounts.
func (c *Client) Preview(ctx context.Context, shopID, code string, lines []domain.PreviewLine) (domain.DiscountPreview, error) {
	req := &discountv1.CalculateDiscountRequest{Code: code, ShopId: shopID}
	for _, l := range lines {
		req.Lines = append(req.Lines, &discountv1.OrderLine{ProductId: l.ProductID, Price: l.Price, Quantity: l.Quantity})
	}
	r, err := c.c.CalculateDiscount(ctx, req)
	if err != nil {
		return domain.DiscountPreview{}, err
	}
	return domain.DiscountPreview{Discount: r.GetDiscount(), Subtotal: r.GetSubtotal(), Total: r.GetTotal()}, nil
}
