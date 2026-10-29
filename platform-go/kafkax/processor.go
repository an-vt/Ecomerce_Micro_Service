package kafkax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/retry"
)

// Record is a consumed Kafka record, decoupled from the client library so the Processor is testable.
type Record struct {
	Topic     string
	Partition int32
	Offset    int64
	Key       []byte
	Value     []byte
	Headers   map[string]string
}

// Handler processes one decoded message. Return inbox.ErrDuplicate for an already-processed message,
// ErrIgnored for a type this consumer does not care about, an errs permanent error for a business rejection
// (dead-lettered at once), anything else is retried.
type Handler func(ctx context.Context, env event.Envelope) error

// ErrIgnored marks a message type the consumer deliberately skips.
var ErrIgnored = errors.New("kafkax: message type ignored")

// Mux dispatches by envelope type. Unknown types are ignored, not dead-lettered: a topic may carry types a
// consumer has no interest in.
type Mux struct{ h map[string]Handler }

// NewMux creates an empty Mux.
func NewMux() *Mux { return &Mux{h: map[string]Handler{}} }

// Handle registers the handler of a message type; it panics on duplicates.
func (m *Mux) Handle(typ string, h Handler) *Mux {
	if _, dup := m.h[typ]; dup {
		panic("kafkax: duplicate handler for " + typ)
	}
	m.h[typ] = h
	return m
}

// Handler returns the dispatching Handler.
func (m *Mux) Handler() Handler {
	return func(ctx context.Context, env event.Envelope) error {
		h, ok := m.h[env.Type]
		if !ok {
			return ErrIgnored
		}
		return h(ctx, env)
	}
}

// Results of one processed record, used as metric label values.
const (
	ResultOK        = "ok"
	ResultDuplicate = "duplicate"
	ResultIgnored   = "ignored"
	ResultRetry     = "retry"
	ResultDLQ       = "dlq"
)

// ProcessorConfig configures a Processor.
type ProcessorConfig struct {
	DLQ         Publisher
	MaxAttempts int          // default 5
	Backoff     retry.Policy // Base / Max / Sleep / Jitter; defaults 200ms / 5s
	Metrics     *metrics.Metrics
	Log         *slog.Logger
	Service     string // value of x-error producer tag, optional
}

// Processor applies the retry / dead-letter policy of docs/08 §5 to one record at a time.
type Processor struct{ c ProcessorConfig }

// NewProcessor applies defaults.
func NewProcessor(c ProcessorConfig) *Processor {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = 5
	}
	if c.Backoff.Base <= 0 {
		c.Backoff.Base = 200 * time.Millisecond
	}
	if c.Backoff.Max <= 0 {
		c.Backoff.Max = 5 * time.Second
	}
	if c.Backoff.Sleep == nil {
		c.Backoff.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	return &Processor{c: c}
}

func (p *Processor) count(topic, result string) {
	if p.c.Metrics != nil {
		p.c.Metrics.KafkaConsumed.WithLabelValues(topic, result).Inc()
	}
}

// Process handles rec. It returns nil when the record is finished — processed, skipped or dead-lettered — and the
// offset may be committed. It returns an error only when the record could not be finished (the dead-letter write
// failed, or ctx was cancelled); the caller must not commit and should retry.
func (p *Processor) Process(ctx context.Context, rec Record, h Handler) error {
	ctx = otelx.Extract(ctx, rec.Headers)
	if id := rec.Headers[event.HeaderRequestID]; id != "" {
		ctx = logger.WithRequestID(ctx, id)
	}
	log := p.c.Log.With("topic", rec.Topic, "partition", rec.Partition, "offset", rec.Offset)

	env, err := event.Parse(rec.Value)
	if err != nil {
		log.WarnContext(ctx, "unparseable message", "error", err.Error())
		return p.deadLetter(ctx, rec, err, 1)
	}
	log = log.With("message_id", env.MessageID, "type", env.Type)

	var last error
	for attempt := 1; attempt <= p.c.MaxAttempts; attempt++ {
		last = h(ctx, env)
		switch {
		case last == nil:
			p.count(rec.Topic, ResultOK)
			return nil
		case errors.Is(last, inbox.ErrDuplicate):
			log.DebugContext(ctx, "duplicate message skipped")
			p.count(rec.Topic, ResultDuplicate)
			return nil
		case errors.Is(last, ErrIgnored):
			p.count(rec.Topic, ResultIgnored)
			return nil
		case errs.IsPermanent(last):
			log.WarnContext(ctx, "message rejected", "code", errs.CodeOf(last), "error", last.Error())
			return p.deadLetter(ctx, rec, last, attempt)
		case ctx.Err() != nil:
			return ctx.Err()
		}
		if attempt == p.c.MaxAttempts {
			break
		}
		p.count(rec.Topic, ResultRetry)
		log.WarnContext(ctx, "message handler failed, retrying", "attempt", attempt, "error", last.Error())
		if err := p.c.Backoff.Sleep(ctx, retry.Backoff(attempt, p.c.Backoff.Base, p.c.Backoff.Max, p.c.Backoff.Jitter)); err != nil {
			return err
		}
	}
	log.ErrorContext(ctx, "message handler gave up", "attempts", p.c.MaxAttempts, "error", last.Error())
	return p.deadLetter(ctx, rec, last, p.c.MaxAttempts)
}

func (p *Processor) deadLetter(ctx context.Context, rec Record, cause error, attempts int) error {
	headers := map[string]string{}
	for k, v := range rec.Headers {
		headers[k] = v
	}
	headers[event.HeaderError] = truncate(cause.Error(), 512)
	headers[event.HeaderAttempts] = strconv.Itoa(attempts)
	headers[event.HeaderOrigTopic] = rec.Topic
	msg := Message{Topic: DLQ(rec.Topic), Key: string(rec.Key), Value: rec.Value, Headers: headers}
	if p.c.DLQ == nil {
		return fmt.Errorf("no dead-letter publisher configured for %s: %w", rec.Topic, cause)
	}
	if err := p.c.DLQ.Publish(ctx, msg); err != nil {
		return fmt.Errorf("dead-letter %s: %w", rec.Topic, err)
	}
	p.count(rec.Topic, ResultDLQ)
	if p.c.Metrics != nil {
		p.c.Metrics.KafkaDLQ.WithLabelValues(rec.Topic).Inc()
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
