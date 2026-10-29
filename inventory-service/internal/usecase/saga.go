package usecase

import (
	"context"
	"errors"
	"sort"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/saga"
)

// ReserveStock is step 1 of the checkout saga: hold stock for an order, all lines or none.
type ReserveStock struct{ d *Deps }

// Execute is idempotent: a repeated command for an order that already holds stock just confirms it. The business
// outcome — held, or refused for lack of stock — is a reply in the outbox, committed in the same transaction as the
// stock change; a shortage is therefore not an error of the handler (nothing to retry, nothing for the DLQ).
func (u ReserveStock) Execute(ctx context.Context, cmd saga.ReserveStock) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		existing, err := u.d.Reservations.ForOrder(ctx, cmd.OrderID)
		if err != nil {
			return err
		}
		if len(existing) > 0 { // already handled: confirm, change nothing
			if existing[0].Status() == domain.ReservationReserved || existing[0].Status() == domain.ReservationCommitted {
				return u.d.reply(ctx, saga.StockReserved{OrderID: cmd.OrderID})
			}
			return u.d.reply(ctx, saga.StockReservationFailed{OrderID: cmd.OrderID, Reason: saga.ReasonStepTimeout})
		}
		need, err := mergeItems(cmd.Items)
		if err != nil {
			return u.d.reply(ctx, saga.StockReservationFailed{OrderID: cmd.OrderID, Reason: saga.ReasonInsufficientStock})
		}
		ids := sortedIDs(need)
		locked, err := u.d.Inventories.Lock(ctx, ids)
		if err != nil {
			return err
		}
		now := u.d.Clock.Now()
		var held []*domain.Reservation
		for _, id := range ids {
			inv, ok := locked[id]
			if !ok {
				return u.d.reply(ctx, saga.StockReservationFailed{OrderID: cmd.OrderID, Reason: saga.ReasonUnknownProduct, ProductID: id})
			}
			if err := inv.Reserve(need[id], now); err != nil {
				var short *domain.InsufficientStockError
				if errors.As(err, &short) {
					// nothing was saved yet: the locks are released with the commit of the reply alone
					return u.d.reply(ctx, saga.StockReservationFailed{OrderID: cmd.OrderID, Reason: saga.ReasonInsufficientStock, ProductID: id})
				}
				return err
			}
			r, err := domain.NewReservation(u.d.IDs.New(), cmd.OrderID, id, need[id], now.Add(u.d.ttl()), now)
			if err != nil {
				return err
			}
			held = append(held, r)
		}
		for _, id := range ids {
			if err := u.d.Inventories.Save(ctx, locked[id]); err != nil {
				return err
			}
		}
		if err := u.d.Reservations.Insert(ctx, held); err != nil {
			return err
		}
		return u.d.reply(ctx, saga.StockReserved{OrderID: cmd.OrderID})
	})
}

// mergeItems sums the quantities per product and rejects non-positive ones.
func mergeItems(items []saga.Item) (map[string]int64, error) {
	if len(items) == 0 {
		return nil, errs.Validation("an order needs at least one item")
	}
	need := map[string]int64{}
	for _, it := range items {
		if it.ProductID == "" || it.Quantity <= 0 {
			return nil, errs.Validation("every item needs a product and a positive quantity")
		}
		need[it.ProductID] += it.Quantity
	}
	return need, nil
}

