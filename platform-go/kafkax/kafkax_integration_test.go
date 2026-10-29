//go:build integration

package kafkax_test

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/retry"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func envBytes(t *testing.T, id, typ, key string) []byte {
	t.Helper()
	env, err := event.NewEnvelope(id, typ, key, key, time.Now(), map[string]string{"n": id})
	require.NoError(t, err)
	b, err := env.Marshal()
	require.NoError(t, err)
	return b
}

type collector struct {
	mu   sync.Mutex
	seen []event.Envelope
}

func (c *collector) handle(_ context.Context, env event.Envelope) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, env)
	return nil
}
func (c *collector) ids() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]string, len(c.seen))
	for i, e := range c.seen {
		out[i] = e.MessageID
	}
	return out
}

func startRunner(t *testing.T, brokers []string, group, topic string, h kafkax.Handler, prod *kafkax.Producer) (stop func()) {
	t.Helper()
	proc := kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: prod, MaxAttempts: 2, Log: slog.Default(),
		Backoff: retry.Policy{Base: 10 * time.Millisecond, Max: 20 * time.Millisecond}})
	r, err := kafkax.NewRunner(kafkax.RunnerConfig{Brokers: brokers, Group: group, Topics: []string{topic}, Handler: h, Processor: proc})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			assert.NoError(t, err)
		case <-time.After(20 * time.Second):
			t.Error("runner did not stop")
		}
	}
}

func TestPublishConsumeKeepsPerKeyOrder(t *testing.T) {
	brokers := testkit.KafkaBrokers(t)
	topic := "it.order." + testkit.Suffix(t)
	testkit.CreateTopics(t, topic)
	prod, err := kafkax.NewProducer(brokers)
	require.NoError(t, err)
	defer prod.Close()
	require.NoError(t, prod.Ping(context.Background()))

	var msgs []kafkax.Message
	for i := 0; i < 10; i++ { // one aggregate, ten messages: must arrive in order
		msgs = append(msgs, kafkax.Message{Topic: topic, Key: "order-1", Value: envBytes(t, fmt.Sprintf("m%02d", i), "StockReserved", "order-1"),
			Headers: map[string]string{"x-request-id": "r1"}})
	}
	require.NoError(t, prod.Publish(context.Background(), msgs...))

	c := &collector{}
	stop := startRunner(t, brokers, "g-"+testkit.Suffix(t), topic, c.handle, prod)
	defer stop()
	require.Eventually(t, func() bool { return len(c.ids()) == 10 }, 60*time.Second, 100*time.Millisecond)
	for i, id := range c.ids() {
		assert.Equal(t, fmt.Sprintf("m%02d", i), id)
	}
}

func TestFailingMessageGoesToTheDLQAndConsumptionContinues(t *testing.T) {
	brokers := testkit.KafkaBrokers(t)
	topic := "it.dlq." + testkit.Suffix(t)
	testkit.CreateTopics(t, topic)
	prod, err := kafkax.NewProducer(brokers)
	require.NoError(t, err)
	defer prod.Close()

	require.NoError(t, prod.Publish(context.Background(),
		kafkax.Message{Topic: topic, Key: "k", Value: envBytes(t, "bad", "X", "k")},
		kafkax.Message{Topic: topic, Key: "k", Value: []byte("not json at all")},
		kafkax.Message{Topic: topic, Key: "k", Value: envBytes(t, "good", "X", "k")}))

	c := &collector{}
	handler := func(ctx context.Context, env event.Envelope) error {
		if env.MessageID == "bad" {
			return errs.FailedPrecondition("NOPE", "rejected")
		}
		return c.handle(ctx, env)
	}
	stop := startRunner(t, brokers, "g-"+testkit.Suffix(t), topic, handler, prod)
	defer stop()
	require.Eventually(t, func() bool { return len(c.ids()) == 1 }, 60*time.Second, 100*time.Millisecond)
	assert.Equal(t, []string{"good"}, c.ids(), "messages after a poison message are not blocked")

	// both rejected records are on the dead-letter topic with diagnostics
	dlq, err := kgo.NewClient(kgo.SeedBrokers(brokers...), kgo.ConsumeTopics(kafkax.DLQ(topic)), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	require.NoError(t, err)
	defer dlq.Close()
	var got []*kgo.Record
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for len(got) < 2 && ctx.Err() == nil {
		dlq.PollFetches(ctx).EachRecord(func(r *kgo.Record) { got = append(got, r) })
	}
	require.Len(t, got, 2)
	for _, r := range got {
		h := map[string]string{}
		for _, hh := range r.Headers {
			h[hh.Key] = string(hh.Value)
		}
		assert.Equal(t, topic, h[event.HeaderOrigTopic])
		assert.NotEmpty(t, h[event.HeaderError])
		assert.NotEmpty(t, h[event.HeaderAttempts])
	}
}

func TestCommittedOffsetsSurviveARestart(t *testing.T) {
	brokers := testkit.KafkaBrokers(t)
	topic := "it.restart." + testkit.Suffix(t)
	testkit.CreateTopics(t, topic)
	group := "g-" + testkit.Suffix(t)
	prod, err := kafkax.NewProducer(brokers)
	require.NoError(t, err)
	defer prod.Close()

	require.NoError(t, prod.Publish(context.Background(),
		kafkax.Message{Topic: topic, Key: "k", Value: envBytes(t, "a", "T", "k")},
		kafkax.Message{Topic: topic, Key: "k", Value: envBytes(t, "b", "T", "k")}))

	first := &collector{}
	stop := startRunner(t, brokers, group, topic, first.handle, prod)
	require.Eventually(t, func() bool { return len(first.ids()) == 2 }, 60*time.Second, 100*time.Millisecond)
	stop() // graceful: commits and leaves the group

	require.NoError(t, prod.Publish(context.Background(), kafkax.Message{Topic: topic, Key: "k", Value: envBytes(t, "c", "T", "k")}))
	second := &collector{}
	stop = startRunner(t, brokers, group, topic, second.handle, prod)
	defer stop()
	require.Eventually(t, func() bool { return len(second.ids()) >= 1 }, 60*time.Second, 100*time.Millisecond)
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, []string{"c"}, second.ids(), "already-committed messages are not redelivered")
}

func TestRunnerPing(t *testing.T) {
	brokers := testkit.KafkaBrokers(t)
	r, err := kafkax.NewRunner(kafkax.RunnerConfig{Brokers: brokers, Group: "g-" + testkit.Suffix(t), Topics: []string{"it.ping"},
		Handler: func(context.Context, event.Envelope) error { return nil }, Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{})})
	require.NoError(t, err)
	assert.NoError(t, r.Ping(context.Background()))
}
