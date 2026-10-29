// Package fake provides in-memory implementations of the inventory ports for unit tests. The World is transactional
// (WithinTx restores every store when the function fails), so "stock change + saga reply, or nothing" can be asserted
// without a database. The repository contract suite (internal/repotest) runs against these fakes and against
// PostgreSQL.
package fake

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/an-vt/ecom/inventory-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// World is all the state of the fakes.
type World struct {
	mu           sync.Mutex
	inventories  map[string]domain.InventorySnapshot // by product id
	reservations map[string]domain.ReservationSnapshot
	Outbox       []outbox.Message
	FailOutbox   error
}

// NewWorld creates an empty world.
func NewWorld() *World {
	return &World{inventories: map[string]domain.InventorySnapshot{}, reservations: map[string]domain.ReservationSnapshot{}}
}

type state struct {
	inv    map[string]domain.InventorySnapshot
	res    map[string]domain.ReservationSnapshot
	outbox []outbox.Message
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
	before := state{clone(w.inventories), clone(w.reservations), append([]outbox.Message(nil), w.Outbox...)}
	w.mu.Unlock()
	if err := fn(ctx); err != nil {
		w.mu.Lock()
		w.inventories, w.reservations, w.Outbox = before.inv, before.res, before.outbox
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

// Replies returns the saga messages recorded in the outbox, as (type, order id) pairs.
func (w *World) Replies() [][2]string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out [][2]string
	for _, m := range w.Outbox {
		out = append(out, [2]string{m.Type, m.Key})
	}
	return out
}

// Reply decodes the payload of the i-th outbox message into v.
func (w *World) Reply(i int, v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	env, err := event.Parse(w.Outbox[i].Payload)
	if err != nil {
		return err
	}
	return env.Decode(v)
}

// ---- inventories --------------------------------------------------------------------------------------------------

type invRepo struct{ w *World }

// Inventories returns the inventory repository.
func (w *World) Inventories() domain.InventoryRepository { return invRepo{w} }

func (r invRepo) Insert(_ context.Context, inv *domain.Inventory) (bool, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.inventories[inv.ProductID()]; ok {
		return false, nil
	}
	r.w.inventories[inv.ProductID()] = inv.Snapshot()
	return true, nil
}

func (r invRepo) Lock(_ context.Context, ids []string) (map[string]*domain.Inventory, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	out := map[string]*domain.Inventory{}
	for _, id := range ids {
		if s, ok := r.w.inventories[id]; ok {
			out[id] = domain.RestoreInventory(s)
		}
	}
	return out, nil
}

func (r invRepo) Find(_ context.Context, id string) (*domain.Inventory, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.inventories[id]; ok {
		return domain.RestoreInventory(s), nil
	}
	return nil, domain.ErrInventoryNotFound()
}

func (r invRepo) Save(_ context.Context, inv *domain.Inventory) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.inventories[inv.ProductID()]; !ok {
		return domain.ErrInventoryNotFound()
	}
	r.w.inventories[inv.ProductID()] = inv.Snapshot()
	return nil
}

// ---- reservations --------------------------------------------------------------------------------------------------

type resRepo struct{ w *World }

// Reservations returns the reservation repository.
func (w *World) Reservations() domain.ReservationRepository { return resRepo{w} }

func (r resRepo) ForOrder(_ context.Context, orderID string) ([]*domain.Reservation, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var out []*domain.Reservation
	for _, s := range r.w.reservations {
		if s.OrderID == orderID {
			out = append(out, domain.RestoreReservation(s))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProductID() < out[j].ProductID() })
	return out, nil
}

func (r resRepo) Insert(_ context.Context, rs []*domain.Reservation) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, x := range rs {
		for _, e := range r.w.reservations {
			if e.OrderID == x.OrderID() && e.ProductID == x.ProductID() {
				return domain.ErrAlreadyReserved()
			}
		}
	}
	for _, x := range rs {
		r.w.reservations[x.ID()] = x.Snapshot()
	}
	return nil
}

func (r resRepo) Save(_ context.Context, x *domain.Reservation) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.reservations[x.ID()]; !ok {
		return domain.ErrInventoryNotFound()
	}
	r.w.reservations[x.ID()] = x.Snapshot()
	return nil
}

func (r resRepo) ExpiredOrders(_ context.Context, now time.Time, limit int) ([]string, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, s := range r.w.reservations {
		if s.Status == domain.ReservationReserved && !now.Before(s.ExpiresAt) && !seen[s.OrderID] {
			seen[s.OrderID] = true
			out = append(out, s.OrderID)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
