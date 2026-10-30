package usecase_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/catalog-service/internal/fake"
	"github.com/an-vt/ecom/catalog-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/principal"
)

type fx struct {
	w     *fake.World
	cache *fake.Cache
	clk   *clock.Fixed
	set   *usecase.Set
}

func newFx() *fx {
	w := fake.NewWorld()
	f := &fx{w: w, cache: fake.NewCache(), clk: clock.NewFixed(time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC))}
	f.set = usecase.NewSet(&usecase.Deps{
		Products: w.Products(), Spus: w.Spus(), Comments: w.Comments(), Shops: w.Shops(),
		Types: domain.DefaultTypes(), Tx: w, Outbox: w, SkuCache: f.cache, Clock: f.clk, IDs: &idgen.Sequence{Prefix: "id-"},
	})
	return f
}

func asShop(id string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeShop, Roles: []string{"shop"}})
}
func asUser(id string, roles ...string) context.Context {
	return principal.With(context.Background(), principal.Principal{ID: id, Type: principal.TypeUser, Roles: roles})
}

var anon = context.Background()

func code(err error) string { return errs.CodeOf(err) }

func shirt(name string) usecase.CreateProductInput {
	return usecase.CreateProductInput{Type: "Clothing", Name: name, Description: "cotton " + name, Price: 150000, Quantity: 25, Thumb: "t.png",
		Attributes: map[string]any{"brand": "Acme", "size": "M", "material": "cotton"}}
}

func (f *fx) create(t *testing.T, shop, name string) usecase.ProductView {
	t.Helper()
	v, err := f.set.CreateProduct.Execute(asShop(shop), shirt(name))
	require.NoError(t, err)
	return v
}

// ---- products ---------------------------------------------------------------------------------------------------

func TestCreateProductStoresAnEventAtomically(t *testing.T) {
	f := newFx()
	require.NoError(t, f.w.Shops().Upsert(anon, domain.ShopProjection{ID: "shop1", Name: "Acme Store"}))
	v := f.create(t, "shop1", "Blue shirt")

	assert.Equal(t, "shop1", v.ShopID)
	assert.Equal(t, "Acme Store", v.ShopName, "the shop name comes from the local projection, not a cross-service join")
	assert.True(t, v.IsDraft)
	assert.Equal(t, "blue-shirt", v.Slug)
	assert.Equal(t, int64(150000), v.Price)

	assert.Equal(t, []string{"catalog.product.created"}, f.w.OutboxTypes())
	msg := f.w.Outbox[0]
	assert.Equal(t, "catalog.product.created", msg.Topic)
	assert.Equal(t, v.ID, msg.Key)
	env, err := event.Parse(msg.Payload)
	require.NoError(t, err)
	var ev domain.ProductCreated
	require.NoError(t, env.Decode(&ev))
	assert.Equal(t, domain.ProductCreated{ProductID: v.ID, ShopID: "shop1", Name: "Blue shirt", Price: 150000, Quantity: 25, Type: "Clothing"}, ev)
}

func TestCreateProductRejections(t *testing.T) {
	f := newFx()
	_, err := f.set.CreateProduct.Execute(anon, shirt("x"))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = f.set.CreateProduct.Execute(asUser("u1", "user"), shirt("x"))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "buyers cannot create products")

	bad := shirt("y")
	bad.Attributes = map[string]any{"brand": "x"}
	_, err = f.set.CreateProduct.Execute(asShop("s"), bad)
	assert.Equal(t, domain.CodeInvalidAttribute, code(err))
	bad = shirt("z")
	bad.Type = "Toy"
	_, err = f.set.CreateProduct.Execute(asShop("s"), bad)
	assert.Equal(t, domain.CodeInvalidType, code(err))

	f.create(t, "s", "Dup")
	_, err = f.set.CreateProduct.Execute(asShop("other"), shirt("Dup"))
	assert.Equal(t, domain.CodeProductNameTaken, code(err))
	assert.Equal(t, 1, f.w.ProductCount())
	assert.Len(t, f.w.Outbox, 1, "rejected creations publish nothing")
}

