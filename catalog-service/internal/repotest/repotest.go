// Package repotest is the repository contract suite: the same behavioural tests run against the in-memory fakes
// (unit tests) and against MongoDB (integration tests), so a fake can never promise something the real repository
// does not do.
package repotest

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
)

// Repos are the repositories under test.
type Repos struct {
	Products domain.ProductRepository
	Spus     domain.SpuRepository
	Comments domain.CommentRepository
	Shops    domain.ShopRepository
}

// New returns a fresh, empty set of repositories for one test.
type New func(t *testing.T) Repos

var ctx = context.Background()
var base = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) // whole milliseconds: MongoDB stores dates at that precision
var reg = domain.DefaultTypes()

// Run executes the whole suite.
func Run(t *testing.T, newRepos New) {
	t.Run("Products", func(t *testing.T) { products(t, newRepos(t)) })
	t.Run("ProductQueries", func(t *testing.T) { productQueries(t, newRepos(t)) })
	t.Run("Spus", func(t *testing.T) { spus(t, newRepos(t)) })
	t.Run("Comments", func(t *testing.T) { comments(t, newRepos(t)) })
	t.Run("Shops", func(t *testing.T) { shops(t, newRepos(t)) })
}

func product(t *testing.T, id, shop, name string, price int64, at time.Time, published bool) *domain.Product {
	t.Helper()
	p, err := domain.NewProduct(reg, id, shop, domain.ProductInput{Type: domain.TypeClothing, Name: name, Description: name + " made of cotton",
		Price: price, Quantity: 10, Thumb: "t.png", Attributes: map[string]any{"brand": "Acme", "size": "M", "material": "cotton",
			"extra": map[string]any{"fit": "slim"}}}, at)
	require.NoError(t, err)
	if published {
		p.Publish(at)
	}
	return p
}

func products(t *testing.T, r Repos) {
	p := product(t, "p1", "shop1", "Blue shirt", 100, base, false)
	require.NoError(t, r.Products.Create(ctx, p))

	got, err := r.Products.FindByID(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, p.Snapshot(), got.Snapshot(), "round trip keeps every field, nested attributes included")

	dup := product(t, "p2", "shop2", "Blue shirt", 5, base, false)
	err = r.Products.Create(ctx, dup)
	assert.Equal(t, domain.CodeProductNameTaken, errs.CodeOf(err), "names are unique")

	_, err = r.Products.FindByID(ctx, "nope")
	assert.Equal(t, domain.CodeProductNotFound, errs.CodeOf(err))
	assert.Equal(t, errs.KindNotFound, errs.KindOf(err))

	p.Publish(base.Add(time.Minute))
	newName := "Blue shirt v2"
	_, err = p.Update(reg, domain.ProductPatch{Name: &newName, Attributes: map[string]any{"size": "L"}}, base.Add(time.Minute))
	require.NoError(t, err)
	require.NoError(t, r.Products.Update(ctx, p))
	again, err := r.Products.FindByID(ctx, "p1")
	require.NoError(t, err)
	assert.Equal(t, p.Snapshot(), again.Snapshot())
	assert.True(t, again.IsPublished())

	ghost := product(t, "ghost", "shop1", "Ghost", 1, base, false)
	assert.Equal(t, domain.CodeProductNotFound, errs.CodeOf(r.Products.Update(ctx, ghost)))

	other := product(t, "p3", "shop1", "Red hat", 7, base, false)
	require.NoError(t, r.Products.Create(ctx, other))
	clash := "Blue shirt v2"
	_, err = other.Update(reg, domain.ProductPatch{Name: &clash}, base)
	require.NoError(t, err)
	assert.Equal(t, domain.CodeProductNameTaken, errs.CodeOf(r.Products.Update(ctx, other)), "renaming onto an existing name is refused")
}

