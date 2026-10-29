package usecase_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/cart-service/internal/fake"
	"github.com/an-vt/ecom/cart-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
)

type fx struct {
	carts   *fake.Carts
	catalog *fake.Catalog
	stock   *fake.Stock
	clk     *clock.Fixed
	set     *usecase.Set
}

func newFx() *fx {
	f := &fx{carts: fake.NewCarts(), clk: clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)),
		catalog: &fake.Catalog{Products_: map[string]domain.ProductInfo{
			"p1":    {ID: "p1", ShopID: "s1", Name: "Shirt", Price: 150000, IsPublished: true},
			"p2":    {ID: "p2", ShopID: "s2", Name: "Coat", Price: 500000, IsPublished: true},
			"draft": {ID: "draft", ShopID: "s1", Name: "Draft", Price: 1, IsPublished: false},
		}},
		stock: &fake.Stock{Have: map[string]int64{"p1": 10, "p2": 3, "draft": 5}}}
	f.set = usecase.NewSet(&usecase.Deps{Carts: f.carts, Catalog: f.catalog, Stock: f.stock, Clock: f.clk, IDs: &idgen.Sequence{Prefix: "cart-"}})
	return f
}

func asUser(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeUser, Roles: []string{"user"}})
}

func code(err error) string { return errs.CodeOf(err) }

func TestOnlyBuyersHaveACart(t *testing.T) {
	f := newFx()
	anon := context.Background()
	shop := principal.With(anon, principal.Principal{ID: "s1", Type: principal.TypeShop})
	for name, ctx := range map[string]context.Context{"anonymous": anon, "shop": shop} {
		_, err := f.set.AddToCart.Execute(ctx, "p1", 1)
		require.Error(t, err, name)
		_, err = f.set.GetCart.Execute(ctx)
		require.Error(t, err, name)
		_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", Quantity: 1}})
		require.Error(t, err, name)
		_, err = f.set.RemoveItem.Execute(ctx, "p1")
		require.Error(t, err, name)
	}
	_, err := f.set.GetCart.Execute(anon)
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = f.set.GetCart.Execute(shop)
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

func TestAddToCartSnapshotsNameAndPrice(t *testing.T) {
	f := newFx()
	v, err := f.set.AddToCart.Execute(asUser("u1"), "p1", 2)
	require.NoError(t, err)
	require.Len(t, v.Items, 1)
	assert.Equal(t, usecase.ItemView{ProductID: "p1", ShopID: "s1", Name: "Shirt", Price: 150000, Quantity: 2}, v.Items[0])
	assert.Equal(t, int64(300000), v.Total)
	assert.Equal(t, int64(1), v.Lines)
	assert.Equal(t, "u1", v.UserID)

	f.catalog.Products_["p1"] = domain.ProductInfo{ID: "p1", ShopID: "s1", Name: "Shirt v2", Price: 120000, IsPublished: true}
	v, err = f.set.AddToCart.Execute(asUser("u1"), "p1", 1)
	require.NoError(t, err)
	assert.Equal(t, int64(3), v.Items[0].Quantity)
	assert.Equal(t, int64(120000), v.Items[0].Price, "the line is re-priced at the current price")
}

func TestAddToCartChecksStockAgainstTheCartTotal(t *testing.T) {
	f := newFx()
	_, err := f.set.AddToCart.Execute(asUser("u1"), "p2", 2)
	require.NoError(t, err)
	_, err = f.set.AddToCart.Execute(asUser("u1"), "p2", 2) // 2 in cart + 2 more > 3 in stock
	require.Error(t, err)
	assert.Equal(t, domain.CodeInsufficientStock, code(err))
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(err))
	last := f.stock.Calls[len(f.stock.Calls)-1]
	assert.Equal(t, []domain.StockLine{{ProductID: "p2", Quantity: 4}}, last, "the question is about the total the cart would hold")

	v, _ := f.set.GetCart.Execute(asUser("u1"))
	assert.Equal(t, int64(2), v.Items[0].Quantity, "a refused add changes nothing")
}

