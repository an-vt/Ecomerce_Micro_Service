// Package domain holds the inventory bounded context: stock per product and the reservations that hold stock for
// orders in progress. It knows nothing about PostgreSQL, gRPC or Kafka (enforced by depguard, docs/03 §2).
package domain

import "github.com/an-vt/ecom/platform-go/errs"

// Domain error codes (docs/05 §2.1).
const (
	CodeInventoryNotFound = "INVENTORY_NOT_FOUND"
	CodeInsufficientStock = "INSUFFICIENT_STOCK"
	CodeNotOwner          = "NOT_OWNER"
	CodeInvalidQuantity   = "INVALID_QUANTITY"
	CodeInvalidState      = "INVALID_STATE"
	CodeNothingReserved   = "NOTHING_RESERVED"
)

// CodeAlreadyReserved is returned when an order tries to reserve the same product twice.
const CodeAlreadyReserved = "ALREADY_RESERVED"

// ErrAlreadyReserved is returned by the reservation store on a duplicate (order, product).
func ErrAlreadyReserved() error {
	return errs.AlreadyExists(CodeAlreadyReserved, "this order already holds stock for the product")
}

// ErrInventoryNotFound is returned when a product has no stock record.
func ErrInventoryNotFound() error {
	return errs.NotFound(CodeInventoryNotFound, "no stock record for this product")
}

// ErrNotOwner is returned when a shop touches another shop's stock.
func ErrNotOwner() error {
	return errs.PermissionDenied(CodeNotOwner, "this product belongs to another shop")
}

// InsufficientStockError says which product ran short.
type InsufficientStockError struct {
	ProductID            string
	Requested, Available int64
}

func (e *InsufficientStockError) Error() string {
	return "insufficient stock for " + e.ProductID
}

// ErrInsufficientStock builds the error for a shortage.
func ErrInsufficientStock(productID string, requested, available int64) error {
	return &InsufficientStockError{ProductID: productID, Requested: requested, Available: available}
}

// Unwrap lets errs.KindOf / CodeOf classify the shortage as a failed precondition.
func (e *InsufficientStockError) Unwrap() error {
	return errs.FailedPrecondition(CodeInsufficientStock, e.Error())
}
