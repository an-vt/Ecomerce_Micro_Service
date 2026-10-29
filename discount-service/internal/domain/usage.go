package domain

import (
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// UsageStatus of a redemption.
type UsageStatus string

const (
	UsageApplied  UsageStatus = "APPLIED"
	UsageReleased UsageStatus = "RELEASED"
)

// Usage records that an order redeemed a code. (discount, order) is unique, which is what makes applying idempotent.
type Usage struct{ s UsageSnapshot }

// UsageSnapshot is the persistable state.
type UsageSnapshot struct {
	ID         string
	DiscountID string
	UserID     string
	OrderID    string
	Amount     int64
	Status     UsageStatus
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// NewUsage records a redemption.
func NewUsage(id, discountID, userID, orderID string, amount int64, now time.Time) (*Usage, error) {
	if id == "" || discountID == "" || userID == "" || orderID == "" || amount < 0 {
		return nil, errs.Validation("a usage needs a discount, a user and an order")
	}
	return &Usage{s: UsageSnapshot{ID: id, DiscountID: discountID, UserID: userID, OrderID: orderID, Amount: amount,
		Status: UsageApplied, CreatedAt: now, UpdatedAt: now}}, nil
}

// RestoreUsage rebuilds a usage from storage.
func RestoreUsage(s UsageSnapshot) *Usage { return &Usage{s: s} }

// Snapshot returns the persistable state.
func (u *Usage) Snapshot() UsageSnapshot { return u.s }

func (u *Usage) DiscountID() string  { return u.s.DiscountID }
func (u *Usage) Amount() int64       { return u.s.Amount }
func (u *Usage) Status() UsageStatus { return u.s.Status }

// Release undoes the redemption; it reports whether anything changed (a repeat is a no-op).
func (u *Usage) Release(now time.Time) bool {
	if u.s.Status == UsageReleased {
		return false
	}
	u.s.Status, u.s.UpdatedAt = UsageReleased, now
	return true
}
