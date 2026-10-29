package kafkax_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/inbox"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/retry"
)

type fakePublisher struct {
	sent []kafkax.Message
	err  error
}

func (f *fakePublisher) Publish(_ context.Context, msgs ...kafkax.Message) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, msgs...)
	return nil
}

type harness struct {
	p       *kafkax.Processor
	dlq     *fakePublisher
	m       *metrics.Metrics
	slept   []time.Duration
	logs    *bytes.Buffer
	calls   int
	lastEnv event.Envelope
}

func newHarness(maxAttempts int) *harness {
	h := &harness{dlq: &fakePublisher{}, m: metrics.New(), logs: &bytes.Buffer{}}
	h.p = kafkax.NewProcessor(kafkax.ProcessorConfig{
		DLQ: h.dlq, MaxAttempts: maxAttempts, Metrics: h.m, Log: logger.New(logger.Options{Writer: h.logs, Level: "debug"}),
		Backoff: retry.Policy{Sleep: func(_ context.Context, d time.Duration) error { h.slept = append(h.slept, d); return nil }, Jitter: func() float64 { return 0.5 }},
	})
	return h
}

func (h *harness) handler(fn func(attempt int) error) kafkax.Handler {
	return func(_ context.Context, env event.Envelope) error {
		h.calls++
		h.lastEnv = env
		return fn(h.calls)
	}
}

func record(t *testing.T, typ string) kafkax.Record {
	t.Helper()
	env, err := event.NewEnvelope("m-"+typ, typ, "agg-1", "agg-1", time.Unix(0, 0), map[string]string{"k": "v"})
	require.NoError(t, err)
	b, err := env.Marshal()
	require.NoError(t, err)
	return kafkax.Record{Topic: "inventory.commands", Partition: 2, Offset: 41, Key: []byte("agg-1"), Value: b,
		Headers: map[string]string{event.HeaderRequestID: "req-7"}}
}

func count(h *harness, result string) float64 {
	return testutil.ToFloat64(h.m.KafkaConsumed.WithLabelValues("inventory.commands", result))
}

func TestSuccess(t *testing.T) {
	h := newHarness(3)
	err := h.p.Process(context.Background(), record(t, "ReserveStock"), h.handler(func(int) error { return nil }))
	require.NoError(t, err)
	assert.Equal(t, 1, h.calls)
	assert.Equal(t, "ReserveStock", h.lastEnv.Type)
	assert.Equal(t, 1.0, count(h, kafkax.ResultOK))
	assert.Empty(t, h.dlq.sent)
}

func TestDuplicateAndIgnoredAreFinishedWithoutDLQ(t *testing.T) {
	h := newHarness(3)
	require.NoError(t, h.p.Process(context.Background(), record(t, "ReserveStock"), h.handler(func(int) error { return inbox.ErrDuplicate })))
	require.NoError(t, h.p.Process(context.Background(), record(t, "Other"), h.handler(func(int) error { return kafkax.ErrIgnored })))
	assert.Equal(t, 1.0, count(h, kafkax.ResultDuplicate))
	assert.Equal(t, 1.0, count(h, kafkax.ResultIgnored))
	assert.Empty(t, h.dlq.sent)
}

func TestTransientFailureIsRetriedWithBackoffThenSucceeds(t *testing.T) {
	h := newHarness(5)
	err := h.p.Process(context.Background(), record(t, "ReserveStock"), h.handler(func(n int) error {
		if n < 3 {
			return errors.New("db timeout")
		}
		return nil
	}))
	require.NoError(t, err)
	assert.Equal(t, 3, h.calls)
	assert.Equal(t, []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}, h.slept, "jittered exponential backoff")
	assert.Equal(t, 2.0, count(h, kafkax.ResultRetry))
	assert.Equal(t, 1.0, count(h, kafkax.ResultOK))
	assert.Empty(t, h.dlq.sent)
}

func TestExhaustedRetriesGoToTheDeadLetterTopic(t *testing.T) {
	h := newHarness(3)
	rec := record(t, "ReserveStock")
	err := h.p.Process(context.Background(), rec, h.handler(func(int) error { return errors.New("still broken") }))
	require.NoError(t, err, "a dead-lettered record is finished: the offset may be committed")
	assert.Equal(t, 3, h.calls)

	require.Len(t, h.dlq.sent, 1)
	m := h.dlq.sent[0]
	assert.Equal(t, "inventory.commands.dlq", m.Topic)
	assert.Equal(t, "agg-1", m.Key, "same key: DLQ keeps per-aggregate order")
	assert.Equal(t, rec.Value, m.Value, "the original bytes")
	assert.Equal(t, "still broken", m.Headers[event.HeaderError])
	assert.Equal(t, "3", m.Headers[event.HeaderAttempts])
	assert.Equal(t, "inventory.commands", m.Headers[event.HeaderOrigTopic])
	assert.Equal(t, "req-7", m.Headers[event.HeaderRequestID], "original headers are kept")
	assert.Equal(t, 1.0, count(h, kafkax.ResultDLQ))
	assert.Equal(t, 1.0, testutil.ToFloat64(h.m.KafkaDLQ.WithLabelValues("inventory.commands")))
	assert.Contains(t, h.logs.String(), `"request_id":"req-7"`)
}

