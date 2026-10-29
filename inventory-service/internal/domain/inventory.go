package domain

import (
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// DefaultLocation is where stock is when the shop does not say (the monolith had a misspelled default).
const DefaultLocation = "unknown"

// Inventory is the stock of one product. Stock is what can still be bought; Reserved is held for orders in progress.
// Invariants: both are never negative, and no operation changes the sum except AddStock / SetStock / Commit.
type Inventory struct {
	id        string
	productID string
	shopID    string
	location  string
	stock     int64
	reserved  int64
	createdAt time.Time
	updatedAt time.Time
}

// InventorySnapshot is the persisted form of an Inventory.
type InventorySnapshot struct {
	ID, ProductID, ShopID, Location string
	Stock, Reserved                 int64
	CreatedAt, UpdatedAt            time.Time
}

// NewInventory creates the stock record of a product.
func NewInventory(id, productID, shopID string, stock int64, now time.Time) (*Inventory, error) {
	if strings.TrimSpace(productID) == "" || strings.TrimSpace(shopID) == "" {
		return nil, errs.Validation("product id and shop id are required")
	}
	if stock < 0 {
		return nil, errs.InvalidArgument(CodeInvalidQuantity, "stock must not be negative")
	}
	return &Inventory{id: id, productID: productID, shopID: shopID, location: DefaultLocation, stock: stock, createdAt: now, updatedAt: now}, nil
}

// RestoreInventory rebuilds an Inventory from storage.
func RestoreInventory(s InventorySnapshot) *Inventory {
	return &Inventory{id: s.ID, productID: s.ProductID, shopID: s.ShopID, location: s.Location, stock: s.Stock, reserved: s.Reserved,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}
}

// Snapshot returns the persisted state.
func (i *Inventory) Snapshot() InventorySnapshot {
	return InventorySnapshot{ID: i.id, ProductID: i.productID, ShopID: i.shopID, Location: i.location, Stock: i.stock, Reserved: i.reserved,
		CreatedAt: i.createdAt, UpdatedAt: i.updatedAt}
}

func (i *Inventory) ID() string        { return i.id }
func (i *Inventory) ProductID() string { return i.productID }
func (i *Inventory) ShopID() string    { return i.shopID }
func (i *Inventory) Location() string  { return i.location }
func (i *Inventory) Stock() int64      { return i.stock }
func (i *Inventory) Reserved() int64   { return i.reserved }

// OwnedBy reports whether shopID owns the product.
func (i *Inventory) OwnedBy(shopID string) bool { return shopID != "" && i.shopID == shopID }

func positive(q int64) error {
	if q <= 0 {
		return errs.InvalidArgument(CodeInvalidQuantity, "quantity must be greater than zero")
	}
	return nil
}

// AddStock puts more units on the shelf; a non-empty location moves the stock record there.
func (i *Inventory) AddStock(q int64, location string, now time.Time) error {
	if err := positive(q); err != nil {
		return err
	}
	i.stock += q
	if l := strings.TrimSpace(location); l != "" {
		i.location = l
	}
	i.updatedAt = now
	return nil
}

// SetStock sets the available units (the shop corrected the quantity of its product). Held units are untouched.
func (i *Inventory) SetStock(q int64, now time.Time) error {
	if q < 0 {
		return errs.InvalidArgument(CodeInvalidQuantity, "stock must not be negative")
	}
	i.stock, i.updatedAt = q, now
	return nil
}

// CanFulfil reports whether q units are available.
func (i *Inventory) CanFulfil(q int64) bool { return q > 0 && i.stock >= q }

// Reserve moves q units from available to reserved. It fails, changing nothing, when fewer are available.
func (i *Inventory) Reserve(q int64, now time.Time) error {
	if err := positive(q); err != nil {
		return err
	}
	if i.stock < q {
		return ErrInsufficientStock(i.productID, q, i.stock)
	}
	i.stock -= q
	i.reserved += q
	i.updatedAt = now
	return nil
}

// Release returns q reserved units to the shelf (compensation or expiry).
func (i *Inventory) Release(q int64, now time.Time) error {
	if err := positive(q); err != nil {
		return err
	}
	if i.reserved < q {
		return errs.FailedPrecondition(CodeInvalidState, "more units released than were reserved")
	}
	i.reserved -= q
	i.stock += q
	i.updatedAt = now
	return nil
}

// Commit turns q reserved units into a sale: they leave the warehouse.
func (i *Inventory) Commit(q int64, now time.Time) error {
	if err := positive(q); err != nil {
		return err
	}
	if i.reserved < q {
		return errs.FailedPrecondition(CodeInvalidState, "more units committed than were reserved")
	}
	i.reserved -= q
	i.updatedAt = now
	return nil
}
