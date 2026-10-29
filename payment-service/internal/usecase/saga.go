package usecase

import (
	"context"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/saga"
)

// ReasonInvalidRequest is the PaymentFailed reason for a command that cannot be a payment (no user, negative amount…).
const ReasonInvalidRequest = "INVALID_REQUEST"

// ProcessPayment is step 3 of the checkout saga: charge the order.
type ProcessPayment struct{ d *Deps }

// Execute is idempotent. The payment row is inserted first, under a unique order id, so two copies of the command
// (a redelivery, a rebalance) can never both reach the provider; the loser reads the winner's outcome. The provider
// gets the order id as its idempotency key, so even a charge that was sent and not recorded (a crash) is not repeated.
// The outcome — paid, declined, provider unavailable — is a reply written in the same transaction as the payment's
// status; only an infrastructure failure of our own makes the handler return an error (and be retried).
func (u ProcessPayment) Execute(ctx context.Context, cmd saga.ProcessPayment) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		p, err := domain.New(domain.NewInput{ID: u.d.IDs.New(), OrderID: cmd.OrderID, UserID: cmd.UserID, Amount: cmd.Amount,
			Currency: u.d.currency(), Method: cmd.Method}, u.d.Clock.Now())
		if err != nil { // a command that cannot be a payment fails the step, so the saga compensates instead of waiting
			return u.d.reply(ctx, saga.PaymentFailed{OrderID: cmd.OrderID, Reason: ReasonInvalidRequest})
		}
		inserted, err := u.d.Payments.Insert(ctx, p)
		if err != nil {
			return err
		}
		if inserted {
			return u.charge(ctx, p, cmd.Token)
		}
		existing, err := u.d.Payments.LockByOrder(ctx, cmd.OrderID)
		if err != nil {
			return err
		}
		return u.answer(ctx, existing, cmd.Token)
	})
}

// answer replies from a payment that already exists. Refunded and cancelled payments mean the compensation got there
// first (the saga timed out): the late command is refused so nobody is charged for a dead order.
func (u ProcessPayment) answer(ctx context.Context, p *domain.Payment, token string) error {
	switch p.Status() {
	case domain.StatusSucceeded:
		return u.d.reply(ctx, saga.PaymentCompleted{OrderID: p.OrderID(), PaymentID: p.ID()})
	case domain.StatusFailed:
		return u.d.reply(ctx, saga.PaymentFailed{OrderID: p.OrderID(), Reason: p.FailureReason()})
	case domain.StatusPending: // never visible outside a transaction today; if it ever is, finish the job
		return u.charge(ctx, p, token)
	default: // REFUNDED, CANCELLED
		return u.d.reply(ctx, saga.PaymentFailed{OrderID: p.OrderID(), Reason: saga.ReasonStepTimeout})
	}
}

func (u ProcessPayment) charge(ctx context.Context, p *domain.Payment, token string) error {
	now := u.d.Clock.Now()
	if p.Amount() == 0 { // discounts covered everything: nothing to ask the provider
		if err := p.Succeed("free_"+p.OrderID(), now); err != nil {
			return err
		}
		return u.settle(ctx, p)
	}
	ref, err := u.d.Provider.Charge(ctx, domain.ChargeRequest{Key: p.OrderID(), Amount: p.Amount(), Currency: p.Snapshot().Currency,
		Method: p.Snapshot().Method, Token: token})
	switch {
	case err == nil:
		err = p.Succeed(ref, now)
	case errs.CodeOf(err) == domain.CodeCardDeclined:
		err = p.Fail(saga.ReasonCardDeclined, now)
	case ctx.Err() != nil:
		return ctx.Err() // we are shutting down: leave the message to be retried, record nothing
	default: // timeout, open circuit, provider error: the card was not charged as far as we know
		err = p.Fail(saga.ReasonProviderUnavailable, now)
	}
	if err != nil {
		return err
	}
	return u.settle(ctx, p)
}

// settle stores the outcome and replies with it.
func (u ProcessPayment) settle(ctx context.Context, p *domain.Payment) error {
	if err := u.d.Payments.Save(ctx, p); err != nil {
		return err
	}
	if p.Status() == domain.StatusSucceeded {
		return u.d.reply(ctx, saga.PaymentCompleted{OrderID: p.OrderID(), PaymentID: p.ID()})
	}
	return u.d.reply(ctx, saga.PaymentFailed{OrderID: p.OrderID(), Reason: p.FailureReason()})
}

// RefundPayment is the compensation of step 3.
type RefundPayment struct{ d *Deps }

// Execute undoes whatever the order's payment did. It always ends in PaymentRefunded unless the provider cannot be
// reached for a refund, in which case it fails (and is retried): a charge must never be left standing by a
// compensation that gave up.
//   - succeeded: ask the provider to refund, mark REFUNDED
//   - not charged yet or never created: record a CANCELLED tombstone so a late ProcessPayment is refused
//   - failed, refunded, cancelled: nothing to undo
func (u RefundPayment) Execute(ctx context.Context, cmd saga.RefundPayment) error {
	return u.d.Tx.WithinTx(ctx, func(ctx context.Context) error {
		p, err := u.load(ctx, cmd.OrderID)
		if err != nil {
			return err
		}
		if p != nil {
			if err := u.compensate(ctx, p); err != nil {
				return err
			}
		}
		return u.d.reply(ctx, saga.PaymentRefunded{OrderID: cmd.OrderID})
	})
}

// load returns the order's payment locked, or nil after leaving a tombstone for an order that has none.
func (u RefundPayment) load(ctx context.Context, orderID string) (*domain.Payment, error) {
	p, err := u.d.Payments.LockByOrder(ctx, orderID)
	if err == nil {
		return p, nil
	}
	if errs.CodeOf(err) != domain.CodePaymentNotFound {
		return nil, err
	}
	tomb, err := domain.NewCancelled(u.d.IDs.New(), orderID, u.d.currency(), u.d.Clock.Now())
	if err != nil {
		return nil, err
	}
	inserted, err := u.d.Payments.Insert(ctx, tomb)
	if err != nil {
		return nil, err
	}
	if inserted {
		return nil, nil
	}
	return u.d.Payments.LockByOrder(ctx, orderID) // a charge was recorded in between: undo that one
}

func (u RefundPayment) compensate(ctx context.Context, p *domain.Payment) error {
	now := u.d.Clock.Now()
	switch {
	case p.NeedsRefund():
		if p.Amount() > 0 {
			err := u.d.Provider.Refund(ctx, domain.RefundRequest{Key: p.OrderID(), ProviderRef: p.ProviderRef(), Amount: p.Amount(), Currency: p.Snapshot().Currency})
			if err != nil {
				return err
			}
		}
		if err := p.Refund(now); err != nil {
			return err
		}
	case p.Status() == domain.StatusPending:
		if err := p.Cancel(now); err != nil {
			return err
		}
	default:
		return nil
	}
	return u.d.Payments.Save(ctx, p)
}
