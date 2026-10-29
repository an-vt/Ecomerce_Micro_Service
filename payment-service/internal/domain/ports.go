package domain

import "context"

// PaymentRepository stores payments. The transaction comes from ctx (platform-go/pgdb).
type PaymentRepository interface {
	// Insert stores a new payment unless the order already has one, and reports whether it did. When another
	// transaction is inserting the same order, it waits for that transaction to finish first.
	Insert(ctx context.Context, p *Payment) (inserted bool, err error)
	// LockByOrder loads the payment of an order and locks it until the end of the transaction; ErrNotFound when absent.
	LockByOrder(ctx context.Context, orderID string) (*Payment, error)
	// Save writes the status fields of a payment loaded with LockByOrder (or inserted by this transaction).
	Save(ctx context.Context, p *Payment) error
}

// ChargeRequest asks the provider to take money. Key makes the call idempotent: asking twice with the same key never
// charges twice (the order id is the key).
type ChargeRequest struct {
	Key      string
	Amount   int64
	Currency string
	Method   string
	Token    string
}

// RefundRequest asks the provider to return a charge.
type RefundRequest struct {
	Key         string
	ProviderRef string
	Amount      int64
	Currency    string
}

// Provider is the payment provider (a port; the mock adapter implements it). A refusal of the payment method is
// reported as ErrCardDeclined; any other error means the provider could not be used and the outcome is unknown.
type Provider interface {
	Charge(ctx context.Context, r ChargeRequest) (providerRef string, err error)
	Refund(ctx context.Context, r RefundRequest) error
}
