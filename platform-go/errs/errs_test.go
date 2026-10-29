package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
)

func TestErrorStringAndUnwrap(t *testing.T) {
	cause := errors.New("boom")
	tests := []struct {
		name string
		err  *errs.Error
		want string
	}{
		{"code only", &errs.Error{Code: "X"}, "X"},
		{"message", errs.New(errs.KindNotFound, "X", "missing"), "X: missing"},
		{"cause", &errs.Error{Code: "X", Cause: cause}, "X: boom"},
		{"message and cause", errs.Wrap(cause, errs.KindInternal, "X", "failed"), "X: failed: boom"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
	assert.ErrorIs(t, errs.Wrap(cause, errs.KindInternal, "X", ""), cause)
}

func TestIsMatchesByCode(t *testing.T) {
	sentinel := errs.FailedPrecondition("INSUFFICIENT_STOCK", "")
	wrapped := fmt.Errorf("reserve: %w", errs.FailedPrecondition("INSUFFICIENT_STOCK", "only 2 left"))
	assert.ErrorIs(t, wrapped, sentinel)
	assert.NotErrorIs(t, wrapped, errs.FailedPrecondition("OTHER", ""))
	assert.NotErrorIs(t, errors.New("plain"), sentinel)
}

func TestKindCodeMessage(t *testing.T) {
	e := fmt.Errorf("ctx: %w", errs.NotFound("ORDER_NOT_FOUND", "no such order"))
	assert.Equal(t, errs.KindNotFound, errs.KindOf(e))
	assert.Equal(t, "ORDER_NOT_FOUND", errs.CodeOf(e))
	assert.Equal(t, "no such order", errs.MessageOf(e))

	plain := errors.New("db password leaked")
	assert.Equal(t, errs.KindInternal, errs.KindOf(plain))
	assert.Equal(t, errs.CodeInternal, errs.CodeOf(plain))
	assert.Equal(t, "internal error", errs.MessageOf(plain), "internal details must not reach clients")
	assert.Equal(t, "internal error", errs.MessageOf(errs.Internal(plain, "secret")))
}

func TestAs(t *testing.T) {
	got, ok := errs.As(fmt.Errorf("x: %w", errs.Validation("bad")))
	require.True(t, ok)
	assert.Equal(t, errs.CodeValidationFailed, got.Code)
	_, ok = errs.As(errors.New("nope"))
	assert.False(t, ok)
}

func TestConstructorsSetKind(t *testing.T) {
	tests := []struct {
		err  *errs.Error
		kind errs.Kind
		http int
		name string
	}{
		{errs.InvalidArgument("C", ""), errs.KindInvalidArgument, 400, "INVALID_ARGUMENT"},
		{errs.Validation("m"), errs.KindInvalidArgument, 400, "INVALID_ARGUMENT"},
		{errs.Unauthenticated("C", ""), errs.KindUnauthenticated, 401, "UNAUTHENTICATED"},
		{errs.PermissionDenied("C", ""), errs.KindPermissionDenied, 403, "PERMISSION_DENIED"},
		{errs.NotFound("C", ""), errs.KindNotFound, 404, "NOT_FOUND"},
		{errs.AlreadyExists("C", ""), errs.KindAlreadyExists, 409, "ALREADY_EXISTS"},
		{errs.Aborted("C", ""), errs.KindAborted, 409, "ABORTED"},
		{errs.FailedPrecondition("C", ""), errs.KindFailedPrecondition, 422, "FAILED_PRECONDITION"},
		{errs.ResourceExhausted("C", ""), errs.KindResourceExhausted, 429, "RESOURCE_EXHAUSTED"},
		{errs.Unavailable("C", ""), errs.KindUnavailable, 503, "UNAVAILABLE"},
		{errs.New(errs.KindDeadlineExceeded, "C", ""), errs.KindDeadlineExceeded, 504, "DEADLINE_EXCEEDED"},
		{errs.Internal(nil, ""), errs.KindInternal, 500, "INTERNAL"},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.err.Code, func(t *testing.T) {
			assert.Equal(t, tt.kind, tt.err.Kind)
			assert.Equal(t, tt.http, errs.HTTPStatus(tt.kind))
			assert.Equal(t, tt.name, tt.kind.String())
		})
	}
	assert.Equal(t, "INTERNAL", errs.Kind(99).String())
	assert.Equal(t, 500, errs.HTTPStatus(errs.Kind(99)))
}

func TestIsPermanent(t *testing.T) {
	assert.True(t, errs.IsPermanent(errs.Validation("bad")))
	assert.True(t, errs.IsPermanent(fmt.Errorf("w: %w", errs.NotFound("X", ""))))
	assert.True(t, errs.IsPermanent(errs.FailedPrecondition("X", "")))
	assert.False(t, errs.IsPermanent(errs.Unavailable("X", "")), "infrastructure errors are retried")
	assert.False(t, errs.IsPermanent(errs.Aborted("X", "")), "aborted (conflict) is retryable")
	assert.False(t, errs.IsPermanent(errors.New("unclassified")), "unclassified errors are retried")
	assert.False(t, errs.IsPermanent(nil))
}