func TestCreateProductIsAtomic(t *testing.T) {
	f := newFx()
	f.w.FailOutbox = errors.New("outbox unavailable")
	_, err := f.set.CreateProduct.Execute(asShop("s"), shirt("A"))
	require.Error(t, err)
	assert.Equal(t, 0, f.w.ProductCount(), "no product without its event")
}

func TestUpdateProductPublishesQuantityOnlyWhenItChanged(t *testing.T) {
	f := newFx()
	v := f.create(t, "shop1", "Shirt")
	price, qty, name := int64(99000), int64(7), "Shirt v2"

	got, err := f.set.UpdateProduct.Execute(asShop("shop1"), usecase.UpdateProductInput{ProductID: v.ID, Price: &price, Name: &name,
		Attributes: map[string]any{"size": "L"}})
	require.NoError(t, err)
	assert.Equal(t, int64(99000), got.Price)
	assert.Equal(t, "L", got.Attributes["size"])
	assert.Equal(t, "Acme", got.Attributes["brand"])

	_, err = f.set.UpdateProduct.Execute(asShop("shop1"), usecase.UpdateProductInput{ProductID: v.ID, Quantity: &qty})
	require.NoError(t, err)

	require.Equal(t, []string{"catalog.product.created", "catalog.product.updated", "catalog.product.updated"}, f.w.OutboxTypes())
	decode := func(i int) domain.ProductUpdated {
		env, _ := event.Parse(f.w.Outbox[i].Payload)
		var ev domain.ProductUpdated
		require.NoError(t, env.Decode(&ev))
		return ev
	}
	assert.Nil(t, decode(1).Quantity, "a price change does not touch stock")
	require.NotNil(t, decode(2).Quantity)
	assert.Equal(t, int64(7), *decode(2).Quantity, "...a quantity change does")
}

func TestUpdateProductChecksOwnershipAndIsAtomic(t *testing.T) {
	f := newFx()
	v := f.create(t, "shop1", "Shirt")
	price := int64(1)

	_, err := f.set.UpdateProduct.Execute(asShop("shop2"), usecase.UpdateProductInput{ProductID: v.ID, Price: &price})
	assert.Equal(t, domain.CodeNotOwner, code(err))
	_, err = f.set.UpdateProduct.Execute(asShop("shop1"), usecase.UpdateProductInput{ProductID: "ghost", Price: &price})
	assert.Equal(t, domain.CodeProductNotFound, code(err))
	empty := ""
	_, err = f.set.UpdateProduct.Execute(asShop("shop1"), usecase.UpdateProductInput{ProductID: v.ID, Name: &empty})
	assert.Error(t, err)

	f.w.FailOutbox = errors.New("down")
	_, err = f.set.UpdateProduct.Execute(asShop("shop1"), usecase.UpdateProductInput{ProductID: v.ID, Price: &price})
	require.Error(t, err)
	f.w.FailOutbox = nil
	again, err := f.set.GetProductsByIds.Execute(anon, []string{v.ID})
	require.NoError(t, err)
	assert.Equal(t, int64(150000), again[0].Price, "a failed event rolls the price change back")
	_, err = f.set.UpdateProduct.Execute(anon, usecase.UpdateProductInput{ProductID: v.ID})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
}

func TestPublishingControlsPublicVisibility(t *testing.T) {
	f := newFx()
	v := f.create(t, "shop1", "Shirt")

	_, err := f.set.GetProduct.Execute(anon, v.ID)
	assert.Equal(t, domain.CodeProductNotFound, code(err), "drafts are invisible to the public")
	pub, err := f.set.ListProducts.Execute(anon, 1, 10, "")
	require.NoError(t, err)
	assert.Empty(t, pub.Items)

	got, err := f.set.SetPublished.Execute(asShop("shop1"), v.ID, true)
	require.NoError(t, err)
	assert.True(t, got.IsPublished)
	assert.False(t, got.IsDraft)
	got, err = f.set.SetPublished.Execute(asShop("shop1"), v.ID, true)
	require.NoError(t, err, "publishing twice is harmless")
	assert.True(t, got.IsPublished)

	seen, err := f.set.GetProduct.Execute(anon, v.ID)
	require.NoError(t, err)
	assert.Equal(t, v.ID, seen.ID)
	pub, _ = f.set.ListProducts.Execute(anon, 1, 10, "")
	assert.Len(t, pub.Items, 1)

	_, err = f.set.SetPublished.Execute(asShop("shop2"), v.ID, false)
	assert.Equal(t, domain.CodeNotOwner, code(err))
	got, err = f.set.SetPublished.Execute(asShop("shop1"), v.ID, false)
	require.NoError(t, err)
	assert.False(t, got.IsPublished)
	_, err = f.set.SetPublished.Execute(asShop("shop1"), "ghost", true)
	assert.Equal(t, domain.CodeProductNotFound, code(err))
	_, err = f.set.SetPublished.Execute(anon, v.ID, true)
	assert.Error(t, err)
}

