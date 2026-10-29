package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/discount-service/internal/domain"
	"github.com/an-vt/ecom/discount-service/internal/fake"
	"github.com/an-vt/ecom/discount-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/saga"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type rig struct {
	uc      *usecase.Set
	w       *fake.World
	catalog *fake.Catalog
	clk     *clock.Fixed
}

func newRig() *rig {
	w := fake.NewWorld()
	clk := clock.NewFixed(now)
	cat := &fake.Catalog{
		Shops: map[string][]string{"shop1": {"p1", "p2", "p3"}, "shop2": {"q1"}},
		Products_: map[string]domain.ProductSummary{
			"p1": {ID: "p1", ShopID: "shop1", Name: "Shirt", Price: 100000, IsPublished: true},
			"p2": {ID: "p2", ShopID: "shop1", Name: "Hat", Price: 50000, IsPublished: true},
			"p3": {ID: "p3", ShopID: "shop1", Name: "Draft", Price: 1, IsPublished: false},
		}}
	uc := usecase.NewSet(&usecase.Deps{Discounts: w.Discounts(), Usages: w.Usages(), Catalog: cat, Tx: w, Outbox: w, Clock: clk, IDs: idgen.UUID{}})
	return &rig{uc: uc, w: w, catalog: cat, clk: clk}
}

func shop(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeShop})
}

func buyer(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeUser})
}

func create(mut func(*usecase.CreateInput)) usecase.CreateInput {
	in := usecase.CreateInput{Code: "sale10", Name: "Ten off", Type: "percent", Value: 10, StartDate: now.Add(-time.Hour), EndDate: now.Add(24 * time.Hour),
		MaxUses: 3, MaxUsesPerUser: 1, MinOrderValue: 10000}
	if mut != nil {
		mut(&in)
	}
	return in
}

func (r *rig) seed(t *testing.T, shopID string, mut func(*usecase.CreateInput)) usecase.DiscountView {
	t.Helper()
	v, err := r.uc.CreateDiscount.Execute(shop(shopID), create(mut))
	require.NoError(t, err)
	return v
}

// ---- management ---------------------------------------------------------------------------------------------------

func TestCreateStoresAndAnnounces(t *testing.T) {
	r := newRig()
	v, err := r.uc.CreateDiscount.Execute(shop("shop1"), create(nil))
	require.NoError(t, err)
	assert.Equal(t, "SALE10", v.Code)
	assert.Equal(t, "shop1", v.ShopID, "the shop is the caller, never a request field")

	require.Equal(t, []string{"discount.discount.created"}, r.w.Types())
	var ev domain.DiscountCreated
	require.NoError(t, r.w.Decode(0, &ev))
	assert.Equal(t, domain.DiscountCreated{DiscountID: v.ID, ShopID: "shop1", Code: "SALE10", Name: "Ten off", Type: "percent", Value: 10}, ev)
}

func TestCreateRefusals(t *testing.T) {
	r := newRig()
	_, err := r.uc.CreateDiscount.Execute(context.Background(), create(nil))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = r.uc.CreateDiscount.Execute(buyer("u1"), create(nil))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "buyers cannot create codes")
	_, err = r.uc.CreateDiscount.Execute(shop("shop1"), create(func(in *usecase.CreateInput) { in.Value = 0 }))
	assert.Equal(t, domain.CodeInvalidDiscount, errs.CodeOf(err))

	r.seed(t, "shop1", nil)
	_, err = r.uc.CreateDiscount.Execute(shop("shop1"), create(nil))
	assert.Equal(t, domain.CodeCodeTaken, errs.CodeOf(err))
	assert.Len(t, r.w.Types(), 1, "a refused create announces nothing")
	_, err = r.uc.CreateDiscount.Execute(shop("shop2"), create(nil))
	assert.NoError(t, err, "another shop may use the same code")
}

func TestCreateOnlyAcceptsOwnProducts(t *testing.T) {
	r := newRig()
	specific := func(ids ...string) func(*usecase.CreateInput) {
		return func(in *usecase.CreateInput) { in.AppliesTo, in.ProductIDs = "specific", ids }
	}
	v := r.seed(t, "shop1", specific("p1", "p2"))
	assert.Equal(t, []string{"p1", "p2"}, v.ProductIDs)
	_, err := r.uc.CreateDiscount.Execute(shop("shop1"), create(func(in *usecase.CreateInput) { in.Code = "OTHER"; specific("p1", "q1")(in) }))
	assert.Equal(t, domain.CodeUnknownProducts, errs.CodeOf(err), "q1 is shop2's product")

	r.catalog.Err = errs.Unavailable("X", "catalog down")
	_, err = r.uc.CreateDiscount.Execute(shop("shop1"), create(func(in *usecase.CreateInput) { in.Code = "THIRD"; specific("p1")(in) }))
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err), "no catalog, no unverified product list")
	r.catalog.Err = nil
	r.catalog.Calls = 0
	r.seed(t, "shop1", func(in *usecase.CreateInput) { in.Code = "ALLSHOP" })
	assert.Zero(t, r.catalog.Calls, "a code for everything needs no catalog call")
}

