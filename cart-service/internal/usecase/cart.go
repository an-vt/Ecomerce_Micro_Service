package usecase

import (
	"context"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// AddToCart adds units of a product to the caller's cart (monolith CartService.addToCart).
type AddToCart struct{ d *Deps }

// Execute checks that the product can be bought and that the cart's total for it (existing + new) is in stock, then
// adds it with the current name and price.
func (u AddToCart) Execute(ctx context.Context, productID string, quantity int64) (CartView, error) {
	me, err := buyer(ctx)
	if err != nil {
		return CartView{}, err
	}
	if productID == "" {
		return CartView{}, errs.Validation("product id is required")
	}
	if quantity <= 0 {
		return CartView{}, errs.InvalidArgument(domain.CodeInvalidQuantity, "quantity must be greater than zero")
	}
	found, err := u.d.products(ctx, []string{productID})
	if err != nil {
		return CartView{}, err
	}
	p := found[productID]

	c, err := u.d.mutate(ctx, me.ID, func(c *domain.Cart) error {
		total := c.QuantityOf(productID) + quantity
		if err := u.d.requireStock(ctx, []domain.StockLine{{ProductID: productID, Quantity: total}}); err != nil {
			return err
		}
		return c.Add(infoToItem(p, quantity), u.d.Clock.Now())
	})
	if err != nil {
		return CartView{}, err
	}
	return viewOf(c), nil
}

// UpdateLine is one line of an update.
type UpdateLine struct {
	ProductID   string
	ShopID      string
	Quantity    int64 // new absolute quantity; 0 removes
	OldQuantity int64 // the client's view of the current quantity; checked when > 0
}

// UpdateCart sets several lines at once (monolith addToCartV2, whose quantity-0 branch was an empty stub).
type UpdateCart struct{ d *Deps }

// Execute validates every line first (product exists, belongs to the stated shop, is in stock) and applies them
// together or not at all.
func (u UpdateCart) Execute(ctx context.Context, lines []UpdateLine) (CartView, error) {
	me, err := buyer(ctx)
	if err != nil {
		return CartView{}, err
	}
	ids, stock, err := checkLines(lines)
	if err != nil {
		return CartView{}, err
	}
	found, err := u.verify(ctx, lines, ids, stock)
	if err != nil {
		return CartView{}, err
	}
	c, err := u.d.mutate(ctx, me.ID, func(c *domain.Cart) error {
		now := u.d.Clock.Now()
		for _, l := range lines {
			item := domain.Item{ProductID: l.ProductID, Quantity: l.Quantity}
			if p, ok := found[l.ProductID]; ok {
				item = infoToItem(p, l.Quantity)
			}
			if err := c.SetQuantity(item, l.OldQuantity, now); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return CartView{}, err
	}
	return viewOf(c), nil
}

// checkLines validates the shape of the request and returns the products and stock lines that need a remote check
// (lines with quantity 0 are removals and need none).
func checkLines(lines []UpdateLine) (ids []string, stock []domain.StockLine, err error) {
	if len(lines) == 0 {
		return nil, nil, errs.Validation("at least one line is required")
	}
	seen := map[string]bool{}
	for _, l := range lines {
		switch {
		case l.ProductID == "":
			return nil, nil, errs.Validation("every line needs a product id")
		case seen[l.ProductID]:
			return nil, nil, errs.Validation("product " + l.ProductID + " appears twice")
		}
		seen[l.ProductID] = true
		if l.Quantity > 0 {
			ids = append(ids, l.ProductID)
			stock = append(stock, domain.StockLine{ProductID: l.ProductID, Quantity: l.Quantity})
		}
	}
	return ids, stock, nil
}

// verify asks the catalog and inventory about the lines that add or change units.
func (u UpdateCart) verify(ctx context.Context, lines []UpdateLine, ids []string, stock []domain.StockLine) (map[string]domain.ProductInfo, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	found, err := u.d.products(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, l := range lines {
		if l.Quantity > 0 && l.ShopID != "" && found[l.ProductID].ShopID != l.ShopID {
			return nil, errs.InvalidArgument(domain.CodeShopMismatch, "product "+l.ProductID+" does not belong to shop "+l.ShopID)
		}
	}
	if err := u.d.requireStock(ctx, stock); err != nil {
		return nil, err
	}
	return found, nil
}

// RemoveItem drops a line (monolith deleteUserCart).
type RemoveItem struct{ d *Deps }

// Execute removes the product; removing one that is not there is fine.
func (u RemoveItem) Execute(ctx context.Context, productID string) (CartView, error) {
	me, err := buyer(ctx)
	if err != nil {
		return CartView{}, err
	}
	if productID == "" {
		return CartView{}, errs.Validation("product id is required")
	}
	c, err := u.d.mutate(ctx, me.ID, func(c *domain.Cart) error {
		c.Remove(productID, u.d.Clock.Now())
		return nil
	})
	if err != nil {
		return CartView{}, err
	}
	return viewOf(c), nil
}

// GetCart returns the caller's cart, empty when there is none yet.
type GetCart struct{ d *Deps }

// Execute loads the cart.
func (u GetCart) Execute(ctx context.Context) (CartView, error) {
	me, err := buyer(ctx)
	if err != nil {
		return CartView{}, err
	}
	c, err := u.d.Carts.FindByUser(ctx, me.ID)
	if err != nil {
		return CartView{}, err
	}
	if c == nil {
		return emptyView(me.ID), nil
	}
	return viewOf(c), nil
}

// OrderedLine is a product and a quantity that were bought.
type OrderedLine struct {
	ProductID string
	Quantity  int64
}

// ClearOrdered removes bought units from a buyer's cart after order.order.confirmed (F-23).
type ClearOrdered struct{ d *Deps }

// Execute takes the ordered quantities out of the cart. It is not idempotent by itself (a repeat would remove again),
// so the Kafka handler runs it behind the inbox, which delivers each event exactly once.
func (u ClearOrdered) Execute(ctx context.Context, userID string, lines []OrderedLine) error {
	if userID == "" {
		return errs.Validation("user id is required")
	}
	c, err := u.d.Carts.FindByUser(ctx, userID)
	if err != nil || c == nil {
		return err
	}
	_, err = u.d.mutate(ctx, userID, func(c *domain.Cart) error {
		now := u.d.Clock.Now()
		for _, l := range lines {
			c.Consume(l.ProductID, l.Quantity, now)
		}
		return nil
	})
	return err
}
