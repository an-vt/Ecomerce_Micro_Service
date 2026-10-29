// Package domain holds the payment bounded context: the payment of one order, its life cycle, and the port to the
// payment provider. It knows nothing about PostgreSQL, Kafka or any provider SDK (enforced by depguard, docs/03 §2).
package domain

import "github.com/an-vt/ecom/platform-go/errs"

// Domain error codes.
const (
	CodePaymentNotFound = "PAYMENT_NOT_FOUND"
	CodeInvalidPayment  = "INVALID_PAYMENT"
	CodeInvalidState    = "INVALID_STATE"
	CodeCardDeclined    = "CARD_DECLINED"
)

// ErrNotFound is returned when an order has no payment record.
func ErrNotFound() error {
	return errs.NotFound(CodePaymentNotFound, "no payment for this order")
}

// ErrCardDeclined is what a provider returns when it refuses the payment method. It is a business answer, not a
// failure of the provider, so it never trips the circuit breaker.
func ErrCardDeclined() error {
	return errs.FailedPrecondition(CodeCardDeclined, "the payment method was declined")
}

func invalid(msg string) error { return errs.InvalidArgument(CodeInvalidPayment, msg) }

func badState(msg string) error { return errs.FailedPrecondition(CodeInvalidState, msg) }
