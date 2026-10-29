package http

import (
	"fmt"
	"log/slog"
	stdhttp "net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/redisx"
)

// requestID adopts a well-formed incoming x-request-id or creates one, returns it and puts it in the context.
func requestID(ids idgen.Generator) gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(event.HeaderRequestID)
		if id == "" || len(id) > 64 || strings.ContainsAny(id, " \r\n\t") {
			id = ids.New()
		}
		c.Header(event.HeaderRequestID, id)
		c.Request = c.Request.WithContext(logger.WithRequestID(c.Request.Context(), id))
		c.Next()
	}
}

// recoverer turns a panic into a 500 envelope and logs the stack.
func recoverer() gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if r := recover(); r != nil {
				slog.Default().ErrorContext(c.Request.Context(), "panic in handler", "panic", fmt.Sprint(r), "stack", string(debug.Stack()), "path", c.Request.URL.Path)
				fail(c, errs.Internal(fmt.Errorf("panic: %v", r), "handler panicked"))
			}
		}()
		c.Next()
	}
}

// accessLog writes one structured line per request, using the route template (low cardinality).
func accessLog() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		slog.Default().InfoContext(c.Request.Context(), "http request",
			"method", c.Request.Method, "route", route, "status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(), "ip", c.ClientIP())
	}
}

// httpMetrics records http_server_duration_seconds{method,route,status}.
func httpMetrics(reg prometheus.Registerer) gin.HandlerFunc {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "http_server_duration_seconds", Help: "REST request handling time.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"method", "route", "status"})
	reg.MustRegister(h)
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		h.WithLabelValues(c.Request.Method, route, strconv.Itoa(c.Writer.Status())).Observe(time.Since(start).Seconds())
	}
}

func securityHeaders(secure bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		if secure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

// cors allows only the configured origins (the monolith hard-coded http://localhost:5173) and answers preflights.
func cors(origins []string) gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" && slices.Contains(origins, origin) {
			h := c.Writer.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Credentials", "true")
			h.Add("Vary", "Origin")
			if c.Request.Method == stdhttp.MethodOptions {
				h.Set("Access-Control-Allow-Methods", "GET,POST,PATCH,PUT,DELETE,OPTIONS")
				h.Set("Access-Control-Allow-Headers", "authorization,content-type,x-api-key,idempotency-key,x-request-id")
				h.Set("Access-Control-Max-Age", "600")
				c.AbortWithStatus(stdhttp.StatusNoContent)
				return
			}
		}
		c.Next()
	}
}

func bodyLimit(max int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > max {
			fail(c, errs.New(errs.KindInvalidArgument, "PAYLOAD_TOO_LARGE", "request body is too large"))
			return
		}
		c.Request.Body = stdhttp.MaxBytesReader(c.Writer, c.Request.Body, max)
		c.Next()
	}
}

// rateLimit allows limit requests per window and client IP, per bucket name. If Redis is unavailable the request
// is let through (and logged): rate limiting is protection, not a gate that may take the API down.
// newRateLimitedMetric registers rate_limited_total{route}: requests refused with 429 (docs/14), the signal of scraping or
// credential stuffing.
func newRateLimitedMetric(reg prometheus.Registerer) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "rate_limited_total", Help: "Requests refused by the rate limiter."}, []string{"route"})
	reg.MustRegister(c)
	return c
}

func rateLimit(l *redisx.Limiter, bucket string, limit int, window time.Duration, limited *prometheus.CounterVec) gin.HandlerFunc {
	return func(c *gin.Context) {
		res, err := l.Allow(c.Request.Context(), bucket+":"+c.ClientIP(), limit, window)
		if err != nil {
			slog.Default().WarnContext(c.Request.Context(), "rate limiter unavailable, letting the request through", "error", err.Error())
			c.Next()
			return
		}
		// Several limiters can apply to one route (general + strict). The headers describe the tightest one.
		if prev, err := strconv.Atoi(c.Writer.Header().Get("X-RateLimit-Remaining")); err != nil || res.Remaining < prev {
			c.Header("X-RateLimit-Limit", strconv.Itoa(limit))
			c.Header("X-RateLimit-Remaining", strconv.Itoa(res.Remaining))
		}
		if !res.Allowed {
			secs := int(res.RetryAfter.Seconds()) + 1
			c.Header("Retry-After", strconv.Itoa(secs))
			route := c.FullPath()
			if route == "" {
				route = "unmatched"
			}
			limited.WithLabelValues(route).Inc()
			fail(c, errs.ResourceExhausted("RATE_LIMITED", "too many requests, slow down"))
			return
		}
		c.Next()
	}
}
