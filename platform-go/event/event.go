// Package event defines the Kafka message envelope and the domain-event contract (docs/08-messaging.md §2).
package event

import (
	"encoding/json"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// Event is implemented by domain events recorded by aggregates.
type Event interface {
	EventType() string   // e.g. "catalog.product.created"
	AggregateID() string // Kafka key
}

// Header names carried as Kafka headers.
const (
	HeaderTraceparent = "traceparent"
	HeaderTracestate  = "tracestate"
	HeaderRequestID   = "x-request-id"
	HeaderError       = "x-error"
	HeaderAttempts    = "x-attempts"
	HeaderOrigTopic   = "x-original-topic"
)

// Envelope is the JSON value of every Kafka message.
type Envelope struct {
	MessageID     string          `json:"messageId"`
	Type          string          `json:"type"`
	OccurredAt    time.Time       `json:"occurredAt"`
	AggregateID   string          `json:"aggregateId"`
	CorrelationID string          `json:"correlationId,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// CodeInvalidEnvelope marks a message that cannot be parsed; it is permanent (goes to the DLQ).
const CodeInvalidEnvelope = "INVALID_ENVELOPE"

// NewEnvelope marshals payload into an Envelope.
func NewEnvelope(messageID, typ, aggregateID, correlationID string, at time.Time, payload any) (Envelope, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, errs.Wrap(err, errs.KindInvalidArgument, CodeInvalidEnvelope, "payload is not serialisable")
	}
	return Envelope{
		MessageID: messageID, Type: typ, OccurredAt: at.UTC(),
		AggregateID: aggregateID, CorrelationID: correlationID, Payload: raw,
	}, nil
}

// FromEvent builds an Envelope for a domain event (correlation id = aggregate id).
func FromEvent(messageID string, at time.Time, ev Event) (Envelope, error) {
	return NewEnvelope(messageID, ev.EventType(), ev.AggregateID(), ev.AggregateID(), at, ev)
}

// Marshal serialises the envelope.
func (e Envelope) Marshal() ([]byte, error) { return json.Marshal(e) }

// Decode unmarshals the payload into v.
func (e Envelope) Decode(v any) error {
	if err := json.Unmarshal(e.Payload, v); err != nil {
		return errs.Wrap(err, errs.KindInvalidArgument, CodeInvalidEnvelope, "payload does not match "+e.Type)
	}
	return nil
}

// Parse reads an envelope from a Kafka message value and checks the mandatory fields.
func Parse(value []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(value, &e); err != nil {
		return Envelope{}, errs.Wrap(err, errs.KindInvalidArgument, CodeInvalidEnvelope, "message is not a valid envelope")
	}
	if e.MessageID == "" || e.Type == "" {
		return Envelope{}, errs.InvalidArgument(CodeInvalidEnvelope, "messageId and type are required")
	}
	return e, nil
}
