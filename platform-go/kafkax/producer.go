// Package kafkax is the franz-go based Kafka layer: a synchronous producer, the retry / dead-letter Processor and
// the consumer-group Runner (docs/08-messaging.md).
package kafkax

import (
	"context"
	"fmt"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"

	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/otelx"
)

// Message is one record to publish.
type Message struct {
	Topic   string
	Key     string
	Value   []byte
	Headers map[string]string
}

// Publisher publishes messages synchronously; it is what the outbox relay and the DLQ writer depend on.
type Publisher interface {
	Publish(ctx context.Context, msgs ...Message) error
}

// Producer is an idempotent, acks=all producer (franz-go defaults).
type Producer struct{ cl *kgo.Client }

// NewProducer connects lazily; extra options are appended (tests, tuning).
func NewProducer(brokers []string, opts ...kgo.Opt) (*Producer, error) {
	base := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.RecordPartitioner(kgo.UniformBytesPartitioner(64<<10, true, true, nil)),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.RecordDeliveryTimeout(30 * time.Second),
		kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(kotel.NewTracer())).Hooks()...),
	}
	cl, err := kgo.NewClient(append(base, opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka producer: %w", err)
	}
	return &Producer{cl: cl}, nil
}

// Publish sends the messages and waits for the broker's acknowledgement. Records of one call that share a key keep
// their order. It returns the first error.
func (p *Producer) Publish(ctx context.Context, msgs ...Message) error {
	recs := make([]*kgo.Record, len(msgs))
	for i, m := range msgs {
		// The record's context is the parent of the producer span: seeding it from the stored headers keeps the
		// trace of the request that wrote the outbox row (the tracing hook writes the traceparent header itself).
		r := &kgo.Record{Topic: m.Topic, Key: []byte(m.Key), Value: m.Value, Context: otelx.Extract(ctx, m.Headers)}
		for k, v := range m.Headers {
			if k == event.HeaderTraceparent || k == event.HeaderTracestate {
				continue
			}
			r.Headers = append(r.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
		}
		recs[i] = r
	}
	return p.cl.ProduceSync(ctx, recs...).FirstErr()
}

// Ping is usable as an httpx.Check.
func (p *Producer) Ping(ctx context.Context) error { return p.cl.Ping(ctx) }

// Close flushes and closes the client.
func (p *Producer) Close() { p.cl.Close() }
