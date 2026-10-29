package domain

import (
	"context"
	"time"
)

// All repositories take the transaction from ctx (platform-go/pgdb) and return domain errors.

// InventoryRepository stores stock records.
type InventoryRepository interface {
	// Insert creates the record unless the product already has one; it reports whether it was created.
	Insert(ctx context.Context, inv *Inventory) (created bool, err error)
	// Lock loads the records of productIDs and locks them for the rest of the transaction (SELECT … FOR UPDATE, in
	// product-id order so concurrent transactions cannot deadlock). Unknown ids are absent from the result.
	Lock(ctx context.Context, productIDs []string) (map[string]*Inventory, error)
	// Find loads one record without locking; ErrInventoryNotFound when absent.
	Find(ctx context.Context, productID string) (*Inventory, error)
	// Save writes the stock figures of a record loaded with Lock.
	Save(ctx context.Context, inv *Inventory) error
}

// ReservationRepository stores reservations.
type ReservationRepository interface {
	// ForOrder lists every reservation of an order, any status.
	ForOrder(ctx context.Context, orderID string) ([]*Reservation, error)
	// Insert stores new reservations; (order, product) is unique.
	Insert(ctx context.Context, rs []*Reservation) error
	// Save writes the status of a reservation.
	Save(ctx context.Context, r *Reservation) error
	// ExpiredOrders returns up to limit order ids that still hold reservations past their deadline.
	ExpiredOrders(ctx context.Context, now time.Time, limit int) ([]string, error)
}
