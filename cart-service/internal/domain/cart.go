// Package domain holds the cart bounded context. A cart belongs to one buyer and holds price snapshots, so the
// order review can detect that a price changed since the line was added. It knows nothing about MongoDB or gRPC.
package domain

import (
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
)

// Error codes (docs/05 §2.1).
const (
	CodeProductNotAvailable = "PRODUCT_NOT_AVAILABLE"
	CodeInsufficientStock   = "INSUFFICIENT_STOCK"
	CodeCartConflict        = "CART_CONFLICT"
	CodeCartChanged         = "CART_CHANGED"
	CodeCartTooBig          = "CART_TOO_BIG"
	CodeInvalidQuantity     = "INVALID_QUANTITY"
	CodeShopMismatch        = "SHOP_MISMATCH"
)

// Limits.
const (
	MaxLines        = 100
	MaxLineQuantity = 1000
)

// State of a cart. Only active carts are used today; the others keep the monolith's vocabulary.
type State string

// Cart states.
const (
	StateActive    State = "active"
	StateCompleted State = "completed"
)

// Item is one line: a product bought from one shop, with the name and price seen when it was added.
type Item struct {
	ProductID string
	ShopID    string
	Name      string
	Price     money.Amount
	Quantity  int64
}

// Cart is the buyer's list of lines.
type Cart struct {
	id        string
	userID    string
	state     State
	items     []Item
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

// CartSnapshot is the persisted form of a Cart.
type CartSnapshot struct {
	ID, UserID           string
	State                State
	Items                []Item
	Version              int64
	CreatedAt, UpdatedAt time.Time
}

// NewCart creates an empty cart for a buyer.
func NewCart(id, userID string, now time.Time) (*Cart, error) {
	if userID == "" {
		return nil, errs.Validation("user id is required")
	}
	return &Cart{id: id, userID: userID, state: StateActive, createdAt: now, updatedAt: now}, nil
}

// RestoreCart rebuilds a Cart from storage.
func RestoreCart(s CartSnapshot) *Cart {
	return &Cart{id: s.ID, userID: s.UserID, state: s.State, items: append([]Item(nil), s.Items...), version: s.Version,
		createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}
}

// Snapshot returns the persisted state.
func (c *Cart) Snapshot() CartSnapshot {
	return CartSnapshot{ID: c.id, UserID: c.userID, State: c.state, Items: append([]Item(nil), c.items...), Version: c.version,
		CreatedAt: c.createdAt, UpdatedAt: c.updatedAt}
}

func (c *Cart) ID() string     { return c.id }
func (c *Cart) UserID() string { return c.userID }
func (c *Cart) Version() int64 { return c.version }
func (c *Cart) Items() []Item  { return append([]Item(nil), c.items...) }
func (c *Cart) Lines() int     { return len(c.items) }
func (c *Cart) IsEmpty() bool  { return len(c.items) == 0 }

// QuantityOf returns how many units of a product the cart holds.
func (c *Cart) QuantityOf(productID string) int64 {
	if i := c.index(productID); i >= 0 {
		return c.items[i].Quantity
	}
	return 0
}

// Total is the sum of price × quantity over all lines (snapshot prices).
func (c *Cart) Total() money.Amount {
	var t money.Amount
	for _, it := range c.items {
		line, _ := it.Price.Mul(it.Quantity)
		t += line
	}
	return t
}

func (c *Cart) index(productID string) int {
	for i, it := range c.items {
		if it.ProductID == productID {
			return i
		}
	}
	return -1
}

func checkQuantity(q int64) error {
	if q <= 0 || q > MaxLineQuantity {
		return errs.InvalidArgument(CodeInvalidQuantity, "quantity must be between 1 and 1000")
	}
	return nil
}

// Add puts q more units of a product in the cart. The line's name and price are refreshed to the given snapshot:
// the buyer pays what the product costs now, not what it cost when the first unit was added.
func (c *Cart) Add(it Item, now time.Time) error {
	if it.ProductID == "" || it.ShopID == "" {
		return errs.Validation("product id and shop id are required")
	}
	if err := checkQuantity(it.Quantity); err != nil {
		return err
	}
	if it.Price < 0 {
		return errs.Validation("price must not be negative")
	}
	if i := c.index(it.ProductID); i >= 0 {
		total := c.items[i].Quantity + it.Quantity
		if total > MaxLineQuantity {
			return errs.InvalidArgument(CodeInvalidQuantity, "a line holds at most 1000 units")
		}
		it.Quantity = total
		c.items[i] = it
	} else {
		if len(c.items) >= MaxLines {
			return errs.FailedPrecondition(CodeCartTooBig, "a cart holds at most 100 different products")
		}
		c.items = append(c.items, it)
	}
	c.updatedAt = now
	return nil
}

// SetQuantity sets a line to an absolute quantity (0 removes it). When expectedOld is above zero it must equal the
// current quantity: the client's view of the cart is stale otherwise (optimistic check, replaces the monolith's
// "apply the difference" arithmetic, which double-applied on retries).
func (c *Cart) SetQuantity(it Item, expectedOld int64, now time.Time) error {
	if it.Quantity < 0 || it.Quantity > MaxLineQuantity {
		return errs.InvalidArgument(CodeInvalidQuantity, "quantity must be between 0 and 1000")
	}
	cur := c.QuantityOf(it.ProductID)
	if expectedOld > 0 && cur != expectedOld {
		return errs.Aborted(CodeCartChanged, "the cart changed since you loaded it; reload and try again")
	}
	if it.Quantity == 0 {
		c.Remove(it.ProductID, now)
		return nil
	}
	if cur == 0 {
		return c.Add(it, now)
	}
	i := c.index(it.ProductID)
	c.items[i] = it
	c.updatedAt = now
	return nil
}

// Remove drops a line; removing an absent line is not an error.
func (c *Cart) Remove(productID string, now time.Time) {
	if i := c.index(productID); i >= 0 {
		c.items = append(c.items[:i], c.items[i+1:]...)
		c.updatedAt = now
	}
}

// Consume takes bought units out of the cart: a line is reduced by the ordered quantity and dropped at zero.
func (c *Cart) Consume(productID string, q int64, now time.Time) {
	i := c.index(productID)
	if i < 0 || q <= 0 {
		return
	}
	if c.items[i].Quantity <= q {
		c.Remove(productID, now)
		return
	}
	c.items[i].Quantity -= q
	c.updatedAt = now
}

// Bump records that the stored document was written (the repository increments the version on every save).
func (c *Cart) Bump() { c.version++ }
