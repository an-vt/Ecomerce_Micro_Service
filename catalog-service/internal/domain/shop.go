package domain

// ShopProjection is catalog's local copy of the shop data it shows next to products, kept up to date from
// auth.shop.registered. It replaces the monolith's populate('product_shop') join across services.
type ShopProjection struct {
	ID    string
	Name  string
	Email string
}