func TestListingSearchAndShopViews(t *testing.T) {
	f := newFx()
	require.NoError(t, f.w.Shops().Upsert(anon, domain.ShopProjection{ID: "s1", Name: "Shop One"}))
	a := f.create(t, "s1", "Cotton shirt")
	f.clk.Advance(time.Minute)
	b := f.create(t, "s1", "Linen shirt")
	f.clk.Advance(time.Minute)
	c := f.create(t, "s2", "Wool coat")
	for _, v := range []usecase.ProductView{a, b, c} {
		_, err := f.set.SetPublished.Execute(asShop(v.ShopID), v.ID, true)
		require.NoError(t, err)
	}
	f.create(t, "s1", "Draft only")

	list, err := f.set.ListProducts.Execute(anon, 1, 2, "")
	require.NoError(t, err)
	assert.Equal(t, int64(3), list.Total)
	assert.Equal(t, 2, list.Limit)
	require.Len(t, list.Items, 2)
	assert.Equal(t, c.ID, list.Items[0].ID, "newest first")
	assert.Equal(t, "", list.Items[0].ShopName, "unknown shops have no name yet")
	assert.Equal(t, "Shop One", list.Items[1].ShopName)

	found, err := f.set.SearchProducts.Execute(anon, "  shirt ", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), found.Total)
	_, err = f.set.SearchProducts.Execute(anon, "   ", 1, 10)
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))

	mine, err := f.set.ListShopProducts.Execute(asShop("s1"), true, 0, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(2), mine.Total)
	assert.Equal(t, 50, mine.Limit, "default page size")
	drafts, err := f.set.ListShopProducts.Execute(asShop("s1"), false, 1, 10)
	require.NoError(t, err)
	require.Len(t, drafts.Items, 1)
	assert.Equal(t, "Draft only", drafts.Items[0].Name)
	_, err = f.set.ListShopProducts.Execute(asUser("u", "user"), true, 1, 10)
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
}

func TestInternalLookups(t *testing.T) {
	f := newFx()
	a := f.create(t, "s1", "A")
	b := f.create(t, "s1", "B")
	_, _ = f.set.SetPublished.Execute(asShop("s1"), a.ID, true)

	got, err := f.set.GetProductsByIds.Execute(anon, []string{b.ID, "ghost", a.ID})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{b.ID, a.ID}, []string{got[0].ID, got[1].ID}, "results follow the request order; unknown ids are skipped")
	assert.True(t, got[1].IsPublished, "callers see the published flag and decide for themselves")
	none, err := f.set.GetProductsByIds.Execute(anon, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
	tooMany := make([]string, 201)
	_, err = f.set.GetProductsByIds.Execute(anon, tooMany)
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))

	ids, err := f.set.ListProductIdsByShop.Execute(anon, "s1")
	require.NoError(t, err)
	assert.Equal(t, []string{a.ID}, ids, "published products only")
	_, err = f.set.ListProductIdsByShop.Execute(anon, "")
	assert.Error(t, err)
}

// ---- spu / sku ---------------------------------------------------------------------------------------------------

func spuInput() domain.SpuInput {
	return domain.SpuInput{Name: "T-Shirt", Price: 100, Categories: []string{"clothes"}, Quantity: 10,
		Variations: []domain.Variation{{Name: "color", Options: []string{"red", "blue"}}},
		Skus:       []domain.SkuInput{{TierIdx: []int{0}, Price: 100, Stock: 5}, {TierIdx: []int{1}, Price: 110, Stock: 2}}}
}