func TestPermanentErrorIsDeadLetteredImmediately(t *testing.T) {
	h := newHarness(5)
	err := h.p.Process(context.Background(), record(t, "ReserveStock"), h.handler(func(int) error {
		return errs.FailedPrecondition("INSUFFICIENT_STOCK", "no")
	}))
	require.NoError(t, err)
	assert.Equal(t, 1, h.calls, "no point retrying a business rejection")
	assert.Empty(t, h.slept)
	require.Len(t, h.dlq.sent, 1)
	assert.Equal(t, "1", h.dlq.sent[0].Headers[event.HeaderAttempts])
}

func TestMalformedMessageIsDeadLettered(t *testing.T) {
	h := newHarness(3)
	rec := kafkax.Record{Topic: "inventory.commands", Key: []byte("k"), Value: []byte("{nope")}
	require.NoError(t, h.p.Process(context.Background(), rec, h.handler(func(int) error { return nil })))
	assert.Equal(t, 0, h.calls, "the handler never sees garbage")
	require.Len(t, h.dlq.sent, 1)
	assert.Contains(t, h.dlq.sent[0].Headers[event.HeaderError], event.CodeInvalidEnvelope)
}

func TestUnfinishableRecordReturnsAnErrorSoTheOffsetIsNotCommitted(t *testing.T) {
	h := newHarness(2)
	h.dlq.err = errors.New("kafka down")
	err := h.p.Process(context.Background(), record(t, "ReserveStock"), h.handler(func(int) error { return errors.New("x") }))
	require.Error(t, err)
	assert.ErrorContains(t, err, "kafka down")

	noDLQ := kafkax.NewProcessor(kafkax.ProcessorConfig{MaxAttempts: 1, Log: slog.Default()})
	err = noDLQ.Process(context.Background(), record(t, "T"), func(context.Context, event.Envelope) error { return errors.New("x") })
	assert.ErrorContains(t, err, "no dead-letter publisher")
}

func TestCancelledContextStopsWithoutDeadLettering(t *testing.T) {
	h := newHarness(5)
	ctx, cancel := context.WithCancel(context.Background())
	err := h.p.Process(ctx, record(t, "ReserveStock"), h.handler(func(int) error { cancel(); return errors.New("interrupted") }))
	assert.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, h.dlq.sent, "a record interrupted by shutdown is redelivered, not parked")
}

func TestBackoffSleepErrorStops(t *testing.T) {
	stop := errors.New("shutdown")
	p := kafkax.NewProcessor(kafkax.ProcessorConfig{MaxAttempts: 3, DLQ: &fakePublisher{}, Log: slog.Default(),
		Backoff: retry.Policy{Sleep: func(context.Context, time.Duration) error { return stop }}})
	err := p.Process(context.Background(), record(t, "T"), func(context.Context, event.Envelope) error { return errors.New("x") })
	assert.ErrorIs(t, err, stop)
}

func TestLongErrorsAreTruncatedInTheHeader(t *testing.T) {
	h := newHarness(1)
	long := string(bytes.Repeat([]byte("e"), 2000))
	require.NoError(t, h.p.Process(context.Background(), record(t, "T"), h.handler(func(int) error { return errors.New(long) })))
	assert.Len(t, h.dlq.sent[0].Headers[event.HeaderError], 512)
}

func TestDefaultsAndRealSleep(t *testing.T) {
	p := kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: &fakePublisher{}, MaxAttempts: 2,
		Backoff: retry.Policy{Base: time.Millisecond, Max: time.Millisecond}})
	calls := 0
	err := p.Process(context.Background(), record(t, "T"), func(context.Context, event.Envelope) error {
		calls++
		if calls < 2 {
			return errors.New("x")
		}
		return nil
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: &fakePublisher{}, MaxAttempts: 3}).
		Process(ctx, record(t, "T"), func(context.Context, event.Envelope) error { return errors.New("x") })
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMux(t *testing.T) {
	var got []string
	mux := kafkax.NewMux().
		Handle("A", func(_ context.Context, e event.Envelope) error { got = append(got, "A:"+e.AggregateID); return nil }).
		Handle("B", func(context.Context, event.Envelope) error { return errors.New("b failed") })
	h := mux.Handler()

	require.NoError(t, h(context.Background(), event.Envelope{Type: "A", AggregateID: "1"}))
	assert.Equal(t, []string{"A:1"}, got)
	assert.EqualError(t, h(context.Background(), event.Envelope{Type: "B"}), "b failed")
	assert.ErrorIs(t, h(context.Background(), event.Envelope{Type: "Z"}), kafkax.ErrIgnored)
	assert.Panics(t, func() { mux.Handle("A", nil) })
}

func TestDLQTopicName(t *testing.T) {
	assert.Equal(t, "order.saga.replies.dlq", kafkax.DLQ(kafkax.TopicSagaReplies))
	assert.Equal(t, "5", strconv.Itoa(5))
}
