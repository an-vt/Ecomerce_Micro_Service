// Package usecase holds the application logic of inventory-service: the shop-facing stock operations and the saga
// participant (reserve / release / commit). It depends on the domain and on the small interfaces below.
package usecase

import (
	"context"

	"github.com/an-vt/ecom/platform-go/outbox"
)

// TxManager runs fn in one database transaction.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Outbox adds messages in the transaction of ctx.
type Outbox interface {
	Add(ctx context.Context, msgs ...outbox.Message) error
}
