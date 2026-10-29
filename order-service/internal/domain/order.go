package domain

import (
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/money"
)

// Status of an order (docs/06 §2.2).
//
//	PENDING → STOCK_RESERVED → DISCOUNT_APPLIED → PAYMENT_PROCESSING → CONFIRMED
//	   └────────────┴───────────────┴──────────────────┴──→ CANCELLING → CANCELLED
//
// DISCOUNT_APPLIED is skipped for orders without a code.
type Status string

const (
	StatusPending           Status = "PENDING"
	StatusStockReserved     Status = "STOCK_RESERVED"
	StatusDiscountApplied   Status = "DISCOUNT_APPLIED"
	StatusPaymentProcessing Status = "PAYMENT_PROCESSING"
	StatusConfirmed         Status = "CONFIRMED"
	StatusCancelling        Status = "CANCELLING"
	StatusCancelled         Status = "CANCELLED"
)

var allowed = map[Status][]Status{
	StatusPending:           {StatusStockReserved, StatusCancelling},
	StatusStockReserved:     {StatusDiscountApplied, StatusPaymentProcessing, StatusCancelling},
	StatusDiscountApplied:   {StatusPaymentProcessing, StatusCancelling},
	StatusPaymentProcessing: {StatusConfirmed, StatusCancelling},
	StatusCancelling:        {StatusCancelled},
}

// Terminal reports whether no further transition is possible.
func (s Status) Terminal() bool { return s == StatusConfirmed || s == StatusCancelled }

// Item is a product line, priced by the catalog when the order was placed.
type Item struct {
	ProductID string
	ShopID    string
	Name      string
	Price     int64
	Quantity  int64
}

// Shipping is where the order goes.
type Shipping struct {
	Name    string
	Phone   string
	Address string
	City    string
}

// DiscountRef is the code a buyer uses on one shop's items.
type DiscountRef struct {
	ShopID string
	Code   string
}

// Order is the aggregate. Its status only moves along the arrows above; the saga is the only caller.
type Order struct{ s OrderSnapshot }

// OrderSnapshot is the persistable state.
type OrderSnapshot struct {
	ID             string
	UserID         string
	Status         Status
	TotalPrice     int64
	TotalDiscount  int64
	FeeShip        int64
	TotalCheckout  int64
	Shipping       Shipping
	PaymentMethod  string
	PaymentToken   string
	Discounts      []DiscountRef
	Items          []Item
	TrackingNumber string
	CancelReason   string
	Version        int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewOrderInput are the fields of a new order. Items carry catalog prices, never client prices.
type NewOrderInput struct {
	ID, UserID    string
	Items         []Item
	Discounts     []DiscountRef
	FeeShip       int64
	Shipping      Shipping
	PaymentMethod string
	PaymentToken  string
}

// NewOrder validates and creates a PENDING order. The total is computed here from the items.
func NewOrder(in NewOrderInput, now time.Time) (*Order, error) {
	if err := validateInput(in); err != nil {
		return nil, err
	}
	var total money.Amount
	for _, it := range in.Items {
		line, err := money.Amount(it.Price).Mul(it.Quantity)
		if err != nil {
			return nil, err
		}
		if total, err = total.Add(line); err != nil {
			return nil, err
		}
	}
	checkout, err := total.Add(money.Amount(in.FeeShip))
	if err != nil {
		return nil, err
	}
	return &Order{s: OrderSnapshot{ID: in.ID, UserID: in.UserID, Status: StatusPending, TotalPrice: int64(total), FeeShip: in.FeeShip,
		TotalCheckout: int64(checkout), Shipping: in.Shipping, PaymentMethod: strings.TrimSpace(in.PaymentMethod), PaymentToken: in.PaymentToken,
		Discounts: append([]DiscountRef(nil), in.Discounts...), Items: append([]Item(nil), in.Items...), CreatedAt: now, UpdatedAt: now}}, nil
}

func validateInput(in NewOrderInput) error {
	switch {
	case in.ID == "" || in.UserID == "":
		return invalid("an order needs an id and a buyer")
	case len(in.Items) == 0:
		return invalid("an order needs at least one item")
	case in.FeeShip < 0:
		return invalid("the shipping fee must not be negative")
	case strings.TrimSpace(in.Shipping.Address) == "":
		return invalid("a shipping address is required")
	case strings.TrimSpace(in.PaymentMethod) == "" || strings.TrimSpace(in.PaymentToken) == "":
		return invalid("a payment method and token are required")
	}
	for _, it := range in.Items {
		if it.ProductID == "" || it.ShopID == "" || it.Quantity <= 0 || it.Price < 0 {
			return invalid("every item needs a product, a shop, a positive quantity and a price")
		}
	}
	return nil
}

// RestoreOrder rebuilds an order from storage.
func RestoreOrder(s OrderSnapshot) *Order { return &Order{s: s} }

// Snapshot returns the persistable state (slices are copies).
func (o *Order) Snapshot() OrderSnapshot {
	s := o.s
	s.Items = append([]Item(nil), o.s.Items...)
	s.Discounts = append([]DiscountRef(nil), o.s.Discounts...)
	return s
}

func (o *Order) ID() string       { return o.s.ID }
func (o *Order) UserID() string   { return o.s.UserID }
func (o *Order) Status() Status   { return o.s.Status }
func (o *Order) AmountDue() int64 { return o.s.TotalCheckout }

// HasDiscounts reports whether the order uses any discount code.
func (o *Order) HasDiscounts() bool { return len(o.s.Discounts) > 0 }

// Advance moves the order along an allowed arrow.
func (o *Order) Advance(to Status, now time.Time) error {
	for _, next := range allowed[o.s.Status] {
		if next == to {
			o.s.Status, o.s.UpdatedAt = to, now
			o.s.Version++
			return nil
		}
	}
	return badState("an order in " + string(o.s.Status) + " cannot move to " + string(to))
}

// StartCancelling moves the order to CANCELLING and remembers why.
func (o *Order) StartCancelling(reason string, now time.Time) error {
	if err := o.Advance(StatusCancelling, now); err != nil {
		return err
	}
	o.s.CancelReason = reason
	return nil
}

// ApplyDiscount records what the discount codes took off (from DiscountsApplied, not from the review) and
// recomputes the amount to charge. Only an order whose stock is held can take it.
func (o *Order) ApplyDiscount(total int64, now time.Time) error {
	if o.s.Status != StatusStockReserved {
		return badState("a discount can only be applied to an order in STOCK_RESERVED, this one is " + string(o.s.Status))
	}
	if total < 0 || total > o.s.TotalPrice {
		return invalid("the discount must be between 0 and the order total")
	}
	o.s.TotalDiscount = total
	o.s.TotalCheckout = o.s.TotalPrice - total + o.s.FeeShip
	o.s.UpdatedAt = now
	return nil
}
