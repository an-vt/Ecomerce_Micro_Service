package kafkax

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/plugin/kotel"
)

// RunnerConfig configures a consumer-group Runner.
type RunnerConfig struct {
	Brokers   []string
	Group     string
	Topics    []string
	Handler   Handler
	Processor *Processor
	Log       *slog.Logger
	// StartAtEnd makes a brand-new group skip history; the default reads from the beginning so events published
	// before the service first started are not lost.
	StartAtEnd bool
	Opts       []kgo.Opt
}

// Runner consumes Topics as part of Group and feeds every record through the Processor. Offsets are committed
// manually, only after a record is finished, in the order records were polled (per-partition order is kept).
type Runner struct {
	cfg RunnerConfig
	cl  *kgo.Client
}

// NewRunner creates the client (it connects lazily).
func NewRunner(cfg RunnerConfig) (*Runner, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	offset := kgo.NewOffset().AtStart()
	if cfg.StartAtEnd {
		offset = kgo.NewOffset().AtEnd()
	}
	opts := []kgo.Opt{
		kgo.SeedBrokers(cfg.Brokers...),
		kgo.ConsumerGroup(cfg.Group),
		kgo.ConsumeTopics(cfg.Topics...),
		kgo.ConsumeResetOffset(offset),
		kgo.DisableAutoCommit(),
		kgo.SessionTimeout(20 * time.Second),
		kgo.WithHooks(kotel.NewKotel(kotel.WithTracer(kotel.NewTracer())).Hooks()...),
	}
	cl, err := kgo.NewClient(append(opts, cfg.Opts...)...)
	if err != nil {
		return nil, fmt.Errorf("kafka consumer: %w", err)
	}
	return &Runner{cfg: cfg, cl: cl}, nil
}

// Ping is usable as an httpx.Check.
func (r *Runner) Ping(ctx context.Context) error { return r.cl.Ping(ctx) }

// Run blocks until ctx is cancelled, then commits what is done and leaves the group. It returns nil on a clean stop.
func (r *Runner) Run(ctx context.Context) error {
	defer r.cl.Close()
	for {
		fetches := r.cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if fetches.IsClientClosed() {
			return nil
		}
		fetches.EachError(func(topic string, partition int32, err error) {
			r.cfg.Log.Warn("kafka fetch error", "topic", topic, "partition", partition, "error", err.Error())
		})
		var stop bool
		fetches.EachRecord(func(rec *kgo.Record) {
			if stop {
				return
			}
			if err := r.finish(ctx, rec); err != nil {
				stop = true // ctx cancelled while the record was unfinished: leave it uncommitted
			}
		})
		if stop {
			return nil
		}
	}
}

// finish processes one record until it is done, retrying infrastructure failures (e.g. the DLQ write) forever,
// then commits its offset.
func (r *Runner) finish(ctx context.Context, rec *kgo.Record) error {
	in := Record{Topic: rec.Topic, Partition: rec.Partition, Offset: rec.Offset, Key: rec.Key, Value: rec.Value, Headers: map[string]string{}}
	for _, h := range rec.Headers {
		in.Headers[h.Key] = string(h.Value)
	}
	for {
		err := r.cfg.Processor.Process(ctx, in, r.cfg.Handler)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		r.cfg.Log.Error("record could not be finished, retrying", "topic", rec.Topic, "offset", rec.Offset, "error", err.Error())
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err := r.cl.CommitRecords(ctx, rec); err != nil && !errors.Is(err, context.Canceled) {
		r.cfg.Log.Warn("offset commit failed (the record will be redelivered)", "topic", rec.Topic, "offset", rec.Offset, "error", err.Error())
	}
	return nil
}
