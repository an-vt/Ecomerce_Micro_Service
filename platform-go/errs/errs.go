// Package errs defines the typed errors shared by all services (docs/05-api-design.md §2.1).
//
// An Error carries a Kind (the category, which decides the gRPC status, the HTTP status and whether a
// message consumer retries) and a Code (the stable machine-readable name, e.g. INSUFFICIENT_STOCK).
package errs

import (
	"errors"
	"fmt"
)

// Kind is the category of an error.
type Kind int

const (
	KindInternal Kind = iota
	KindInvalidArgument
	KindNotFound
	KindAlreadyExists
	KindUnauthenticated
	KindPermissionDenied
	KindFailedPrecondition
	KindAborted
	KindResourceExhausted
	KindUnavailable
	KindDeadlineExceeded
)

var kindNames = map[Kind]string{
	KindInternal:           "INTERNAL",
	KindInvalidArgument:    "INVALID_ARGUMENT",
	KindNotFound:           "NOT_FOUND",
	KindAlreadyExists:      "ALREADY_EXISTS",
	KindUnauthenticated:    "UNAUTHENTICATED",
	KindPermissionDenied:   "PERMISSION_DENIED",
	KindFailedPrecondition: "FAILED_PRECONDITION",
	KindAborted:            "ABORTED",
	KindResourceExhausted:  "RESOURCE_EXHAUSTED",
	KindUnavailable:        "UNAVAILABLE",
	KindDeadlineExceeded:   "DEADLINE_EXCEEDED",
}

func (k Kind) String() string {
	if n, ok := kindNames[k]; ok {
		return n
	}
	return "INTERNAL"
}

// Common codes used by several services; services define their own domain codes next to their domain.
const (
	CodeValidationFailed = "VALIDATION_FAILED"
	CodeNotFound         = "NOT_FOUND"
	CodeInternal         = "INTERNAL"
	CodeUnavailable      = "UPSTREAM_UNAVAILABLE"
)

// Error is the error type returned by domain and use-case code.
type Error struct {
	Kind    Kind
	Code    string
	Message string
	Cause   error
}

// New creates an Error.
func New(kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message}
}

// Wrap creates an Error that keeps cause for errors.Is / errors.As.
func Wrap(cause error, kind Kind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message, Cause: cause}
}

func (e *Error) Error() string {
	switch {
	case e.Cause != nil && e.Message != "":
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	case e.Cause != nil:
		return fmt.Sprintf("%s: %v", e.Code, e.Cause)
	case e.Message != "":
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Code
}

func (e *Error) Unwrap() error { return e.Cause }

// Is matches another *Error with the same Code, so errors.Is(err, ErrInsufficientStock) works for sentinels.
func (e *Error) Is(target error) bool {
	var t *Error
	if !errors.As(target, &t) {
		return false
	}
	return t.Code == e.Code
}

// Constructors for the common kinds.

func InvalidArgument(code, msg string) *Error    { return New(KindInvalidArgument, code, msg) }
func NotFound(code, msg string) *Error           { return New(KindNotFound, code, msg) }
func AlreadyExists(code, msg string) *Error      { return New(KindAlreadyExists, code, msg) }
func Unauthenticated(code, msg string) *Error    { return New(KindUnauthenticated, code, msg) }
func PermissionDenied(code, msg string) *Error   { return New(KindPermissionDenied, code, msg) }
func FailedPrecondition(code, msg string) *Error { return New(KindFailedPrecondition, code, msg) }
func Aborted(code, msg string) *Error            { return New(KindAborted, code, msg) }
func ResourceExhausted(code, msg string) *Error  { return New(KindResourceExhausted, code, msg) }
func Unavailable(code, msg string) *Error        { return New(KindUnavailable, code, msg) }
func Internal(cause error, msg string) *Error    { return Wrap(cause, KindInternal, CodeInternal, msg) }
func Validation(msg string) *Error               { return New(KindInvalidArgument, CodeValidationFailed, msg) }

// As returns the *Error in err's chain.
func As(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// KindOf returns the kind of err; errors that are not *Error are KindInternal.
func KindOf(err error) Kind {
	if e, ok := As(err); ok {
		return e.Kind
	}
	return KindInternal
}

// CodeOf returns the code of err; errors that are not *Error are CodeInternal.
func CodeOf(err error) string {
	if e, ok := As(err); ok {
		return e.Code
	}
	return CodeInternal
}

// MessageOf returns the client-safe message: the message of an *Error, a generic text otherwise
// (internal error details are never exposed).
func MessageOf(err error) string {
	if e, ok := As(err); ok && e.Kind != KindInternal {
		return e.Message
	}
	return "internal error"
}

// HTTPStatus maps a kind to the HTTP status used by the gateway (docs/05 §1.3).
func HTTPStatus(k Kind) int {
	switch k {
	case KindInvalidArgument:
		return 400
	case KindUnauthenticated:
		return 401
	case KindPermissionDenied:
		return 403
	case KindNotFound:
		return 404
	case KindAlreadyExists, KindAborted:
		return 409
	case KindFailedPrecondition:
		return 422
	case KindResourceExhausted:
		return 429
	case KindUnavailable:
		return 503
	case KindDeadlineExceeded:
		return 504
	}
	return 500
}

// IsPermanent reports whether retrying the same input cannot succeed (business rejections). Message consumers
// send permanent failures straight to the dead-letter topic (docs/08 §5). Unclassified errors are retryable.
func IsPermanent(err error) bool {
	switch KindOf(err) {
	case KindInvalidArgument, KindNotFound, KindAlreadyExists, KindUnauthenticated,
		KindPermissionDenied, KindFailedPrecondition:
		return true
	}
	return false
}
