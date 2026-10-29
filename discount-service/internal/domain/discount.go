package domain

import (
	"regexp"
	"strings"
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
)

// Type of reduction.
type Type string

// Applies-to scope.
type Scope string

const (
	TypeFixedAmount Type = "fixed_amount"
	TypePercent     Type = "percent"

	ScopeAll      Scope = "all"
	ScopeSpecific Scope = "specific"
)

// MaxProducts bounds the product list of a "specific" discount.
const MaxProducts = 500

var codePattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{2,31}$`)

// NormalizeCode trims and upper-cases a code: codes are case-insensitive for the buyer.
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// Line is one product line of an order, priced.
type Line struct {
	ProductID string
	Price     money.Amount
	Quantity  int64
}

// Calculation is the outcome of applying a code to an order.
type Calculation struct {
	Subtotal money.Amount // all lines of the shop
	Eligible money.Amount // the part of the subtotal the code applies to
	Discount money.Amount
}

// Total is what the shop's lines cost after the discount.
func (c Calculation) Total() money.Amount { return c.Subtotal - c.Discount }

// Discount is the aggregate. The usage counter is only changed through Use / Unuse, which the caller must invoke
// while holding the row lock (the repository's LockByCodes / LockByID), so two orders can never both take the last use.
type Discount struct {
	s DiscountSnapshot
}

// DiscountSnapshot is the persistable state.
type DiscountSnapshot struct {
	ID             string
	ShopID         string
	Code           string
	Name           string
	Description    string
	Type           Type
	Value          int64
	StartDate      time.Time
	EndDate        time.Time
	MaxUses        int64
	UsedCount      int64
	MaxUsesPerUser int64 // 0: unlimited
	MinOrderValue  int64
	IsActive       bool
	AppliesTo      Scope
	ProductIDs     []string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewInput are the fields a shop chooses when creating a code.
type NewInput struct {
	ID, ShopID, Code, Name, Description string
	Type                                Type
	Value                               int64
	StartDate, EndDate                  time.Time
	MaxUses, MaxUsesPerUser             int64
	MinOrderValue                       int64
	AppliesTo                           Scope
	ProductIDs                          []string
}

// New validates and creates a discount (monolith rules: end after start, end in the future, plus the amount rules the
// monolith lacked: a percent is 1..100, a value is positive, a "specific" code lists products).
func New(in NewInput, now time.Time) (*Discount, error) {
	code := NormalizeCode(in.Code)
	if err := validateIdentity(in, code); err != nil {
		return nil, err
	}
	if err := validateRules(in); err != nil {
		return nil, err
	}
	if err := validateDates(in.StartDate, in.EndDate, now); err != nil {
		return nil, err
	}
	scope, ids, err := normalizeScope(in.AppliesTo, in.ProductIDs)
	if err != nil {
		return nil, err
	}
	return &Discount{s: DiscountSnapshot{ID: in.ID, ShopID: in.ShopID, Code: code, Name: strings.TrimSpace(in.Name),
		Description: in.Description, Type: in.Type, Value: in.Value, StartDate: in.StartDate.UTC(), EndDate: in.EndDate.UTC(),
		MaxUses: in.MaxUses, MaxUsesPerUser: in.MaxUsesPerUser, MinOrderValue: in.MinOrderValue, IsActive: true,
		AppliesTo: scope, ProductIDs: ids, CreatedAt: now, UpdatedAt: now}}, nil
}

func validateIdentity(in NewInput, code string) error {
	switch {
	case in.ID == "" || in.ShopID == "":
		return errs.Validation("id and shop are required")
	case !codePattern.MatchString(code):
		return invalid("the code must be 3-32 characters: letters, digits, '-' or '_'")
	case strings.TrimSpace(in.Name) == "":
		return invalid("a name is required")
	}
	return nil
}

func validateRules(in NewInput) error {
	switch {
	case in.Type != TypeFixedAmount && in.Type != TypePercent:
		return invalid("type must be fixed_amount or percent")
	case in.Value <= 0:
		return invalid("the value must be positive")
	case in.Type == TypePercent && in.Value > 100:
		return invalid("a percent discount is at most 100")
	case in.MaxUses < 1:
		return invalid("max_uses must be at least 1")
	case in.MaxUsesPerUser < 0 || in.MinOrderValue < 0:
		return invalid("limits must not be negative")
	}
	return nil
}

func validateDates(start, end, now time.Time) error {
	switch {
	case start.IsZero() || end.IsZero():
		return invalid("start and end dates are required")
	case !end.After(start):
		return invalid("the end date must be after the start date")
	case !end.After(now):
		return invalid("the end date must be in the future")
	}
	return nil
}

// normalizeScope validates the scope and de-duplicates the product list ("all" carries none).
func normalizeScope(scope Scope, ids []string) (Scope, []string, error) {
	switch scope {
	case "", ScopeAll:
		return ScopeAll, nil, nil
	case ScopeSpecific:
		seen := map[string]bool{}
		var out []string
		for _, id := range ids {
			if id = strings.TrimSpace(id); id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
		if len(out) == 0 {
			return "", nil, invalid("a discount for specific products needs at least one product")
		}
		if len(out) > MaxProducts {
			return "", nil, invalid("a discount lists at most 500 products")
		}
		return ScopeSpecific, out, nil
	default:
		return "", nil, invalid("applies_to must be all or specific")
	}
}

// Restore rebuilds a discount from storage.
func Restore(s DiscountSnapshot) *Discount { return &Discount{s: s} }

// Snapshot returns the persistable state.
func (d *Discount) Snapshot() DiscountSnapshot {
	s := d.s
	s.ProductIDs = append([]string(nil), d.s.ProductIDs...)
	return s
}

func (d *Discount) ID() string     { return d.s.ID }
func (d *Discount) ShopID() string { return d.s.ShopID }
func (d *Discount) Code() string   { return d.s.Code }

// Patch holds the fields a shop may change after creation; nil leaves a field as it is. Code, type, value and start
// date are fixed: buyers and orders already rely on them.
type Patch struct {
	Name, Description *string
	EndDate           *time.Time
	IsActive          *bool
	MaxUses           *int64
	MaxUsesPerUser    *int64
	MinOrderValue     *int64
	AppliesTo         *Scope
	ProductIDs        []string // used with ReplaceProducts
	ReplaceProducts   bool
}

// Update applies a patch. A refused patch changes nothing.
func (d *Discount) Update(p Patch, now time.Time) error {
	n := d.s
	for _, step := range []func(*DiscountSnapshot, Patch) error{applyText, applyLimits, applyScope} {
		if err := step(&n, p); err != nil {
			return err
		}
	}
	n.UpdatedAt = now
	d.s = n
	return nil
}

func applyText(n *DiscountSnapshot, p Patch) error {
	if p.Name != nil {
		if strings.TrimSpace(*p.Name) == "" {
			return invalid("a name is required")
		}
		n.Name = strings.TrimSpace(*p.Name)
	}
	if p.Description != nil {
		n.Description = *p.Description
	}
	if p.EndDate != nil {
		if !p.EndDate.After(n.StartDate) {
			return invalid("the end date must be after the start date")
		}
		n.EndDate = p.EndDate.UTC()
	}
	if p.IsActive != nil {
		n.IsActive = *p.IsActive
	}
	return nil
}

func applyLimits(n *DiscountSnapshot, p Patch) error {
	if p.MaxUses != nil {
		if *p.MaxUses < 1 || *p.MaxUses < n.UsedCount {
			return invalid("max_uses cannot be below the uses already made")
		}
		n.MaxUses = *p.MaxUses
	}
	if p.MaxUsesPerUser != nil {
		if *p.MaxUsesPerUser < 0 {
			return invalid("limits must not be negative")
		}
		n.MaxUsesPerUser = *p.MaxUsesPerUser
	}
	if p.MinOrderValue != nil {
		if *p.MinOrderValue < 0 {
			return invalid("limits must not be negative")
		}
		n.MinOrderValue = *p.MinOrderValue
	}
	return nil
}

func applyScope(n *DiscountSnapshot, p Patch) error {
	if p.AppliesTo == nil && !p.ReplaceProducts {
		return nil
	}
	scope, ids := n.AppliesTo, n.ProductIDs
	if p.AppliesTo != nil {
		scope = *p.AppliesTo
	}
	if p.ReplaceProducts {
		ids = p.ProductIDs
	}
	s, list, err := normalizeScope(scope, ids)
	if err != nil {
		return err
	}
	n.AppliesTo, n.ProductIDs = s, list
	return nil
}

// CanBeUsedBy decides whether the code may be redeemed now by a user who has already used it usedByUser times.
// The checks are ordered from the cheapest explanation to the most specific one.
func (d *Discount) CanBeUsedBy(usedByUser int64, now time.Time) error {
	switch {
	case !d.s.IsActive:
		return notUsable(CodeInactive, "this discount code is not active")
	case now.Before(d.s.StartDate):
		return notUsable(CodeNotStarted, "this discount code is not valid yet")
	case !now.Before(d.s.EndDate):
		return notUsable(CodeExpired, "this discount code has expired")
	case d.s.UsedCount >= d.s.MaxUses:
		return notUsable(CodeUsedUp, "this discount code has been used up")
	case d.s.MaxUsesPerUser > 0 && usedByUser >= d.s.MaxUsesPerUser:
		return notUsable(CodeUserLimitReached, "you have already used this discount code the maximum number of times")
	}
	return nil
}

// Calculate prices the code against an order's lines of this shop. The minimum order value is checked against the
// whole subtotal; the reduction applies to the lines the code covers: a percent of them, or a fixed amount capped at
// their value, so a discount never makes a total negative.
func (d *Discount) Calculate(lines []Line) (Calculation, error) {
	var c Calculation
	covered := map[string]bool{}
	for _, id := range d.s.ProductIDs {
		covered[id] = true
	}
	for _, l := range lines {
		if l.Quantity <= 0 || l.Price < 0 {
			return Calculation{}, errs.Validation("every line needs a positive quantity and a price")
		}
		v, err := l.Price.Mul(l.Quantity)
		if err != nil {
			return Calculation{}, err
		}
		if c.Subtotal, err = c.Subtotal.Add(v); err != nil {
			return Calculation{}, err
		}
		if d.s.AppliesTo == ScopeAll || covered[l.ProductID] {
			if c.Eligible, err = c.Eligible.Add(v); err != nil {
				return Calculation{}, err
			}
		}
	}
	if c.Eligible == 0 {
		return Calculation{}, notUsable(CodeNotApplicable, "this discount code does not apply to the products in the order")
	}
	if int64(c.Subtotal) < d.s.MinOrderValue {
		return Calculation{}, notUsable(CodeMinOrderNotReached, "the order is below the minimum value for this discount code")
	}
	switch d.s.Type {
	case TypePercent:
		amount, err := c.Eligible.Percent(int(d.s.Value))
		if err != nil {
			return Calculation{}, err
		}
		c.Discount = amount
	default:
		c.Discount = money.Min(money.Amount(d.s.Value), c.Eligible)
	}
	return c, nil
}

// Use counts one redemption. Call it only with the row locked and after CanBeUsedBy.
func (d *Discount) Use(now time.Time) error {
	if d.s.UsedCount >= d.s.MaxUses {
		return notUsable(CodeUsedUp, "this discount code has been used up")
	}
	d.s.UsedCount++
	d.s.UpdatedAt = now
	return nil
}

// Unuse gives a redemption back (compensation); it never goes below zero.
func (d *Discount) Unuse(now time.Time) {
	if d.s.UsedCount > 0 {
		d.s.UsedCount--
		d.s.UpdatedAt = now
	}
}
