// Package domain holds the order bounded context: the Order aggregate with its status guard, and the CheckoutSaga, the
// pure state machine that decides which command to send next and how to undo the steps already done. It knows nothing
// about PostgreSQL, gRPC or Kafka (enforced by depguard, docs/03 §2), which is what lets every branch of the saga be
// unit-tested without I/O.
package domain

import (
	"regexp"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Domain error codes (docs/05 §2.1).
const (
	CodeOrderNotFound       = "ORDER_NOT_FOUND"
	CodeInvalidOrder        = "INVALID_ORDER"
	CodeInvalidState        = "INVALID_STATE"
	CodeIdempotencyConflict = "IDEMPOTENCY_CONFLICT"
	CodeIdempotencyKey      = "IDEMPOTENCY_KEY_REQUIRED"
	CodeProductUnavailable  = "PRODUCT_NOT_AVAILABLE"
	CodeShopMismatch        = "SHOP_MISMATCH"
	CodeTooManyDiscounts    = "DISCOUNT_PER_SHOP"
)

var idPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// ValidID reports whether s can be an order id (a UUID). Anything else cannot exist, and must not reach a UUID column,
// where it would be a database error instead of a plain "not found" (a 500, or a retry loop on a poisoned message).
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ErrNotFound is returned when an order does not exist, or belongs to someone else (the two look the same on purpose).
func ErrNotFound() error { return errs.NotFound(CodeOrderNotFound, "no such order") }

// ErrIdempotencyConflict is returned when an idempotency key is reused with a different request.
func ErrIdempotencyConflict() error {
	return errs.Aborted(CodeIdempotencyConflict, "this Idempotency-Key was already used with a different request")
}

func invalid(msg string) error { return errs.InvalidArgument(CodeInvalidOrder, msg) }

func badState(msg string) error { return errs.FailedPrecondition(CodeInvalidState, msg) }

// CodeSagaNotFound is returned for a saga that does not exist (a reply for an unknown order).
const CodeSagaNotFound = "SAGA_NOT_FOUND"

// ErrSagaNotFound is returned when an order has no saga.
func ErrSagaNotFound() error {
	return errs.NotFound(CodeSagaNotFound, "no checkout saga for this order")
}
