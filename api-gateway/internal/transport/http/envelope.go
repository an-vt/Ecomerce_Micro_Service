// Package http is the public REST API (docs/05-api-design.md §1): Gin handlers, middleware and guards. It talks to
// the services only through the generated gRPC client interfaces handed to it, so it is tested with stub servers.
package http

import (
	"errors"
	"log/slog"
	stdhttp "net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/logger"
)

type successBody struct {
	Message  string `json:"message"`
	Status   int    `json:"status"`
	Metadata any    `json:"metadata,omitempty"`
}

type errorBody struct {
	Status    string `json:"status"` // always "error"
	Code      int    `json:"code"`
	Error     string `json:"error"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

// ok writes the success envelope kept from the monolith's SuccessResponse.
func ok(c *gin.Context, status int, message string, metadata any) {
	c.JSON(status, successBody{Message: message, Status: status, Metadata: metadata})
}

// fail maps any error to the error envelope: the kind decides the HTTP status (docs/05 §1.3), an open circuit
// breaker adds Retry-After, and internal details are never exposed.
func fail(c *gin.Context, err error) {
	kind := errs.KindOf(err)
	status := errs.HTTPStatus(kind)
	if after, open := breaker.RetryAfter(err); open {
		secs := int(after.Seconds())
		if secs < 1 {
			secs = 1
		}
		c.Header("Retry-After", strconv.Itoa(secs))
		status = stdhttp.StatusServiceUnavailable
	}
	if kind == errs.KindInternal && !errors.Is(err, errBadRequest) {
		slog.Default().ErrorContext(c.Request.Context(), "request failed", "error", err.Error(), "path", c.FullPath())
	}
	c.AbortWithStatusJSON(status, errorBody{
		Status: "error", Code: status, Error: errs.CodeOf(err), Message: errs.MessageOf(err),
		RequestID: logger.RequestID(c.Request.Context()),
	})
}

var errBadRequest = errors.New("bad request")

// badRequest reports a malformed body or parameter.
func badRequest(c *gin.Context, msg string) { fail(c, errs.Validation(msg)) }
