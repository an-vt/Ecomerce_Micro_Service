package domain

import (
	"context"

	"github.com/an-vt/ecom/platform-go/money"
)

// ProductSummary is what the catalog tells us about a product.
type ProductSummary struct {
	ID          string
	ShopID      string
	Name        string
	Price       money.Amount
	Thumb       string
	IsPublished bool
}

// Catalog is the part of catalog-service this context uses (reached through a circuit-breaker-protected client).
type Catalog interface {
	// ProductIDsByShop lists the ids of a shop's products.
	ProductIDsByShop(ctx context.Context, shopID string) ([]string, error)
	// Products loads products by id; unknown ids are absent.
	Products(ctx context.Context, ids []string) (map[string]ProductSummary, error)
}