func TestCreateIsAtomicWithItsEvent(t *testing.T) {
	r := newRig()
	r.w.FailOutbox = errors.New("outbox down")
	_, err := r.uc.CreateDiscount.Execute(shop("shop1"), create(nil))
	require.Error(t, err)
	r.w.FailOutbox = nil
	_, err = r.uc.CreateDiscount.Execute(shop("shop1"), create(nil))
	assert.NoError(t, err, "the failed attempt left no code behind")
}

func TestUpdate(t *testing.T) {
	r := newRig()
	v := r.seed(t, "shop1", nil)
	name, off := "Renamed", false
	got, err := r.uc.UpdateDiscount.Execute(shop("shop1"), usecase.UpdateInput{DiscountID: v.ID, Name: &name, IsActive: &off})
	require.NoError(t, err)
	assert.Equal(t, "Renamed", got.Name)
	assert.False(t, got.IsActive)

	_, err = r.uc.UpdateDiscount.Execute(shop("shop2"), usecase.UpdateInput{DiscountID: v.ID, Name: &name})
	assert.Equal(t, domain.CodeNotOwner, errs.CodeOf(err))
	_, err = r.uc.UpdateDiscount.Execute(shop("shop1"), usecase.UpdateInput{DiscountID: "ghost", Name: &name})
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err))
	_, err = r.uc.UpdateDiscount.Execute(buyer("u"), usecase.UpdateInput{DiscountID: v.ID, Name: &name})
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	empty := ""
	_, err = r.uc.UpdateDiscount.Execute(shop("shop1"), usecase.UpdateInput{DiscountID: v.ID, Name: &empty})
	assert.Equal(t, domain.CodeInvalidDiscount, errs.CodeOf(err))

	specific := "specific"
	got, err = r.uc.UpdateDiscount.Execute(shop("shop1"), usecase.UpdateInput{DiscountID: v.ID, AppliesTo: &specific, ProductIDs: []string{"p2"}, ReplaceProductIDs: true})
	require.NoError(t, err)
	assert.Equal(t, []string{"p2"}, got.ProductIDs)
	_, err = r.uc.UpdateDiscount.Execute(shop("shop1"), usecase.UpdateInput{DiscountID: v.ID, ProductIDs: []string{"q1"}, ReplaceProductIDs: true})
	assert.Equal(t, domain.CodeUnknownProducts, errs.CodeOf(err), "the replacement list is checked too")
}

func TestListShopDiscounts(t *testing.T) {
	r := newRig()
	for _, c := range []string{"AAA", "BBB", "CCC"} {
		r.seed(t, "shop1", func(in *usecase.CreateInput) { in.Code = c })
		r.clk.Advance(time.Second)
	}
	r.seed(t, "shop2", nil)

	res, err := r.uc.ListShopDiscounts.Execute(shop("shop1"), 1, 2)
	require.NoError(t, err)
	assert.EqualValues(t, 3, res.Total, "only the caller's codes")
	require.Len(t, res.Items, 2)
	assert.Equal(t, "CCC", res.Items[0].Code, "newest first")
	_, err = r.uc.ListShopDiscounts.Execute(buyer("u"), 1, 2)
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

// ---- products of a code -------------------------------------------------------------------------------------------

func TestListProductsForCode(t *testing.T) {
	r := newRig()
	r.seed(t, "shop1", func(in *usecase.CreateInput) { in.Code = "EVERYTHING" })
	r.seed(t, "shop1", func(in *usecase.CreateInput) {
		in.Code = "SOME"
		in.AppliesTo, in.ProductIDs = "specific", []string{"p2", "p1"}
	})
	ctx := context.Background() // public: no caller needed

	all, err := r.uc.ListProductsForCode.Execute(ctx, "shop1", "everything", 1, 10)
	require.NoError(t, err)
	assert.EqualValues(t, 3, all.Total, "the shop's whole catalogue")
	require.Len(t, all.Items, 2, "unpublished products are not shown")
	assert.Equal(t, "p1", all.Items[0].ID)

	some, err := r.uc.ListProductsForCode.Execute(ctx, "shop1", "SOME", 1, 10)
	require.NoError(t, err)
	require.Len(t, some.Items, 2)
	assert.Equal(t, []string{"p2", "p1"}, []string{some.Items[0].ID, some.Items[1].ID}, "in the order the shop listed them")

	page2, err := r.uc.ListProductsForCode.Execute(ctx, "shop1", "SOME", 2, 1)
	require.NoError(t, err)
	require.Len(t, page2.Items, 1)
	assert.Equal(t, "p1", page2.Items[0].ID)
	past, err := r.uc.ListProductsForCode.Execute(ctx, "shop1", "SOME", 9, 10)
	require.NoError(t, err)
	assert.Empty(t, past.Items)

	_, err = r.uc.ListProductsForCode.Execute(ctx, "shop1", "NOPE", 1, 10)
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err))
	_, err = r.uc.ListProductsForCode.Execute(ctx, "shop2", "SOME", 1, 10)
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err), "a code is looked up inside its shop")

	r.catalog.Err = errs.Unavailable("X", "down")
	_, err = r.uc.ListProductsForCode.Execute(ctx, "shop1", "SOME", 1, 10)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err))
	_, err = r.uc.ListProductsForCode.Execute(ctx, "shop1", "EVERYTHING", 1, 10)
	assert.Equal(t, errs.KindUnavailable, errs.KindOf(err))
}

