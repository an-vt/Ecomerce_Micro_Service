//go:build integration

package kafka_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/an-vt/ecom/order-service/internal/database/migrations"
	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/fake"
	"github.com/an-vt/ecom/order-service/internal/repository/postgres"
	transport "github.com/an-vt/ecom/order-service/internal/transport/kafka"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
	"github.com/an-vt/ecom/platform-go/testkit"
)

// participants plays inventory, discount and payment: it consumes the command topics and answers on order.saga.replies
// according to a script, exactly as the real services do (same topics, same keys, same envelopes).
type participants struct {
	mu       sync.Mutex
	received []string        // "<command>/<orderID>" in arrival order
	drop     map[string]bool // commands to leave unanswered: "<command>/<orderID>"
	prod     *kafkax.Producer
}

func (p *participants) commandsOf(orderID string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []string
	for _, r := range p.received {
		if cmd, ok := strings.CutSuffix(r, "/"+orderID); ok {
			out = append(out, cmd)
		}
	}
	return out
}

func (p *participants) handle(ctx context.Context, env event.Envelope) error {
	key := env.Type + "/" + env.AggregateID
	p.mu.Lock()
	p.received = append(p.received, key)
	dropped := p.drop[key]
	p.mu.Unlock()
	if dropped {
		return nil
	}
	reply, ok := p.answer(env)
	if !ok {
		return nil
	}
	out, err := saga.Envelope(idgen.UUID{}.New(), time.Now(), reply)
	if err != nil {
		return err
	}
	raw, err := out.Marshal()
	if err != nil {
		return err
	}
	return p.prod.Publish(ctx, kafkax.Message{Topic: saga.TopicReplies, Key: env.AggregateID, Value: raw})
}

func (p *participants) answer(env event.Envelope) (saga.Message, bool) {
	id := env.AggregateID
	switch env.Type {
	case saga.CmdReserveStock:
		return saga.StockReserved{OrderID: id}, true
	case saga.CmdReleaseStock:
		return saga.StockReleased{OrderID: id}, true
	case saga.CmdCommitStock:
		return saga.StockCommitted{OrderID: id}, true
	case saga.CmdApplyDiscounts:
		return saga.DiscountsApplied{OrderID: id, TotalDiscount: 20000}, true
	case saga.CmdReleaseDiscount:
		return saga.DiscountsReleased{OrderID: id}, true
	case saga.CmdProcessPayment:
		var c saga.ProcessPayment
		_ = env.Decode(&c)
		if c.Token == "tok_fail" {
			return saga.PaymentFailed{OrderID: id, Reason: saga.ReasonCardDeclined}, true
		}
		return saga.PaymentCompleted{OrderID: id, PaymentID: "pay-" + id}, true
	case saga.CmdRefundPayment:
		return saga.PaymentRefunded{OrderID: id}, true
	}
	return nil, false
}

var topicsOnce sync.Once

type system struct {
	uc    *usecase.Set
	store *pgdb.Store
	parts *participants
}