func TestAddToCartRejections(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, err := f.set.AddToCart.Execute(ctx, "ghost", 1)
	assert.Equal(t, domain.CodeProductNotAvailable, code(err))
	_, err = f.set.AddToCart.Execute(ctx, "draft", 1)
	assert.Equal(t, domain.CodeProductNotAvailable, code(err), "unpublished products cannot be bought")
	_, err = f.set.AddToCart.Execute(ctx, "p1", 0)
	assert.Equal(t, domain.CodeInvalidQuantity, code(err))
	_, err = f.set.AddToCart.Execute(ctx, "", 1)
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	v, _ := f.set.GetCart.Execute(ctx)
	assert.Empty(t, v.Items)
}

func TestDependencyOutagesPropagateAndLeaveTheCartAlone(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	f.catalog.Err = errs.Unavailable(errs.CodeUnavailable, "catalog circuit open")
	_, err := f.set.AddToCart.Execute(ctx, "p1", 1)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err))

	f.catalog.Err = nil
	f.stock.Err = errs.Unavailable(errs.CodeUnavailable, "inventory circuit open")
	_, err = f.set.AddToCart.Execute(ctx, "p1", 1)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err), "no silent 'assume in stock'")
	assert.Equal(t, 0, f.carts.Saves)
	v, _ := f.set.GetCart.Execute(ctx)
	assert.Empty(t, v.Items)
}

func TestCartsAreIsolatedPerBuyer(t *testing.T) {
	f := newFx()
	_, err := f.set.AddToCart.Execute(asUser("alice"), "p1", 1)
	require.NoError(t, err)
	v, err := f.set.GetCart.Execute(asUser("bob"))
	require.NoError(t, err)
	assert.Empty(t, v.Items, "another buyer's cart is not reachable: there is no user id in any request")
	assert.Equal(t, "bob", v.UserID)
	assert.Equal(t, "active", v.State)
}

func TestUpdateCartAppliesAllLinesOrNone(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, err := f.set.AddToCart.Execute(ctx, "p1", 2)
	require.NoError(t, err)

	v, err := f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{
		{ProductID: "p1", ShopID: "s1", Quantity: 5, OldQuantity: 2},
		{ProductID: "p2", ShopID: "s2", Quantity: 1},
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), v.Lines)
	assert.Equal(t, int64(5*150000+500000), v.Total)

	// the second line fails (not enough stock) so the first must not apply either
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", Quantity: 1}, {ProductID: "p2", Quantity: 99}})
	assert.Equal(t, domain.CodeInsufficientStock, code(err))
	v, _ = f.set.GetCart.Execute(ctx)
	assert.Equal(t, int64(5), v.Items[0].Quantity)

	v, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", Quantity: 0}})
	require.NoError(t, err)
	assert.Equal(t, int64(1), v.Lines, "quantity 0 removes the line")
	assert.Equal(t, "p2", v.Items[0].ProductID)
}

func TestUpdateCartRejections(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, err := f.set.AddToCart.Execute(ctx, "p1", 2)
	require.NoError(t, err)

	_, err = f.set.UpdateCart.Execute(ctx, nil)
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "", Quantity: 1}})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", Quantity: 1}, {ProductID: "p1", Quantity: 2}})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), "the same product twice is ambiguous")
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", ShopID: "wrong-shop", Quantity: 1}})
	assert.Equal(t, domain.CodeShopMismatch, code(err), "monolith: product.shopId must equal the stated shop")
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "ghost", Quantity: 1}})
	assert.Equal(t, domain.CodeProductNotAvailable, code(err))
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", Quantity: 3, OldQuantity: 7}})
	assert.Equal(t, domain.CodeCartChanged, code(err), "stale client view")
	_, err = f.set.UpdateCart.Execute(ctx, []usecase.UpdateLine{{ProductID: "p1", Quantity: 2000}})
	assert.Error(t, err)
	v, _ := f.set.GetCart.Execute(ctx)
	assert.Equal(t, int64(2), v.Items[0].Quantity, "nothing above changed the cart")
}

