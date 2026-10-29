// Package usecase holds the application logic of cart-service. It depends on the domain and on its ports; concrete
// adapters (MongoDB, gRPC clients) are wired in internal/app.
package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/money"
	"github.com/an-vt/ecom/platform-go/principal"
)

// Deps is everything the use cases need.
type Deps struct {
	Carts   domain.CartRepository
	Catalog domain.Catalog
	Stock   domain.Stock
	Clock   clock.Clock
	IDs     idgen.Generator
}

// maxAttempts bounds the optimistic-concurrency retries of one request.
const maxAttempts = 4

// buyer returns the authenticated buyer. The gateway enforces this already; carts are never addressed by a user id
// taken from a request (the monolith trusted the body, which let anyone edit anyone's cart).
func buyer(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	if !p.IsUser() {
		return principal.Principal{}, errs.PermissionDenied("FORBIDDEN", "only buyers have a cart")
	}
	return p, nil
}

// mutate loads the buyer's cart (or starts an empty one), applies fn and saves it. A concurrent change of the same cart
// makes the save fail with a conflict; the whole load–apply–save is then repeated on the fresh cart.
func (d *Deps) mutate(ctx context.Context, userID string, fn func(c *domain.Cart) error) (*domain.Cart, error) {
	var last error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		c, err := d.Carts.FindByUser(ctx, userID)
		if err != nil {
			return nil, err
		}
		if c == nil {
			if c, err = domain.NewCart(d.IDs.New(), userID, d.Clock.Now()); err != nil {
				return nil, err
			}
		}
		if err := fn(c); err != nil {
			return nil, err
		}
		err = d.Carts.Save(ctx, c)
		if err == nil {
			return c, nil
		}
		if errs.CodeOf(err) != domain.CodeCartConflict {
			return nil, err
		}
		last = err
	}
	return nil, errors.Join(errs.Aborted(domain.CodeCartConflict, "the cart is being changed elsewhere; try again"), last)
}

// ItemView is a cart line as exposed to callers.
type ItemView struct {
	ProductID, ShopID, Name string
	Price, Quantity         int64
}

// CartView is a cart as exposed to callers.
type CartView struct {
	ID, UserID, State string
	Items             []ItemView
	Lines             int64
	Total             int64
	UpdatedAt         time.Time
}

func viewOf(c *domain.Cart) CartView {
	s := c.Snapshot()
	items := make([]ItemView, len(s.Items))
	for i, it := range s.Items {
		items[i] = ItemView{ProductID: it.ProductID, ShopID: it.ShopID, Name: it.Name, Price: int64(it.Price), Quantity: it.Quantity}
	}
	return CartView{ID: s.ID, UserID: s.UserID, State: string(s.State), Items: items, Lines: int64(len(items)), Total: int64(c.Total()), UpdatedAt: s.UpdatedAt}
}

func emptyView(userID string) CartView {
	return CartView{UserID: userID, State: string(domain.StateActive), Items: []ItemView{}}
}

func infoToItem(p domain.ProductInfo, q int64) domain.Item {
	return domain.Item{ProductID: p.ID, ShopID: p.ShopID, Name: p.Name, Price: money.Amount(p.Price), Quantity: q}
}

// product returns a product that exists and can be bought, or PRODUCT_NOT_AVAILABLE.
func (d *Deps) products(ctx context.Context, ids []string) (map[string]domain.ProductInfo, error) {
	found, err := d.Catalog.Products(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		p, ok := found[id]
		if !ok || !p.IsPublished {
			return nil, errs.NotFound(domain.CodeProductNotAvailable, "product "+id+" is not available")
		}
	}
	return found, nil
}

// requireStock fails with INSUFFICIENT_STOCK unless every line can be served.
func (d *Deps) requireStock(ctx context.Context, lines []domain.StockLine) error {
	if len(lines) == 0 {
		return nil
	}
	ok, err := d.Stock.Available(ctx, lines)
	if err != nil {
		return err
	}
	if !ok {
		return errs.FailedPrecondition(domain.CodeInsufficientStock, "not enough stock for the requested quantity")
	}
	return nil
}
