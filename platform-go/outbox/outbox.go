// Package outbox implements the transactional outbox (docs/08-messaging.md §3): use cases add messages in the
// same database transaction as the state change, and a Relay publishes them to Kafka afterwards. Storage lives in
// pgoutbox and mongooutbox; this package holds the message type and the relay loop.
package outbox

import (
	"context"
	"time"

	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/otelx"
)

// Message is one outbox row.
type Message struct {
	ID        string // = envelope message id
	Topic     string
	Key       string // aggregate id: all messages of one aggregate stay on one partition, in order
	Type      string
	Payload   []byte // the marshalled event.Envelope
	Headers   map[string]string
	Attempts  int
	CreatedAt time.Time
}

// New builds a Message from an envelope and stamps the trace context and request id of ctx into its headers, so
// the consumer's span continues the producing request even though publishing happens later, in another process.
func New(ctx context.Context, topic string, env event.Envelope) (Message, error) {
	payload, err := env.Marshal()
	if err != nil {
		return Message{}, err
	}
	h := otelx.Inject(ctx)
	if id := logger.RequestID(ctx); id != "" {
		h[event.HeaderRequestID] = id
	}
	return Message{ID: env.MessageID, Topic: topic, Key: env.AggregateID, Type: env.Type, Payload: payload, Headers: h}, nil
}

// Writer adds messages inside the transaction carried by ctx.
type Writer interface {
	Add(ctx context.Context, msgs ...Message) error
}

// Batch is a set of claimed messages, oldest first. Close must always be called.
type Batch interface {
	Messages() []Message
	MarkSent(ctx context.Context, ids ...string) error
	// MarkFailed records a failed publish and delays the next try until retryAt.
	MarkFailed(ctx context.Context, id string, attempts int, retryAt time.Time) error
	Close(ctx context.Context) error
}

// Store is the storage half of the relay.
type Store interface {
	// Claim returns up to limit due messages. It returns an empty batch when another relay is the active one.
	Claim(ctx context.Context, limit int) (Batch, error)
	// Stats: number of pending messages and the age of the oldest.
	Stats(ctx context.Context) (pending int64, oldest time.Duration, err error)
	// Purge deletes sent messages older than before.
	Purge(ctx context.Context, before time.Time) (int64, error)
}
