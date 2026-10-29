package domain_test

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/event"
)

var now = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
var reg = domain.DefaultTypes()

func clothing() domain.ProductInput {
	return domain.ProductInput{Type: domain.TypeClothing, Name: "  Áo thun Đẹp  ", Description: "cotton", Price: 150000, Quantity: 40, Thumb: "t.png",
		Attributes: map[string]any{"brand": "Acme", "size": "M", "material": "cotton"}}
}

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Áo thun Đẹp": "ao-thun-dep", "  Hello,   World!! ": "hello-world", "iPhone 15 Pro": "iphone-15-pro",
		"---": "", "": "", "Ghế Sofa (3 chỗ)": "ghe-sofa-3-cho", "ÀÁẢÃẠ": "aaaaa",
	} {
		assert.Equal(t, want, domain.Slugify(in), in)
	}
}

// Regression: a shared x/text transformer made concurrent product creation panic or garble slugs.
func TestSlugifyIsSafeForConcurrentUse(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				if got := domain.Slugify("Áo thun Đẹp"); got != "ao-thun-dep" {
					t.Errorf("garbled slug under concurrency: %q", got)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestTypeRegistryValidatesPerType(t *testing.T) {
	tests := []struct {
		name  string
		typ   domain.Type
		attrs map[string]any
		code  string
	}{
		{"electronic ok", domain.TypeElectronic, map[string]any{"manufacturer": "Sony", "model": "A7", "color": "black"}, ""},
		{"clothing ok", domain.TypeClothing, map[string]any{"brand": "b", "size": "M", "material": "m"}, ""},
		{"furniture ok", domain.TypeFurniture, map[string]any{"brand": "b", "size": "L", "material": "oak"}, ""},
		{"unknown type", "Toy", map[string]any{}, domain.CodeInvalidType},
		{"missing size", domain.TypeClothing, map[string]any{"brand": "b", "material": "m"}, domain.CodeInvalidAttribute},
		{"blank value", domain.TypeClothing, map[string]any{"brand": "b", "size": "  ", "material": "m"}, domain.CodeInvalidAttribute},
		{"nil value", domain.TypeElectronic, map[string]any{"manufacturer": nil, "model": "x", "color": "y"}, domain.CodeInvalidAttribute},
		{"nil attrs", domain.TypeFurniture, nil, domain.CodeInvalidAttribute},
	}
	for _, tt := range tests {
		err := reg.Validate(tt.typ, tt.attrs)
		if tt.code == "" {
			assert.NoError(t, err, tt.name)
			continue
		}
		require.Error(t, err, tt.name)
		assert.Equal(t, tt.code, errs.CodeOf(err), tt.name)
		assert.Equal(t, errs.KindInvalidArgument, errs.KindOf(err))
	}
	assert.Equal(t, []domain.Type{"Clothing", "Electronic", "Furniture"}, reg.Types())
	assert.Contains(t, reg.Validate(domain.TypeClothing, nil).Error(), "brand, size, material")
}

func TestNewProductStartsAsADraftWithDefaults(t *testing.T) {
	p, err := domain.NewProduct(reg, "p1", "shop1", clothing(), now)
	require.NoError(t, err)
	assert.Equal(t, "Áo thun Đẹp", p.Snapshot().Name)
	assert.Equal(t, "ao-thun-dep", p.Snapshot().Slug)
	assert.Equal(t, int64(150000), int64(p.Price()))
	assert.True(t, p.Snapshot().IsDraft)
	assert.False(t, p.IsPublished())
	assert.Equal(t, domain.DefaultRating, p.Snapshot().RatingsAverage)
	assert.True(t, p.OwnedBy("shop1"))
	assert.False(t, p.OwnedBy("shop2"))
	assert.False(t, p.OwnedBy(""))
	assert.Equal(t, domain.TypeClothing, p.Type())
	assert.Equal(t, int64(40), p.Quantity())
	assert.Equal(t, "p1", p.ID())
	assert.Equal(t, "shop1", p.ShopID())
	assert.Equal(t, "Áo thun Đẹp", p.Name())
}

func TestNewProductRejections(t *testing.T) {
	bad := map[string]func(*domain.ProductInput){
		"no name":        func(in *domain.ProductInput) { in.Name = "  " },
		"long name":      func(in *domain.ProductInput) { in.Name = strings.Repeat("n", 256) },
		"negative price": func(in *domain.ProductInput) { in.Price = -1 },
		"negative stock": func(in *domain.ProductInput) { in.Quantity = -1 },
		"bad type":       func(in *domain.ProductInput) { in.Type = "Toy" },
		"no attributes":  func(in *domain.ProductInput) { in.Attributes = nil },
	}
	for name, mut := range bad {
		in := clothing()
		mut(&in)
		_, err := domain.NewProduct(reg, "p", "shop", in, now)
		assert.Error(t, err, name)
	}
	_, err := domain.NewProduct(reg, "p", "", clothing(), now)
	assert.Error(t, err, "a product needs a shop")
}

func TestNewProductCopiesAttributes(t *testing.T) {
	in := clothing()
	p, _ := domain.NewProduct(reg, "p", "s", in, now)
	in.Attributes["size"] = "XXL"
	assert.Equal(t, "M", p.Snapshot().Attributes["size"], "later changes to the input do not leak in")
	snap := p.Snapshot()
	snap.Attributes["size"] = "S"
	assert.Equal(t, "M", p.Snapshot().Attributes["size"], "snapshots are copies")
}

func ptr[T any](v T) *T { return &v }

func TestUpdateMergesAttributesAndReportsStockChange(t *testing.T) {
	in := clothing()
	in.Attributes["extra"] = map[string]any{"fit": "slim", "care": map[string]any{"wash": "cold", "iron": "low"}}
	p, _ := domain.NewProduct(reg, "p", "s", in, now)

	changed, err := p.Update(reg, domain.ProductPatch{
		Name: ptr("New Name"), Price: ptr(int64(99000)),
		Attributes: map[string]any{"size": "L", "extra": map[string]any{"care": map[string]any{"wash": "warm"}}},
	}, now.Add(time.Hour))
	require.NoError(t, err)
	assert.False(t, changed, "quantity was not part of the patch")
	s := p.Snapshot()
	assert.Equal(t, "New Name", s.Name)
	assert.Equal(t, "new-name", s.Slug, "the slug follows the name")
	assert.Equal(t, int64(99000), int64(s.Price))
	assert.Equal(t, "L", s.Attributes["size"])
	assert.Equal(t, "Acme", s.Attributes["brand"], "untouched attributes stay")
	care := s.Attributes["extra"].(map[string]any)["care"].(map[string]any)
	assert.Equal(t, "warm", care["wash"])
	assert.Equal(t, "low", care["iron"], "nested maps are merged, not replaced")
	assert.Equal(t, "slim", s.Attributes["extra"].(map[string]any)["fit"])
	assert.Equal(t, now.Add(time.Hour), s.UpdatedAt)

	changed, err = p.Update(reg, domain.ProductPatch{Quantity: ptr(int64(0))}, now)
	require.NoError(t, err)
	assert.True(t, changed, "setting the quantity to zero is still a stock change")
}

func TestUpdateIsAllOrNothing(t *testing.T) {
	p, _ := domain.NewProduct(reg, "p", "s", clothing(), now)
	before := p.Snapshot()
	for name, patch := range map[string]domain.ProductPatch{
		"blank name":     {Name: ptr("  ")},
		"negative price": {Price: ptr(int64(-5))},
		"negative stock": {Quantity: ptr(int64(-1))},
		"drops required": {Attributes: map[string]any{"size": ""}},
	} {
		_, err := p.Update(reg, patch, now.Add(time.Minute))
		require.Error(t, err, name)
		assert.Equal(t, before, p.Snapshot(), "%s: a rejected update changes nothing", name)
	}
}

func TestPublishCycle(t *testing.T) {
	p, _ := domain.NewProduct(reg, "p", "s", clothing(), now)
	p.Publish(now.Add(time.Minute))
	assert.True(t, p.IsPublished())
	assert.False(t, p.Snapshot().IsDraft)
	p.Unpublish(now.Add(2 * time.Minute))
	assert.False(t, p.IsPublished())
	assert.True(t, p.Snapshot().IsDraft)
	assert.Equal(t, p.Snapshot(), domain.RestoreProduct(p.Snapshot()).Snapshot())
}

// ---- SPU / SKU ---------------------------------------------------------------------------------------------------

func spuInput() domain.SpuInput {
	return domain.SpuInput{
		Name: "T-Shirt", Price: 100, Categories: []string{"clothes"}, Quantity: 10,
		Variations: []domain.Variation{{Name: "color", Options: []string{"red", "blue"}}, {Name: "size", Options: []string{"S", "M", "L"}}},
		Skus: []domain.SkuInput{
			{TierIdx: []int{0, 0}, Price: 100, Stock: 5}, {TierIdx: []int{0, 2}, Price: 110, Stock: 3, Sort: 1},
			{TierIdx: []int{1, 1}, Price: 120, Stock: 2, IsDefault: true},
		},
	}
}

func TestNewSpuBuildsSkus(t *testing.T) {
	spu, skus, err := domain.NewSpu("spu1", "shop1", spuInput(), now)
	require.NoError(t, err)
	assert.Equal(t, "t-shirt", spu.Slug)
	assert.True(t, spu.IsDraft)
	require.Len(t, skus, 3)
	assert.Equal(t, "spu1.1", skus[0].ID)
	assert.Equal(t, "spu1.3", skus[2].ID)
	assert.False(t, skus[0].IsDefault)
	assert.True(t, skus[2].IsDefault, "the flagged SKU is the default")
	assert.Equal(t, "spu1", skus[1].SpuID)
}

func TestFirstSkuIsTheDefaultWhenNoneIsFlagged(t *testing.T) {
	in := spuInput()
	in.Skus[2].IsDefault = false
	_, skus, err := domain.NewSpu("s", "shop", in, now)
	require.NoError(t, err)
	assert.True(t, skus[0].IsDefault)
	assert.False(t, skus[1].IsDefault)
}

func TestNewSpuWithoutVariationsOrSkus(t *testing.T) {
	spu, skus, err := domain.NewSpu("s", "shop", domain.SpuInput{Name: "Plain", Price: 1, Categories: []string{"c"}}, now)
	require.NoError(t, err)
	assert.Empty(t, skus)
	assert.NotNil(t, spu)
}

func TestNewSpuRejections(t *testing.T) {
	bad := map[string]func(*domain.SpuInput){
		"no name":          func(in *domain.SpuInput) { in.Name = "" },
		"no category":      func(in *domain.SpuInput) { in.Categories = nil },
		"negative price":   func(in *domain.SpuInput) { in.Price = -1 },
		"empty variation":  func(in *domain.SpuInput) { in.Variations[0].Options = nil },
		"unnamed tier":     func(in *domain.SpuInput) { in.Variations[1].Name = " " },
		"two defaults":     func(in *domain.SpuInput) { in.Skus[0].IsDefault, in.Skus[2].IsDefault = true, true },
		"wrong tier count": func(in *domain.SpuInput) { in.Skus[0].TierIdx = []int{0} },
		"option too big":   func(in *domain.SpuInput) { in.Skus[0].TierIdx = []int{2, 0} },
		"negative option":  func(in *domain.SpuInput) { in.Skus[0].TierIdx = []int{0, -1} },
		"duplicate combo":  func(in *domain.SpuInput) { in.Skus[1].TierIdx = []int{0, 0} },
		"negative stock":   func(in *domain.SpuInput) { in.Skus[0].Stock = -1 },
	}
	for name, mut := range bad {
		in := spuInput()
		mut(&in)
		_, _, err := domain.NewSpu("s", "shop", in, now)
		assert.Error(t, err, name)
	}
	_, _, err := domain.NewSpu("s", "", spuInput(), now)
	assert.Error(t, err)
	_, _, err = domain.NewSpu("s", "shop", func() domain.SpuInput { in := spuInput(); in.Skus[0].TierIdx = []int{9, 0}; return in }(), now)
	assert.Equal(t, domain.CodeInvalidSku, errs.CodeOf(err))
}

// ---- comments (nested set) ------------------------------------------------------------------------------------------

func TestNewComment(t *testing.T) {
	c, err := domain.NewComment("c1", "p1", "u1", "  nice product ", "", now)
	require.NoError(t, err)
	assert.Equal(t, "nice product", c.Content)
	for name, args := range map[string][]string{"empty": {"c", "p", "u", " "}, "no product": {"c", "", "u", "x"}, "no user": {"c", "p", "", "x"}} {
		_, err := domain.NewComment(args[0], args[1], args[2], args[3], "", now)
		assert.Error(t, err, name)
	}
	_, err = domain.NewComment("c", "p", "u", strings.Repeat("x", 2001), "", now)
	assert.Error(t, err)
}

func TestPlacementArithmetic(t *testing.T) {
	root := domain.PlanRoot(0)
	assert.Equal(t, domain.Placement{Left: 1, Right: 2}, root)
	assert.Equal(t, domain.Placement{Left: 3, Right: 4}, domain.PlanRoot(2))

	r := domain.PlanReply(2) // parent occupies (1,2): the reply takes (2,3), the parent's right moves to 4
	assert.Equal(t, int64(2), r.Left)
	assert.Equal(t, int64(3), r.Right)
	assert.True(t, r.Shift)
	assert.Equal(t, int64(2), r.ShiftRightsFrom)

	rm := domain.PlanDelete(domain.Comment{Left: 3, Right: 8})
	assert.Equal(t, domain.Removal{Left: 3, Right: 8, Width: 6}, rm)
}

// tree is an in-memory model of one product's comment tree that applies the same plans the repository applies.
type tree struct{ nodes map[string]*domain.Comment }

func (tr *tree) maxRight() int64 {
	var m int64
	for _, n := range tr.nodes {
		if n.Right > m {
			m = n.Right
		}
	}
	return m
}

func (tr *tree) addRoot(id string) {
	p := domain.PlanRoot(tr.maxRight())
	tr.nodes[id] = &domain.Comment{ID: id, Left: p.Left, Right: p.Right}
}

func (tr *tree) addReply(parentID, id string) {
	parent := tr.nodes[parentID]
	p := domain.PlanReply(parent.Right)
	for _, n := range tr.nodes {
		if n.Right >= p.ShiftRightsFrom {
			n.Right += 2
		}
		if n.Left > p.ShiftLeftsAfter {
			n.Left += 2
		}
	}
	tr.nodes[id] = &domain.Comment{ID: id, ParentID: parentID, Left: p.Left, Right: p.Right}
}

func (tr *tree) remove(id string) {
	rm := domain.PlanDelete(*tr.nodes[id])
	for k, n := range tr.nodes {
		if n.Left >= rm.Left && n.Right <= rm.Right {
			delete(tr.nodes, k)
		}
	}
	for _, n := range tr.nodes {
		if n.Left > rm.Right {
			n.Left -= rm.Width
		}
		if n.Right > rm.Right {
			n.Right -= rm.Width
		}
	}
}

// consistent: values are 1..2n, each used exactly once, and every parent's interval contains its children's.
func (tr *tree) consistent() error {
	var vals []int64
	for _, n := range tr.nodes {
		if n.Left >= n.Right {
			return fmt.Errorf("%s: left %d >= right %d", n.ID, n.Left, n.Right)
		}
		vals = append(vals, n.Left, n.Right)
		if n.ParentID != "" {
			p, ok := tr.nodes[n.ParentID]
			if !ok {
				return fmt.Errorf("%s: parent %s missing", n.ID, n.ParentID)
			}
			if p.Left >= n.Left || n.Right >= p.Right {
				return fmt.Errorf("%s (%d,%d) is not inside parent %s (%d,%d)", n.ID, n.Left, n.Right, p.ID, p.Left, p.Right)
			}
		}
	}
	sort.Slice(vals, func(i, j int) bool { return vals[i] < vals[j] })
	for i, v := range vals {
		if v != int64(i+1) {
			return fmt.Errorf("values are not 1..%d without gaps: %v", len(vals), vals)
		}
	}
	return nil
}

func TestNestedSetStaysConsistentUnderRandomRepliesAndDeletes(t *testing.T) {
	for seed := int64(1); seed <= 40; seed++ {
		rng := rand.New(rand.NewSource(seed))
		tr := &tree{nodes: map[string]*domain.Comment{}}
		next := 0
		newID := func() string { next++; return fmt.Sprintf("c%d", next) }
		for step := 0; step < 60; step++ {
			switch {
			case len(tr.nodes) == 0 || rng.Intn(10) < 2:
				tr.addRoot(newID())
			case rng.Intn(10) < 7:
				ids := keys(tr.nodes)
				tr.addReply(ids[rng.Intn(len(ids))], newID())
			default:
				ids := keys(tr.nodes)
				tr.remove(ids[rng.Intn(len(ids))])
			}
			require.NoError(t, tr.consistent(), "seed %d step %d", seed, step)
		}
	}
}

func keys(m map[string]*domain.Comment) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestDeletingAThreadRemovesExactlyItsSubtree(t *testing.T) {
	tr := &tree{nodes: map[string]*domain.Comment{}}
	tr.addRoot("a")
	tr.addReply("a", "a1")
	tr.addReply("a1", "a1x")
	tr.addReply("a", "a2")
	tr.addRoot("b")
	tr.addReply("b", "b1")
	require.NoError(t, tr.consistent())

	tr.remove("a1")
	assert.NotContains(t, tr.nodes, "a1")
	assert.NotContains(t, tr.nodes, "a1x", "descendants go with it")
	assert.Contains(t, tr.nodes, "a2")
	assert.Contains(t, tr.nodes, "b1")
	require.NoError(t, tr.consistent())
	tr.remove("a")
	assert.Len(t, tr.nodes, 2)
	require.NoError(t, tr.consistent())
}

// ---- events -------------------------------------------------------------------------------------------------------

func TestEvents(t *testing.T) {
	var _ event.Event = domain.ProductCreated{}
	var _ event.Event = domain.ProductUpdated{}
	assert.Equal(t, "catalog.product.created", domain.ProductCreated{ProductID: "p"}.EventType())
	assert.Equal(t, "p", domain.ProductCreated{ProductID: "p"}.AggregateID())
	assert.Equal(t, "catalog.product.updated", domain.ProductUpdated{ProductID: "p"}.EventType())
	assert.Equal(t, "p", domain.ProductUpdated{ProductID: "p"}.AggregateID())
}

func TestErrorConstructors(t *testing.T) {
	assert.Equal(t, errs.KindNotFound, errs.KindOf(domain.ErrProductNotFound()))
	assert.Equal(t, errs.KindAlreadyExists, errs.KindOf(domain.ErrProductNameTaken()))
	assert.Equal(t, errs.KindPermissionDenied, errs.KindOf(domain.ErrNotOwner()))
	assert.Equal(t, domain.CodeSpuNotFound, errs.CodeOf(domain.ErrSpuNotFound()))
	assert.Equal(t, domain.CodeSpuNameTaken, errs.CodeOf(domain.ErrSpuNameTaken()))
	assert.Equal(t, domain.CodeSkuNotFound, errs.CodeOf(domain.ErrSkuNotFound()))
	assert.Equal(t, domain.CodeCommentNotFound, errs.CodeOf(domain.ErrCommentNotFound()))
}