func productQueries(t *testing.T, r Repos) {
	// 5 published (ascending age, mixed prices), 1 draft, one of another shop
	specs := []struct {
		id, shop, name string
		price          int64
		age            time.Duration
		published      bool
	}{
		{"a", "s1", "Cotton shirt", 300, 0, true},
		{"b", "s1", "Linen shirt", 100, time.Minute, true},
		{"c", "s1", "Wool coat", 500, 2 * time.Minute, true},
		{"d", "s2", "Silk scarf", 200, 3 * time.Minute, true},
		{"e", "s1", "Denim jacket", 400, 4 * time.Minute, true},
		{"f", "s1", "Secret draft shirt", 50, 5 * time.Minute, false},
	}
	for _, s := range specs {
		require.NoError(t, r.Products.Create(ctx, product(t, s.id, s.shop, s.name, s.price, base.Add(s.age), s.published)))
	}
	ids := func(ps []*domain.Product) []string {
		out := make([]string, len(ps))
		for i, p := range ps {
			out[i] = p.ID()
		}
		return out
	}

	list, total, err := r.Products.ListPublished(ctx, domain.ListQuery{Page: 1, Limit: 3})
	require.NoError(t, err)
	assert.Equal(t, int64(5), total, "drafts are not listed")
	assert.Equal(t, []string{"e", "d", "c"}, ids(list), "newest first by default")
	list, _, _ = r.Products.ListPublished(ctx, domain.ListQuery{Page: 2, Limit: 3})
	assert.Equal(t, []string{"b", "a"}, ids(list), "second page")
	list, _, _ = r.Products.ListPublished(ctx, domain.ListQuery{Page: 9, Limit: 3})
	assert.Empty(t, list)
	list, _, _ = r.Products.ListPublished(ctx, domain.ListQuery{Page: 1, Limit: 10, Sort: "price_asc"})
	assert.Equal(t, []string{"b", "d", "a", "e", "c"}, ids(list))
	list, _, _ = r.Products.ListPublished(ctx, domain.ListQuery{Page: 1, Limit: 10, Sort: "price_desc"})
	assert.Equal(t, []string{"c", "e", "a", "d", "b"}, ids(list))

	found, total, err := r.Products.Search(ctx, "shirt", 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(2), total, "published products only: the draft shirt stays hidden")
	assert.ElementsMatch(t, []string{"a", "b"}, ids(found))
	none, total, _ := r.Products.Search(ctx, "spaceship", 1, 10)
	assert.Empty(t, none)
	assert.Equal(t, int64(0), total)

	mine, total, err := r.Products.ListByShop(ctx, "s1", true, 1, 10)
	require.NoError(t, err)
	assert.Equal(t, int64(4), total)
	assert.Equal(t, []string{"e", "c", "b", "a"}, ids(mine))
	drafts, total, _ := r.Products.ListByShop(ctx, "s1", false, 1, 10)
	assert.Equal(t, []string{"f"}, ids(drafts))
	assert.Equal(t, int64(1), total)

	shopIDs, err := r.Products.IDsByShop(ctx, "s1")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "b", "c", "e"}, shopIDs, "published products only")

	several, err := r.Products.FindByIDs(ctx, []string{"d", "zzz", "a"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a", "d"}, ids(several), "unknown ids are skipped")
}

func spus(t *testing.T, r Repos) {
	spu, skus, err := domain.NewSpu("spu1", "shop1", domain.SpuInput{
		Name: "T-Shirt", Thumb: "t.png", Description: "d", Price: 100, Categories: []string{"clothes", "men"}, Quantity: 10,
		Attributes: map[string]any{"brand": "Acme"},
		Variations: []domain.Variation{{Name: "color", Options: []string{"red", "blue"}, Images: []string{"r.png"}}, {Name: "size", Options: []string{"S", "M"}}},
		Skus:       []domain.SkuInput{{TierIdx: []int{1, 0}, Price: 120, Stock: 3, Sort: 2}, {TierIdx: []int{0, 0}, Price: 100, Stock: 5, Sort: 1, IsDefault: true}},
	}, base)
	require.NoError(t, err)
	require.NoError(t, r.Spus.Create(ctx, spu, skus))

	got, err := r.Spus.FindByID(ctx, "spu1")
	require.NoError(t, err)
	assert.Equal(t, *spu, *got, "round trip, variations and attributes included")

	list, err := r.Spus.SkusOf(ctx, "spu1")
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "spu1.2", list[0].ID, "ordered by sort")
	assert.Equal(t, *skus[1], *list[0])

	sku, err := r.Spus.FindSku(ctx, "spu1.1", "spu1")
	require.NoError(t, err)
	assert.Equal(t, int64(120), int64(sku.Price))
	_, err = r.Spus.FindSku(ctx, "spu1.1", "other-spu")
	assert.Equal(t, domain.CodeSkuNotFound, errs.CodeOf(err), "a sku is looked up within its spu")
	_, err = r.Spus.FindSku(ctx, "spu1.9", "spu1")
	assert.Equal(t, domain.CodeSkuNotFound, errs.CodeOf(err))
	_, err = r.Spus.FindByID(ctx, "missing")
	assert.Equal(t, domain.CodeSpuNotFound, errs.CodeOf(err))

	dup, dupSkus, _ := domain.NewSpu("spu2", "shop2", domain.SpuInput{Name: "T-Shirt", Price: 1, Categories: []string{"c"}}, base)
	assert.Equal(t, domain.CodeSpuNameTaken, errs.CodeOf(r.Spus.Create(ctx, dup, dupSkus)))
	empty, err := r.Spus.SkusOf(ctx, "spu2")
	require.NoError(t, err)
	assert.Empty(t, empty, "a rejected spu leaves nothing behind")
}

