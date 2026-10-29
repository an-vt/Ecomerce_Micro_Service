package usecase

import (
	"context"
	"strings"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/pagination"
)

// CreateProductInput is the data of a new product.
type CreateProductInput struct {
	Type        string
	Name        string
	Description string
	Price       int64
	Quantity    int64
	Thumb       string
	Attributes  map[string]any
}

// CreateProduct adds a draft product for the calling shop (monolith ProductFactory.createProduct).
type CreateProduct struct{ d *Deps }

// Execute validates through the type strategy, stores the product and publishes catalog.product.created in the
// same transaction, so inventory is told about exactly the products that exist (the monolith wrote the inventory
// row in a second, unrelated write).
func (u CreateProduct) Execute(ctx context.Context, in CreateProductInput) (ProductView, error) {
	shop, err := shopCaller(ctx)
	if err != nil {
		return ProductView{}, err
	}
	p, err := domain.NewProduct(u.d.Types, u.d.IDs.New(), shop.ID, domain.ProductInput{
		Type: domain.Type(in.Type), Name: in.Name, Description: in.Description, Price: in.Price, Quantity: in.Quantity,
		Thumb: in.Thumb, Attributes: in.Attributes}, u.d.Clock.Now())
	if err != nil {
		return ProductView{}, err
	}
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.d.Products.Create(ctx, p); err != nil {
			return err
		}
		return u.d.emit(ctx, kafkax.TopicProductCreated, domain.ProductCreated{ProductID: p.ID(), ShopID: p.ShopID(), Name: p.Name(),
			Price: int64(p.Price()), Quantity: p.Quantity(), Type: string(p.Type())})
	})
	if err != nil {
		return ProductView{}, err
	}
	vs, err := u.d.views(ctx, []*domain.Product{p})
	if err != nil {
		return ProductView{}, err
	}
	return vs[0], nil
}

// UpdateProductInput is a partial update; nil fields are left alone.
type UpdateProductInput struct {
	ProductID   string
	Name        *string
	Description *string
	Thumb       *string
	Price       *int64
	Quantity    *int64
	Attributes  map[string]any
}

// UpdateProduct changes a product of the calling shop (monolith updateProduct).
type UpdateProduct struct{ d *Deps }

// Execute applies the patch atomically and publishes catalog.product.updated; the stock quantity is included only
// when it was part of the patch, which is when inventory should react (monolith updateInventory).
func (u UpdateProduct) Execute(ctx context.Context, in UpdateProductInput) (ProductView, error) {
	shop, err := shopCaller(ctx)
	if err != nil {
		return ProductView{}, err
	}
	var p *domain.Product
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = u.d.Products.FindByID(ctx, in.ProductID); err != nil {
			return err
		}
		if !p.OwnedBy(shop.ID) {
			return domain.ErrNotOwner()
		}
		qtyChanged, err := p.Update(u.d.Types, domain.ProductPatch{Name: in.Name, Description: in.Description, Thumb: in.Thumb,
			Price: in.Price, Quantity: in.Quantity, Attributes: in.Attributes}, u.d.Clock.Now())
		if err != nil {
			return err
		}
		if err := u.d.Products.Update(ctx, p); err != nil {
			return err
		}
		ev := domain.ProductUpdated{ProductID: p.ID(), ShopID: p.ShopID(), Name: p.Name(), Price: int64(p.Price())}
		if qtyChanged {
			q := p.Quantity()
			ev.Quantity = &q
		}
		return u.d.emit(ctx, kafkax.TopicProductUpdated, ev)
	})
	if err != nil {
		return ProductView{}, err
	}
	vs, err := u.d.views(ctx, []*domain.Product{p})
	if err != nil {
		return ProductView{}, err
	}
	return vs[0], nil
}

// SetPublished publishes or unpublishes a product of the calling shop.
type SetPublished struct{ d *Deps }

// Execute changes the visibility; doing it twice is harmless.
func (u SetPublished) Execute(ctx context.Context, productID string, publish bool) (ProductView, error) {
	shop, err := shopCaller(ctx)
	if err != nil {
		return ProductView{}, err
	}
	p, err := u.d.Products.FindByID(ctx, productID)
	if err != nil {
		return ProductView{}, err
	}
	if !p.OwnedBy(shop.ID) {
		return ProductView{}, domain.ErrNotOwner()
	}
	if p.IsPublished() != publish {
		if publish {
			p.Publish(u.d.Clock.Now())
		} else {
			p.Unpublish(u.d.Clock.Now())
		}
		if err := u.d.Products.Update(ctx, p); err != nil {
			return ProductView{}, err
		}
	}
	vs, err := u.d.views(ctx, []*domain.Product{p})
	if err != nil {
		return ProductView{}, err
	}
	return vs[0], nil
}

