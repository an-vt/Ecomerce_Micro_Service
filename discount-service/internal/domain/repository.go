package domain

import "context"

// All repositories take the transaction from ctx (platform-go/pgdb) and return domain errors.

// CodeRef identifies a discount by shop and (normalized) code.
type CodeRef struct{ ShopID, Code string }

// DiscountRepository stores discounts.
type DiscountRepository interface {
	// Insert stores a new discount; ErrCodeTaken when the shop already has the code.
	Insert(ctx context.Context, d *Discount) error
	// Save writes every mutable field of a discount loaded with a Lock method.
	Save(ctx context.Context, d *Discount) error
	// Find loads one discount without locking; ErrNotFound when absent.
	Find(ctx context.Context, id string) (*Discount, error)
	// FindByCode loads a discount without locking; ErrNotFound when absent.
	FindByCode(ctx context.Context, ref CodeRef) (*Discount, error)
	// LockByID loads a discount and locks it for the rest of the transaction (SELECT … FOR UPDATE); ErrNotFound when absent.
	LockByID(ctx context.Context, id string) (*Discount, error)
	// LockByCodes locks the discounts of refs in a fixed order (shop, code) so concurrent transactions cannot deadlock.
	// Unknown refs are absent from the result.
	LockByCodes(ctx context.Context, refs []CodeRef) (map[CodeRef]*Discount, error)
	// LockByIDs locks discounts by id, in id order. Unknown ids are absent from the result.
	LockByIDs(ctx context.Context, ids []string) (map[string]*Discount, error)
	// ListByShop returns a page of a shop's discounts, newest first, and how many it has.
	ListByShop(ctx context.Context, shopID string, offset, limit int) ([]*Discount, int64, error)
}

// UsageRepository stores redemptions.
type UsageRepository interface {
	// ForOrder lists the usages of an order, any status.
	ForOrder(ctx context.Context, orderID string) ([]*Usage, error)
	// CountApplied counts the APPLIED usages of a user for each discount.
	CountApplied(ctx context.Context, userID string, discountIDs []string) (map[string]int64, error)
	// Insert stores new usages; (discount, order) is unique.
	Insert(ctx context.Context, us []*Usage) error
	// Save writes the status of a usage.
	Save(ctx context.Context, u *Usage) error
}
