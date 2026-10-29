// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fakes and
// against PostgreSQL, so a fake can never promise something the real repository does not do.
package repotest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Repos are the repositories under test.
type Repos struct {
	Discounts domain.DiscountRepository
	Usages    domain.UsageRepository
}

// New returns a fresh, empty set of repositories for one test.
type New func(t *testing.T) Repos

var ctx = context.Background()
var base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

// Run executes the whole suite.
func Run(t *testing.T, newRepos New) {
	t.Run("Discounts", func(t *testing.T) { discounts(t, newRepos(t)) })
	t.Run("Locking", func(t *testing.T) { locking(t, newRepos(t)) })
	t.Run("Listing", func(t *testing.T) { listing(t, newRepos(t)) })
	t.Run("Usages", func(t *testing.T) { usages(t, newRepos(t)) })
}

func uuid(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012d", n) }

// Disc builds a valid discount; created offsets its creation time so listing order is deterministic.
func Disc(t *testing.T, n int, shop, code string, mut func(*domain.NewInput)) *domain.Discount {
	t.Helper()
	in := domain.NewInput{ID: uuid(n), ShopID: shop, Code: code, Name: "Name " + code, Description: "d", Type: domain.TypePercent, Value: 10,
		StartDate: base.Add(-time.Hour), EndDate: base.Add(48 * time.Hour), MaxUses: 5, MaxUsesPerUser: 2, MinOrderValue: 1000}
	if mut != nil {
		mut(&in)
	}
	d, err := domain.New(in, base.Add(time.Duration(n)*time.Second))
	require.NoError(t, err)
	return d
}

func discounts(t *testing.T, r Repos) {
	a := Disc(t, 1, "shop-a", "SPRING", func(in *domain.NewInput) {
		in.AppliesTo, in.ProductIDs = domain.ScopeSpecific, []string{"p1", "p2"}
	})
	require.NoError(t, r.Discounts.Insert(ctx, a))

	got, err := r.Discounts.Find(ctx, a.ID())
	require.NoError(t, err)
	assert.Equal(t, a.Snapshot(), got.Snapshot(), "round trip keeps every field, products in order")
	byCode, err := r.Discounts.FindByCode(ctx, domain.CodeRef{ShopID: "shop-a", Code: "SPRING"})
	require.NoError(t, err)
	assert.Equal(t, a.ID(), byCode.ID())

	_, err = r.Discounts.Find(ctx, uuid(99))
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err))
	_, err = r.Discounts.FindByCode(ctx, domain.CodeRef{ShopID: "shop-b", Code: "SPRING"})
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err), "codes are per shop")

	dup := Disc(t, 2, "shop-a", "SPRING", nil)
	assert.Equal(t, domain.CodeCodeTaken, errs.CodeOf(r.Discounts.Insert(ctx, dup)), "a shop cannot reuse a code")
	assert.NoError(t, r.Discounts.Insert(ctx, Disc(t, 3, "shop-b", "SPRING", nil)), "another shop can")

	require.NoError(t, got.Update(domain.Patch{ProductIDs: []string{"p9"}, ReplaceProducts: true}, base.Add(time.Hour)))
	require.NoError(t, got.Use(base))
	require.NoError(t, r.Discounts.Save(ctx, got))
	again, err := r.Discounts.Find(ctx, a.ID())
	require.NoError(t, err)
	assert.Equal(t, got.Snapshot(), again.Snapshot())
	assert.EqualValues(t, 1, again.Snapshot().UsedCount)

	ghost := Disc(t, 50, "shop-a", "GHOST", nil)
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(r.Discounts.Save(ctx, ghost)))
}

