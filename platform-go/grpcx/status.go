// Package grpcx holds the gRPC server and client plumbing shared by all services (docs/09, docs/05 §2.1).
package grpcx

import (
	"errors"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/an-vt/ecom/platform-go/errs"
)

// ErrorDomain tags the ErrorInfo detail that carries the domain error code.
const ErrorDomain = "ecom"

var kindToCode = map[errs.Kind]codes.Code{
	errs.KindInternal:           codes.Internal,
	errs.KindInvalidArgument:    codes.InvalidArgument,
	errs.KindNotFound:           codes.NotFound,
	errs.KindAlreadyExists:      codes.AlreadyExists,
	errs.KindUnauthenticated:    codes.Unauthenticated,
	errs.KindPermissionDenied:   codes.PermissionDenied,
	errs.KindFailedPrecondition: codes.FailedPrecondition,
	errs.KindAborted:            codes.Aborted,
	errs.KindResourceExhausted:  codes.ResourceExhausted,
	errs.KindUnavailable:        codes.Unavailable,
	errs.KindDeadlineExceeded:   codes.DeadlineExceeded,
}

// CodeOf maps an error kind to its gRPC code.
func CodeOf(k errs.Kind) codes.Code {
	if c, ok := kindToCode[k]; ok {
		return c
	}
	return codes.Internal
}

// KindOf maps a gRPC code back to an error kind (codes without a counterpart become Internal).
func KindOf(c codes.Code) errs.Kind {
	for k, code := range kindToCode {
		if code == c {
			return k
		}
	}
	return errs.KindInternal
}

// ToStatus converts any error to a gRPC status error. *errs.Error keeps its kind and code (as ErrorInfo.Reason);
// internal details are never sent to the caller. Existing status errors pass through.
func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	if _, ok := status.FromError(err); ok && !isWrappedDomain(err) {
		return err
	}
	kind, code, msg := errs.KindOf(err), errs.CodeOf(err), errs.MessageOf(err)
	st := status.New(CodeOf(kind), msg)
	if withDetails, derr := st.WithDetails(&errdetails.ErrorInfo{Reason: code, Domain: ErrorDomain}); derr == nil {
		st = withDetails
	}
	return st.Err()
}

func isWrappedDomain(err error) bool {
	_, ok := errs.As(err)
	return ok
}

// FromStatus converts a gRPC error back into an *errs.Error with the original domain code, so callers can use
// errs.KindOf / errs.CodeOf on errors that crossed the wire. Non-status errors pass through.
func FromStatus(err error) error {
	if err == nil {
		return nil
	}
	var existing *errs.Error
	if errors.As(err, &existing) {
		return err
	}
	st, ok := status.FromError(err)
	if !ok {
		return err
	}
	kind := KindOf(st.Code())
	code := defaultCode(kind)
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok && info.GetDomain() == ErrorDomain && info.GetReason() != "" {
			code = info.GetReason()
			break
		}
	}
	return errs.Wrap(err, kind, code, st.Message())
}

func defaultCode(k errs.Kind) string {
	switch k {
	case errs.KindUnavailable:
		return errs.CodeUnavailable
	case errs.KindNotFound:
		return errs.CodeNotFound
	case errs.KindInvalidArgument:
		return errs.CodeValidationFailed
	}
	return k.String()
}

// IsInfraFailure says whether a gRPC error means "the dependency is unhealthy" and should count towards opening
// a circuit breaker (docs/09 §1.1). Business errors and caller cancellations do not.
func IsInfraFailure(err error) bool {
	if err == nil {
		return false
	}
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.Internal, codes.Unknown, codes.ResourceExhausted, codes.DataLoss:
		return true
	}
	return false
}
