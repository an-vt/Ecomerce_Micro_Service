package usecase

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
	"github.com/an-vt/ecom/platform-go/saga"
)

// ApplyDiscounts is step 2 of the checkout saga: redeem the order's codes, all of them or none.
type ApplyDiscounts struct{ d *Deps }

// rejected is a business refusal of the whole command: it becomes a DiscountsRejected reply, not a handler error
// (nothing to retry, nothing for the dead-letter topic).
type rejected struct {
	reason, code string
}

func (r *rejected) Error() string { return "discount rejected: " + r.reason }

func reject(err error, code string) *rejected {
	// the order's cancel reason is the specific DISCOUNT_* code (expired, used up, ...); anything else is generic
	reason := errs.CodeOf(err)
	if !strings.HasPrefix(reason, "DISCOUNT_") {
		reason = saga.ReasonDiscountRejected
	}
	return &rejected{reason: reason, code: code}
}

// Execute is idempotent: an order that already redeemed its codes just gets the same answer again. The redemption
// (usage rows + counters) and the reply commit in one transaction; the counters are changed only under the row lock, so
// two orders racing for the last use cannot both get it.
func (u ApplyDiscounts) Execute(ctx context.Context, cmd saga.ApplyDiscounts) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		existing, err := u.d.Usages.ForOrder(ctx, cmd.OrderID)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return u.again(ctx, cmd.OrderID, existing)
		}
		total, err := u.redeem(ctx, cmd)
		var rj *rejected
		switch {
		case errors.As(err, &rj):
			// redeem changed nothing durable; the reply is the whole outcome
			return u.d.reply(ctx, saga.DiscountsRejected{OrderID: cmd.OrderID, Reason: rj.reason, Code: rj.code})
		case err != nil:
			return err
		}
		return u.d.reply(ctx, saga.DiscountsApplied{OrderID: cmd.OrderID, TotalDiscount: total})
	})
}

// again answers a repeated command from what is already recorded. Usages that were released mean the compensation got
// there first (the saga timed out): the late command is refused so the codes are not taken again.
func (u ApplyDiscounts) again(ctx context.Context, orderID string, existing []*domain.Usage) error {
	var total int64
	for _, x := range existing {
		if x.Status() != domain.UsageApplied {
			return u.d.reply(ctx, saga.DiscountsRejected{OrderID: orderID, Reason: saga.ReasonStepTimeout})
		}
		total += x.Amount()
	}
	return u.d.reply(ctx, saga.DiscountsApplied{OrderID: orderID, TotalDiscount: total})
}

func (u ApplyDiscounts) redeem(ctx context.Context, cmd saga.ApplyDiscounts) (int64, error) {
	refs, err := codeRefs(cmd)
	if err != nil {
		return 0, err
	}
	locked, err := u.d.Discounts.LockByCodes(ctx, refs)
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(locked))
	for _, d := range locked {
		ids = append(ids, d.ID())
	}
	counts, err := u.d.Usages.CountApplied(ctx, cmd.UserID, ids)
	if err != nil {
		return 0, err
	}

	now := u.d.Clock.Now()
	var total money.Amount
	var usages []*domain.Usage
	for _, ref := range refs {
		disc, ok := locked[ref]
		if !ok {
			return 0, reject(domain.ErrNotFound(), ref.Code)
		}
		x, amount, err := u.redeemOne(disc, cmd, counts[disc.ID()], now)
		if err != nil {
			return 0, err
		}
		if total, err = total.Add(amount); err != nil {
			return 0, reject(err, ref.Code)
		}
		usages = append(usages, x)
	}
	// every code passed: only now is anything written
	for _, ref := range refs {
		if err := u.d.Discounts.Save(ctx, locked[ref]); err != nil {
			return 0, err
		}
	}
	if err := u.d.Usages.Insert(ctx, usages); err != nil {
		return 0, err
	}
	return int64(total), nil
}

// redeemOne checks one locked discount against the order, counts the use on the in-memory copy and builds its usage
// record. Nothing is stored here.
func (u ApplyDiscounts) redeemOne(disc *domain.Discount, cmd saga.ApplyDiscounts, usedByUser int64, now time.Time) (*domain.Usage, money.Amount, error) {
	code := disc.Code()
	if err := disc.CanBeUsedBy(usedByUser, now); err != nil {
		return nil, 0, reject(err, code)
	}
	calc, err := disc.Calculate(linesOfShop(cmd.Items, disc.ShopID()))
	if err != nil {
		return nil, 0, reject(err, code)
	}
	if err := disc.Use(now); err != nil {
		return nil, 0, reject(err, code)
	}
	x, err := domain.NewUsage(u.d.IDs.New(), disc.ID(), cmd.UserID, cmd.OrderID, int64(calc.Discount), now)
	if err != nil {
		return nil, 0, err
	}
	return x, calc.Discount, nil
}

// codeRefs validates the command and returns its (shop, code) pairs in shop order.
func codeRefs(cmd saga.ApplyDiscounts) ([]domain.CodeRef, error) {
	if cmd.UserID == "" || len(cmd.Discounts) == 0 {
		return nil, reject(errs.Validation("a user and at least one code are required"), "")
	}
	refs := make([]domain.CodeRef, 0, len(cmd.Discounts))
	shops := map[string]bool{}
	for _, r := range cmd.Discounts {
		ref := domain.CodeRef{ShopID: r.ShopID, Code: domain.NormalizeCode(r.Code)}
		if shops[ref.ShopID] { // one code per shop and order (F-11)
			return nil, reject(errs.InvalidArgument(domain.CodeDuplicateShop, "one discount code per shop"), r.Code)
		}
		shops[ref.ShopID] = true
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ShopID < refs[j].ShopID })
	return refs, nil
}

func linesOfShop(items []saga.Item, shopID string) []domain.Line {
	var out []domain.Line
	for _, it := range items {
		if it.ShopID == shopID {
			out = append(out, domain.Line{ProductID: it.ProductID, Price: money.Amount(it.Price), Quantity: it.Quantity})
		}
	}
	return out
}

// ReleaseDiscounts is the compensation of step 2.
type ReleaseDiscounts struct{ d *Deps }

// Execute gives the order's redemptions back, once: the usage rows flip to RELEASED and each counter drops by one. It
// replies DiscountsReleased even when the order redeemed nothing (the saga never got that far, or this is a repeat):
// compensations must be harmless to repeat.
func (u ReleaseDiscounts) Execute(ctx context.Context, cmd saga.ReleaseDiscounts) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		usages, err := u.d.Usages.ForOrder(ctx, cmd.OrderID)
		if err != nil {
			return err
		}
		var open []*domain.Usage
		var ids []string
		for _, x := range usages {
			if x.Status() == domain.UsageApplied {
				open = append(open, x)
				ids = append(ids, x.DiscountID())
			}
		}
		if len(open) > 0 {
			locked, err := u.d.Discounts.LockByIDs(ctx, ids)
			if err != nil {
				return err
			}
			now := u.d.Clock.Now()
			for _, x := range open {
				if d, ok := locked[x.DiscountID()]; ok {
					d.Unuse(now)
				}
				x.Release(now)
				if err := u.d.Usages.Save(ctx, x); err != nil {
					return err
				}
			}
			for _, d := range locked {
				if err := u.d.Discounts.Save(ctx, d); err != nil {
					return err
				}
			}
		}
		return u.d.reply(ctx, saga.DiscountsReleased{OrderID: cmd.OrderID})
	})
}
