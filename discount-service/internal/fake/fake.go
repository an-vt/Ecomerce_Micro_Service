// Package fake provides in-memory implementations of the discount ports for unit tests. The World is transactional
// (WithinTx restores every store when the function fails), so "redemption + saga reply, or nothing" can be asserted
// without a database. The repository contract suite (internal/repotest) runs against these fakes and against
// PostgreSQL.
package fake

import (
	"context"
	"sort"
	"sync"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// World is all the state of the fakes.
type World struct {
	mu         sync.Mutex
	discounts  map[string]domain.DiscountSnapshot
	usages     map[string]domain.UsageSnapshot
	Outbox     []outbox.Message
	FailOutbox error
}

// NewWorld creates an empty world.
func NewWorld() *World {
	return &World{discounts: map[string]domain.DiscountSnapshot{}, usages: map[string]domain.UsageSnapshot{}}
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
	d, u, o := clone(w.discounts), clone(w.usages), append([]outbox.Message(nil), w.Outbox...)
	w.mu.Unlock()
	if err := fn(ctx); err != nil {
		w.mu.Lock()
		w.discounts, w.usages, w.Outbox = d, u, o
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

// ---- discounts ----------------------------------------------------------------------------------------------------

type discountRepo struct{ w *World }

// Discounts returns the discount repository.
func (w *World) Discounts() domain.DiscountRepository { return discountRepo{w} }

func (r discountRepo) Insert(_ context.Context, d *domain.Discount) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, s := range r.w.discounts {
		if s.ShopID == d.ShopID() && s.Code == d.Code() {
			return domain.ErrCodeTaken()
		}
	}
	r.w.discounts[d.ID()] = d.Snapshot()
	return nil
}

func (r discountRepo) Save(_ context.Context, d *domain.Discount) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.discounts[d.ID()]; !ok {
		return domain.ErrNotFound()
	}
	r.w.discounts[d.ID()] = d.Snapshot()
	return nil
}

func (r discountRepo) Find(_ context.Context, id string) (*domain.Discount, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.discounts[id]; ok {
		return domain.Restore(s), nil
	}
	return nil, domain.ErrNotFound()
}

func (r discountRepo) FindByCode(_ context.Context, ref domain.CodeRef) (*domain.Discount, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, s := range r.w.discounts {
		if s.ShopID == ref.ShopID && s.Code == ref.Code {
			return domain.Restore(s), nil
		}
	}
	return nil, domain.ErrNotFound()
}

func (r discountRepo) LockByID(ctx context.Context, id string) (*domain.Discount, error) {
	return r.Find(ctx, id)
}

func (r discountRepo) LockByCodes(ctx context.Context, refs []domain.CodeRef) (map[domain.CodeRef]*domain.Discount, error) {
	out := map[domain.CodeRef]*domain.Discount{}
	for _, ref := range refs {
		if d, err := r.FindByCode(ctx, ref); err == nil {
			out[ref] = d
		}
	}
	return out, nil
}

func (r discountRepo) LockByIDs(ctx context.Context, ids []string) (map[string]*domain.Discount, error) {
	out := map[string]*domain.Discount{}
	for _, id := range ids {
		if d, err := r.Find(ctx, id); err == nil {
			out[id] = d
		}
	}
	return out, nil
}

func (r discountRepo) ListByShop(_ context.Context, shopID string, offset, limit int) ([]*domain.Discount, int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var all []domain.DiscountSnapshot
	for _, s := range r.w.discounts {
		if s.ShopID == shopID {
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
	if offset > len(all) {
		offset = len(all)
	}
	end := min(offset+limit, len(all))
	out := make([]*domain.Discount, 0, end-offset)
	for _, s := range all[offset:end] {
		out = append(out, domain.Restore(s))
	}
	return out, total, nil
}

// ---- usages -------------------------------------------------------------------------------------------------------

type usageRepo struct{ w *World }

// Usages returns the usage repository.
func (w *World) Usages() domain.UsageRepository { return usageRepo{w} }

func (r usageRepo) ForOrder(_ context.Context, orderID string) ([]*domain.Usage, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var snaps []domain.UsageSnapshot
	for _, s := range r.w.usages {
		if s.OrderID == orderID {
			snaps = append(snaps, s)
		}
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].DiscountID < snaps[j].DiscountID })
	out := make([]*domain.Usage, len(snaps))
	for i, s := range snaps {
		out[i] = domain.RestoreUsage(s)
	}
	return out, nil
}

func (r usageRepo) CountApplied(_ context.Context, userID string, discountIDs []string) (map[string]int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	want := map[string]bool{}
	for _, id := range discountIDs {
		want[id] = true
	}
	out := map[string]int64{}
	for _, s := range r.w.usages {
		if s.UserID == userID && s.Status == domain.UsageApplied && want[s.DiscountID] {
			out[s.DiscountID]++
		}
	}
	return out, nil
}

func (r usageRepo) Insert(_ context.Context, us []*domain.Usage) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, u := range us {
		s := u.Snapshot()
		for _, e := range r.w.usages {
			if e.DiscountID == s.DiscountID && e.OrderID == s.OrderID {
				return domain.ErrAlreadyUsed()
			}
		}
	}
	for _, u := range us {
		r.w.usages[u.Snapshot().ID] = u.Snapshot()
	}
	return nil
}

func (r usageRepo) Save(_ context.Context, u *domain.Usage) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	s := u.Snapshot()
	if _, ok := r.w.usages[s.ID]; !ok {
		return domain.ErrNotFound()
	}
	r.w.usages[s.ID] = s
	return nil
}

// Catalog is a canned catalog.
type Catalog struct {
	Shops     map[string][]string // shop -> product ids
	Products_ map[string]domain.ProductSummary
	Err       error
	Calls     int
}

func (c *Catalog) ProductIDsByShop(_ context.Context, shopID string) ([]string, error) {
	c.Calls++
	return append([]string(nil), c.Shops[shopID]...), c.Err
}

func (c *Catalog) Products(_ context.Context, ids []string) (map[string]domain.ProductSummary, error) {
	c.Calls++
	if c.Err != nil {
		return nil, c.Err
	}
	out := map[string]domain.ProductSummary{}
	for _, id := range ids {
		if p, ok := c.Products_[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}
