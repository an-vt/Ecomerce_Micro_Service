package usecase

import (
	"context"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pagination"
)

// ListProductsForCode shows the products a code applies to. It is public (API key only): shoppers browse a code's
// products before logging in.
type ListProductsForCode struct{ d *Deps }

// Execute pages through the code's product list — the listed products for a "specific" code, the shop's whole catalogue
// for an "all" code — and returns the published ones with their current catalog data.
func (u ListProductsForCode) Execute(ctx context.Context, shopID, code string, page, limit int) (pagination.Result[domain.ProductSummary], error) {
	disc, err := u.d.Discounts.FindByCode(ctx, domain.CodeRef{ShopID: shopID, Code: domain.NormalizeCode(code)})
	if err != nil {
		return pagination.Result[domain.ProductSummary]{}, err
	}
	ids := disc.Snapshot().ProductIDs
	if disc.Snapshot().AppliesTo == domain.ScopeAll {
		if ids, err = u.d.Catalog.ProductIDsByShop(ctx, shopID); err != nil {
			return pagination.Result[domain.ProductSummary]{}, err
		}
	}
	pg := pagination.New(page, limit)
	total := int64(len(ids))
	from := min(pg.Offset(), len(ids))
	to := min(from+pg.Limit, len(ids))
	var out []domain.ProductSummary
	if to > from {
		found, err := u.d.Catalog.Products(ctx, ids[from:to])
		if err != nil {
			return pagination.Result[domain.ProductSummary]{}, err
		}
		for _, id := range ids[from:to] { // keep the list's order
			if p, ok := found[id]; ok && p.IsPublished && p.ShopID == shopID {
				out = append(out, p)
			}
		}
	}
	return pagination.NewResult(pg, out, total), nil
}