// GetProduct is the public product page.
type GetProduct struct{ d *Deps }

// Execute returns a published product; drafts are invisible to the public (a shop sees its drafts through
// ListShopProducts).
func (u GetProduct) Execute(ctx context.Context, productID string) (ProductView, error) {
	p, err := u.d.Products.FindByID(ctx, productID)
	if err != nil {
		return ProductView{}, err
	}
	if !p.IsPublished() {
		return ProductView{}, domain.ErrProductNotFound()
	}
	vs, err := u.d.views(ctx, []*domain.Product{p})
	if err != nil {
		return ProductView{}, err
	}
	return vs[0], nil
}

// ListProducts is the public product list.
type ListProducts struct{ d *Deps }

// Execute returns a page of published products.
func (u ListProducts) Execute(ctx context.Context, page, limit int, sort string) (Page[ProductView], error) {
	pg := pagination.New(page, limit)
	items, total, err := u.d.Products.ListPublished(ctx, domain.ListQuery{Page: pg.Page, Limit: pg.Limit, Sort: sort})
	if err != nil {
		return Page[ProductView]{}, err
	}
	return u.page(ctx, items, total, pg)
}

func (u ListProducts) page(ctx context.Context, items []*domain.Product, total int64, pg pagination.Page) (Page[ProductView], error) {
	vs, err := u.d.views(ctx, items)
	if err != nil {
		return Page[ProductView]{}, err
	}
	return Page[ProductView]{Items: vs, Total: total, Page: pg.Page, Limit: pg.Limit}, nil
}

// SearchProducts is the public full-text search.
type SearchProducts struct{ d *Deps }

// Execute returns published products matching keyword, best match first.
func (u SearchProducts) Execute(ctx context.Context, keyword string, page, limit int) (Page[ProductView], error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return Page[ProductView]{}, errs.Validation("keyword is required")
	}
	pg := pagination.New(page, limit)
	items, total, err := u.d.Products.Search(ctx, keyword, pg.Page, pg.Limit)
	if err != nil {
		return Page[ProductView]{}, err
	}
	return ListProducts(u).page(ctx, items, total, pg)
}

// ListShopProducts lists the calling shop's drafts or published products.
type ListShopProducts struct{ d *Deps }

// Execute returns a page of the shop's products.
func (u ListShopProducts) Execute(ctx context.Context, published bool, page, limit int) (Page[ProductView], error) {
	shop, err := shopCaller(ctx)
	if err != nil {
		return Page[ProductView]{}, err
	}
	pg := pagination.New(page, limit)
	items, total, err := u.d.Products.ListByShop(ctx, shop.ID, published, pg.Page, pg.Limit)
	if err != nil {
		return Page[ProductView]{}, err
	}
	return ListProducts(u).page(ctx, items, total, pg)
}

// GetProductsByIds serves the services that must re-price what a buyer asks for (cart, order).
type GetProductsByIds struct{ d *Deps }

// Execute returns the known products among ids, in the order of ids (unknown ids are skipped).
func (u GetProductsByIds) Execute(ctx context.Context, ids []string) ([]ProductView, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > 200 {
		return nil, errs.Validation("at most 200 ids per call")
	}
	found, err := u.d.Products.FindByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]*domain.Product, len(found))
	for _, p := range found {
		byID[p.ID()] = p
	}
	ordered := make([]*domain.Product, 0, len(found))
	for _, id := range ids {
		if p, ok := byID[id]; ok {
			ordered = append(ordered, p)
			delete(byID, id)
		}
	}
	return u.d.views(ctx, ordered)
}

// ListProductIdsByShop serves discount-service (which products a shop-wide code applies to).
type ListProductIdsByShop struct{ d *Deps }

// Execute returns the ids of the shop's published products.
func (u ListProductIdsByShop) Execute(ctx context.Context, shopID string) ([]string, error) {
	if shopID == "" {
		return nil, errs.Validation("shop id is required")
	}
	return u.d.Products.IDsByShop(ctx, shopID)
}