// ---- preview ------------------------------------------------------------------------------------------------------

var order = []domain.Line{{ProductID: "p1", Price: 100000, Quantity: 2}}

func TestCalculatePreview(t *testing.T) {
	r := newRig()
	r.seed(t, "shop1", nil)
	got, err := r.uc.CalculateDiscount.Execute(buyer("alice"), "shop1", " sale10 ", order)
	require.NoError(t, err)
	assert.Equal(t, usecase.Preview{Discount: 20000, Subtotal: 200000, Total: 180000}, got)
	assert.Equal(t, []string{"discount.discount.created"}, r.w.Types(), "a preview records nothing")
}

func TestCalculateRefusals(t *testing.T) {
	r := newRig()
	v := r.seed(t, "shop1", func(in *usecase.CreateInput) { in.MinOrderValue = 500000 })
	_, err := r.uc.CalculateDiscount.Execute(context.Background(), "shop1", "SALE10", order)
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = r.uc.CalculateDiscount.Execute(shop("shop1"), "shop1", "SALE10", order)
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "shops do not redeem codes")
	_, err = r.uc.CalculateDiscount.Execute(buyer("a"), "shop1", "GHOST", order)
	assert.Equal(t, domain.CodeDiscountNotFound, errs.CodeOf(err))
	_, err = r.uc.CalculateDiscount.Execute(buyer("a"), "shop1", "SALE10", order)
	assert.Equal(t, domain.CodeMinOrderNotReached, errs.CodeOf(err))

	r.clk.Advance(48 * time.Hour)
	_, err = r.uc.CalculateDiscount.Execute(buyer("a"), "shop1", "SALE10", order)
	assert.Equal(t, domain.CodeExpired, errs.CodeOf(err))
	_ = v
}

func TestCalculateKnowsTheCallersOwnUses(t *testing.T) {
	r := newRig()
	r.seed(t, "shop1", nil) // one use per user
	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "alice", "SALE10")))
	_, err := r.uc.CalculateDiscount.Execute(buyer("alice"), "shop1", "SALE10", order)
	assert.Equal(t, domain.CodeUserLimitReached, errs.CodeOf(err))
	_, err = r.uc.CalculateDiscount.Execute(buyer("bob"), "shop1", "SALE10", order)
	assert.NoError(t, err, "the limit is per user")
}

// ---- saga ---------------------------------------------------------------------------------------------------------

func applyCmd(orderID, user string, codes ...string) saga.ApplyDiscounts {
	cmd := saga.ApplyDiscounts{OrderID: orderID, UserID: user, Items: []saga.Item{
		{ProductID: "p1", ShopID: "shop1", Quantity: 2, Price: 100000},
		{ProductID: "q1", ShopID: "shop2", Quantity: 1, Price: 40000}}}
	for _, c := range codes {
		shopID := "shop1"
		if c == "OTHERSHOP" {
			shopID = "shop2"
		}
		cmd.Discounts = append(cmd.Discounts, saga.DiscountRequest{ShopID: shopID, Code: c})
	}
	return cmd
}