func TestSpuAndSkuFlow(t *testing.T) {
	f := newFx()
	spu, skus, err := f.set.CreateSpu.Execute(asShop("shop1"), spuInput())
	require.NoError(t, err)
	require.Len(t, skus, 2)

	gotSpu, gotSkus, err := f.set.GetSpu.Execute(anon, spu.ID)
	require.NoError(t, err)
	assert.Equal(t, spu.ID, gotSpu.ID)
	assert.Len(t, gotSkus, 2)
	_, _, err = f.set.GetSpu.Execute(anon, "ghost")
	assert.Equal(t, domain.CodeSpuNotFound, code(err))

	_, _, err = f.set.CreateSpu.Execute(asShop("shop2"), spuInput())
	assert.Equal(t, domain.CodeSpuNameTaken, code(err))
	_, _, err = f.set.CreateSpu.Execute(asUser("u", "user"), spuInput())
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err))
	bad := spuInput()
	bad.Name = "Other"
	bad.Skus[0].TierIdx = []int{9}
	_, _, err = f.set.CreateSpu.Execute(asShop("shop1"), bad)
	assert.Equal(t, domain.CodeInvalidSku, code(err))
}

func TestSkuLookupIsCachedIncludingMisses(t *testing.T) {
	f := newFx()
	spu, skus, err := f.set.CreateSpu.Execute(asShop("shop1"), spuInput())
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		s, err := f.set.GetSku.Execute(anon, skus[0].ID, spu.ID)
		require.NoError(t, err)
		assert.Equal(t, skus[0].ID, s.ID)
	}
	assert.Equal(t, 1, f.cache.Loads, "three lookups, one database read")

	for i := 0; i < 3; i++ {
		_, err := f.set.GetSku.Execute(anon, "no-such-sku", spu.ID)
		assert.Equal(t, domain.CodeSkuNotFound, code(err))
	}
	assert.Equal(t, 2, f.cache.Loads, "an unknown id is read once, then served as a cached miss")
}

func TestSkuOfAnotherSpuIsNotFoundEvenWhenCached(t *testing.T) {
	f := newFx()
	spu, skus, _ := f.set.CreateSpu.Execute(asShop("shop1"), spuInput())
	_, err := f.set.GetSku.Execute(anon, skus[0].ID, spu.ID)
	require.NoError(t, err)
	_, err = f.set.GetSku.Execute(anon, skus[0].ID, "another-spu")
	assert.Equal(t, domain.CodeSkuNotFound, code(err), "the cache is keyed by sku, the spu id is still enforced")
	_, err = f.set.GetSku.Execute(anon, "", spu.ID)
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = f.set.GetSku.Execute(anon, skus[0].ID, "")
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
}

// ---- comments ----------------------------------------------------------------------------------------------------

func TestCommentThreads(t *testing.T) {
	f := newFx()
	p := f.create(t, "shop1", "Shirt")
	user := asUser("u1", "user")

	root, err := f.set.CreateComment.Execute(user, usecase.CreateCommentInput{ProductID: p.ID, Content: "great"})
	require.NoError(t, err)
	assert.Equal(t, int64(1), root.Left)
	assert.Equal(t, "u1", root.UserID, "the author comes from the token, never from the request")
	reply, err := f.set.CreateComment.Execute(asUser("u2", "user"), usecase.CreateCommentInput{ProductID: p.ID, Content: "agreed", ParentCommentID: root.ID})
	require.NoError(t, err)
	assert.Equal(t, root.ID, reply.ParentID)

	top, err := f.set.ListComments.Execute(anon, p.ID, "", 10, 0)
	require.NoError(t, err)
	require.Len(t, top, 1)
	kids, err := f.set.ListComments.Execute(anon, p.ID, root.ID, 10, -5)
	require.NoError(t, err)
	require.Len(t, kids, 1, "a negative skip is treated as 0")
	_, err = f.set.ListComments.Execute(anon, "", "", 10, 0)
	assert.Error(t, err)
}

