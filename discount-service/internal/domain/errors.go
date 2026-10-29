// Package domain holds the discount bounded context: discount codes, the rules that decide whether and how much a
// code takes off an order, and the usage records that count them. It knows nothing about PostgreSQL, gRPC or Kafka
// (enforced by depguard, docs/03 §2).
package domain

import "github.com/an-vt/ecom/platform-go/errs"

// Domain error codes (docs/05 §2.1).
const (
	CodeDiscountNotFound   = "DISCOUNT_NOT_FOUND"
	CodeCodeTaken          = "DISCOUNT_CODE_TAKEN"
	CodeNotOwner           = "NOT_OWNER"
	CodeInvalidDiscount    = "INVALID_DISCOUNT"
	CodeExpired            = "DISCOUNT_EXPIRED"
	CodeNotStarted         = "DISCOUNT_NOT_STARTED"
	CodeInactive           = "DISCOUNT_INACTIVE"
	CodeUsedUp             = "DISCOUNT_USED_UP"
	CodeUserLimitReached   = "DISCOUNT_USER_LIMIT"
	CodeMinOrderNotReached = "DISCOUNT_MIN_ORDER"
	CodeNotApplicable      = "DISCOUNT_NOT_APPLICABLE"
	CodeDuplicateShop      = "DISCOUNT_DUPLICATE_SHOP"
	CodeUnknownProducts    = "UNKNOWN_PRODUCTS"
)

// ErrNotFound is returned when a code does not exist.
func ErrNotFound() error {
	return errs.NotFound(CodeDiscountNotFound, "no such discount code")
}

// ErrCodeTaken is returned when a shop reuses one of its codes.
func ErrCodeTaken() error {
	return errs.AlreadyExists(CodeCodeTaken, "this shop already has a discount with that code")
}

// ErrNotOwner is returned when a shop touches another shop's discount.
func ErrNotOwner() error {
	return errs.PermissionDenied(CodeNotOwner, "this discount belongs to another shop")
}

func invalid(msg string) error { return errs.InvalidArgument(CodeInvalidDiscount, msg) }

func notUsable(code, msg string) error { return errs.FailedPrecondition(code, msg) }

// CodeAlreadyUsed is returned when an order records a second usage of the same discount.
const CodeAlreadyUsed = "DISCOUNT_ALREADY_USED"

// ErrAlreadyUsed is returned by the usage store on a duplicate (discount, order).
func ErrAlreadyUsed() error {
	return errs.AlreadyExists(CodeAlreadyUsed, "this order already used the discount")
}
