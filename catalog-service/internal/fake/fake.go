// Package fake provides in-memory implementations of the catalog ports for unit tests. The World is transactional:
// WithinTx restores every store when the function fails, so "product + outbox event, or nothing" can be asserted
// without a database. The repository contract suite (internal/repotest) runs against these fakes and against
// MongoDB, which keeps the two from drifting apart.
package fake

import (
	"context"
	"sort"
	"strings"
	"sync"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/outbox"
)

// World is all the state of the fakes.
type World struct {
	mu       sync.Mutex
	products map[string]domain.ProductSnapshot
	spus     map[string]domain.Spu
	skus     map[string]domain.Sku
	comments map[string]domain.Comment
	shops    map[string]domain.ShopProjection

	Outbox     []outbox.Message
	FailOutbox error
}

// NewWorld creates an empty world.
func NewWorld() *World {
	return &World{products: map[string]domain.ProductSnapshot{}, spus: map[string]domain.Spu{}, skus: map[string]domain.Sku{},
		comments: map[string]domain.Comment{}, shops: map[string]domain.ShopProjection{}}
}

type state struct {
	products map[string]domain.ProductSnapshot
	spus     map[string]domain.Spu
	skus     map[string]domain.Sku
	comments map[string]domain.Comment
	shops    map[string]domain.ShopProjection
	outbox   []outbox.Message
}

func clone[K comparable, V any](m map[K]V) map[K]V {
	out := make(map[K]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func (w *World) save() state {
	w.mu.Lock()
	defer w.mu.Unlock()
	return state{clone(w.products), clone(w.spus), clone(w.skus), clone(w.comments), clone(w.shops), append([]outbox.Message(nil), w.Outbox...)}
}

func (w *World) restore(s state) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.products, w.spus, w.skus, w.comments, w.shops, w.Outbox = s.products, s.spus, s.skus, s.comments, s.shops, s.outbox
}

// WithinTx runs fn and restores every store if it fails.
func (w *World) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	before := w.save()
	if err := fn(ctx); err != nil {
		w.restore(before)
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

// OutboxTypes lists the types of the recorded messages.
func (w *World) OutboxTypes() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]string, len(w.Outbox))
	for i, m := range w.Outbox {
		out[i] = m.Type
	}
	return out
}

// ProductCount returns the number of stored products.
func (w *World) ProductCount() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.products) }

// ---- products ---------------------------------------------------------------------------------------------------

type productRepo struct{ w *World }

// Products returns the product repository.
func (w *World) Products() domain.ProductRepository { return productRepo{w} }

func (r productRepo) Create(_ context.Context, p *domain.Product) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, e := range r.w.products {
		if e.Name == p.Name() {
			return domain.ErrProductNameTaken()
		}
	}
	r.w.products[p.ID()] = p.Snapshot()
	return nil
}

func (r productRepo) FindByID(_ context.Context, id string) (*domain.Product, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.products[id]; ok {
		return domain.RestoreProduct(s), nil
	}
	return nil, domain.ErrProductNotFound()
}

func (r productRepo) FindByIDs(_ context.Context, ids []string) ([]*domain.Product, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var out []*domain.Product
	for _, id := range ids {
		if s, ok := r.w.products[id]; ok {
			out = append(out, domain.RestoreProduct(s))
		}
	}
	return out, nil
}

func (r productRepo) Update(_ context.Context, p *domain.Product) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if _, ok := r.w.products[p.ID()]; !ok {
		return domain.ErrProductNotFound()
	}
	for id, e := range r.w.products {
		if id != p.ID() && e.Name == p.Name() {
			return domain.ErrProductNameTaken()
		}
	}
	r.w.products[p.ID()] = p.Snapshot()
	return nil
}

func page(items []domain.ProductSnapshot, pageNo, limit int) ([]*domain.Product, int64) {
	total := int64(len(items))
	start := (pageNo - 1) * limit
	if start > len(items) {
		start = len(items)
	}
	end := start + limit
	if end > len(items) {
		end = len(items)
	}
	out := make([]*domain.Product, 0, end-start)
	for _, s := range items[start:end] {
		out = append(out, domain.RestoreProduct(s))
	}
	return out, total
}

