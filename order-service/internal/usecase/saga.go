package usecase

import (
	"context"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/saga"
)

// HandleSagaReply advances the checkout when a participant answers.
type HandleSagaReply struct{ d *Deps }

// Execute locks the saga (and the order) so replies and the sweeper for one order are handled one at a time, lets the
// state machine decide, and stores the outcome with the commands it produced. A reply that does not fit the current
// state (late, duplicated, from a step already given up on) changes nothing and is only counted.
func (u HandleSagaReply) Execute(ctx context.Context, m saga.Message) error {
	if !domain.ValidID(m.Order()) { // not an order of ours: permanent, so the message goes to the dead-letter topic at once
		return domain.ErrSagaNotFound()
	}
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		sg, err := u.d.Sagas.Lock(ctx, m.Order())
		if err != nil {
			return err
		}
		res := sg.OnReply(m, u.d.Clock.Now(), u.d.timeouts())
		if res.Stale {
			u.d.metrics().StaleReply()
			return nil
		}
		o, err := u.d.Orders.Lock(ctx, sg.ID())
		if err != nil {
			return err
		}
		return u.d.commit(ctx, sg, o, res)
	})
}

// SweepSagaTimeouts acts on checkouts whose step deadline passed.
type SweepSagaTimeouts struct{ d *Deps }

// Execute handles up to limit overdue sagas and returns how many. Each saga is its own transaction and is taken with
// SKIP LOCKED, so several replicas can sweep at once without handling one timeout twice; handling moves the deadline
// (or ends the saga), so a saga is not picked again until it is overdue again.
func (u SweepSagaTimeouts) Execute(ctx context.Context, limit int) (int, error) {
	handled := 0
	for handled < limit {
		found := false
		err := u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
			now := u.d.Clock.Now()
			sg, err := u.d.Sagas.LockNextDue(ctx, now)
			if err != nil || sg == nil {
				return err
			}
			found = true
			o, err := u.d.Orders.Lock(ctx, sg.ID())
			if err != nil {
				return err
			}
			return u.d.commit(ctx, sg, o, sg.OnTimeout(now, u.d.timeouts()))
		})
		if err != nil {
			return handled, err
		}
		if !found {
			break
		}
		handled++
	}
	return handled, nil
}
