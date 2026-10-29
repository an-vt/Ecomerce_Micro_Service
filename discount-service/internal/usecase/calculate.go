package usecase

import (
	"context"

	"github.com/an-vt/ecom/discount-service/internal/domain"
)

// CalculateDiscount previews what a code would take off an order.
type CalculateDiscount struct{ d *Deps }

// Preview is the answer.
type Preview struct {
	Discount, Subtotal, Total int64
}

// Execute is read-only: it applies the same rules as the saga step (active, dates, uses left, the caller's own uses,
// minimum order, covered products) without recording anything. The saga re-checks under a lock, so a preview that
// says yes can still lose a race for the last use; that is the point of the check being repeated.
func (u CalculateDiscount) Execute(ctx context.Context, shopID, code string, lines []domain.Line) (Preview, error) {
	me, err := userCaller(ctx)
	if err != nil {
		return Preview{}, err
	}
	disc, err := u.d.Discounts.FindByCode(ctx, domain.CodeRef{ShopID: shopID, Code: domain.NormalizeCode(code)})
	if err != nil {
		return Preview{}, err
	}
	counts, err := u.d.Usages.CountApplied(ctx, me.ID, []string{disc.ID()})
	if err != nil {
		return Preview{}, err
	}
	if err := disc.CanBeUsedBy(counts[disc.ID()], u.d.Clock.Now()); err != nil {
		return Preview{}, err
	}
	c, err := disc.Calculate(lines)
	if err != nil {
		return Preview{}, err
	}
	return Preview{Discount: int64(c.Discount), Subtotal: int64(c.Subtotal), Total: int64(c.Total())}, nil
}
