package domain

import "context"

// Product is what the catalog tells us about a product.
type Product struct {
	ID          string
	ShopID      string
	Name        string
	Price       int64
	IsPublished bool
}

// Catalog is the part of catalog-service the review uses.
type Catalog interface {
	// Products loads products by id; unknown ids are absent from the result.
	Products(ctx context.Context, ids []string) (map[string]Product, error)
}

// DiscountPreview is what a code would take off a shop's lines.
type DiscountPreview struct {
	Discount int64
	Subtotal int64
	Total    int64
}

// PreviewLine is a priced line of a shop.
type PreviewLine struct {
	ProductID string
	Price     int64
	Quantity  int64
}

// Discounts is the part of discount-service the review uses. The buyer's identity travels in the context, so the
// buyer's own earlier uses are counted.
type Discounts interface {
	Preview(ctx context.Context, shopID, code string, lines []PreviewLine) (DiscountPreview, error)
}
