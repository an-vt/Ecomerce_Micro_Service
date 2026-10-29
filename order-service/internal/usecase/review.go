package usecase

import (
	"context"
	"sort"

	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
)

// Limits of one checkout request.
const (
	MaxShops = 20
	MaxLines = 100
)

// ItemInput is a product and a quantity as the buyer sent them. The price is never an input.
type ItemInput struct {
	ProductID string
	Quantity  int64
}

// ShopOrder is what a buyer wants from one shop, with at most one discount code.
type ShopOrder struct {
	ShopID       string
	DiscountCode string
	Items        []ItemInput
}

// ReviewShop is one shop's part of a review.
type ReviewShop struct {
	ShopID       string
	Items        []domain.Item
	Subtotal     int64
	DiscountCode string
	Discount     int64
	Total        int64
}

// Review is the priced checkout.
type Review struct {
	Shops         []ReviewShop
	TotalPrice    int64
	TotalDiscount int64
	FeeShip       int64
	TotalCheckout int64
}

// ReviewCheckout prices a checkout from the catalog and previews its discounts.
type ReviewCheckout struct{ d *Deps }

// Execute re-prices every item (the client's prices are never trusted), checks that each product exists, is published
// and belongs to the shop it was listed under, and asks discount-service what each code would take off. It writes
// nothing. The buyer's identity travels in ctx, so the discount service counts the buyer's own earlier uses.
func (u ReviewCheckout) Execute(ctx context.Context, orders []ShopOrder) (Review, error) {
	if _, err := userCaller(ctx); err != nil {
		return Review{}, err
	}
	return u.d.review(ctx, orders)
}

func validateShopOrders(orders []ShopOrder) ([]string, error) {
	if len(orders) == 0 {
		return nil, errs.Validation("at least one shop order is required")
	}
	if len(orders) > MaxShops {
		return nil, errs.Validation("at most 20 shops per checkout")
	}
	shops, products := map[string]bool{}, map[string]bool{}
	var ids []string
	for _, so := range orders {
		if so.ShopID == "" || len(so.Items) == 0 {
			return nil, errs.Validation("every shop order needs a shop and at least one item")
		}
		if shops[so.ShopID] {
			return nil, errs.Validation("shop " + so.ShopID + " appears twice")
		}
		shops[so.ShopID] = true
		for _, it := range so.Items {
			switch {
			case it.ProductID == "" || it.Quantity < 1:
				return nil, errs.Validation("every item needs a product and a quantity of at least 1")
			case products[it.ProductID]:
				return nil, errs.Validation("product " + it.ProductID + " appears twice")
			}
			products[it.ProductID] = true
			ids = append(ids, it.ProductID)
		}
	}
	if len(ids) > MaxLines {
		return nil, errs.Validation("at most 100 items per checkout")
	}
	return ids, nil
}

func (d *Deps) review(ctx context.Context, orders []ShopOrder) (Review, error) {
	ids, err := validateShopOrders(orders)
	if err != nil {
		return Review{}, err
	}
	found, err := d.Catalog.Products(ctx, ids)
	if err != nil {
		return Review{}, err
	}
	out := Review{FeeShip: d.FeeShip}
	var price, discount money.Amount
	for _, so := range orders {
		rs, err := d.reviewShop(ctx, so, found)
		if err != nil {
			return Review{}, err
		}
		out.Shops = append(out.Shops, rs)
		if price, err = price.Add(money.Amount(rs.Subtotal)); err != nil {
			return Review{}, err
		}
		discount += money.Amount(rs.Discount) // bounded by the subtotals, which did not overflow
	}
	checkout, err := (price - discount).Add(money.Amount(d.FeeShip))
	if err != nil {
		return Review{}, err
	}
	out.TotalPrice, out.TotalDiscount, out.TotalCheckout = int64(price), int64(discount), int64(checkout)
	return out, nil
}

func (d *Deps) reviewShop(ctx context.Context, so ShopOrder, found map[string]domain.Product) (ReviewShop, error) {
	rs := ReviewShop{ShopID: so.ShopID, DiscountCode: so.DiscountCode}
	var subtotal money.Amount
	var lines []domain.PreviewLine
	for _, it := range so.Items {
		p, ok := found[it.ProductID]
		switch {
		case !ok || !p.IsPublished:
			return ReviewShop{}, errs.NotFound(domain.CodeProductUnavailable, "product "+it.ProductID+" is not available")
		case p.ShopID != so.ShopID:
			return ReviewShop{}, errs.InvalidArgument(domain.CodeShopMismatch, "product "+it.ProductID+" does not belong to shop "+so.ShopID)
		}
		line, err := money.Amount(p.Price).Mul(it.Quantity)
		if err != nil {
			return ReviewShop{}, err
		}
		if subtotal, err = subtotal.Add(line); err != nil {
			return ReviewShop{}, err
		}
		rs.Items = append(rs.Items, domain.Item{ProductID: p.ID, ShopID: p.ShopID, Name: p.Name, Price: p.Price, Quantity: it.Quantity})
		lines = append(lines, domain.PreviewLine{ProductID: p.ID, Price: p.Price, Quantity: it.Quantity})
	}
	rs.Subtotal = int64(subtotal)
	rs.Total = rs.Subtotal
	if so.DiscountCode != "" {
		pv, err := d.Discounts.Preview(ctx, so.ShopID, so.DiscountCode, lines)
		if err != nil {
			return ReviewShop{}, err
		}
		rs.Discount, rs.Total = pv.Discount, rs.Subtotal-pv.Discount
	}
	return rs, nil
}

// discountRefs lists the codes of a request, shop order first.
func discountRefs(orders []ShopOrder) []domain.DiscountRef {
	var refs []domain.DiscountRef
	for _, so := range orders {
		if so.DiscountCode != "" {
			refs = append(refs, domain.DiscountRef{ShopID: so.ShopID, Code: so.DiscountCode})
		}
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ShopID < refs[j].ShopID })
	return refs
}