func (r *rig) lastReply(t *testing.T, v any) string {
	t.Helper()
	types := r.w.Types()
	require.NotEmpty(t, types)
	require.NoError(t, r.w.Decode(len(types)-1, v))
	return types[len(types)-1]
}

func TestApplyRedeemsEveryCodeAndRepliesTheTotal(t *testing.T) {
	r := newRig()
	a := r.seed(t, "shop1", nil)
	r.seed(t, "shop2", func(in *usecase.CreateInput) {
		in.Code = "OTHERSHOP"
		in.Type = "fixed_amount"
		in.Value = 15000
		in.MinOrderValue = 0
	})

	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "alice", "SALE10", "OTHERSHOP")))
	var got saga.DiscountsApplied
	assert.Equal(t, saga.ReplyDiscountsApplied, r.lastReply(t, &got))
	assert.EqualValues(t, 20000+15000, got.TotalDiscount, "10% of shop1's 200000 plus shop2's fixed 15000")
	assert.Equal(t, "o1", got.OrderID)

	d, err := r.w.Discounts().Find(context.Background(), a.ID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, d.Snapshot().UsedCount)
}

func TestApplyIsIdempotent(t *testing.T) {
	r := newRig()
	a := r.seed(t, "shop1", nil)
	for i := 0; i < 3; i++ {
		require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "alice", "SALE10")))
	}
	var got saga.DiscountsApplied
	r.lastReply(t, &got)
	assert.EqualValues(t, 20000, got.TotalDiscount, "a repeat gets the original answer")
	d, _ := r.w.Discounts().Find(context.Background(), a.ID)
	assert.EqualValues(t, 1, d.Snapshot().UsedCount, "and counts once")
}

func TestApplyRejectsWithAReplyAndChangesNothing(t *testing.T) {
	cases := []struct {
		name   string
		cmd    func() saga.ApplyDiscounts
		setup  func(*rig)
		reason string
		code   string
	}{
		{"unknown code", func() saga.ApplyDiscounts { return applyCmd("o", "alice", "GHOST") }, nil, domain.CodeDiscountNotFound, "GHOST"},
		{"expired", func() saga.ApplyDiscounts { return applyCmd("o", "alice", "SALE10") }, func(r *rig) { r.clk.Advance(72 * time.Hour) }, domain.CodeExpired, "SALE10"},
		{"below the minimum", func() saga.ApplyDiscounts {
			c := applyCmd("o", "alice", "SALE10")
			c.Items = []saga.Item{{ProductID: "p1", ShopID: "shop1", Quantity: 1, Price: 500}}
			return c
		}, nil, domain.CodeMinOrderNotReached, "SALE10"},
		{"two codes of one shop", func() saga.ApplyDiscounts { return applyCmd("o", "alice", "SALE10", "sale10") }, nil, domain.CodeDuplicateShop, "sale10"},
		{"no codes", func() saga.ApplyDiscounts { return applyCmd("o", "alice") }, nil, saga.ReasonDiscountRejected, ""},
		{"no user", func() saga.ApplyDiscounts { return applyCmd("o", "", "SALE10") }, nil, saga.ReasonDiscountRejected, ""},
		{"nothing of that shop in the order", func() saga.ApplyDiscounts {
			c := applyCmd("o", "alice", "SALE10")
			c.Items = []saga.Item{{ProductID: "q1", ShopID: "shop2", Quantity: 1, Price: 40000}}
			return c
		}, nil, domain.CodeNotApplicable, "SALE10"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig()
			a := r.seed(t, "shop1", nil)
			if tc.setup != nil {
				tc.setup(r)
			}
			require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), tc.cmd()), "a business refusal is a reply, not a handler error")
			var rej saga.DiscountsRejected
			assert.Equal(t, saga.ReplyDiscountsRejected, r.lastReply(t, &rej))
			assert.Equal(t, tc.reason, rej.Reason)
			assert.Equal(t, tc.code, rej.Code)
			d, _ := r.w.Discounts().Find(context.Background(), a.ID)
			assert.EqualValues(t, 0, d.Snapshot().UsedCount)
			us, _ := r.w.Usages().ForOrder(context.Background(), "o")
			assert.Empty(t, us)
		})
	}
}

