// Package logger builds the service's slog logger: JSON to stdout, secrets redacted, and trace / request ids
// added to every record from the context (docs/14-observability.md §1).
package logger

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"go.opentelemetry.io/otel/trace"
)

type ctxKey struct{}

// WithRequestID stores the request id in ctx so that every log line written with that ctx carries it.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// RequestID returns the request id stored in ctx, or "".
func RequestID(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Options configures New.
type Options struct {
	Service string
	Level   string    // debug | info | warn | error (default info)
	Writer  io.Writer // default os.Stdout
	// Redact lists extra attribute keys to mask (case-insensitive) on top of DefaultRedacted.
	Redact []string
}

// DefaultRedacted are masked everywhere (docs/14 §1).
var DefaultRedacted = []string{"authorization", "cookie", "set-cookie", "password", "token", "x-api-key", "payment.token", "refresh_token", "access_token", "secret"}

const masked = "[REDACTED]"

// ParseLevel converts a level name; unknown names mean info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	}
	return slog.LevelInfo
}

// New returns the JSON logger. The output keys are time, level, msg, service, trace_id, span_id, request_id.
func New(o Options) *slog.Logger {
	w := o.Writer
	if w == nil {
		w = os.Stdout
	}
	redact := map[string]bool{}
	for _, k := range append(append([]string{}, DefaultRedacted...), o.Redact...) {
		redact[strings.ToLower(k)] = true
	}
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: ParseLevel(o.Level),
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if redact[strings.ToLower(a.Key)] || redact[strings.ToLower(strings.Join(append(append([]string{}, groups...), a.Key), "."))] {
				return slog.String(a.Key, masked)
			}
			return a
		},
	})
	l := slog.New(&ctxHandler{Handler: h})
	if o.Service != "" {
		l = l.With("service", o.Service)
	}
	return l
}

// ctxHandler adds trace and request ids from the context to every record.
type ctxHandler struct{ slog.Handler }

func (h *ctxHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	if id := RequestID(ctx); id != "" {
		r.AddAttrs(slog.String("request_id", id))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *ctxHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ctxHandler{Handler: h.Handler.WithAttrs(attrs)}
}

func (h *ctxHandler) WithGroup(name string) slog.Handler {
	return &ctxHandler{Handler: h.Handler.WithGroup(name)}
}
