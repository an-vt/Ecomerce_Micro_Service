// Package mongorepo implements the catalog repositories with the official MongoDB driver (docs/04 §2). Reads and
// writes use the context, which carries the transaction when there is one (platform-go/mongox).
package mongorepo

import (
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/mongox"
)

// Repositories bundles the MongoDB implementations.
type Repositories struct {
	Products domain.ProductRepository
	Spus     domain.SpuRepository
	Comments domain.CommentRepository
	Shops    domain.ShopRepository
}

// New creates all repositories over one store.
func New(s *mongox.Store) Repositories {
	db := s.DB()
	return Repositories{
		Products: &productRepo{c: db.Collection("products")},
		Spus:     &spuRepo{spus: db.Collection("spus"), skus: db.Collection("skus")},
		Comments: &commentRepo{c: db.Collection("comments"), tx: mongox.NewTxManager(s)},
		Shops:    &shopRepo{c: db.Collection("shops")},
	}
}

// plain converts the values the driver decodes into interface{} (bson.D, bson.M, bson.A) to the plain Go types the
// domain uses (map[string]any, []any), recursively.
func plain(v any) any {
	switch x := v.(type) {
	case bson.D:
		m := make(map[string]any, len(x))
		for _, e := range x {
			m[e.Key] = plain(e.Value)
		}
		return m
	case bson.M:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = plain(e)
		}
		return m
	case map[string]any:
		m := make(map[string]any, len(x))
		for k, e := range x {
			m[k] = plain(e)
		}
		return m
	case bson.A:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = plain(e)
		}
		return a
	case []any:
		a := make([]any, len(x))
		for i, e := range x {
			a[i] = plain(e)
		}
		return a
	}
	return v
}

func plainMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return plain(m).(map[string]any)
}

func isDuplicate(err error) bool { return mongox.IsDuplicateKey(err) }

var _ = mongo.ErrNoDocuments
