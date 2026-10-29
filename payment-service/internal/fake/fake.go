// Package fake provides in-memory implementations of the payment ports for unit tests. The World is transactional
// (WithinTx restores the store when the function fails), so "payment status + saga reply, or nothing" can be asserted
// without a database. The repository contract suite (internal/repotest) runs against these fakes and against
// PostgreSQL.
package fake

import (
	"context"
	"sync"

	"github.com/an-vt/ecom/payment-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// World is all the state of the fakes.
type World struct {
	mu         sync.Mutex
	payments   map[string]domain.Snapshot // by order id
	Outbox     []outbox.Message
	FailOutbox error
}

// NewWorld creates an empty world.
func NewWorld() *World { return &World{payments: map[string]domain.Snapshot{}} }

// WithinTx runs fn and restores the store if it fails.
func (w *World) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	w.mu.Lock()
	before := make(map[string]domain.Snapshot, len(w.payments))
	for k, v := range w.payments {
		before[k] = v
	}
	out := append([]outbox.Message(nil), w.Outbox...)
	w.mu.Unlock()
	if err := fn(ctx); err != nil {
		w.mu.Lock()
		w.payments, w.Outbox = before, out
		w.mu.Unlock()
		return err
	}
	return nil
}

// Add implements the outbox port.
func (w *World) Add(_ context.Context, msgs ...outbox.Message) error {
	if w.FailOutbox != nil {
		return w.FailOutbox
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Outbox = append(w.Outbox, msgs...)
	return nil
}

// Types returns the type of every outbox message, in order.
func (w *World) Types() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.Outbox))
	for _, m := range w.Outbox {
		out = append(out, m.Type)
	}
	return out
}

// Decode decodes the payload of the i-th outbox message into v.
func (w *World) Decode(i int, v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	env, err := event.Parse(w.Outbox[i].Payload)
	if err != nil {
		return err
	}
	return env.Decode(v)
}

// Payment returns the stored snapshot of an order's payment.
func (w *World) Payment(orderID string) (domain.Snapshot, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.payments[orderID]
	return s, ok
}

type repo struct{ w *World }

// Payments returns the payment repository.
func (w *World) Payments() domain.PaymentRepository { return repo{w} }

func (r repo) Insert(_ context.Context, p *domain.Payment) (bool, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.payments[p.OrderID()]; ok {
		return false, nil
	}
	r.w.payments[p.OrderID()] = p.Snapshot()
	return true, nil
}

func (r repo) LockByOrder(_ context.Context, orderID string) (*domain.Payment, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.payments[orderID]; ok {
		return domain.Restore(s), nil
	}
	return nil, domain.ErrNotFound()
}

func (r repo) Save(_ context.Context, p *domain.Payment) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.payments[p.OrderID()]; !ok {
		return domain.ErrNotFound()
	}
	r.w.payments[p.OrderID()] = p.Snapshot()
	return nil
}

// Provider is a scriptable provider that counts its calls.
type Provider struct {
	mu          sync.Mutex
	ChargeFn    func(ctx context.Context, r domain.ChargeRequest) (string, error)
	RefundFn    func(ctx context.Context, r domain.RefundRequest) error
	ChargeCalls []domain.ChargeRequest
	RefundCalls []domain.RefundRequest
}

// Charge implements domain.Provider.
func (p *Provider) Charge(ctx context.Context, r domain.ChargeRequest) (string, error) {
	p.mu.Lock()
	p.ChargeCalls = append(p.ChargeCalls, r)
	fn := p.ChargeFn
	p.mu.Unlock()
	if fn == nil {
		return "ref_" + r.Key, nil
	}
	return fn(ctx, r)
}

// Refund implements domain.Provider.
func (p *Provider) Refund(ctx context.Context, r domain.RefundRequest) error {
	p.mu.Lock()
	p.RefundCalls = append(p.RefundCalls, r)
	fn := p.RefundFn
	p.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, r)
}
