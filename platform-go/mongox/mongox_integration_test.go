//go:build integration

package mongox_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/platform-go/mongox"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func open(t *testing.T) (*mongox.Store, *mongox.TxManager) {
	t.Helper()
	uri, db := testkit.MongoURI(t)
	s, err := mongox.Open(context.Background(), mongox.Config{URI: uri, Database: db})
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.DB().Drop(context.Background()); _ = s.Close(context.Background()) })
	// collections must exist before a multi-document transaction touches them
	require.NoError(t, s.DB().CreateCollection(context.Background(), "items"))
	_, err = s.DB().Collection("items").Indexes().CreateOne(context.Background(),
		mongo.IndexModel{Keys: bson.D{{Key: "name", Value: 1}}, Options: options.Index().SetUnique(true)})
	require.NoError(t, err)
	return s, mongox.NewTxManager(s)
}

func count(t *testing.T, s *mongox.Store) int64 {
	t.Helper()
	n, err := s.DB().Collection("items").CountDocuments(context.Background(), bson.M{})
	require.NoError(t, err)
	return n
}

func TestCommitAndRollback(t *testing.T) {
	s, tx := open(t)
	ctx := context.Background()
	items := s.DB().Collection("items")

	require.NoError(t, tx.WithinTx(ctx, func(ctx context.Context) error {
		assert.True(t, mongox.InTx(ctx))
		_, err := items.InsertOne(ctx, bson.M{"name": "a"})
		return err
	}))
	assert.Equal(t, int64(1), count(t, s))

	boom := errors.New("boom")
	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		_, _ = items.InsertOne(ctx, bson.M{"name": "b"})
		return boom
	})
	assert.ErrorIs(t, err, boom)
	assert.Equal(t, int64(1), count(t, s), "rolled back")
	assert.False(t, mongox.InTx(ctx))
}

func TestNestedCallJoinsTheOuterTransaction(t *testing.T) {
	s, tx := open(t)
	ctx := context.Background()
	err := tx.WithinTx(ctx, func(ctx context.Context) error {
		if err := tx.WithinTx(ctx, func(ctx context.Context) error {
			_, err := s.DB().Collection("items").InsertOne(ctx, bson.M{"name": "a"})
			return err
		}); err != nil {
			return err
		}
		return errors.New("outer fails after inner succeeded")
	})
	require.Error(t, err)
	assert.Equal(t, int64(0), count(t, s))
}

func TestWritesInsideATransactionAreInvisibleUntilCommit(t *testing.T) {
	s, tx := open(t)
	ctx := context.Background()
	inside := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- tx.WithinTx(ctx, func(ctx context.Context) error {
			if _, err := s.DB().Collection("items").InsertOne(ctx, bson.M{"name": "pending"}); err != nil {
				return err
			}
			select {
			case inside <- struct{}{}:
			default:
			}
			<-release
			return nil
		})
	}()
	<-inside
	assert.Equal(t, int64(0), count(t, s), "uncommitted data is not visible outside the transaction")
	close(release)
	require.NoError(t, <-done)
	assert.Equal(t, int64(1), count(t, s))
}

func TestErrorHelpers(t *testing.T) {
	s, _ := open(t)
	ctx := context.Background()
	_, err := s.DB().Collection("items").InsertOne(ctx, bson.M{"name": "dup"})
	require.NoError(t, err)
	_, err = s.DB().Collection("items").InsertOne(ctx, bson.M{"name": "dup"})
	assert.True(t, mongox.IsDuplicateKey(err))
	assert.False(t, mongox.IsDuplicateKey(errors.New("x")))

	err = s.DB().Collection("items").FindOne(ctx, bson.M{"name": "none"}).Err()
	assert.True(t, mongox.IsNotFound(err))
	assert.False(t, mongox.IsNotFound(nil))
	assert.NoError(t, s.Ping(ctx))
	assert.NotNil(t, s.Client())
}

func TestOpenFailsOnUnreachableServer(t *testing.T) {
	_, err := mongox.Open(context.Background(), mongox.Config{URI: "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=300", Database: "x"})
	assert.Error(t, err)
}
