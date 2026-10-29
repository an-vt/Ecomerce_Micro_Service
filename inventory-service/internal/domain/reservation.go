package domain

import (
	"time"

	"github.com/an-vt/ecom/platform-go/errs"
)

// ReservationStatus is the state of a hold.
type ReservationStatus string

const (
	ReservationReserved  ReservationStatus = "RESERVED"
	ReservationCommitted ReservationStatus = "COMMITTED"
	ReservationReleased  ReservationStatus = "RELEASED"
	ReservationExpired   ReservationStatus = "EXPIRED"
)

// Reservation is the stock of one product held for one order. RESERVED is the only state that can change: it ends
// in COMMITTED (sold), RELEASED (compensation) or EXPIRED (nobody finished the order in time).
type Reservation struct {
	id        string
	orderID   string
	productID string
	quantity  int64
	status    ReservationStatus
	expiresAt time.Time
	createdAt time.Time
	updatedAt time.Time
}

// ReservationSnapshot is the persisted form of a Reservation.
type ReservationSnapshot struct {
	ID, OrderID, ProductID string
	Quantity               int64
	Status                 ReservationStatus
	ExpiresAt              time.Time
	CreatedAt, UpdatedAt   time.Time
}

// NewReservation holds q units of a product for an order until expiresAt.
func NewReservation(id, orderID, productID string, q int64, expiresAt, now time.Time) (*Reservation, error) {
	if orderID == "" || productID == "" {
		return nil, errs.Validation("order id and product id are required")
	}
	if err := positive(q); err != nil {
		return nil, err
	}
	return &Reservation{id: id, orderID: orderID, productID: productID, quantity: q, status: ReservationReserved, expiresAt: expiresAt, createdAt: now, updatedAt: now}, nil
}

// RestoreReservation rebuilds a Reservation from storage.
func RestoreReservation(s ReservationSnapshot) *Reservation {
	return &Reservation{id: s.ID, orderID: s.OrderID, productID: s.ProductID, quantity: s.Quantity, status: s.Status,
		expiresAt: s.ExpiresAt, createdAt: s.CreatedAt, updatedAt: s.UpdatedAt}
}

// Snapshot returns the persisted state.
func (r *Reservation) Snapshot() ReservationSnapshot {
	return ReservationSnapshot{ID: r.id, OrderID: r.orderID, ProductID: r.productID, Quantity: r.quantity, Status: r.status,
		ExpiresAt: r.expiresAt, CreatedAt: r.createdAt, UpdatedAt: r.updatedAt}
}

func (r *Reservation) ID() string                { return r.id }
func (r *Reservation) OrderID() string           { return r.orderID }
func (r *Reservation) ProductID() string         { return r.productID }
func (r *Reservation) Quantity() int64           { return r.quantity }
func (r *Reservation) Status() ReservationStatus { return r.status }
func (r *Reservation) ExpiresAt() time.Time      { return r.expiresAt }

// Active reports whether the hold is still in force.
func (r *Reservation) Active() bool { return r.status == ReservationReserved }

// IsExpired reports whether an active hold has outlived its deadline.
func (r *Reservation) IsExpired(now time.Time) bool { return r.Active() && !now.Before(r.expiresAt) }

func (r *Reservation) to(next ReservationStatus, now time.Time) error {
	if r.status != ReservationReserved {
		return errs.FailedPrecondition(CodeInvalidState, "reservation is already "+string(r.status))
	}
	r.status, r.updatedAt = next, now
	return nil
}

// Commit marks the hold as sold.
func (r *Reservation) Commit(now time.Time) error { return r.to(ReservationCommitted, now) }

// Release marks the hold as given back.
func (r *Reservation) Release(now time.Time) error { return r.to(ReservationReleased, now) }

// Expire marks the hold as lapsed.
func (r *Reservation) Expire(now time.Time) error { return r.to(ReservationExpired, now) }
