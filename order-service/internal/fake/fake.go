// Package fake provides in-memory implementations of the order ports for unit tests. The World is transactional
// (WithinTx restores every store when the function fails), so "order status + saga state + commands, or nothing" can be
// asserted without a database. The repository contract suite (internal/repotest) runs against these fakes and against
// PostgreSQL.
package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// World is all the state of the fakes.
type World struct {
	mu         sync.Mutex
	orders     map[string]domain.OrderSnapshot
	sagas      map[string]domain.SagaSnapshot
	keys       map[string]domain.IdempotencyRecord
	Outbox     []outbox.Message
	FailOutbox error
}

// NewWorld creates an empty world.
func NewWorld() *World {
	return &World{orders: map[string]domain.OrderSnapshot{}, sagas: map[string]domain.SagaSnapshot{}, keys: map[string]domain.IdempotencyRecord{}}
}

func clone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// WithinTx runs fn and restores every store if it fails.
func (w *World) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	w.mu.Lock()
	o, s, k, out := clone(w.orders), clone(w.sagas), clone(w.keys), append([]outbox.Message(nil), w.Outbox...)
	w.mu.Unlock()
	if err := fn(ctx); err != nil {
		w.mu.Lock()
		w.orders, w.sagas, w.keys, w.Outbox = o, s, k, out
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

// Topics returns the topic of every outbox message, in order.
func (w *World) Topics() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, 0, len(w.Outbox))
	for _, m := range w.Outbox {
		out = append(out, m.Topic)
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

// Key returns the aggregate key of the i-th outbox message.
func (w *World) Key(i int) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.Outbox[i].Key
}

// Order returns the stored snapshot of an order.
func (w *World) Order(id string) (domain.OrderSnapshot, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.orders[id]
	return s, ok
}

// Saga returns the stored snapshot of a saga.
func (w *World) Saga(id string) (domain.SagaSnapshot, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s, ok := w.sagas[id]
	return s, ok
}

// OrderIDs lists the stored orders' ids.
func (w *World) OrderIDs() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var ids []string
	for id := range w.orders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ---- orders -------------------------------------------------------------------------------------------------------

type orderRepo struct{ w *World }

// Orders returns the order repository.
func (w *World) Orders() domain.OrderRepository { return orderRepo{w} }

func (r orderRepo) Insert(_ context.Context, o *domain.Order) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.orders[o.ID()]; ok {
		return errs.AlreadyExists("ORDER_EXISTS", "the order already exists")
	}
	r.w.orders[o.ID()] = o.Snapshot()
	return nil
}

func (r orderRepo) Find(_ context.Context, id string) (*domain.Order, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.orders[id]; ok {
		return domain.RestoreOrder(s), nil
	}
	return nil, domain.ErrNotFound()
}

func (r orderRepo) Lock(ctx context.Context, id string) (*domain.Order, error) {
	return r.Find(ctx, id)
}

// Save writes only the mutable fields, like the SQL implementation: the items and the payment token never change.
func (r orderRepo) Save(_ context.Context, o *domain.Order) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	cur, ok := r.w.orders[o.ID()]
	if !ok {
		return domain.ErrNotFound()
	}
	n := o.Snapshot()
	cur.Status, cur.TotalDiscount, cur.TotalCheckout = n.Status, n.TotalDiscount, n.TotalCheckout
	cur.CancelReason, cur.TrackingNumber, cur.Version, cur.UpdatedAt = n.CancelReason, n.TrackingNumber, n.Version, n.UpdatedAt
	r.w.orders[o.ID()] = cur
	return nil
}

func (r orderRepo) ListByUser(_ context.Context, userID string, offset, limit int) ([]*domain.Order, int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var all []domain.OrderSnapshot
	for _, s := range r.w.orders {
		if s.UserID == userID {
			all = append(all, s)
		}
	}
	sort.Slice(all, func(i, j int) bool { // newest first, id as the tie-break
		if !all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].CreatedAt.After(all[j].CreatedAt)
		}
		return all[i].ID > all[j].ID
	})
	total := int64(len(all))
	offset = min(offset, len(all))
	end := min(offset+limit, len(all))
	out := make([]*domain.Order, 0, end-offset)
	for _, s := range all[offset:end] {
		out = append(out, domain.RestoreOrder(s))
	}
	return out, total, nil
}

