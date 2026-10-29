// Package domain holds the auth bounded context: accounts, sessions, one-time tokens, API keys and RBAC.
// It has no knowledge of databases, gRPC or Kafka (enforced by depguard, docs/03 §2).
package domain

import "github.com/an-vt/ecom/platform-go/errs"

// Domain error codes (docs/05 §2.1). Constructors below give each one its kind, which decides the gRPC and HTTP
// status; the code is what clients and tests match on.
const (
	CodeEmailTaken         = "EMAIL_TAKEN"
	CodeInvalidCredentials = "INVALID_CREDENTIALS"
	CodeAccountInactive    = "ACCOUNT_INACTIVE"
	CodePasswordNotSet     = "PASSWORD_NOT_SET"
	CodeTokenInvalid       = "TOKEN_INVALID"
	CodeTokenReused        = "TOKEN_REUSED"
	CodeAccountNotFound    = "ACCOUNT_NOT_FOUND"
	CodeRoleExists         = "ROLE_EXISTS"
	CodeResourceExists     = "RESOURCE_EXISTS"
	CodeResourceNotFound   = "RESOURCE_NOT_FOUND"
	CodeInvalidEmail       = "INVALID_EMAIL"
	CodeWeakPassword       = "WEAK_PASSWORD"
)

func errEmailTaken() *errs.Error {
	return errs.AlreadyExists(CodeEmailTaken, "an account with this email already exists")
}
func errInvalidCredentials() *errs.Error {
	return errs.Unauthenticated(CodeInvalidCredentials, "email or password is incorrect")
}
func errAccountInactive() *errs.Error {
	return errs.PermissionDenied(CodeAccountInactive, "this account cannot sign in")
}
func errPasswordNotSet() *errs.Error {
	return errs.FailedPrecondition(CodePasswordNotSet, "this account has no password yet; set one first")
}
func errTokenInvalid() *errs.Error {
	return errs.Unauthenticated(CodeTokenInvalid, "the token is invalid or has expired")
}
func errTokenReused() *errs.Error {
	return errs.PermissionDenied(CodeTokenReused, "this refresh token was already used; the session has been revoked")
}

// Constructors used by repositories, which translate storage failures into domain errors.

// ErrEmailTaken is returned when an account with the same (type, email) exists.
func ErrEmailTaken() error { return errEmailTaken() }

// ErrInvalidCredentials is the single error for an unknown email and a wrong password (no user enumeration).
func ErrInvalidCredentials() error { return errInvalidCredentials() }

// ErrAccountNotFound is returned by lookups.
func ErrAccountNotFound() error { return errs.NotFound(CodeAccountNotFound, "account not found") }

// ErrTokenInvalid covers unknown, expired, used and revoked tokens alike.
func ErrTokenInvalid() error { return errTokenInvalid() }

// ErrTokenReused is returned when an already-rotated refresh token is presented again.
func ErrTokenReused() error { return errTokenReused() }
