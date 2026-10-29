package http

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"

	"github.com/an-vt/ecom/platform-go/idgen"
)

// A panic in any handler must become a 500 envelope without leaking details, and the server must keep serving.
func TestRecovererTurnsAPanicIntoAnErrorEnvelope(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(requestID(idgen.ULID{}), recoverer())
	r.GET("/boom", func(*gin.Context) { panic("secret internal detail: db password is hunter2") })
	r.GET("/fine", func(c *gin.Context) { c.String(200, "ok") })

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/boom", nil))
	assert.Equal(t, 500, rec.Code)
	assert.Contains(t, rec.Body.String(), `"error":"INTERNAL"`)
	assert.NotContains(t, rec.Body.String(), "hunter2")
	assert.True(t, strings.Contains(rec.Body.String(), `"requestId"`), "the request id lets support find the log line")

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/fine", nil))
	assert.Equal(t, 200, rec.Code, "the next request is served normally")
}
