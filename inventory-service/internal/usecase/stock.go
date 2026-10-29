package usecase

import (
	"context"
	"sort"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Stock is a stock record as exposed to callers.
type Stock struct {
	ProductID, ShopID, Location string
	Stock, Reserved             int64
}

func stockOf(i *domain.Inventory) Stock {
	return Stock{ProductID: i.ProductID(), ShopID: i.ShopID(), Location: i.Location(), Stock: i.Stock(), Reserved: i.Reserved()}
}

// ProductCreated is what inventory needs from catalog.product.created.
type ProductCreated struct {
	ProductID, ShopID string
	Quantity          int64
}

// CreateInventory makes the stock record of a new product (monolith: written inline by the product service).
type CreateInventory struct{ d *Deps }

// Execute inserts the record unless it exists already: a late or repeated event never overwrites newer stock.
func (u CreateInventory) Execute(ctx context.Context, in ProductCreated) error {
	inv, err := domain.NewInventory(u.d.IDs.New(), in.ProductID, in.ShopID, in.Quantity, u.d.Clock.Now())
	if err != nil {
		return err
	}
	_, err = u.d.Inventories.Insert(ctx, inv)
	return err
}

// ProductUpdated is what inventory needs from catalog.product.updated.
type ProductUpdated struct {
	ProductID, ShopID string
	Quantity          *int64 // nil: the update did not touch stock
}

// SyncStock applies a quantity change made in the catalog (monolith updateInventory).
type SyncStock struct{ d *Deps }

// Execute sets the available stock. catalog.product.created and .updated are different topics, so the update can
// arrive first: the record is created on the spot in that case.
func (u SyncStock) Execute(ctx context.Context, in ProductUpdated) error {
	if in.Quantity == nil {
		return nil
	}
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		locked, err := u.d.Inventories.Lock(ctx, []string{in.ProductID})
		if err != nil {
			return err
		}
		inv, ok := locked[in.ProductID]
		if !ok {
			created, err := domain.NewInventory(u.d.IDs.New(), in.ProductID, in.ShopID, *in.Quantity, u.d.Clock.Now())
			if err != nil {
				return err
			}
			_, err = u.d.Inventories.Insert(ctx, created)
			return err
		}
		if err := inv.SetStock(*in.Quantity, u.d.Clock.Now()); err != nil {
			return err
		}
		return u.d.Inventories.Save(ctx, inv)
	})
}

// AddStockInput is a shop adding units to one of its products.
type AddStockInput struct {
	ProductID string
	Amount    int64
	Location  string
}

// AddStock lets a shop add units (monolith InventoryService.addStockToInventory, which crashed on a missing import).
type AddStock struct{ d *Deps }

// Execute checks ownership and adds the units under a row lock, so concurrent additions are all counted.
func (u AddStock) Execute(ctx context.Context, in AddStockInput) (Stock, error) {
	shop, err := shopCaller(ctx)
	if err != nil {
		return Stock{}, err
	}
	var out Stock
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		locked, err := u.d.Inventories.Lock(ctx, []string{in.ProductID})
		if err != nil {
			return err
		}
		inv, ok := locked[in.ProductID]
		if !ok {
			return domain.ErrInventoryNotFound()
		}
		if !inv.OwnedBy(shop.ID) {
			return domain.ErrNotOwner()
		}
		if err := inv.AddStock(in.Amount, in.Location, u.d.Clock.Now()); err != nil {
			return err
		}
		if err := u.d.Inventories.Save(ctx, inv); err != nil {
			return err
		}
		out = stockOf(inv)
		return nil
	})
	return out, err
}

// GetStock reads one stock record.
type GetStock struct{ d *Deps }

// Execute returns the record of a product.
func (u GetStock) Execute(ctx context.Context, productID string) (Stock, error) {
	inv, err := u.d.Inventories.Find(ctx, productID)
	if err != nil {
		return Stock{}, err
	}
	return stockOf(inv), nil
}

// Line is a product and a quantity.
type Line struct {
	ProductID string
	Quantity  int64
}

// Shortage is a line that cannot be served.
type Shortage struct {
	ProductID            string
	Requested, Available int64
}

// CheckStock tells whether every line is available (used by cart-service before accepting a line).
type CheckStock struct{ d *Deps }

// Execute checks all lines at once. Products without a stock record count as having none.
func (u CheckStock) Execute(ctx context.Context, lines []Line) (bool, []Shortage, error) {
	if len(lines) == 0 {
		return true, nil, nil
	}
	if len(lines) > 100 {
		return false, nil, errs.Validation("at most 100 lines per check")
	}
	ids := make([]string, len(lines))
	for i, l := range lines {
		if l.Quantity <= 0 {
			return false, nil, errs.InvalidArgument(domain.CodeInvalidQuantity, "quantities must be greater than zero")
		}
		ids[i] = l.ProductID
	}
	// need the totals per product: the same product may appear twice
	need := map[string]int64{}
	for _, l := range lines {
		need[l.ProductID] += l.Quantity
	}
	var shortages []Shortage
	for _, id := range sortedKeys(need) {
		var have int64
		if inv, err := u.d.Inventories.Find(ctx, id); err == nil {
			have = inv.Stock()
		} else if errs.CodeOf(err) != domain.CodeInventoryNotFound {
			return false, nil, err
		}
		if have < need[id] {
			shortages = append(shortages, Shortage{ProductID: id, Requested: need[id], Available: have})
		}
	}
	return len(shortages) == 0, shortages, nil
}

func sortedKeys(m map[string]int64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
