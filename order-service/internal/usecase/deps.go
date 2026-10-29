package usecase

import (
	"context"
	"time"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

// Deps is everything the use cases need.
type Deps struct {
	Orders    domain.OrderRepository
	Sagas     domain.SagaRepository
	Keys      domain.IdempotencyRepository
	Catalog   domain.Catalog
	Discounts domain.Discounts
	Tx        TxManager
	Outbox    Outbox
	Clock     clock.Clock
	IDs       idgen.Generator
	Metrics   SagaMetrics
	Timeouts  domain.Timeouts
	FeeShip   int64 // flat shipping fee per order (shipping is out of scope, docs/01)
}

func (d *Deps) metrics() SagaMetrics {
	if d.Metrics == nil {
		return nopMetrics{}
	}
	return d.Metrics
}

func (d *Deps) timeouts() domain.Timeouts {
	t := d.Timeouts
	if t.Step <= 0 {
		t.Step = 30 * time.Second
	}
	if t.Compensation <= 0 {
		t.Compensation = 30 * time.Second
	}
	return t
}

func userCaller(ctx context.Context) (principal.Principal, error) {
	p, ok := principal.From(ctx)
	if !ok {
		return principal.Principal{}, errs.Unauthenticated("UNAUTHENTICATED", "this call needs an authenticated caller")
	}
	if !p.IsUser() {
		return principal.Principal{}, errs.PermissionDenied("FORBIDDEN", "only buyers can place and view orders")
	}
	return p, nil
}

// topicOf maps a saga command to the topic of the service that executes it.
func topicOf(kind string) string {
	switch kind {
	case saga.CmdApplyDiscounts, saga.CmdReleaseDiscount:
		return saga.TopicDiscountCommands
	case saga.CmdProcessPayment, saga.CmdRefundPayment:
		return saga.TopicPaymentCommands
	default:
		return saga.TopicInventoryCommands
	}
}

// commandFor builds the message for a command kind from the order. The saga only decides which commands to send; what
// they contain (items, the amount to charge after discounts, the payment token) comes from the order.
func commandFor(kind string, o *domain.Order) saga.Message {
	s := o.Snapshot()
	switch kind {
	case saga.CmdReserveStock:
		return saga.ReserveStock{OrderID: s.ID, Items: sagaItems(s.Items)}
	case saga.CmdApplyDiscounts:
		refs := make([]saga.DiscountRequest, len(s.Discounts))
		for i, d := range s.Discounts {
			refs[i] = saga.DiscountRequest{ShopID: d.ShopID, Code: d.Code}
		}
		return saga.ApplyDiscounts{OrderID: s.ID, UserID: s.UserID, Discounts: refs, Items: sagaItems(s.Items)}
	case saga.CmdProcessPayment:
		return saga.ProcessPayment{OrderID: s.ID, UserID: s.UserID, Amount: s.TotalCheckout, Method: s.PaymentMethod, Token: s.PaymentToken}
	case saga.CmdCommitStock:
		return saga.CommitStock{OrderID: s.ID}
	case saga.CmdReleaseStock:
		return saga.ReleaseStock{OrderID: s.ID}
	case saga.CmdReleaseDiscount:
		return saga.ReleaseDiscounts{OrderID: s.ID}
	default: // saga.CmdRefundPayment
		return saga.RefundPayment{OrderID: s.ID}
	}
}

func sagaItems(items []domain.Item) []saga.Item {
	out := make([]saga.Item, len(items))
	for i, it := range items {
		out[i] = saga.Item{ProductID: it.ProductID, ShopID: it.ShopID, Quantity: it.Quantity, Price: it.Price}
	}
	return out
}

func (d *Deps) add(ctx context.Context, topic string, env event.Envelope) error {
	msg, err := outbox.New(ctx, topic, env)
	if err != nil {
		return err
	}
	return d.Outbox.Add(ctx, msg)
}

// send puts the commands in the outbox of the current transaction, keyed by the order id so a command and its
// compensation reach a participant in order.
func (d *Deps) send(ctx context.Context, o *domain.Order, kinds []string) error {
	for _, kind := range kinds {
		env, err := saga.Envelope(d.IDs.New(), d.Clock.Now(), commandFor(kind, o))
		if err != nil {
			return err
		}
		if err := d.add(ctx, topicOf(kind), env); err != nil {
			return err
		}
	}
	return nil
}

func (d *Deps) emit(ctx context.Context, topic string, ev event.Event) error {
	env, err := event.FromEvent(d.IDs.New(), d.Clock.Now(), ev)
	if err != nil {
		return err
	}
	return d.add(ctx, topic, env)
}

// commit applies a saga result to the order and persists everything in the current transaction: the order's new
// status, the saga's new state, the commands to send and, when the checkout ended, the order event. Either all of it
// is stored or none of it, so a crash can never leave a command unsent or an event unannounced.
func (d *Deps) commit(ctx context.Context, sg *domain.Saga, o *domain.Order, res domain.Result) error {
	now := d.Clock.Now()
	changed := res.TotalDiscount != nil
	if res.TotalDiscount != nil {
		if err := o.ApplyDiscount(*res.TotalDiscount, now); err != nil {
			return err
		}
	}
	for _, step := range res.Orders {
		var err error
		if step.To == domain.StatusCancelling {
			err = o.StartCancelling(step.Reason, now)
		} else {
			err = o.Advance(step.To, now)
		}
		if err != nil {
			return err
		}
		changed = true
	}
	if changed {
		if err := d.Orders.Save(ctx, o); err != nil {
			return err
		}
	}
	if err := d.Sagas.Save(ctx, sg); err != nil {
		return err
	}
	if err := d.send(ctx, o, res.Commands); err != nil {
		return err
	}
	if err := d.announce(ctx, sg, o, res); err != nil {
		return err
	}
	d.report(sg, res, now)
	return nil
}

// announce publishes the order event of a finished checkout.
func (d *Deps) announce(ctx context.Context, sg *domain.Saga, o *domain.Order, res domain.Result) error {
	s := o.Snapshot()
	switch res.Finished {
	case domain.StateCompleted:
		items := make([]domain.ConfirmedItem, len(s.Items))
		for i, it := range s.Items {
			items[i] = domain.ConfirmedItem{ProductID: it.ProductID, Quantity: it.Quantity}
		}
		return d.emit(ctx, domain.TopicConfirmed, domain.OrderConfirmed{OrderID: s.ID, UserID: s.UserID, Items: items, TotalCheckout: s.TotalCheckout})
	case domain.StateFailed:
		return d.emit(ctx, domain.TopicCancelled, domain.OrderCancelled{OrderID: s.ID, UserID: s.UserID, Reason: s.CancelReason})
	}
	return nil
}

func (d *Deps) report(sg *domain.Saga, res domain.Result, now time.Time) {
	m := d.metrics()
	if res.TimedOut != "" {
		m.StepTimeout(res.TimedOut)
	}
	if res.Compensating || (res.Resent && res.TimedOut == domain.StateCompensating) {
		for _, c := range res.Commands {
			m.Compensation(c)
		}
	}
	if res.Finished != "" {
		m.Finished(res.Finished, sg.Snapshot().Reason, now.Sub(sg.Snapshot().CreatedAt))
	}
}
