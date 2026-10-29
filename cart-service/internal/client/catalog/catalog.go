// Package catalogclient adapts catalog-service's gRPC API to the cart's Catalog port.
package catalogclient

import (
	"context"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/money"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
)

// Client implements domain.Catalog. The connection comes from grpcx.Dial, so every call has a deadline, a circuit
// breaker (an open circuit surfaces as an UNAVAILABLE error) and retries for reads.
type Client struct {
	c catalogv1.CatalogServiceClient
}

// New wraps a generated client.
func New(c catalogv1.CatalogServiceClient) *Client { return &Client{c: c} }

// Products implements domain.Catalog.
func (c *Client) Products(ctx context.Context, ids []string) (map[string]domain.ProductInfo, error) {
	r, err := c.c.GetProductsByIds(ctx, &catalogv1.GetProductsByIdsRequest{Ids: ids})
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.ProductInfo, len(r.GetProducts()))
	for _, p := range r.GetProducts() {
		out[p.GetId()] = domain.ProductInfo{ID: p.GetId(), ShopID: p.GetShopId(), Name: p.GetName(), Price: money.Amount(p.GetPrice()), IsPublished: p.GetIsPublished()}
	}
	return out, nil
}
