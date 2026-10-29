package outbox_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// ---- fakes ---------------------------------------------------------------------------------------------------------

type row struct {
	outbox.Message
	status  string
	availAt time.Time
}

type memStore struct {
	mu     sync.Mutex
	rows   []*row
	closed int
	purged time.Time
	failOn string
}

func (s *memStore) add(id, key string) {
	s.rows = append(s.rows, &row{Message: outbox.Message{ID: id, Key: key, Topic: "t", Payload: []byte(`{}`)}, status: "PENDING"})
}

type memBatch struct {
	s    *memStore
	msgs []outbox.Message
}

func (s *memStore) Claim(_ context.Context, limit int) (outbox.Batch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOn == "claim" {
		return nil, errors.New("db down")
	}
	var msgs []outbox.Message
	for _, r := range s.rows {
		if r.status == "PENDING" && len(msgs) < limit {
			msgs = append(msgs, r.Message)
		}
	}
	return &memBatch{s: s, msgs: msgs}, nil
}
func (b *memBatch) Messages() []outbox.Message { return b.msgs }
func (b *memBatch) MarkSent(_ context.Context, ids ...string) error {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	if b.s.failOn == "marksent" {
		return errors.New("update failed")
	}
	for _, id := range ids {
		for _, r := range b.s.rows {
			if r.ID == id {
				r.status = "SENT"
			}
		}
	}
	return nil
}
func (b *memBatch) MarkFailed(_ context.Context, id string, attempts int, retryAt time.Time) error {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	if b.s.failOn == "markfailed" {
		return errors.New("update failed")
	}
	for _, r := range b.s.rows {
		if r.ID == id {
			r.Attempts, r.availAt = attempts, retryAt
		}
	}
	return nil
}
func (b *memBatch) Close(context.Context) error {
	b.s.mu.Lock()
	b.s.closed++
	b.s.mu.Unlock()
	return nil
}
func (s *memStore) Stats(context.Context) (int64, time.Duration, error) {
	n := int64(0)
	for _, r := range s.rows {
		if r.status == "PENDING" {
			n++
		}
	}
	return n, 3 * time.Second, nil
}
func (s *memStore) Purge(_ context.Context, before time.Time) (int64, error) {
	s.purged = before
	return 2, nil
}
func (s *memStore) status(id string) *row {
	for _, r := range s.rows {
		if r.ID == id {
			return r
		}
	}
	return nil
}

type fakePub struct {
	mu       sync.Mutex
	sent     []kafkax.Message
	failKeys map[string]bool
	batchErr error
}

func (p *fakePub) Publish(_ context.Context, msgs ...kafkax.Message) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(msgs) > 1 && p.batchErr != nil {
		return p.batchErr
	}
	for _, m := range msgs {
		if p.failKeys[m.Key] {
			return errors.New("broker unavailable for " + m.Key)
		}
	}
	p.sent = append(p.sent, msgs...)
	return nil
}
func (p *fakePub) keys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.sent))
	for i, m := range p.sent {
		out[i] = m.Key
	}
	return out
}

// ---- tests ---------------------------------------------------------------------------------------------------------

func TestNewStampsTraceAndRequestID(t *testing.T) {
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	otel.SetTracerProvider(tp)
	_, _ = otelx.Init(context.Background(), otelx.Config{})
	ctx, span := otelx.Tracer("t").Start(context.Background(), "request")
	defer span.End()
	ctx = logger.WithRequestID(ctx, "req-1")

	env, err := event.NewEnvelope("m1", "StockReserved", "order-9", "order-9", time.Unix(0, 0), map[string]int{"n": 1})
	require.NoError(t, err)
	msg, err := outbox.New(ctx, "order.saga.replies", env)
	require.NoError(t, err)

	assert.Equal(t, "m1", msg.ID)
	assert.Equal(t, "order-9", msg.Key)
	assert.Equal(t, "StockReserved", msg.Type)
	assert.Contains(t, msg.Headers, "traceparent")
	assert.Equal(t, "req-1", msg.Headers[event.HeaderRequestID])
	parsed, err := event.Parse(msg.Payload)
	require.NoError(t, err)
	assert.Equal(t, "m1", parsed.MessageID)
}

func TestNewWithoutContextHasNoTraceHeaders(t *testing.T) {
	env, _ := event.NewEnvelope("m", "T", "a", "", time.Now(), 1)
	msg, err := outbox.New(context.Background(), "t", env)
	require.NoError(t, err)
	assert.NotContains(t, msg.Headers, "traceparent")
	assert.NotContains(t, msg.Headers, event.HeaderRequestID)
}