func sortedIDs(m map[string]int64) []string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// settle moves the active reservations of an order that pick selects out of RESERVED, applying mark to each
// reservation and apply to its inventory. It returns how many reservations it settled.
func (d *Deps) settle(ctx context.Context, orderID string, pick func(*domain.Reservation) bool,
	mark func(*domain.Reservation) error, apply func(*domain.Inventory, int64) error) (int, error) {
	rs, err := d.Reservations.ForOrder(ctx, orderID)
	if err != nil {
		return 0, err
	}
	var chosen []*domain.Reservation
	ids := map[string]int64{}
	for _, r := range rs {
		if r.Active() && pick(r) {
			chosen = append(chosen, r)
			ids[r.ProductID()] += r.Quantity()
		}
	}
	if len(chosen) == 0 {
		return 0, nil
	}
	sorted := sortedIDs(ids)
	locked, err := d.Inventories.Lock(ctx, sorted)
	if err != nil {
		return 0, err
	}
	for _, r := range chosen {
		inv, ok := locked[r.ProductID()]
		if !ok {
			return 0, domain.ErrInventoryNotFound()
		}
		if err := apply(inv, r.Quantity()); err != nil {
			return 0, err
		}
		if err := mark(r); err != nil {
			return 0, err
		}
		if err := d.Reservations.Save(ctx, r); err != nil {
			return 0, err
		}
	}
	for _, id := range sorted {
		if err := d.Inventories.Save(ctx, locked[id]); err != nil {
			return 0, err
		}
	}
	return len(chosen), nil
}

func always(*domain.Reservation) bool { return true }

// ReleaseStock is the compensation of step 1.
type ReleaseStock struct{ d *Deps }

// Execute returns the held units to the shelf. It replies StockReleased even when nothing was held (the order never
// got that far, or this is a repeat): compensations must be harmless to repeat.
func (u ReleaseStock) Execute(ctx context.Context, cmd saga.ReleaseStock) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		now := u.d.Clock.Now()
		if _, err := u.d.settle(ctx, cmd.OrderID, always,
			func(r *domain.Reservation) error { return r.Release(now) },
			func(i *domain.Inventory, q int64) error { return i.Release(q, now) }); err != nil {
			return err
		}
		return u.d.reply(ctx, saga.StockReleased{OrderID: cmd.OrderID})
	})
}

// CommitStock is step 4: the payment succeeded, the held units are sold.
type CommitStock struct{ d *Deps }

// Execute commits the active holds. Repeating it is fine; committing an order whose hold lapsed or was released is a
// permanent failure (it should be impossible: the hold outlives the saga timeouts) that goes to the dead-letter topic.
func (u CommitStock) Execute(ctx context.Context, cmd saga.CommitStock) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		rs, err := u.d.Reservations.ForOrder(ctx, cmd.OrderID)
		if err != nil {
			return err
		}
		if len(rs) == 0 {
			return errs.FailedPrecondition(domain.CodeNothingReserved, "no stock is held for order "+cmd.OrderID)
		}
		for _, r := range rs {
			if r.Status() == domain.ReservationReleased || r.Status() == domain.ReservationExpired {
				return errs.FailedPrecondition(domain.CodeInvalidState, "the stock hold of order "+cmd.OrderID+" is "+string(r.Status()))
			}
		}
		now := u.d.Clock.Now()
		if _, err := u.d.settle(ctx, cmd.OrderID, always,
			func(r *domain.Reservation) error { return r.Commit(now) },
			func(i *domain.Inventory, q int64) error { return i.Commit(q, now) }); err != nil {
			return err
		}
		return u.d.reply(ctx, saga.StockCommitted{OrderID: cmd.OrderID})
	})
}

// ExpireReservations is the safety net: holds nobody finished are returned to the shelf.
type ExpireReservations struct{ d *Deps }

// Execute handles up to limit overdue orders and returns how many it expired. Each order is its own transaction.
func (u ExpireReservations) Execute(ctx context.Context, limit int) (int, error) {
	orders, err := u.d.Reservations.ExpiredOrders(ctx, u.d.Clock.Now(), limit)
	if err != nil {
		return 0, err
	}
	done := 0
	for _, orderID := range orders {
		err := u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
			now := u.d.Clock.Now()
			n, err := u.d.settle(ctx, orderID,
				func(r *domain.Reservation) bool { return r.IsExpired(now) }, // only holds that are really overdue
				func(r *domain.Reservation) error { return r.Expire(now) },
				func(i *domain.Inventory, q int64) error { return i.Release(q, now) })
			if n > 0 {
				done++
			}
			return err
		})
		if err != nil {
			return done, err
		}
	}
	return done, nil
}