func TestRemoveItemIsIdempotent(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, _ = f.set.AddToCart.Execute(ctx, "p1", 1)
	v, err := f.set.RemoveItem.Execute(ctx, "p1")
	require.NoError(t, err)
	assert.Empty(t, v.Items)
	_, err = f.set.RemoveItem.Execute(ctx, "p1")
	require.NoError(t, err, "removing again is fine")
	_, err = f.set.RemoveItem.Execute(asUser("never-had-a-cart"), "p1")
	require.NoError(t, err)
	_, err = f.set.RemoveItem.Execute(ctx, "")
	assert.Error(t, err)
}

func TestConcurrentChangesFromTwoDevicesAreBothKept(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, err := f.set.AddToCart.Execute(ctx, "p1", 1)
	require.NoError(t, err)

	// between this request's load and save, the other device adds p2 (a version conflict for us)
	f.carts.BeforeSave = func(userID string) {
		_, e := f.set.AddToCart.Execute(asUser(userID), "p2", 1)
		require.NoError(t, e)
	}
	v, err := f.set.AddToCart.Execute(ctx, "p1", 1)
	require.NoError(t, err, "the conflict is retried on the fresh cart")
	assert.Equal(t, int64(2), v.Items[0].Quantity)
	assert.Equal(t, int64(2), v.Lines, "the other device's p2 survived")
}

func TestPersistentConflictsGiveUp(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, _ = f.set.AddToCart.Execute(ctx, "p1", 1)
	var mu sync.Mutex
	bumps := 0
	var arm func()
	arm = func() {
		f.carts.BeforeSave = func(userID string) {
			mu.Lock()
			bumps++
			mu.Unlock()
			c, _ := f.carts.FindByUser(context.Background(), userID)
			_ = c.Add(domain.Item{ProductID: "x", ShopID: "s", Quantity: 1}, time.Now())
			require.NoError(t, f.carts.Save(context.Background(), c))
			arm() // sabotage every attempt
		}
	}
	arm()
	_, err := f.set.AddToCart.Execute(ctx, "p1", 1)
	require.Error(t, err)
	assert.Equal(t, domain.CodeCartConflict, code(err))
	assert.Equal(t, errs.KindAborted, errs.KindOf(err))
	assert.Equal(t, 4, bumps, "bounded retries")
}

func TestClearOrderedTakesBoughtUnitsOut(t *testing.T) {
	f := newFx()
	ctx := asUser("u1")
	_, _ = f.set.AddToCart.Execute(ctx, "p1", 5)
	_, _ = f.set.AddToCart.Execute(ctx, "p2", 2)

	require.NoError(t, f.set.ClearOrdered.Execute(context.Background(), "u1", []usecase.OrderedLine{{ProductID: "p1", Quantity: 3}, {ProductID: "p2", Quantity: 2}, {ProductID: "gone", Quantity: 1}}))
	v, _ := f.set.GetCart.Execute(ctx)
	require.Len(t, v.Items, 1)
	assert.Equal(t, usecase.ItemView{ProductID: "p1", ShopID: "s1", Name: "Shirt", Price: 150000, Quantity: 2}, v.Items[0],
		"bought units leave the cart; what the buyer added to it meanwhile stays")

	require.NoError(t, f.set.ClearOrdered.Execute(context.Background(), "nobody", []usecase.OrderedLine{{ProductID: "p1", Quantity: 1}}), "no cart: nothing to do")
	assert.Error(t, f.set.ClearOrdered.Execute(context.Background(), "", nil))
	f.carts.BeforeSave = nil
}

func TestRepositoryErrorsPropagate(t *testing.T) {
	f := newFx()
	boom := errors.New("mongo down")
	set := usecase.NewSet(&usecase.Deps{Carts: failingRepo{boom}, Catalog: f.catalog, Stock: f.stock, Clock: f.clk, IDs: idgen.ULID{}})
	_, err := set.GetCart.Execute(asUser("u1"))
	assert.ErrorIs(t, err, boom)
	_, err = set.AddToCart.Execute(asUser("u1"), "p1", 1)
	assert.ErrorIs(t, err, boom)
	assert.ErrorIs(t, set.ClearOrdered.Execute(context.Background(), "u1", nil), boom)
}

type failingRepo struct{ err error }

func (r failingRepo) FindByUser(context.Context, string) (*domain.Cart, error) { return nil, r.err }
func (r failingRepo) Save(context.Context, *domain.Cart) error                 { return r.err }
