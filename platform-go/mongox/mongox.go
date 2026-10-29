// Package mongox opens MongoDB (driver v2) and provides the transaction manager the use cases depend on
// (docs/03-project-structure.md §2.2). Multi-document transactions need a replica set (docs/11 §1).
//
// Like pgdb, the transaction travels in the context: repositories call collection methods with the ctx they
// receive, and the driver enlists them in the session carried by that ctx.
package mongox

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readconcern"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"
	"go.opentelemetry.io/contrib/instrumentation/go.mongodb.org/mongo-driver/v2/mongo/otelmongo"
)

// Config for Open.
type Config struct {
	URI      string // mongodb://user:pass@host:27017/db?replicaSet=rs0&authSource=db
	Database string
	MaxPool  uint64 // default 50
}

// Store owns the client.
type Store struct {
	client *mongo.Client
	db     *mongo.Database
}

// Open connects with tracing, majority write concern and snapshot-safe defaults, and pings.
func Open(ctx context.Context, c Config) (*Store, error) {
	if c.MaxPool == 0 {
		c.MaxPool = 50
	}
	opts := options.Client().ApplyURI(c.URI).
		SetMonitor(otelmongo.NewMonitor()).
		SetMaxPoolSize(c.MaxPool).
		SetWriteConcern(writeconcern.Majority()).
		SetServerSelectionTimeout(10 * time.Second)
	client, err := mongo.Connect(opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %w", err)
	}
	s := &Store{client: client, db: client.Database(c.Database)}
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := s.Ping(pctx); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, fmt.Errorf("mongodb: %w", err)
	}
	return s, nil
}

// DB returns the database. Operations take the ctx, which carries the transaction when there is one.
func (s *Store) DB() *mongo.Database { return s.db }

// Client returns the client (sessions, admin commands).
func (s *Store) Client() *mongo.Client { return s.client }

// Close disconnects.
func (s *Store) Close(ctx context.Context) error { return s.client.Disconnect(ctx) }

// Ping is usable as an httpx.Check.
func (s *Store) Ping(ctx context.Context) error { return s.client.Ping(ctx, nil) }

type txKey struct{}

// InTx reports whether ctx carries a transaction started by TxManager.
func InTx(ctx context.Context) bool { v, _ := ctx.Value(txKey{}).(bool); return v }

// TxManager runs functions inside a MongoDB transaction.
type TxManager struct{ store *Store }

// NewTxManager creates a manager.
func NewTxManager(s *Store) *TxManager { return &TxManager{store: s} }

// WithinTx runs fn in a transaction and commits when it returns nil. The driver re-runs the callback on
// TransientTransactionError and retries the commit on UnknownTransactionCommitResult, so fn must be safe to re-run.
// A call made while ctx already carries a transaction joins it.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if InTx(ctx) {
		return fn(ctx)
	}
	sess, err := m.store.client.StartSession()
	if err != nil {
		return err
	}
	defer sess.EndSession(ctx)
	txOpts := options.Transaction().SetReadConcern(readconcern.Snapshot()).SetWriteConcern(writeconcern.Majority())
	_, err = sess.WithTransaction(ctx, func(sc context.Context) (any, error) {
		return nil, fn(context.WithValue(sc, txKey{}, true))
	}, txOpts)
	return err
}

// IsDuplicateKey reports a unique index violation.
func IsDuplicateKey(err error) bool { return mongo.IsDuplicateKeyError(err) }

// IsNotFound reports mongo.ErrNoDocuments.
func IsNotFound(err error) bool { return err == mongo.ErrNoDocuments } //nolint:errorlint // sentinel returned unwrapped by the driver