func TestRunOncePublishesBatchInOrderAndMarksSent(t *testing.T) {
	s, p := &memStore{}, &fakePub{}
	for _, id := range []string{"a", "b", "c"} {
		s.add(id, "k-"+id)
	}
	m := metrics.New()
	n, err := outbox.NewRelay(s, p, outbox.RelayOptions{Metrics: m}).RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Equal(t, []string{"k-a", "k-b", "k-c"}, p.keys())
	for _, id := range []string{"a", "b", "c"} {
		assert.Equal(t, "SENT", s.status(id).status)
	}
	assert.Equal(t, 1, s.closed, "the batch is always closed")
	assert.Equal(t, 3.0, testutil.ToFloat64(m.OutboxPublished.WithLabelValues("t")))

	n, err = outbox.NewRelay(s, p, outbox.RelayOptions{}).RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 0, n, "nothing left")
}

func TestFailedMessageBlocksLaterMessagesOfTheSameKeyOnly(t *testing.T) {
	s := &memStore{}
	s.add("a1", "A")
	s.add("a2", "A") // same aggregate as a1: must wait behind it
	s.add("b1", "B")
	p := &fakePub{failKeys: map[string]bool{"A": true}, batchErr: errors.New("batch failed")}
	now := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	m := metrics.New()

	n, err := outbox.NewRelay(s, p, outbox.RelayOptions{Now: func() time.Time { return now }, Jitter: func() float64 { return 0.5 }, Metrics: m}).RunOnce(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, []string{"B"}, p.keys(), "other aggregates are not held back")
	assert.Equal(t, "SENT", s.status("b1").status)
	assert.Equal(t, "PENDING", s.status("a1").status)
	assert.Equal(t, "PENDING", s.status("a2").status)
	assert.Equal(t, 1, s.status("a1").Attempts)
	assert.Equal(t, 0, s.status("a2").Attempts, "a2 was skipped, not attempted")
	assert.Equal(t, now.Add(250*time.Millisecond), s.status("a1").availAt, "retry delay = jittered backoff")
	assert.Equal(t, 1.0, testutil.ToFloat64(m.OutboxErrors.WithLabelValues("t")))
}

func TestStoreErrors(t *testing.T) {
	s, p := &memStore{failOn: "claim"}, &fakePub{}
	_, err := outbox.NewRelay(s, p, outbox.RelayOptions{}).RunOnce(context.Background())
	assert.ErrorContains(t, err, "db down")

	s = &memStore{failOn: "marksent"}
	s.add("a", "A")
	_, err = outbox.NewRelay(s, p, outbox.RelayOptions{}).RunOnce(context.Background())
	assert.ErrorContains(t, err, "update failed", "published but not marked: redelivery is possible, consumers dedupe")

	s = &memStore{failOn: "markfailed"}
	s.add("a", "A")
	_, err = outbox.NewRelay(s, &fakePub{failKeys: map[string]bool{"A": true}}, outbox.RelayOptions{}).RunOnce(context.Background())
	assert.ErrorContains(t, err, "update failed")
}

func TestRunLoopPublishesUpdatesGaugesPurgesAndStops(t *testing.T) {
	s, p := &memStore{}, &fakePub{}
	s.add("a", "A")
	m := metrics.New()
	r := outbox.NewRelay(s, p, outbox.RelayOptions{Poll: 5 * time.Millisecond, Metrics: m, Retention: 24 * time.Hour})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	require.Eventually(t, func() bool { return len(p.keys()) == 1 }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { return testutil.ToFloat64(m.OutboxOldestAge) == 3 }, 2*time.Second, 5*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("relay did not stop")
	}
	assert.False(t, s.purged.IsZero(), "old sent messages are purged")
	assert.Equal(t, 0.0, testutil.ToFloat64(m.OutboxPending))
}

func TestRunKeepsGoingWithoutSleepingWhileBatchesAreFull(t *testing.T) {
	s, p := &memStore{}, &fakePub{}
	for i := 0; i < 5; i++ {
		s.add(string(rune('a'+i)), "k")
	}
	r := outbox.NewRelay(s, p, outbox.RelayOptions{Poll: time.Hour, Batch: 2})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = r.Run(ctx); close(done) }()
	require.Eventually(t, func() bool { return len(p.keys()) == 5 }, 2*time.Second, 5*time.Millisecond, "full batches are followed immediately, not after the poll interval")
	cancel()
	<-done
	ids := p.keys()
	sort.Strings(ids)
	assert.Len(t, ids, 5)
}
