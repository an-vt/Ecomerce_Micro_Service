// Package domain holds the catalog bounded context: products (with a strategy per type), SPU/SKU and comments.
// It knows nothing about MongoDB, Redis, gRPC or Kafka (enforced by depguard, docs/03 §2).
package domain

import "github.com/an-vt/ecom/platform-go/errs"

// Domain error codes (docs/05 §2.1).
const (
	CodeProductNotFound  = "PRODUCT_NOT_FOUND"
	CodeProductNameTaken = "PRODUCT_NAME_TAKEN"
	CodeInvalidType      = "INVALID_PRODUCT_TYPE"
	CodeInvalidAttribute = "INVALID_ATTRIBUTES"
	CodeNotOwner         = "NOT_OWNER"
	CodeSpuNotFound      = "SPU_NOT_FOUND"
	CodeSpuNameTaken     = "SPU_NAME_TAKEN"
	CodeSkuNotFound      = "SKU_NOT_FOUND"
	CodeInvalidSku       = "INVALID_SKU"
	CodeCommentNotFound  = "COMMENT_NOT_FOUND"
	CodeForbidden        = "FORBIDDEN"
)

// ErrProductNotFound is returned by lookups.
func ErrProductNotFound() error { return errs.NotFound(CodeProductNotFound, "product not found") }

// ErrProductNameTaken is returned when a product with the same name exists.
func ErrProductNameTaken() error {
	return errs.AlreadyExists(CodeProductNameTaken, "a product with this name already exists")
}

// ErrNotOwner is returned when a shop touches another shop's product.
func ErrNotOwner() error {
	return errs.PermissionDenied(CodeNotOwner, "this product belongs to another shop")
}

// ErrSpuNotFound is returned by SPU lookups.
func ErrSpuNotFound() error { return errs.NotFound(CodeSpuNotFound, "spu not found") }

// ErrSpuNameTaken is returned when an SPU with the same name exists.
func ErrSpuNameTaken() error {
	return errs.AlreadyExists(CodeSpuNameTaken, "a spu with this name already exists")
}

// ErrSkuNotFound is returned by SKU lookups.
func ErrSkuNotFound() error { return errs.NotFound(CodeSkuNotFound, "sku not found") }

// ErrCommentNotFound is returned by comment lookups.
func ErrCommentNotFound() error { return errs.NotFound(CodeCommentNotFound, "comment not found") }
