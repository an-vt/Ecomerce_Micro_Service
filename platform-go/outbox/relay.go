package outbox

import (
	"context"
	"log/slog"
	"time"

	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/retry"
)

// RelayOptions tunes the relay; zero values get the documented defaults.
type RelayOptions struct {
	Poll      time.Duration // default 200ms
	Batch     int           // default 100
	Retention time.Duration // sent messages are kept this long; default 7 days
	Metrics   *metrics.Metrics
	Log       *slog.Logger
	Now       func() time.Time
	Jitter    func() float64
}

// Relay publishes outbox messages to Kafka.
type Relay struct {
	store Store
	pub   kafkax.Publisher
	o     RelayOptions
}

// NewRelay creates a relay.
func NewRelay(store Store, pub kafkax.Publisher, o RelayOptions) *Relay {
	if o.Poll <= 0 {
		o.Poll = 200 * time.Millisecond
	}
	if o.Batch <= 0 {
		o.Batch = 100
	}
	if o.Retention <= 0 {
		o.Retention = 7 * 24 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Relay{store: store, pub: pub, o: o}
}

func toKafka(m Message) kafkax.Message {
	return kafkax.Message{Topic: m.Topic, Key: m.Key, Value: m.Payload, Headers: m.Headers}
}

// RunOnce claims one batch and publishes it. The whole batch is published in one call first; if that fails the
// messages are published one by one to find out which ones fail, and a failed message blocks later messages of
// the same key (their order must not change) until its retry time. It returns how many messages were published.
func (r *Relay) RunOnce(ctx context.Context) (int, error) {
	batch, err := r.store.Claim(ctx, r.o.Batch)
	if err != nil {
		return 0, err
	}
	defer func() { _ = batch.Close(context.WithoutCancel(ctx)) }()
	msgs := batch.Messages()
	if len(msgs) == 0 {
		return 0, nil
	}

	all := make([]kafkax.Message, len(msgs))
	for i, m := range msgs {
		all[i] = toKafka(m)
	}
	// Bookkeeping must not be cut short by the publish deadline or a shutdown: a message that reached Kafka but is
	// not marked sent would be published again.
	bk, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	var sent []string
	fail := func(m Message, perr error) error {
		attempts := m.Attempts + 1
		retryAt := r.o.Now().Add(retry.Backoff(attempts, 500*time.Millisecond, time.Minute, r.o.Jitter))
		r.countError(m.Topic)
		r.o.Log.WarnContext(ctx, "outbox publish failed", "message_id", m.ID, "topic", m.Topic, "attempts", attempts, "error", perr.Error())
		return batch.MarkFailed(bk, m.ID, attempts, retryAt)
	}

	if err := r.pub.Publish(ctx, all...); err == nil {
		for _, m := range msgs {
			sent = append(sent, m.ID)
			r.countPublished(m.Topic)
		}
	} else if len(msgs) == 1 {
		if merr := fail(msgs[0], err); merr != nil {
			return 0, merr
		}
	} else {
		r.o.Log.WarnContext(ctx, "batch publish failed, publishing one by one", "error", err.Error(), "messages", len(msgs))
		blocked := map[string]bool{}
		for _, m := range msgs {
			if blocked[m.Key] {
				continue
			}
			if perr := r.pub.Publish(ctx, toKafka(m)); perr != nil {
				blocked[m.Key] = true
				if merr := fail(m, perr); merr != nil {
					return len(sent), merr
				}
				continue
			}
			sent = append(sent, m.ID)
			r.countPublished(m.Topic)
		}
	}
	if len(sent) > 0 {
		if err := batch.MarkSent(bk, sent...); err != nil {
			return 0, err
		}
	}
	return len(sent), nil
}

func (r *Relay) countPublished(topic string) {
	if r.o.Metrics != nil {
		r.o.Metrics.OutboxPublished.WithLabelValues(topic).Inc()
	}
}

func (r *Relay) countError(topic string) {
	if r.o.Metrics != nil {
		r.o.Metrics.OutboxErrors.WithLabelValues(topic).Inc()
	}
}

// Run polls until ctx is cancelled: publish, update the backlog gauges, and purge old sent rows hourly.
func (r *Relay) Run(ctx context.Context) error {
	tick := time.NewTicker(r.o.Poll)
	defer tick.Stop()
	nextPurge := r.o.Now()
	for {
		n, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.o.Log.ErrorContext(ctx, "outbox relay failed", "error", err.Error())
		}
		r.updateGauges(ctx)
		if now := r.o.Now(); !now.Before(nextPurge) {
			if deleted, perr := r.store.Purge(ctx, now.Add(-r.o.Retention)); perr == nil && deleted > 0 {
				r.o.Log.InfoContext(ctx, "purged sent outbox messages", "deleted", deleted)
			}
			nextPurge = now.Add(time.Hour)
		}
		if n == r.o.Batch {
			continue // more is waiting: do not sleep
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

func (r *Relay) updateGauges(ctx context.Context) {
	if r.o.Metrics == nil {
		return
	}
	pending, oldest, err := r.store.Stats(ctx)
	if err != nil {
		return
	}
	r.o.Metrics.OutboxPending.Set(float64(pending))
	r.o.Metrics.OutboxOldestAge.Set(oldest.Seconds())
}
