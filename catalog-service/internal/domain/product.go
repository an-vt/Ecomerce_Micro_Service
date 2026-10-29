package domain

import (
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
)

// DefaultRating is the starting average (monolith: 4.5).
const DefaultRating = 4.5

// Product is one sellable item. Unlike the monolith it is a single aggregate stored in one document: the type
// strategy validates its attributes, it does not split them into a second collection.
type Product struct {
	id             string
	shopID         string
	name           string
	description    string
	slug           string
	price          money.Amount
	quantity       int64
	typ            Type
	thumb          string
	attributes     map[string]any
	ratingsAverage float64
	isDraft        bool
	isPublished    bool
	createdAt      time.Time
	updatedAt      time.Time
}

// ProductSnapshot is the persisted form of a Product.
type ProductSnapshot struct {
	ID             string
	ShopID         string
	Name           string
	Description    string
	Slug           string
	Price          money.Amount
	Quantity       int64
	Type           Type
	Thumb          string
	Attributes     map[string]any
	RatingsAverage float64
	IsDraft        bool
	IsPublished    bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ProductInput is the data to create a product.
type ProductInput struct {
	Type        Type
	Name        string
	Description string
	Price       int64
	Quantity    int64
	Thumb       string
	Attributes  map[string]any
}

func validateCommon(name string, price, quantity int64) error {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 255 {
		return errs.Validation("name is required and must be at most 255 characters")
	}
	if price < 0 {
		return errs.Validation("price must not be negative")
	}
	if quantity < 0 {
		return errs.Validation("quantity must not be negative")
	}
	return nil
}

// NewProduct creates a draft product owned by shopID.
func NewProduct(reg *TypeRegistry, id, shopID string, in ProductInput, now time.Time) (*Product, error) {
	if shopID == "" {
		return nil, errs.Validation("shop id is required")
	}
	if err := validateCommon(in.Name, in.Price, in.Quantity); err != nil {
		return nil, err
	}
	if err := reg.Validate(in.Type, in.Attributes); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	return &Product{
		id: id, shopID: shopID, name: name, description: in.Description, slug: Slugify(name), price: money.Amount(in.Price),
		quantity: in.Quantity, typ: in.Type, thumb: in.Thumb, attributes: cloneMap(in.Attributes), ratingsAverage: DefaultRating,
		isDraft: true, createdAt: now, updatedAt: now,
	}, nil
}

// RestoreProduct rebuilds a Product from storage.
func RestoreProduct(s ProductSnapshot) *Product {
	return &Product{id: s.ID, shopID: s.ShopID, name: s.Name, description: s.Description, slug: s.Slug, price: s.Price,
		quantity: s.Quantity, typ: s.Type, thumb: s.Thumb, attributes: cloneMap(s.Attributes), ratingsAverage: s.RatingsAverage,
		isDraft: s.IsDraft, isPublished: s.IsPublished, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}
}

// Snapshot returns the persisted state.
func (p *Product) Snapshot() ProductSnapshot {
	return ProductSnapshot{ID: p.id, ShopID: p.shopID, Name: p.name, Description: p.description, Slug: p.slug, Price: p.price,
		Quantity: p.quantity, Type: p.typ, Thumb: p.thumb, Attributes: cloneMap(p.attributes), RatingsAverage: p.ratingsAverage,
		IsDraft: p.isDraft, IsPublished: p.isPublished, CreatedAt: p.createdAt, UpdatedAt: p.updatedAt}
}

func (p *Product) ID() string          { return p.id }
func (p *Product) ShopID() string      { return p.shopID }
func (p *Product) Name() string        { return p.name }
func (p *Product) Price() money.Amount { return p.price }
func (p *Product) Quantity() int64     { return p.quantity }
func (p *Product) Type() Type          { return p.typ }
func (p *Product) IsPublished() bool   { return p.isPublished }

// OwnedBy reports whether shopID owns the product.
func (p *Product) OwnedBy(shopID string) bool { return shopID != "" && p.shopID == shopID }

// ProductPatch holds the fields an update may change; nil means "leave as is". Attributes are merged key by key,
// nested maps included (the monolith's updateObjectNestedParse), so a partial update keeps the other attributes.
type ProductPatch struct {
	Name        *string
	Description *string
	Thumb       *string
	Price       *int64
	Quantity    *int64
	Attributes  map[string]any
}

// Update applies the patch after validating the result. It reports whether the stock quantity was part of it, so
// the caller can tell inventory-service.
func (p *Product) Update(reg *TypeRegistry, patch ProductPatch, now time.Time) (quantityChanged bool, err error) {
	next := *p
	next.attributes = cloneMap(p.attributes)
	if patch.Name != nil {
		next.name = strings.TrimSpace(*patch.Name)
		next.slug = Slugify(next.name)
	}
	if patch.Description != nil {
		next.description = *patch.Description
	}
	if patch.Thumb != nil {
		next.thumb = *patch.Thumb
	}
	if patch.Price != nil {
		next.price = money.Amount(*patch.Price)
	}
	if patch.Quantity != nil {
		next.quantity = *patch.Quantity
		quantityChanged = true
	}
	if len(patch.Attributes) > 0 {
		mergeInto(next.attributes, patch.Attributes)
	}
	if err := validateCommon(next.name, int64(next.price), next.quantity); err != nil {
		return false, err
	}
	if err := reg.Validate(next.typ, next.attributes); err != nil {
		return false, err
	}
	next.updatedAt = now
	*p = next
	return quantityChanged, nil
}

// Publish makes the product visible to buyers.
func (p *Product) Publish(now time.Time) { p.isDraft, p.isPublished, p.updatedAt = false, true, now }

// Unpublish hides it again (it becomes a draft).
func (p *Product) Unpublish(now time.Time) { p.isDraft, p.isPublished, p.updatedAt = true, false, now }

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			out[k] = cloneMap(sub)
		} else {
			out[k] = v
		}
	}
	return out
}

// mergeInto merges src into dst recursively: maps are merged, anything else replaces.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		sv, srcIsMap := v.(map[string]any)
		dv, dstIsMap := dst[k].(map[string]any)
		if srcIsMap && dstIsMap {
			mergeInto(dv, sv)
			continue
		}
		if srcIsMap {
			dst[k] = cloneMap(sv)
			continue
		}
		dst[k] = v
	}
}