// ---- sagas --------------------------------------------------------------------------------------------------------

type sagaRepo struct{ w *World }

// Sagas returns the saga repository.
func (w *World) Sagas() domain.SagaRepository { return sagaRepo{w} }

func (r sagaRepo) Insert(_ context.Context, s *domain.Saga) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.sagas[s.ID()]; ok {
		return errs.AlreadyExists("SAGA_EXISTS", "the saga already exists")
	}
	r.w.sagas[s.ID()] = s.Snapshot()
	return nil
}

func (r sagaRepo) Lock(_ context.Context, id string) (*domain.Saga, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.sagas[id]; ok {
		return domain.RestoreSaga(s), nil
	}
	return nil, domain.ErrSagaNotFound()
}

func (r sagaRepo) Save(_ context.Context, s *domain.Saga) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.sagas[s.ID()]; !ok {
		return domain.ErrSagaNotFound()
	}
	r.w.sagas[s.ID()] = s.Snapshot()
	return nil
}

func (r sagaRepo) LockNextDue(_ context.Context, now time.Time) (*domain.Saga, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var due []domain.SagaSnapshot
	for _, s := range r.w.sagas {
		if !s.State.Terminal() && s.StepDeadline.Before(now) {
			due = append(due, s)
		}
	}
	if len(due) == 0 {
		return nil, nil
	}
	sort.Slice(due, func(i, j int) bool {
		if !due[i].StepDeadline.Equal(due[j].StepDeadline) {
			return due[i].StepDeadline.Before(due[j].StepDeadline)
		}
		return due[i].ID < due[j].ID
	})
	return domain.RestoreSaga(due[0]), nil
}

// ---- idempotency keys ---------------------------------------------------------------------------------------------

type keyRepo struct{ w *World }

// Keys returns the idempotency repository.
func (w *World) Keys() domain.IdempotencyRepository { return keyRepo{w} }

func (r keyRepo) Claim(_ context.Context, rec domain.IdempotencyRecord) (bool, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	k := rec.UserID + "/" + rec.Key
	if _, ok := r.w.keys[k]; ok {
		return false, nil
	}
	r.w.keys[k] = rec
	return true, nil
}

func (r keyRepo) Find(_ context.Context, userID, key string) (*domain.IdempotencyRecord, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if rec, ok := r.w.keys[userID+"/"+key]; ok {
		return &rec, nil
	}
	return nil, nil
}

// ---- remote services ----------------------------------------------------------------------------------------------

// Catalog is a canned catalog. It is safe for concurrent use.
type Catalog struct {
	mu        sync.Mutex
	Products_ map[string]domain.Product
	Err       error
	Calls     int
}

// Products implements domain.Catalog.
func (c *Catalog) Products(_ context.Context, ids []string) (map[string]domain.Product, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	if c.Err != nil {
		return nil, c.Err
	}
	out := map[string]domain.Product{}
	for _, id := range ids {
		if p, ok := c.Products_[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

// Discounts is a canned discount service: each code takes Percent off the lines it is asked about.
type Discounts struct {
	mu      sync.Mutex
	Percent map[string]int64 // "shop/CODE" -> percent
	Err     error
	Calls   []string
}

// Preview implements domain.Discounts.
func (d *Discounts) Preview(_ context.Context, shopID, code string, lines []domain.PreviewLine) (domain.DiscountPreview, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.Calls = append(d.Calls, shopID+"/"+code)
	if d.Err != nil {
		return domain.DiscountPreview{}, d.Err
	}
	pct, ok := d.Percent[shopID+"/"+code]
	if !ok {
		return domain.DiscountPreview{}, errs.NotFound("DISCOUNT_NOT_FOUND", "no such discount code")
	}
	var sub int64
	for _, l := range lines {
		sub += l.Price * l.Quantity
	}
	disc := sub * pct / 100
	return domain.DiscountPreview{Discount: disc, Subtotal: sub, Total: sub - disc}, nil
}
