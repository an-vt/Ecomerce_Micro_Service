// Package pgoutbox is the PostgreSQL implementation of the outbox store, the outbox writer and the inbox guard
// (docs/08-messaging.md §3–4). Everything works on the transaction carried by the context (pgdb.Store.DB), so
// the outbox row and the business change commit or roll back together.
package pgoutbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"hash/fnv"
	"time"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

type row struct {
	bun.BaseModel `bun:"table:outbox_messages"`

	ID          string            `bun:"id,pk"`
	Topic       string            `bun:"topic"`
	Key         string            `bun:"key"`
	Type        string            `bun:"type"`
	Payload     json.RawMessage   `bun:"payload,type:jsonb"`
	Headers     map[string]string `bun:"headers,type:jsonb"`
	Status      string            `bun:"status"`
	Attempts    int               `bun:"attempts"`
	AvailableAt time.Time         `bun:"available_at"`
	CreatedAt   time.Time         `bun:"created_at"`
	SentAt      *time.Time        `bun:"sent_at"`
}

// Writer implements outbox.Writer.
type Writer struct{ store *pgdb.Store }

// NewWriter creates a writer.
func NewWriter(s *pgdb.Store) *Writer { return &Writer{store: s} }

// Add inserts the messages in the transaction carried by ctx (or autocommit when there is none — avoid that for
// business events).
func (w *Writer) Add(ctx context.Context, msgs ...outbox.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	rows := make([]row, len(msgs))
	for i, m := range msgs {
		h := m.Headers
		if h == nil {
			h = map[string]string{}
		}
		rows[i] = row{ID: m.ID, Topic: m.Topic, Key: m.Key, Type: m.Type, Payload: m.Payload, Headers: h,
			Status: "PENDING", AvailableAt: now, CreatedAt: now.Add(time.Duration(i) * time.Microsecond)} // keeps the insertion order
	}
	_, err := w.store.DB(ctx).NewInsert().Model(&rows).Exec(ctx)
	return err
}

// Store implements outbox.Store.
type Store struct {
	db     *bun.DB
	lockID int64
}

// NewStore creates the relay store. service seeds the advisory-lock key, so different services never share a lock.
func NewStore(s *pgdb.Store, service string) *Store {
	h := fnv.New64a()
	_, _ = h.Write([]byte("outbox:" + service))
	return &Store{db: s.Raw(), lockID: int64(h.Sum64() >> 1)}
}

type batch struct {
	tx   bun.Tx
	msgs []outbox.Message
	done bool
}

// Claim locks the due rows in a short transaction. A transaction-scoped advisory lock makes only one relay (of
// possibly several replicas) active at a time — parallel claiming could publish two messages of the same aggregate
// out of order — and FOR UPDATE SKIP LOCKED keeps a stuck session from blocking the others.
func (s *Store) Claim(ctx context.Context, limit int) (outbox.Batch, error) {
	// The transaction outlives the caller's context on purpose: the relay publishes to Kafka while holding it, and
	// a publish deadline must not roll it back before the outcome is recorded. Close ends it; the database's
	// idle_in_transaction_session_timeout is the backstop for a hung process.
	tx, err := s.db.BeginTx(context.WithoutCancel(ctx), &sql.TxOptions{})
	if err != nil {
		return nil, err
	}
	var leader bool
	if err := tx.NewSelect().ColumnExpr("pg_try_advisory_xact_lock(?)", s.lockID).Scan(ctx, &leader); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	b := &batch{tx: tx}
	if !leader {
		return b, nil
	}
	var rows []row
	err = tx.NewSelect().Model(&rows).
		Where("status = 'PENDING'").Where("available_at <= now()").
		OrderExpr("created_at, id").Limit(int64(limit)).For("UPDATE SKIP LOCKED").Scan(ctx)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	for _, r := range rows {
		b.msgs = append(b.msgs, outbox.Message{ID: r.ID, Topic: r.Topic, Key: r.Key, Type: r.Type,
			Payload: r.Payload, Headers: r.Headers, Attempts: r.Attempts, CreatedAt: r.CreatedAt})
	}
	return b, nil
}

func (b *batch) Messages() []outbox.Message { return b.msgs }

func (b *batch) MarkSent(ctx context.Context, ids ...string) error {
	_, err := b.tx.NewUpdate().Model((*row)(nil)).Set("status = 'SENT'").Set("sent_at = now()").
		Where("id IN (?)", bun.List(ids)).Exec(ctx)
	return err
}

func (b *batch) MarkFailed(ctx context.Context, id string, attempts int, retryAt time.Time) error {
	_, err := b.tx.NewUpdate().Model((*row)(nil)).Set("attempts = ?", attempts).Set("available_at = ?", retryAt.UTC()).
		Where("id = ?", id).Exec(ctx)
	return err
}

// Close commits (the lock and the row locks are released with it).
func (b *batch) Close(context.Context) error {
	if b.done {
		return nil
	}
	b.done = true
	return b.tx.Commit()
}

// Stats counts pending rows and the age of the oldest.
func (s *Store) Stats(ctx context.Context) (int64, time.Duration, error) {
	var pending int64
	var oldestSec float64
	err := s.db.NewSelect().TableExpr("outbox_messages").
		ColumnExpr("count(*)").
		ColumnExpr("coalesce(extract(epoch FROM now() - min(created_at)), 0)").
		Where("status = 'PENDING'").Scan(ctx, &pending, &oldestSec)
	return pending, time.Duration(oldestSec * float64(time.Second)), err
}

// Purge deletes sent rows older than before.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.NewDelete().Model((*row)(nil)).Where("status = 'SENT'").Where("sent_at < ?", before.UTC()).Exec(ctx)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- inbox ----------------------------------------------------------------------------------------------------

// Guard implements inbox.Guard: INSERT ... ON CONFLICT DO NOTHING in the handler's transaction.
type Guard struct{ store *pgdb.Store }

// NewGuard creates the inbox guard.
func NewGuard(s *pgdb.Store) *Guard { return &Guard{store: s} }

// TryRecord reports whether (consumer, messageID) is new.
func (g *Guard) TryRecord(ctx context.Context, consumer, messageID string) (bool, error) {
	res, err := g.store.DB(ctx).NewRaw(
		`INSERT INTO inbox_messages (message_id, consumer) VALUES (?, ?) ON CONFLICT DO NOTHING`, messageID, consumer).Exec(ctx)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}
