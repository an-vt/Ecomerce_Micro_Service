// Package inbox makes message handlers idempotent (docs/08-messaging.md §4): the handler's effects and a
// (messageID, consumer) record are committed in the same transaction, so a redelivered message changes nothing.
package inbox

import (
	"context"
	"errors"
)

// ErrDuplicate is returned by Handle when the message was already processed. Consumers treat it as success.
var ErrDuplicate = errors.New("inbox: message already processed")

// Guard records that a consumer has processed a message. Implementations insert in the transaction carried by ctx
// (pgoutbox / mongooutbox provide them).
type Guard interface {
	// TryRecord returns true when this is the first time (consumer, messageID) is seen.
	TryRecord(ctx context.Context, consumer, messageID string) (first bool, err error)
}

// TxManager is the part of pgdb.TxManager / mongox.TxManager that Handle needs.
type TxManager interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Handle runs fn exactly once per (consumer, messageID): record + effects commit together, a duplicate returns
// ErrDuplicate without running fn.
func Handle(ctx context.Context, tx TxManager, g Guard, consumer, messageID string, fn func(ctx context.Context) error) error {
	return tx.WithinTx(ctx, func(ctx context.Context) error {
		first, err := g.TryRecord(ctx, consumer, messageID)
		if err != nil {
			return err
		}
		if !first {
			return ErrDuplicate
		}
		return fn(ctx)
	})
}