func comment(t *testing.T, id, product, parent string) *domain.Comment {
	t.Helper()
	c, err := domain.NewComment(id, product, "user-"+id, "text of "+id, parent, base)
	require.NoError(t, err)
	return c
}

func positions(t *testing.T, r Repos, ids ...string) string {
	t.Helper()
	out := ""
	for _, id := range ids {
		c, err := r.Comments.FindByID(ctx, id)
		if err != nil {
			out += fmt.Sprintf("%s=gone ", id)
			continue
		}
		out += fmt.Sprintf("%s=(%d,%d) ", id, c.Left, c.Right)
	}
	return out
}

func comments(t *testing.T, r Repos) {
	a := comment(t, "A", "p1", "")
	require.NoError(t, r.Comments.AddRoot(ctx, a))
	assert.Equal(t, "A=(1,2) ", positions(t, r, "A"))

	a1 := comment(t, "A1", "p1", "A")
	require.NoError(t, r.Comments.AddReply(ctx, a, a1))
	a2 := comment(t, "A2", "p1", "A")
	parent, _ := r.Comments.FindByID(ctx, "A")
	require.NoError(t, r.Comments.AddReply(ctx, parent, a2))
	a1a := comment(t, "A1a", "p1", "A1")
	parent, _ = r.Comments.FindByID(ctx, "A1")
	require.NoError(t, r.Comments.AddReply(ctx, parent, a1a))
	b := comment(t, "B", "p1", "")
	require.NoError(t, r.Comments.AddRoot(ctx, b))
	assert.Equal(t, "A=(1,8) A1=(2,5) A1a=(3,4) A2=(6,7) B=(9,10) ", positions(t, r, "A", "A1", "A1a", "A2", "B"),
		"replies open a gap inside the parent and push everything to the right")

	other := comment(t, "X", "p2", "")
	require.NoError(t, r.Comments.AddRoot(ctx, other))
	assert.Equal(t, "X=(1,2) ", positions(t, r, "X"), "every product has its own tree")

	top, err := r.Comments.List(ctx, "p1", "", 10, 0)
	require.NoError(t, err)
	require.Len(t, top, 2)
	assert.Equal(t, []string{"A", "B"}, []string{top[0].ID, top[1].ID})
	kids, err := r.Comments.List(ctx, "p1", "A", 10, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"A1", "A2"}, []string{kids[0].ID, kids[1].ID})
	paged, _ := r.Comments.List(ctx, "p1", "A", 1, 1)
	require.Len(t, paged, 1)
	assert.Equal(t, "A2", paged[0].ID, "skip and limit page through one level")

	cur, _ := r.Comments.FindByID(ctx, "A1")
	require.NoError(t, r.Comments.DeleteSubtree(ctx, cur))
	assert.Equal(t, "A=(1,4) A1=gone A1a=gone A2=(2,3) B=(5,6) ", positions(t, r, "A", "A1", "A1a", "A2", "B"),
		"deleting a thread removes its replies and closes the gap")
	assert.Equal(t, "X=(1,2) ", positions(t, r, "X"), "other products are untouched")

	_, err = r.Comments.FindByID(ctx, "zzz")
	assert.Equal(t, domain.CodeCommentNotFound, errs.CodeOf(err))
}

func shops(t *testing.T, r Repos) {
	require.NoError(t, r.Shops.Upsert(ctx, domain.ShopProjection{ID: "s1", Name: "Acme", Email: "a@acme.com"}))
	require.NoError(t, r.Shops.Upsert(ctx, domain.ShopProjection{ID: "s2", Name: "Beta", Email: "b@beta.com"}))
	require.NoError(t, r.Shops.Upsert(ctx, domain.ShopProjection{ID: "s1", Name: "Acme Renamed", Email: "a@acme.com"}))

	got, err := r.Shops.FindByIDs(ctx, []string{"s1", "s3"})
	require.NoError(t, err)
	assert.Len(t, got, 1, "unknown shops are simply absent")
	assert.Equal(t, "Acme Renamed", got["s1"].Name, "upsert updates")
	none, err := r.Shops.FindByIDs(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, none)
}
