package domain

import (
	"strings"
	"time"
)

// Status of a payment.
//
//	PENDING ──charge ok──▶ SUCCEEDED ──refund──▶ REFUNDED
//	   │
//	   ├──charge refused──▶ FAILED
//	   └──compensation first──▶ CANCELLED
//
// A CANCELLED payment is a tombstone: the saga undid the step before it ran (or while it was in flight), so a
// ProcessPayment that arrives late must not charge anyone.
type Status string

const (
	StatusPending   Status = "PENDING"
	StatusSucceeded Status = "SUCCEEDED"
	StatusFailed    Status = "FAILED"
	StatusRefunded  Status = "REFUNDED"
	StatusCancelled Status = "CANCELLED"
)

// Payment is the aggregate: one per order (the store enforces it), which is what makes charging idempotent.
type Payment struct{ s Snapshot }

// Snapshot is the persistable state.
type Snapshot struct {
	ID            string
	OrderID       string
	UserID        string
	Amount        int64
	Currency      string
	Method        string
	Status        Status
	ProviderRef   string
	FailureReason string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// NewInput are the fields of a charge request.
type NewInput struct {
	ID, OrderID, UserID string
	Amount              int64 // minor units; 0 is possible when discounts cover the whole order
	Currency            string
	Method              string
}

// New starts a payment in PENDING.
func New(in NewInput, now time.Time) (*Payment, error) {
	switch {
	case in.ID == "" || in.OrderID == "":
		return nil, invalid("a payment needs an id and an order")
	case in.UserID == "":
		return nil, invalid("a payment needs a user")
	case in.Amount < 0:
		return nil, invalid("the amount must not be negative")
	case len(in.Currency) != 3:
		return nil, invalid("the currency must be a 3-letter code")
	case in.Amount > 0 && strings.TrimSpace(in.Method) == "":
		return nil, invalid("a payment method is required")
	}
	return &Payment{s: Snapshot{ID: in.ID, OrderID: in.OrderID, UserID: in.UserID, Amount: in.Amount, Currency: strings.ToUpper(in.Currency),
		Method: strings.TrimSpace(in.Method), Status: StatusPending, CreatedAt: now, UpdatedAt: now}}, nil
}

// NewCancelled records that the order's payment was compensated before any charge was attempted.
func NewCancelled(id, orderID, currency string, now time.Time) (*Payment, error) {
	if id == "" || orderID == "" || len(currency) != 3 {
		return nil, invalid("a payment needs an id, an order and a currency")
	}
	return &Payment{s: Snapshot{ID: id, OrderID: orderID, Currency: strings.ToUpper(currency), Status: StatusCancelled, CreatedAt: now, UpdatedAt: now}}, nil
}

// Restore rebuilds a payment from storage.
func Restore(s Snapshot) *Payment { return &Payment{s: s} }

// Snapshot returns the persistable state.
func (p *Payment) Snapshot() Snapshot { return p.s }

func (p *Payment) ID() string          { return p.s.ID }
func (p *Payment) OrderID() string     { return p.s.OrderID }
func (p *Payment) Status() Status      { return p.s.Status }
func (p *Payment) Amount() int64       { return p.s.Amount }
func (p *Payment) ProviderRef() string { return p.s.ProviderRef }
func (p *Payment) FailureReason() string {
	return p.s.FailureReason
}

// Succeed records the provider's confirmation.
func (p *Payment) Succeed(providerRef string, now time.Time) error {
	if p.s.Status != StatusPending {
		return badState("only a pending payment can succeed, this one is " + string(p.s.Status))
	}
	p.s.Status, p.s.ProviderRef, p.s.UpdatedAt = StatusSucceeded, providerRef, now
	return nil
}

// Fail records why the charge did not happen.
func (p *Payment) Fail(reason string, now time.Time) error {
	if p.s.Status != StatusPending {
		return badState("only a pending payment can fail, this one is " + string(p.s.Status))
	}
	p.s.Status, p.s.FailureReason, p.s.UpdatedAt = StatusFailed, reason, now
	return nil
}

// Refund records that the provider returned the money.
func (p *Payment) Refund(now time.Time) error {
	if p.s.Status != StatusSucceeded {
		return badState("only a succeeded payment can be refunded, this one is " + string(p.s.Status))
	}
	p.s.Status, p.s.UpdatedAt = StatusRefunded, now
	return nil
}

// Cancel turns a payment that has not been charged into a tombstone.
func (p *Payment) Cancel(now time.Time) error {
	if p.s.Status != StatusPending {
		return badState("only a pending payment can be cancelled, this one is " + string(p.s.Status))
	}
	p.s.Status, p.s.UpdatedAt = StatusCancelled, now
	return nil
}

// NeedsRefund reports whether compensating this payment means giving money back.
func (p *Payment) NeedsRefund() bool { return p.s.Status == StatusSucceeded }
