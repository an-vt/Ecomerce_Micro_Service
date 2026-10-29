// Package fake provides in-memory implementations of the cart ports for unit tests. The cart store implements the
// same optimistic-version contract as MongoDB; the repository contract suite (internal/repotest) runs against both.
package fake

import (
	"context"
	"sync"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Carts is an in-memory CartRepository with optimistic concurrency.
type Carts struct {
	mu     sync.Mutex
	byUser map[string]domain.CartSnapshot
	// BeforeSave lets a test change the stored cart between a use case's load and its save (to force a conflict).
	BeforeSave func(userID string)
	Saves      int
}

// NewCarts creates an empty store.
func NewCarts() *Carts { return &Carts{byUser: map[string]domain.CartSnapshot{}} }

// FindByUser implements domain.CartRepository.
func (r *Carts) FindByUser(_ context.Context, userID string) (*domain.Cart, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.byUser[userID]; ok {
		return domain.RestoreCart(s), nil
	}
	return nil, nil
}

// Save implements domain.CartRepository.
func (r *Carts) Save(_ context.Context, c *domain.Cart) error {
	if r.BeforeSave != nil {
		hook := r.BeforeSave
		r.BeforeSave = nil
		hook(c.UserID())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	cur, exists := r.byUser[c.UserID()]
	if (!exists && c.Version() != 0) || (exists && cur.Version != c.Version()) {
		return errs.Aborted(domain.CodeCartConflict, "the cart was changed concurrently")
	}
	c.Bump()
	r.byUser[c.UserID()] = c.Snapshot()
	r.Saves++
	return nil
}

// Put stores a snapshot directly (test setup).
func (r *Carts) Put(s domain.CartSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byUser[s.UserID] = s
}

// Catalog is a settable domain.Catalog.
type Catalog struct {
	Products_ map[string]domain.ProductInfo
	Err       error
	Calls     int
}

// Products implements domain.Catalog.
func (c *Catalog) Products(_ context.Context, ids []string) (map[string]domain.ProductInfo, error) {
	c.Calls++
	if c.Err != nil {
		return nil, c.Err
	}
	out := map[string]domain.ProductInfo{}
	for _, id := range ids {
		if p, ok := c.Products_[id]; ok {
			out[id] = p
		}
	}
	return out, nil
}

// Stock is a settable domain.Stock: Have maps product to available units.
type Stock struct {
	Have  map[string]int64
	Err   error
	Calls [][]domain.StockLine
}

// Available implements domain.Stock.
func (s *Stock) Available(_ context.Context, lines []domain.StockLine) (bool, error) {
	s.Calls = append(s.Calls, lines)
	if s.Err != nil {
		return false, s.Err
	}
	for _, l := range lines {
		if s.Have[l.ProductID] < l.Quantity {
			return false, nil
		}
	}
	return true, nil
}