func (r productRepo) filter(pred func(domain.ProductSnapshot) bool) []domain.ProductSnapshot {
	var out []domain.ProductSnapshot
	for _, s := range r.w.products {
		if pred(s) {
			out = append(out, s)
		}
	}
	return out
}

func newestFirst(s []domain.ProductSnapshot) {
	sort.Slice(s, func(i, j int) bool {
		if !s[i].CreatedAt.Equal(s[j].CreatedAt) {
			return s[i].CreatedAt.After(s[j].CreatedAt)
		}
		return s[i].ID < s[j].ID
	})
}

func (r productRepo) ListPublished(_ context.Context, q domain.ListQuery) ([]*domain.Product, int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	items := r.filter(func(s domain.ProductSnapshot) bool { return s.IsPublished })
	switch q.Sort {
	case "price_asc":
		sort.Slice(items, func(i, j int) bool { return items[i].Price < items[j].Price })
	case "price_desc":
		sort.Slice(items, func(i, j int) bool { return items[i].Price > items[j].Price })
	default:
		newestFirst(items)
	}
	out, total := page(items, q.Page, q.Limit)
	return out, total, nil
}

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9') }) {
		out[w] = true
	}
	return out
}

func (r productRepo) Search(_ context.Context, keyword string, pageNo, limit int) ([]*domain.Product, int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	q := words(keyword)
	items := r.filter(func(s domain.ProductSnapshot) bool {
		if !s.IsPublished {
			return false
		}
		have := words(s.Name + " " + s.Description)
		for w := range q {
			if have[w] {
				return true
			}
		}
		return false
	})
	newestFirst(items)
	out, total := page(items, pageNo, limit)
	return out, total, nil
}

func (r productRepo) ListByShop(_ context.Context, shopID string, published bool, pageNo, limit int) ([]*domain.Product, int64, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	items := r.filter(func(s domain.ProductSnapshot) bool { return s.ShopID == shopID && s.IsPublished == published })
	newestFirst(items)
	out, total := page(items, pageNo, limit)
	return out, total, nil
}

