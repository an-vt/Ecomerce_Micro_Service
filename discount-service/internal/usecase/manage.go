package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/pagination"
)

// DiscountView is a discount as the transports show it.
type DiscountView struct {
	domain.DiscountSnapshot
}

func view(d *domain.Discount) DiscountView { return DiscountView{d.Snapshot()} }

// CreateInput are the fields of a new discount.
type CreateInput struct {
	Code, Name, Description string
	Type                    string
	Value                   int64
	StartDate, EndDate      time.Time
	MaxUses, MaxUsesPerUser int64
	MinOrderValue           int64
	AppliesTo               string
	ProductIDs              []string
}

// CreateDiscount lets a shop add a code.
type CreateDiscount struct{ d *Deps }

// Execute validates the rules, checks the listed products belong to the shop, stores the discount and announces it.
func (u CreateDiscount) Execute(ctx context.Context, in CreateInput) (DiscountView, error) {
	me, err := shopCaller(ctx)
	if err != nil {
		return DiscountView{}, err
	}
	now := u.d.Clock.Now()
	disc, err := domain.New(domain.NewInput{ID: u.d.IDs.New(), ShopID: me.ID, Code: in.Code, Name: in.Name, Description: in.Description,
		Type: domain.Type(in.Type), Value: in.Value, StartDate: in.StartDate, EndDate: in.EndDate, MaxUses: in.MaxUses,
		MaxUsesPerUser: in.MaxUsesPerUser, MinOrderValue: in.MinOrderValue, AppliesTo: domain.Scope(in.AppliesTo), ProductIDs: in.ProductIDs}, now)
	if err != nil {
		return DiscountView{}, err
	}
	if err := u.d.ownProducts(ctx, me.ID, disc.Snapshot().ProductIDs); err != nil {
		return DiscountView{}, err
	}
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := u.d.Discounts.Insert(ctx, disc); err != nil {
			return err
		}
		s := disc.Snapshot()
		return u.d.emit(ctx, domain.TopicCreated, domain.DiscountCreated{DiscountID: s.ID, ShopID: s.ShopID, Code: s.Code,
			Name: s.Name, Type: string(s.Type), Value: s.Value})
	})
	if err != nil {
		return DiscountView{}, err
	}
	return view(disc), nil
}

// UpdateInput are the optional changes of a discount.
type UpdateInput struct {
	DiscountID              string
	Name, Description       *string
	EndDate                 *time.Time
	IsActive                *bool
	MaxUses, MaxUsesPerUser *int64
	MinOrderValue           *int64
	AppliesTo               *string
	ProductIDs              []string
	ReplaceProductIDs       bool
}

// UpdateDiscount lets the owning shop change a code.
type UpdateDiscount struct{ d *Deps }

// Execute locks the row (so an update cannot race a redemption), checks ownership and applies the patch.
func (u UpdateDiscount) Execute(ctx context.Context, in UpdateInput) (DiscountView, error) {
	me, err := shopCaller(ctx)
	if err != nil {
		return DiscountView{}, err
	}
	if in.ReplaceProductIDs {
		if err := u.d.ownProducts(ctx, me.ID, in.ProductIDs); err != nil {
			return DiscountView{}, err
		}
	}
	var out DiscountView
	err = u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		disc, err := u.d.Discounts.LockByID(ctx, in.DiscountID)
		if err != nil {
			return err
		}
		if disc.ShopID() != me.ID {
			return domain.ErrNotOwner()
		}
		p := domain.Patch{Name: in.Name, Description: in.Description, EndDate: in.EndDate, IsActive: in.IsActive, MaxUses: in.MaxUses,
			MaxUsesPerUser: in.MaxUsesPerUser, MinOrderValue: in.MinOrderValue, ProductIDs: in.ProductIDs, ReplaceProducts: in.ReplaceProductIDs}
		if in.AppliesTo != nil {
			scope := domain.Scope(*in.AppliesTo)
			p.AppliesTo = &scope
		}
		if err := disc.Update(p, u.d.Clock.Now()); err != nil {
			return err
		}
		if err := u.d.Discounts.Save(ctx, disc); err != nil {
			return err
		}
		out = view(disc)
		return nil
	})
	return out, err
}

// ListShopDiscounts lists the caller's own codes.
type ListShopDiscounts struct{ d *Deps }

// Execute returns one page, newest first.
func (u ListShopDiscounts) Execute(ctx context.Context, page, limit int) (pagination.Result[DiscountView], error) {
	me, err := shopCaller(ctx)
	if err != nil {
		return pagination.Result[DiscountView]{}, err
	}
	pg := pagination.New(page, limit)
	items, total, err := u.d.Discounts.ListByShop(ctx, me.ID, pg.Offset(), pg.Limit)
	if err != nil {
		return pagination.Result[DiscountView]{}, err
	}
	views := make([]DiscountView, len(items))
	for i, d := range items {
		views[i] = view(d)
	}
	return pagination.NewResult(pg, views, total), nil
}