// start wires order-service for real (PostgreSQL, outbox relay, Kafka reply consumer, sweeper) against real Kafka, with
// scripted participants on the other side.
func start(t *testing.T) *system {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	brokers := testkit.KafkaBrokers(t)
	// the topic names are the production constants, so they are created once for the whole test binary
	topicsOnce.Do(func() {
		testkit.CreateTopics(t, saga.TopicInventoryCommands, saga.TopicDiscountCommands, saga.TopicPaymentCommands, saga.TopicReplies, domain.TopicConfirmed, domain.TopicCancelled)
	})

	store, err := pgdb.Open(ctx, pgdb.Config{DSN: testkit.PostgresDSN(t), DBName: "order_saga_test"})
	require.NoError(t, err)
	_, err = pgmigrate.NewRunner(store.Raw(), "order_saga_test", nil).Migrate(ctx, false)
	require.NoError(t, err)

	prod, err := kafkax.NewProducer(brokers)
	require.NoError(t, err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	repos := postgres.New(store)
	tx := pgdb.NewTxManager(store)
	cat := &fake.Catalog{Products_: map[string]domain.Product{
		"p1": {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 100000, IsPublished: true},
		"p2": {ID: "p2", ShopID: "s2", Name: "Hat", Price: 50000, IsPublished: true}}}
	uc := usecase.NewSet(&usecase.Deps{Orders: repos.Orders, Sagas: repos.Sagas, Keys: repos.Keys, Catalog: cat,
		Discounts: &fake.Discounts{Percent: map[string]int64{"s1/SALE10": 10}}, Tx: tx, Outbox: pgoutbox.NewWriter(store), Clock: clock.System{}, IDs: idgen.UUID{},
		Timeouts: domain.Timeouts{Step: 1500 * time.Millisecond, Compensation: time.Second}, FeeShip: 5000})

	sfx := testkit.Suffix(t)
	parts := &participants{drop: map[string]bool{}, prod: prod}
	commandRunner, err := kafkax.NewRunner(kafkax.RunnerConfig{Brokers: brokers, Group: "participants-" + sfx, Log: log,
		Topics: []string{saga.TopicInventoryCommands, saga.TopicDiscountCommands, saga.TopicPaymentCommands},
		Handler: kafkax.NewMux().Handle(saga.CmdReserveStock, parts.handle).Handle(saga.CmdReleaseStock, parts.handle).Handle(saga.CmdCommitStock, parts.handle).
			Handle(saga.CmdApplyDiscounts, parts.handle).Handle(saga.CmdReleaseDiscount, parts.handle).Handle(saga.CmdProcessPayment, parts.handle).
			Handle(saga.CmdRefundPayment, parts.handle).Handler(),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: prod, Log: log})})
	require.NoError(t, err)
	replyRunner, err := kafkax.NewRunner(kafkax.RunnerConfig{Brokers: brokers, Group: "order-" + sfx, Topics: transport.Topics, Log: log,
		Handler:   transport.Handler(uc, tx, pgoutbox.NewGuard(store)),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: prod, Log: log})})
	require.NoError(t, err)
	relay := outbox.NewRelay(pgoutbox.NewStore(store, "order-service-"+sfx), prod, outbox.RelayOptions{Log: log})

	var wg sync.WaitGroup
	run := func(f func(context.Context) error) {
		wg.Add(1)
		go func() { defer wg.Done(); _ = f(ctx) }()
	}
	run(commandRunner.Run)
	run(replyRunner.Run)
	run(relay.Run)
	run(func(ctx context.Context) error { // the sweeper
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				_, _ = uc.SweepSagaTimeouts.Execute(ctx, 20)
			}
		}
	})
	t.Cleanup(func() {
		cancel()
		wg.Wait()
		prod.Close()
		_ = store.Close()
	})
	return &system{uc: uc, store: store, parts: parts}
}

func buyer() context.Context {
	return principal.With(context.Background(), principal.Principal{ID: "alice", Type: principal.TypeUser})
}

func placeIn(key, code, token string) usecase.PlaceInput {
	return usecase.PlaceInput{ShopOrders: []usecase.ShopOrder{
		{ShopID: "s1", DiscountCode: code, Items: []usecase.ItemInput{{ProductID: "p1", Quantity: 2}}},
		{ShopID: "s2", Items: []usecase.ItemInput{{ProductID: "p2", Quantity: 1}}}},
		Shipping: domain.Shipping{Name: "An", Address: "1 Main St"}, PaymentMethod: "card", PaymentToken: token, IdempotencyKey: key}
}

func (s *system) status(t *testing.T, id string) (status, reason string) {
	t.Helper()
	var r *string
	require.NoError(t, s.store.Raw().QueryRow(`SELECT status, cancel_reason FROM orders WHERE id = ?`, id).Scan(&status, &r))
	if r != nil {
		reason = *r
	}
	return status, reason
}