func (r productRepo) IDsByShop(_ context.Context, shopID string) ([]string, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var ids []string
	for _, s := range r.w.products {
		if s.ShopID == shopID && s.IsPublished {
			ids = append(ids, s.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// ---- spus / skus --------------------------------------------------------------------------------------------------

type spuRepo struct{ w *World }

// Spus returns the SPU repository.
func (w *World) Spus() domain.SpuRepository { return spuRepo{w} }

func (r spuRepo) Create(_ context.Context, spu *domain.Spu, skus []*domain.Sku) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	for _, e := range r.w.spus {
		if e.Name == spu.Name {
			return domain.ErrSpuNameTaken()
		}
	}
	r.w.spus[spu.ID] = *spu
	for _, s := range skus {
		r.w.skus[s.ID] = *s
	}
	return nil
}

func (r spuRepo) FindByID(_ context.Context, id string) (*domain.Spu, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.spus[id]; ok {
		return &s, nil
	}
	return nil, domain.ErrSpuNotFound()
}

func (r spuRepo) SkusOf(_ context.Context, spuID string) ([]*domain.Sku, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var out []*domain.Sku
	for _, s := range r.w.skus {
		if s.SpuID == spuID {
			c := s
			out = append(out, &c)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Sort != out[j].Sort {
			return out[i].Sort < out[j].Sort
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (r spuRepo) FindSku(_ context.Context, skuID, spuID string) (*domain.Sku, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if s, ok := r.w.skus[skuID]; ok && s.SpuID == spuID {
		return &s, nil
	}
	return nil, domain.ErrSkuNotFound()
}

// ---- comments (nested set) ---------------------------------------------------------------------------------------

type commentRepo struct{ w *World }

// Comments returns the comment repository.
func (w *World) Comments() domain.CommentRepository { return commentRepo{w} }

func (r commentRepo) FindByID(_ context.Context, id string) (*domain.Comment, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	if c, ok := r.w.comments[id]; ok {
		return &c, nil
	}
	return nil, domain.ErrCommentNotFound()
}

func (r commentRepo) AddRoot(_ context.Context, c *domain.Comment) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var maxRight int64
	for _, e := range r.w.comments {
		if e.ProductID == c.ProductID && e.Right > maxRight {
			maxRight = e.Right
		}
	}
	p := domain.PlanRoot(maxRight)
	c.Left, c.Right = p.Left, p.Right
	r.w.comments[c.ID] = *c
	return nil
}

func (r commentRepo) AddReply(_ context.Context, parent, c *domain.Comment) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	cur, ok := r.w.comments[parent.ID]
	if !ok {
		return domain.ErrCommentNotFound()
	}
	p := domain.PlanReply(cur.Right)
	for id, e := range r.w.comments {
		if e.ProductID != c.ProductID {
			continue
		}
		if e.Right >= p.ShiftRightsFrom {
			e.Right += 2
		}
		if e.Left > p.ShiftLeftsAfter {
			e.Left += 2
		}
		r.w.comments[id] = e
	}
	c.Left, c.Right, c.ParentID = p.Left, p.Right, parent.ID
	r.w.comments[c.ID] = *c
	return nil
}

func (r commentRepo) DeleteSubtree(_ context.Context, c *domain.Comment) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	cur, ok := r.w.comments[c.ID]
	if !ok {
		return domain.ErrCommentNotFound()
	}
	rm := domain.PlanDelete(cur)
	for id, e := range r.w.comments {
		if e.ProductID != cur.ProductID {
			continue
		}
		if e.Left >= rm.Left && e.Right <= rm.Right {
			delete(r.w.comments, id)
			continue
		}
		if e.Left > rm.Right {
			e.Left -= rm.Width
		}
		if e.Right > rm.Right {
			e.Right -= rm.Width
		}
		r.w.comments[id] = e
	}
	return nil
}

func (r commentRepo) List(_ context.Context, productID, parentID string, limit, skip int) ([]*domain.Comment, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	var all []domain.Comment
	for _, e := range r.w.comments {
		if e.ProductID == productID && e.ParentID == parentID {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Left < all[j].Left })
	var out []*domain.Comment
	for i, e := range all {
		if i < skip || len(out) >= limit {
			continue
		}
		c := e
		out = append(out, &c)
	}
	return out, nil
}

// ---- shops --------------------------------------------------------------------------------------------------------

type shopRepo struct{ w *World }

// Shops returns the shop projection repository.
func (w *World) Shops() domain.ShopRepository { return shopRepo{w} }

func (r shopRepo) Upsert(_ context.Context, s domain.ShopProjection) error {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	r.w.shops[s.ID] = s
	return nil
}

func (r shopRepo) FindByIDs(_ context.Context, ids []string) (map[string]domain.ShopProjection, error) {
	r.w.mu.Lock()
	defer r.w.mu.Unlock()
	out := map[string]domain.ShopProjection{}
	for _, id := range ids {
		if s, ok := r.w.shops[id]; ok {
			out[id] = s
		}
	}
	return out, nil
}

// ---- cache ---------------------------------------------------------------------------------------------------------

// Cache is an in-memory SkuCache that caches misses too and counts how often the loader ran.
type Cache struct {
	mu    sync.Mutex
	items map[string]*domain.Sku // nil value = cached miss
	Loads int
}

// NewCache creates an empty cache.
func NewCache() *Cache { return &Cache{items: map[string]*domain.Sku{}} }

// Lookup implements usecase.SkuCache.
func (c *Cache) Lookup(ctx context.Context, skuID string, load func(ctx context.Context) (*domain.Sku, bool, error)) (*domain.Sku, bool, error) {
	c.mu.Lock()
	v, hit := c.items[skuID]
	c.mu.Unlock()
	if hit {
		return v, v != nil, nil
	}
	s, found, err := load(ctx)
	if err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	c.Loads++
	if found {
		c.items[skuID] = s
	} else {
		c.items[skuID] = nil
	}
	c.mu.Unlock()
	return s, found, nil
}