func locking(t *testing.T, r Repos) {
	for i, code := range []string{"AAA", "BBB", "CCC"} {
		require.NoError(t, r.Discounts.Insert(ctx, Disc(t, 10+i, "shop-a", code, nil)))
	}
	require.NoError(t, r.Discounts.Insert(ctx, Disc(t, 20, "shop-b", "AAA", nil)))

	got, err := r.Discounts.LockByCodes(ctx, []domain.CodeRef{
		{ShopID: "shop-b", Code: "AAA"}, {ShopID: "shop-a", Code: "CCC"}, {ShopID: "shop-a", Code: "NOPE"}})
	require.NoError(t, err)
	require.Len(t, got, 2, "unknown refs are absent")
	assert.Equal(t, uuid(20), got[domain.CodeRef{ShopID: "shop-b", Code: "AAA"}].ID())
	assert.Equal(t, uuid(12), got[domain.CodeRef{ShopID: "shop-a", Code: "CCC"}].ID())

	byID, err := r.Discounts.LockByIDs(ctx, []string{uuid(11), uuid(99), uuid(10)})
	require.NoError(t, err)
	assert.Len(t, byID, 2)
	assert.Contains(t, byID, uuid(10))

	one, err := r.Discounts.LockByID(ctx, uuid(11))
	require.NoError(t, err)
	assert.Equal(t, "BBB", one.Code())
	_, err = r.Discounts.LockByID(ctx, uuid(99))
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err))

	empty, err := r.Discounts.LockByCodes(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func listing(t *testing.T, r Repos) {
	for i := 1; i <= 5; i++ {
		require.NoError(t, r.Discounts.Insert(ctx, Disc(t, 30+i, "shop-a", fmt.Sprintf("CODE%d", i), nil)))
	}
	require.NoError(t, r.Discounts.Insert(ctx, Disc(t, 40, "shop-b", "OTHER", nil)))

	page1, total, err := r.Discounts.ListByShop(ctx, "shop-a", 0, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 5, total, "the total counts only this shop")
	require.Len(t, page1, 2)
	assert.Equal(t, "CODE5", page1[0].Code(), "newest first")
	assert.Equal(t, "CODE4", page1[1].Code())
	page3, _, err := r.Discounts.ListByShop(ctx, "shop-a", 4, 2)
	require.NoError(t, err)
	require.Len(t, page3, 1)
	assert.Equal(t, "CODE1", page3[0].Code())
	past, _, err := r.Discounts.ListByShop(ctx, "shop-a", 50, 2)
	require.NoError(t, err)
	assert.Empty(t, past)
	none, total, err := r.Discounts.ListByShop(ctx, "nobody", 0, 10)
	require.NoError(t, err)
	assert.Empty(t, none)
	assert.EqualValues(t, 0, total)
}

func usage(t *testing.T, n int, discount, user, order string, amount int64) *domain.Usage {
	t.Helper()
	u, err := domain.NewUsage(uuid(n), discount, user, order, amount, base)
	require.NoError(t, err)
	return u
}

func usages(t *testing.T, r Repos) {
	d1, d2 := Disc(t, 60, "shop-a", "ONE", nil), Disc(t, 61, "shop-a", "TWO", nil)
	require.NoError(t, r.Discounts.Insert(ctx, d1))
	require.NoError(t, r.Discounts.Insert(ctx, d2))

	none, err := r.Usages.ForOrder(ctx, "order-1")
	require.NoError(t, err)
	assert.Empty(t, none)

	require.NoError(t, r.Usages.Insert(ctx, nil), "nothing to store is fine")
	require.NoError(t, r.Usages.Insert(ctx, []*domain.Usage{
		usage(t, 70, d1.ID(), "alice", "order-1", 500), usage(t, 71, d2.ID(), "alice", "order-1", 300)}))
	require.NoError(t, r.Usages.Insert(ctx, []*domain.Usage{usage(t, 72, d1.ID(), "alice", "order-2", 100)}))
	require.NoError(t, r.Usages.Insert(ctx, []*domain.Usage{usage(t, 73, d1.ID(), "bob", "order-3", 100)}))

	got, err := r.Usages.ForOrder(ctx, "order-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, usage(t, 70, d1.ID(), "alice", "order-1", 500).Snapshot(), got[0].Snapshot(), "round trip, ordered by discount")

	// (discount, order) is unique: this is what makes a redelivered ApplyDiscounts harmless
	err = r.Usages.Insert(ctx, []*domain.Usage{usage(t, 74, d1.ID(), "alice", "order-1", 500)})
	assert.Equal(t, domain.CodeAlreadyUsed, errs.CodeOf(err))

	counts, err := r.Usages.CountApplied(ctx, "alice", []string{d1.ID(), d2.ID()})
	require.NoError(t, err)
	assert.EqualValues(t, 2, counts[d1.ID()])
	assert.EqualValues(t, 1, counts[d2.ID()])
	counts, err = r.Usages.CountApplied(ctx, "bob", []string{d1.ID(), d2.ID()})
	require.NoError(t, err)
	assert.EqualValues(t, 1, counts[d1.ID()])
	assert.EqualValues(t, 0, counts[d2.ID()])
	counts, err = r.Usages.CountApplied(ctx, "alice", nil)
	require.NoError(t, err)
	assert.Empty(t, counts)

	// a released usage no longer counts toward the per-user limit
	first := got[0]
	assert.True(t, first.Release(base.Add(time.Minute)))
	require.NoError(t, r.Usages.Save(ctx, first))
	counts, err = r.Usages.CountApplied(ctx, "alice", []string{d1.ID()})
	require.NoError(t, err)
	assert.EqualValues(t, 1, counts[d1.ID()])
	reread, err := r.Usages.ForOrder(ctx, "order-1")
	require.NoError(t, err)
	assert.Equal(t, domain.UsageReleased, reread[0].Status())

	ghost := usage(t, 99, d1.ID(), "alice", "order-9", 1)
	assert.Error(t, r.Usages.Save(ctx, ghost))
}