func TestApplyIsAllOrNothingAcrossShops(t *testing.T) {
	r := newRig()
	a := r.seed(t, "shop1", nil)
	r.seed(t, "shop2", func(in *usecase.CreateInput) { in.Code = "OTHERSHOP"; in.MaxUses = 1 })
	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "bob", "OTHERSHOP"))) // takes shop2's only use

	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o2", "alice", "SALE10", "OTHERSHOP")))
	var rej saga.DiscountsRejected
	assert.Equal(t, saga.ReplyDiscountsRejected, r.lastReply(t, &rej))
	assert.Equal(t, domain.CodeUsedUp, rej.Reason)
	d, _ := r.w.Discounts().Find(context.Background(), a.ID)
	assert.EqualValues(t, 0, d.Snapshot().UsedCount, "alice's valid shop1 code was not consumed either")
}

func TestApplyOnlyCountsEachUsersOwnUses(t *testing.T) {
	r := newRig()
	r.seed(t, "shop1", nil) // 3 uses, 1 per user
	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "alice", "SALE10")))
	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o2", "alice", "SALE10")))
	var rej saga.DiscountsRejected
	assert.Equal(t, saga.ReplyDiscountsRejected, r.lastReply(t, &rej))
	assert.Equal(t, domain.CodeUserLimitReached, rej.Reason)
	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o3", "bob", "SALE10")))
	var ok saga.DiscountsApplied
	assert.Equal(t, saga.ReplyDiscountsApplied, r.lastReply(t, &ok))
}

func TestApplyFailsAsAWholeWhenTheOutboxFails(t *testing.T) {
	r := newRig()
	a := r.seed(t, "shop1", nil)
	r.w.FailOutbox = errors.New("outbox down")
	err := r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "alice", "SALE10"))
	require.Error(t, err, "the message is retried")
	r.w.FailOutbox = nil
	d, _ := r.w.Discounts().Find(context.Background(), a.ID)
	assert.EqualValues(t, 0, d.Snapshot().UsedCount, "the redemption rolled back with the reply")
	require.NoError(t, r.uc.ApplyDiscounts.Execute(context.Background(), applyCmd("o1", "alice", "SALE10")))
	d, _ = r.w.Discounts().Find(context.Background(), a.ID)
	assert.EqualValues(t, 1, d.Snapshot().UsedCount)
}

func TestReleaseGivesTheUseBackOnce(t *testing.T) {
	r := newRig()
	a := r.seed(t, "shop1", nil)
	ctx := context.Background()
	require.NoError(t, r.uc.ApplyDiscounts.Execute(ctx, applyCmd("o1", "alice", "SALE10")))

	for i := 0; i < 3; i++ {
		require.NoError(t, r.uc.ReleaseDiscounts.Execute(ctx, saga.ReleaseDiscounts{OrderID: "o1"}))
		var rel saga.DiscountsReleased
		assert.Equal(t, saga.ReplyDiscountsReleased, r.lastReply(t, &rel))
		assert.Equal(t, "o1", rel.OrderID)
	}
	d, _ := r.w.Discounts().Find(ctx, a.ID)
	assert.EqualValues(t, 0, d.Snapshot().UsedCount, "released once, not three times")

	// alice can use the code again, her per-user count dropped too
	require.NoError(t, r.uc.ApplyDiscounts.Execute(ctx, applyCmd("o2", "alice", "SALE10")))
	var ok saga.DiscountsApplied
	assert.Equal(t, saga.ReplyDiscountsApplied, r.lastReply(t, &ok))

	// releasing an order that never redeemed anything still answers
	require.NoError(t, r.uc.ReleaseDiscounts.Execute(ctx, saga.ReleaseDiscounts{OrderID: "never-applied"}))
	var rel saga.DiscountsReleased
	assert.Equal(t, saga.ReplyDiscountsReleased, r.lastReply(t, &rel))
}

func TestALateApplyAfterItsReleaseIsRefused(t *testing.T) {
	r := newRig()
	a := r.seed(t, "shop1", nil)
	ctx := context.Background()
	require.NoError(t, r.uc.ApplyDiscounts.Execute(ctx, applyCmd("o1", "alice", "SALE10")))
	require.NoError(t, r.uc.ReleaseDiscounts.Execute(ctx, saga.ReleaseDiscounts{OrderID: "o1"}))

	require.NoError(t, r.uc.ApplyDiscounts.Execute(ctx, applyCmd("o1", "alice", "SALE10")))
	var rej saga.DiscountsRejected
	assert.Equal(t, saga.ReplyDiscountsRejected, r.lastReply(t, &rej))
	assert.Equal(t, saga.ReasonStepTimeout, rej.Reason)
	d, _ := r.w.Discounts().Find(ctx, a.ID)
	assert.EqualValues(t, 0, d.Snapshot().UsedCount)
}
