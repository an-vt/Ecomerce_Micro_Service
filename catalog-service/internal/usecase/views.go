package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
)

// ProductView is a product as exposed to callers, with the shop's display name joined from the local projection.
type ProductView struct {
	ID             string
	ShopID         string
	ShopName       string
	Name           string
	Description    string
	Slug           string
	Price          int64
	Quantity       int64
	Type           string
	Thumb          string
	Attributes     map[string]any
	RatingsAverage float64
	IsDraft        bool
	IsPublished    bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Page is one page of results.
type Page[T any] struct {
	Items       []T
	Total       int64
	Page, Limit int
}

func viewOf(p *domain.Product, shopName string) ProductView {
	s := p.Snapshot()
	return ProductView{ID: s.ID, ShopID: s.ShopID, ShopName: shopName, Name: s.Name, Description: s.Description, Slug: s.Slug,
		Price: int64(s.Price), Quantity: s.Quantity, Type: string(s.Type), Thumb: s.Thumb, Attributes: s.Attributes,
		RatingsAverage: s.RatingsAverage, IsDraft: s.IsDraft, IsPublished: s.IsPublished, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

// views converts products, joining shop names with one query.
func (d *Deps) views(ctx context.Context, products []*domain.Product) ([]ProductView, error) {
	seen := map[string]bool{}
	var shopIDs []string
	for _, p := range products {
		if !seen[p.ShopID()] {
			seen[p.ShopID()] = true
			shopIDs = append(shopIDs, p.ShopID())
		}
	}
	names := map[string]domain.ShopProjection{}
	if len(shopIDs) > 0 {
		var err error
		if names, err = d.Shops.FindByIDs(ctx, shopIDs); err != nil {
			return nil, err
		}
	}
	out := make([]ProductView, len(products))
	for i, p := range products {
		out[i] = viewOf(p, names[p.ShopID()].Name)
	}
	return out, nil
}
