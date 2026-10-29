// Package mongooutbox is the MongoDB implementation of the outbox store, writer and inbox guard
// (docs/08-messaging.md §3–4). Like pgoutbox it works on the transaction carried by ctx (mongox.TxManager).
// One relay is active at a time, chosen by a short lease document, which keeps per-aggregate order.
package mongooutbox

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/outbox"
)

const (
	outboxColl = "outbox_messages"
	inboxColl  = "inbox_messages"
	leaseColl  = "outbox_lease"
	leaseTTL   = 15 * time.Second
)

type doc struct {
	ID          string            `bson:"_id"`
	Topic       string            `bson:"topic"`
	Key         string            `bson:"key"`
	Type        string            `bson:"type"`
	Payload     string            `bson:"payload"`
	Headers     map[string]string `bson:"headers"`
	Status      string            `bson:"status"`
	Attempts    int               `bson:"attempts"`
	AvailableAt time.Time         `bson:"available_at"`
	CreatedAt   time.Time         `bson:"created_at"`
	SentAt      *time.Time        `bson:"sent_at,omitempty"`
}

// Writer implements outbox.Writer.
type Writer struct{ db *mongo.Database }

// NewWriter creates a writer.
func NewWriter(db *mongo.Database) *Writer { return &Writer{db: db} }

// Add inserts the messages with the session carried by ctx.
func (w *Writer) Add(ctx context.Context, msgs ...outbox.Message) error {
	if len(msgs) == 0 {
		return nil
	}
	now := time.Now().UTC()
	docs := make([]any, len(msgs))
	for i, m := range msgs {
		h := m.Headers
		if h == nil {
			h = map[string]string{}
		}
		docs[i] = doc{ID: m.ID, Topic: m.Topic, Key: m.Key, Type: m.Type, Payload: string(m.Payload), Headers: h,
			Status: "PENDING", AvailableAt: now, CreatedAt: now.Add(time.Duration(i) * time.Microsecond)}
	}
	_, err := w.db.Collection(outboxColl).InsertMany(ctx, docs)
	return err
}

// Store implements outbox.Store.
type Store struct {
	db    *mongo.Database
	owner string
}

// NewStore creates the relay store; every process gets its own lease owner id.
func NewStore(db *mongo.Database) *Store { return &Store{db: db, owner: bson.NewObjectID().Hex()} }

func (s *Store) lead(ctx context.Context) (bool, error) {
	now := time.Now().UTC()
	err := s.db.Collection(leaseColl).FindOneAndUpdate(ctx,
		bson.M{"_id": "relay", "$or": bson.A{bson.M{"owner": s.owner}, bson.M{"expires_at": bson.M{"$lt": now}}}},
		bson.M{"$set": bson.M{"owner": s.owner, "expires_at": now.Add(leaseTTL)}},
		options.FindOneAndUpdate().SetUpsert(true)).Err()
	switch {
	case err == nil || errors.Is(err, mongo.ErrNoDocuments): // updated, or the upsert created it
		return true, nil
	case mongo.IsDuplicateKeyError(err): // somebody else holds a live lease
		return false, nil
	}
	return false, err
}

type batch struct {
	db   *mongo.Database
	msgs []outbox.Message
}

// Claim returns the due messages when this process holds the relay lease, else an empty batch.
func (s *Store) Claim(ctx context.Context, limit int) (outbox.Batch, error) {
	b := &batch{db: s.db}
	ok, err := s.lead(ctx)
	if err != nil || !ok {
		return b, err
	}
	cur, err := s.db.Collection(outboxColl).Find(ctx,
		bson.M{"status": "PENDING", "available_at": bson.M{"$lte": time.Now().UTC()}},
		options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []doc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		b.msgs = append(b.msgs, outbox.Message{ID: d.ID, Topic: d.Topic, Key: d.Key, Type: d.Type,
			Payload: []byte(d.Payload), Headers: d.Headers, Attempts: d.Attempts, CreatedAt: d.CreatedAt})
	}
	return b, nil
}