func TestCommentRejections(t *testing.T) {
	f := newFx()
	p := f.create(t, "shop1", "Shirt")
	other := f.create(t, "shop1", "Other")
	user := asUser("u1", "user")

	_, err := f.set.CreateComment.Execute(anon, usecase.CreateCommentInput{ProductID: p.ID, Content: "x"})
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = f.set.CreateComment.Execute(user, usecase.CreateCommentInput{ProductID: "ghost", Content: "x"})
	assert.Equal(t, domain.CodeProductNotFound, code(err))
	_, err = f.set.CreateComment.Execute(user, usecase.CreateCommentInput{ProductID: p.ID, Content: "  "})
	assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	_, err = f.set.CreateComment.Execute(user, usecase.CreateCommentInput{ProductID: p.ID, Content: "x", ParentCommentID: "ghost"})
	assert.Equal(t, domain.CodeCommentNotFound, code(err))

	root, _ := f.set.CreateComment.Execute(user, usecase.CreateCommentInput{ProductID: p.ID, Content: "on p"})
	_, err = f.set.CreateComment.Execute(user, usecase.CreateCommentInput{ProductID: other.ID, Content: "x", ParentCommentID: root.ID})
	assert.Equal(t, domain.CodeCommentNotFound, code(err), "a reply cannot attach to a comment of another product")
}

func TestDeleteCommentPermissions(t *testing.T) {
	f := newFx()
	p := f.create(t, "shop1", "Shirt")
	other := f.create(t, "shop1", "Other")
	author := asUser("u1", "user")
	root, _ := f.set.CreateComment.Execute(author, usecase.CreateCommentInput{ProductID: p.ID, Content: "mine"})
	_, _ = f.set.CreateComment.Execute(asUser("u2", "user"), usecase.CreateCommentInput{ProductID: p.ID, Content: "reply", ParentCommentID: root.ID})

	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(f.set.DeleteComment.Execute(asUser("u3", "user"), root.ID, p.ID)), "strangers cannot delete")
	assert.Equal(t, domain.CodeCommentNotFound, code(f.set.DeleteComment.Execute(author, root.ID, other.ID)), "the product id must match")
	assert.Equal(t, domain.CodeCommentNotFound, code(f.set.DeleteComment.Execute(author, "ghost", p.ID)))
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(f.set.DeleteComment.Execute(anon, root.ID, p.ID)))

	require.NoError(t, f.set.DeleteComment.Execute(author, root.ID, p.ID))
	left, _ := f.set.ListComments.Execute(anon, p.ID, "", 10, 0)
	assert.Empty(t, left)
	kids, _ := f.set.ListComments.Execute(anon, p.ID, root.ID, 10, 0)
	assert.Empty(t, kids, "the reply went with its thread")

	again, _ := f.set.CreateComment.Execute(author, usecase.CreateCommentInput{ProductID: p.ID, Content: "again"})
	require.NoError(t, f.set.DeleteComment.Execute(asUser("boss", "user", "admin"), again.ID, p.ID), "administrators can moderate")
}

// ---- shop projection ---------------------------------------------------------------------------------------------

func TestUpsertShop(t *testing.T) {
	f := newFx()
	require.NoError(t, f.set.UpsertShop.Execute(anon, domain.ShopProjection{ID: "s1", Name: "Old"}))
	require.NoError(t, f.set.UpsertShop.Execute(anon, domain.ShopProjection{ID: "s1", Name: "New"}))
	got, _ := f.w.Shops().FindByIDs(anon, []string{"s1"})
	assert.Equal(t, "New", got["s1"].Name)
	assert.Error(t, f.set.UpsertShop.Execute(anon, domain.ShopProjection{Name: "no id"}))
}

// ---- image uploads ------------------------------------------------------------------------------------------------

func withMedia(f *fx, m *fake.Media) {
	f.set = usecase.NewSet(&usecase.Deps{
		Products: f.w.Products(), Spus: f.w.Spus(), Comments: f.w.Comments(), Shops: f.w.Shops(),
		Types: domain.DefaultTypes(), Tx: f.w, Outbox: f.w, SkuCache: f.cache, Clock: f.clk, IDs: &idgen.Sequence{Prefix: "id-"},
		Media: m, MediaPolicy: domain.MediaPolicy{MaxBytes: 1 << 20, TTL: 10 * time.Minute},
	})
}

