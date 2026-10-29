package domain

import (
	"context"

	"github.com/an-vt/ecom/platform-go/money"
)

// ProductInfo is what the cart needs to know about a product.
type ProductInfo struct {
	ID          string
	ShopID      string
	Name        string
	Price       money.Amount
	IsPublished bool
}

// Catalog answers questions about products (gRPC to catalog-service, behind a circuit breaker).
type Catalog interface {
	// Products returns the known products among ids; unknown ids are absent.
	Products(ctx context.Context, ids []string) (map[string]ProductInfo, error)
}

// StockLine is a product and a quantity to check.
type StockLine struct {
	ProductID string
	Quantity  int64
}

// Stock answers availability questions (gRPC to inventory-service, behind a circuit breaker).
type Stock interface {
	// Available reports whether every line can be served.
	Available(ctx context.Context, lines []StockLine) (bool, error)
}
