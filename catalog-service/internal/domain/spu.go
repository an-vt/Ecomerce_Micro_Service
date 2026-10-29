package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
)

// Variation is one tier of a variant product (colour, size, …).
type Variation struct {
	Name    string
	Options []string
	Images  []string
}

// Spu is a standard product unit: the product page. Its purchasable combinations are Skus.
type Spu struct {
	ID             string
	ShopID         string
	Name           string
	Thumb          string
	Description    string
	Slug           string
	Price          money.Amount
	Categories     []string
	Quantity       int64
	Attributes     map[string]any
	RatingsAverage float64
	Variations     []Variation
	IsDraft        bool
	IsPublished    bool
	CreatedAt      time.Time
}

// Sku is one combination of variation options with its own price and stock.
type Sku struct {
	ID        string // "<spuId>.<n>"
	SpuID     string
	TierIdx   []int // chosen option index per variation tier
	IsDefault bool
	Slug      string
	Sort      int
	Price     money.Amount
	Stock     int64
}

// SkuInput describes one SKU to create.
type SkuInput struct {
	TierIdx   []int
	IsDefault bool
	Sort      int
	Price     int64
	Stock     int64
}

// SpuInput describes an SPU with its SKUs (monolith SpuService.newSpu).
type SpuInput struct {
	Name        string
	Thumb       string
	Description string
	Price       int64
	Categories  []string
	Quantity    int64
	Attributes  map[string]any
	Variations  []Variation
	Skus        []SkuInput
}

func invalidSku(format string, args ...any) error {
	return errs.InvalidArgument(CodeInvalidSku, fmt.Sprintf(format, args...))
}

// NewSpu validates the SPU and builds its SKUs. Each SKU's tier indexes must point at an existing option of every
// variation tier; exactly one SKU is the default (the first when none is flagged).
func NewSpu(id, shopID string, in SpuInput, now time.Time) (*Spu, []*Sku, error) {
	if shopID == "" {
		return nil, nil, errs.Validation("shop id is required")
	}
	if err := validateCommon(in.Name, in.Price, in.Quantity); err != nil {
		return nil, nil, err
	}
	if len(in.Categories) == 0 {
		return nil, nil, errs.Validation("at least one category is required")
	}
	for i, v := range in.Variations {
		if strings.TrimSpace(v.Name) == "" || len(v.Options) == 0 {
			return nil, nil, invalidSku("variation %d needs a name and at least one option", i)
		}
	}
	name := strings.TrimSpace(in.Name)
	spu := &Spu{ID: id, ShopID: shopID, Name: name, Thumb: in.Thumb, Description: in.Description, Slug: Slugify(name),
		Price: money.Amount(in.Price), Categories: append([]string(nil), in.Categories...), Quantity: in.Quantity,
		Attributes: cloneMap(in.Attributes), RatingsAverage: DefaultRating, Variations: in.Variations, IsDraft: true, CreatedAt: now}

	skus, err := buildSkus(id, name, in)
	if err != nil {
		return nil, nil, err
	}
	return spu, skus, nil
}

// buildSkus validates every SKU against the variation tiers and builds them. Exactly one SKU is the default (the
// first when none is flagged); no two SKUs may pick the same combination of options.
func buildSkus(spuID, name string, in SpuInput) ([]*Sku, error) {
	defaults := 0
	for _, s := range in.Skus {
		if s.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return nil, invalidSku("only one sku can be the default")
	}
	skus := make([]*Sku, len(in.Skus))
	seen := map[string]bool{}
	for i, s := range in.Skus {
		if err := checkSku(i, s, in.Variations, seen); err != nil {
			return nil, err
		}
		skus[i] = &Sku{ID: fmt.Sprintf("%s.%d", spuID, i+1), SpuID: spuID, TierIdx: append([]int(nil), s.TierIdx...),
			IsDefault: s.IsDefault || (defaults == 0 && i == 0), Slug: Slugify(fmt.Sprintf("%s %v", name, s.TierIdx)),
			Sort: s.Sort, Price: money.Amount(s.Price), Stock: s.Stock}
	}
	return skus, nil
}

func checkSku(i int, s SkuInput, tiers []Variation, seen map[string]bool) error {
	if s.Price < 0 || s.Stock < 0 {
		return invalidSku("sku %d has a negative price or stock", i)
	}
	if len(s.TierIdx) != len(tiers) {
		return invalidSku("sku %d must pick one option for each of the %d variation tiers", i, len(tiers))
	}
	key := fmt.Sprint(s.TierIdx)
	if seen[key] {
		return invalidSku("sku %d repeats the combination %v", i, s.TierIdx)
	}
	seen[key] = true
	for tier, idx := range s.TierIdx {
		if idx < 0 || idx >= len(tiers[tier].Options) {
			return invalidSku("sku %d: option %d does not exist in tier %q", i, idx, tiers[tier].Name)
		}
	}
	return nil
}