func (s *system) waitFor(t *testing.T, id, want string) (reason string) {
	t.Helper()
	require.Eventually(t, func() bool { st, _ := s.status(t, id); return st == want }, 60*time.Second, 100*time.Millisecond,
		"order %s did not reach %s", id, want)
	_, reason = s.status(t, id)
	return reason
}

func (s *system) published(t *testing.T, eventType, orderID string) bool {
	t.Helper()
	var n int
	require.NoError(t, s.store.Raw().QueryRow(`SELECT count(*) FROM outbox_messages WHERE type = ? AND key = ? AND status = 'SENT'`, eventType, orderID).Scan(&n))
	return n > 0
}

// Acceptance #5, happy path: the order is accepted, every participant is asked in turn over Kafka, and it ends CONFIRMED.
func TestCheckoutReachesConfirmedOverKafka(t *testing.T) {
	s := start(t)
	p, err := s.uc.PlaceOrder.Execute(buyer(), placeIn("k-happy", "SALE10", "tok_ok"))
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPending, p.Status, "the client gets its answer before the saga runs")

	s.waitFor(t, p.OrderID, "CONFIRMED")
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdApplyDiscounts, saga.CmdProcessPayment, saga.CmdCommitStock}, s.parts.commandsOf(p.OrderID),
		"each step asked once, in order")
	require.Eventually(t, func() bool { return s.published(t, "order.order.confirmed", p.OrderID) }, 30*time.Second, 100*time.Millisecond, "the event reached Kafka")

	var charged int64
	require.NoError(t, s.store.Raw().QueryRow(`SELECT total_checkout FROM orders WHERE id = ?`, p.OrderID).Scan(&charged))
	assert.EqualValues(t, 235000, charged, "250000 - the 20000 discount-service reported + 5000 shipping")
}

// Acceptance #5, failure path: payment is declined, every earlier step is undone, and the order ends CANCELLED.
func TestPaymentFailureCancelsAndCompensatesOverKafka(t *testing.T) {
	s := start(t)
	p, err := s.uc.PlaceOrder.Execute(buyer(), placeIn("k-declined", "SALE10", "tok_fail"))
	require.NoError(t, err)

	reason := s.waitFor(t, p.OrderID, "CANCELLED")
	assert.Equal(t, saga.ReasonCardDeclined, reason)
	cmds := s.parts.commandsOf(p.OrderID)
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdApplyDiscounts, saga.CmdProcessPayment}, cmds[:3])
	assert.ElementsMatch(t, []string{saga.CmdReleaseDiscount, saga.CmdReleaseStock}, cmds[3:], "all the compensations, none missing, none extra")
	assert.NotContains(t, cmds, saga.CmdCommitStock)
	assert.NotContains(t, cmds, saga.CmdRefundPayment, "nothing was charged")
	require.Eventually(t, func() bool { return s.published(t, "order.order.cancelled", p.OrderID) }, 30*time.Second, 100*time.Millisecond)
}

// A participant that never answers: the sweeper gives up on the step and compensates, and the order is cancelled.
func TestASilentParticipantIsTimedOutAndCompensated(t *testing.T) {
	s := start(t)
	p, err := s.uc.PlaceOrder.Execute(buyer(), placeIn("k-silent", "", "tok_ok"))
	require.NoError(t, err)
	s.parts.mu.Lock()
	s.parts.drop[saga.CmdReserveStock+"/"+p.OrderID] = true
	s.parts.mu.Unlock()

	reason := s.waitFor(t, p.OrderID, "CANCELLED")
	assert.Equal(t, saga.ReasonStepTimeout, reason)
	assert.Equal(t, []string{saga.CmdReserveStock, saga.CmdReleaseStock}, s.parts.commandsOf(p.OrderID))
}
