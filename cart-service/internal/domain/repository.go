package domain

import "context"

// CartRepository stores carts. All methods take the transaction from ctx.
type CartRepository interface {
	// FindByUser loads the buyer's cart; (nil, nil) when they have none yet.
	FindByUser(ctx context.Context, userID string) (*Cart, error)
	// Save inserts a new cart (version 0) or replaces an existing one only if its stored version equals the cart's
	// version (optimistic concurrency); it returns CodeCartConflict otherwise. On success the cart's version is
	// incremented.
	Save(ctx context.Context, c *Cart) error
}
