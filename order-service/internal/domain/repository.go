package domain

import (
	"context"
	"time"
)

// All repositories take the transaction from ctx (platform-go/pgdb) and return domain errors.

// OrderRepository stores orders and their items.
type OrderRepository interface {
	// Insert stores a new order with its items.
	Insert(ctx context.Context, o *Order) error
	// Find loads an order without locking; ErrNotFound when absent.
	Find(ctx context.Context, id string) (*Order, error)
	// Lock loads an order and locks it until the end of the transaction; ErrNotFound when absent.
	Lock(ctx context.Context, id string) (*Order, error)
	// Save writes the mutable fields (status, totals, cancel reason, version, updated_at).
	Save(ctx context.Context, o *Order) error
	// ListByUser returns a page of a buyer's orders, newest first, and how many they have.
	ListByUser(ctx context.Context, userID string, offset, limit int) ([]*Order, int64, error)
}

// SagaRepository stores the checkout sagas.
type SagaRepository interface {
	Insert(ctx context.Context, s *Saga) error
	// Lock loads a saga and locks it until the end of the transaction; ErrSagaNotFound when absent.
	Lock(ctx context.Context, id string) (*Saga, error)
	Save(ctx context.Context, s *Saga) error
	// LockNextDue locks one unfinished saga whose deadline is before now, skipping rows other transactions hold
	// (SELECT … FOR UPDATE SKIP LOCKED), so several replicas can sweep without handling a timeout twice. It returns
	// nil, nil when nothing is due.
	LockNextDue(ctx context.Context, now time.Time) (*Saga, error)
}

// IdempotencyRecord binds an Idempotency-Key of a buyer to the order it created.
type IdempotencyRecord struct {
	UserID      string
	Key         string
	RequestHash string
	OrderID     string
	CreatedAt   time.Time
}

// IdempotencyRepository stores idempotency keys.
type IdempotencyRepository interface {
	// Claim stores the record unless (user, key) exists, and reports whether it did. When another transaction is
	// claiming the same key it waits for that transaction to finish first.
	Claim(ctx context.Context, r IdempotencyRecord) (claimed bool, err error)
	// Find returns the record of (user, key), or nil, nil.
	Find(ctx context.Context, userID, key string) (*IdempotencyRecord, error)
}