func (b *batch) Messages() []outbox.Message { return b.msgs }

func (b *batch) MarkSent(ctx context.Context, ids ...string) error {
	_, err := b.db.Collection(outboxColl).UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"status": "SENT", "sent_at": time.Now().UTC()}})
	return err
}

func (b *batch) MarkFailed(ctx context.Context, id string, attempts int, retryAt time.Time) error {
	_, err := b.db.Collection(outboxColl).UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"attempts": attempts, "available_at": retryAt.UTC()}})
	return err
}

func (b *batch) Close(context.Context) error { return nil }

// Stats counts pending messages and the age of the oldest.
func (s *Store) Stats(ctx context.Context) (int64, time.Duration, error) {
	coll := s.db.Collection(outboxColl)
	n, err := coll.CountDocuments(ctx, bson.M{"status": "PENDING"})
	if err != nil || n == 0 {
		return n, 0, err
	}
	var oldest doc
	if err := coll.FindOne(ctx, bson.M{"status": "PENDING"}, options.FindOne().SetSort(bson.D{{Key: "created_at", Value: 1}})).Decode(&oldest); err != nil {
		return n, 0, err
	}
	return n, time.Since(oldest.CreatedAt), nil
}

// Purge deletes sent messages older than before.
func (s *Store) Purge(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.Collection(outboxColl).DeleteMany(ctx, bson.M{"status": "SENT", "sent_at": bson.M{"$lt": before.UTC()}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// ---- inbox ----------------------------------------------------------------------------------------------------

// Guard implements inbox.Guard with an upsert, which does not abort the surrounding transaction on a duplicate
// (a duplicate-key insert would).
type Guard struct{ db *mongo.Database }

// NewGuard creates the inbox guard.
func NewGuard(db *mongo.Database) *Guard { return &Guard{db: db} }

// TryRecord reports whether (consumer, messageID) is new.
func (g *Guard) TryRecord(ctx context.Context, consumer, messageID string) (bool, error) {
	res, err := g.db.Collection(inboxColl).UpdateOne(ctx, bson.M{"_id": consumer + ":" + messageID},
		bson.M{"$setOnInsert": bson.M{"consumer": consumer, "message_id": messageID, "processed_at": time.Now().UTC()}},
		options.UpdateOne().SetUpsert(true))
	if err != nil {
		return false, err
	}
	return res.UpsertedCount == 1, nil
}

// ---- migration ----------------------------------------------------------------------------------------------

// CreateCollections returns the migration that creates the outbox, inbox and lease collections with their
// indexes. Every MongoDB service registers it from a one-line migration file.
func CreateCollections(name string) mongomigrate.Migration { return createCollections(name) }

type createCollections string

func (m createCollections) Name() string { return string(m) }

func (m createCollections) Up(ctx context.Context, db *mongo.Database) error {
	for _, c := range []string{outboxColl, inboxColl, leaseColl} {
		if err := db.CreateCollection(ctx, c); err != nil && !isNamespaceExists(err) {
			return err
		}
	}
	_, err := db.Collection(outboxColl).Indexes().CreateMany(ctx, []mongo.IndexModel{
		{Keys: bson.D{{Key: "status", Value: 1}, {Key: "available_at", Value: 1}, {Key: "created_at", Value: 1}}},
		{Keys: bson.D{{Key: "sent_at", Value: 1}}, Options: options.Index().SetPartialFilterExpression(bson.M{"status": "SENT"})},
	})
	return err
}

func (m createCollections) Down(ctx context.Context, db *mongo.Database) error {
	for _, c := range []string{outboxColl, inboxColl, leaseColl} {
		if err := db.Collection(c).Drop(ctx); err != nil {
			return err
		}
	}
	return nil
}

func isNamespaceExists(err error) bool {
	var ce mongo.CommandError
	return errors.As(err, &ce) && ce.Code == 48
}