func TestAShopGetsAnUploadURLScopedToItself(t *testing.T) {
	f, m := newFx(), &fake.Media{}
	withMedia(f, m)
	up, err := f.set.CreateUploadURL.Execute(asShop("shop-7"), "image/webp", 4096)
	require.NoError(t, err)
	assert.Equal(t, "products/shop-7/id-1.webp", up.Key, "scoped by shop, named by a generated id, extension from the declared type")
	require.Len(t, m.Calls, 1)
	assert.Equal(t, fake.MediaCall{Key: "products/shop-7/id-1.webp", ContentType: "image/webp", Size: 4096, TTL: 10 * time.Minute}, m.Calls[0])

	again, err := f.set.CreateUploadURL.Execute(asShop("shop-7"), "image/webp", 4096)
	require.NoError(t, err)
	assert.NotEqual(t, up.Key, again.Key, "every upload gets its own key: nobody overwrites an existing image")
}

func TestOnlyAllowedImageTypesAndSizesAreSigned(t *testing.T) {
	f, m := newFx(), &fake.Media{}
	withMedia(f, m)
	for ct, ext := range map[string]string{"image/jpeg": "jpg", "image/png": "png", "image/webp": "webp", "image/gif": "gif"} {
		up, err := f.set.CreateUploadURL.Execute(asShop("s"), ct, 100)
		require.NoError(t, err, ct)
		assert.Regexp(t, `\.`+ext+`$`, up.Key)
	}
	for name, tc := range map[string]struct {
		ct   string
		size int64
		code string
	}{
		"html":        {"text/html", 100, domain.CodeUnsupportedMedia},
		"svg":         {"image/svg+xml", 100, domain.CodeUnsupportedMedia},
		"executable":  {"application/octet-stream", 100, domain.CodeUnsupportedMedia},
		"empty type":  {"", 100, domain.CodeUnsupportedMedia},
		"type tricks": {"image/png; charset=x", 100, domain.CodeUnsupportedMedia},
		"too large":   {"image/png", 1<<20 + 1, domain.CodeFileTooLarge},
		"zero":        {"image/png", 0, errs.CodeValidationFailed},
		"negative":    {"image/png", -5, errs.CodeValidationFailed},
	} {
		before := len(m.Calls)
		_, err := f.set.CreateUploadURL.Execute(asShop("s"), tc.ct, tc.size)
		require.Error(t, err, name)
		assert.Equal(t, tc.code, errs.CodeOf(err), name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err), name)
		assert.Len(t, m.Calls, before, "%s: nothing is signed for a refused upload", name)
	}
	_, err := f.set.CreateUploadURL.Execute(asShop("s"), "image/png", 1<<20)
	assert.NoError(t, err, "exactly the limit is fine")
}

func TestUploadURLsAreForShopsOnly(t *testing.T) {
	f, m := newFx(), &fake.Media{}
	withMedia(f, m)
	_, err := f.set.CreateUploadURL.Execute(anon, "image/png", 100)
	assert.Equal(t, errs.KindUnauthenticated, errs.KindOf(err))
	_, err = f.set.CreateUploadURL.Execute(asUser("u1", "user"), "image/png", 100)
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(err), "the monolith's upload routes needed no login at all")
	assert.Empty(t, m.Calls)
}

func TestUploadsAreOffWithoutAnObjectStore(t *testing.T) {
	f := newFx() // no Media
	_, err := f.set.CreateUploadURL.Execute(asShop("s"), "image/png", 100)
	assert.Equal(t, domain.CodeMediaDisabled, errs.CodeOf(err))
	assert.Equal(t, errs.KindFailedPrecondition, errs.KindOf(err))
}

func TestStoreFailuresPassThrough(t *testing.T) {
	f, m := newFx(), &fake.Media{Err: errors.New("signer exploded")}
	withMedia(f, m)
	_, err := f.set.CreateUploadURL.Execute(asShop("s"), "image/png", 100)
	require.Error(t, err)
	assert.Equal(t, "signer exploded", err.Error())
}
