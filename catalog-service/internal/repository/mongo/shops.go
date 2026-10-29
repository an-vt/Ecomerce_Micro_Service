package mongorepo

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
)

type shopDoc struct {
	ID    string `bson:"_id"`
	Name  string `bson:"name"`
	Email string `bson:"email"`
}

type shopRepo struct{ c *mongo.Collection }

func (r *shopRepo) Upsert(ctx context.Context, s domain.ShopProjection) error {
	_, err := r.c.ReplaceOne(ctx, bson.M{"_id": s.ID}, shopDoc{ID: s.ID, Name: s.Name, Email: s.Email}, options.Replace().SetUpsert(true))
	return err
}

func (r *shopRepo) FindByIDs(ctx context.Context, ids []string) (map[string]domain.ShopProjection, error) {
	out := map[string]domain.ShopProjection{}
	if len(ids) == 0 {
		return out, nil
	}
	cur, err := r.c.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		return nil, err
	}
	var docs []shopDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	for _, d := range docs {
		out[d.ID] = domain.ShopProjection{ID: d.ID, Name: d.Name, Email: d.Email}
	}
	return out, nil
}
