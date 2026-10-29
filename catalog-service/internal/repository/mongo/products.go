package mongorepo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/money"
)

type productDoc struct {
	ID             string         `bson:"_id"`
	ShopID         string         `bson:"shopId"`
	Name           string         `bson:"name"`
	Description    string         `bson:"description"`
	Slug           string         `bson:"slug"`
	Price          int64          `bson:"price"`
	Quantity       int64          `bson:"quantity"`
	Type           string         `bson:"type"`
	Thumb          string         `bson:"thumb"`
	Attributes     map[string]any `bson:"attributes"`
	RatingsAverage float64        `bson:"ratingsAverage"`
	IsDraft        bool           `bson:"isDraft"`
	IsPublished    bool           `bson:"isPublished"`
	CreatedAt      time.Time      `bson:"createdAt"`
	UpdatedAt      time.Time      `bson:"updatedAt"`
}

func toProductDoc(p *domain.Product) productDoc {
	s := p.Snapshot()
	return productDoc{ID: s.ID, ShopID: s.ShopID, Name: s.Name, Description: s.Description, Slug: s.Slug, Price: int64(s.Price),
		Quantity: s.Quantity, Type: string(s.Type), Thumb: s.Thumb, Attributes: s.Attributes, RatingsAverage: s.RatingsAverage,
		IsDraft: s.IsDraft, IsPublished: s.IsPublished, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func (d productDoc) toDomain() *domain.Product {
	return domain.RestoreProduct(domain.ProductSnapshot{ID: d.ID, ShopID: d.ShopID, Name: d.Name, Description: d.Description, Slug: d.Slug,
		Price: money.Amount(d.Price), Quantity: d.Quantity, Type: domain.Type(d.Type), Thumb: d.Thumb, Attributes: plainMap(d.Attributes),
		RatingsAverage: d.RatingsAverage, IsDraft: d.IsDraft, IsPublished: d.IsPublished, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC()})
}

type productRepo struct{ c *mongo.Collection }

func (r *productRepo) Create(ctx context.Context, p *domain.Product) error {
	_, err := r.c.InsertOne(ctx, toProductDoc(p))
	if isDuplicate(err) {
		return domain.ErrProductNameTaken()
	}
	return err
}

func (r *productRepo) FindByID(ctx context.Context, id string) (*domain.Product, error) {
	var d productDoc
	err := r.c.FindOne(ctx, bson.M{"_id": id}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrProductNotFound()
	}
	if err != nil {
		return nil, err
	}
	return d.toDomain(), nil
}

func (r *productRepo) many(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) ([]*domain.Product, error) {
	cur, err := r.c.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	var docs []productDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]*domain.Product, len(docs))
	for i := range docs {
		out[i] = docs[i].toDomain()
	}
	return out, nil
}

func (r *productRepo) FindByIDs(ctx context.Context, ids []string) ([]*domain.Product, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return r.many(ctx, bson.M{"_id": bson.M{"$in": ids}})
}

func (r *productRepo) Update(ctx context.Context, p *domain.Product) error {
	res, err := r.c.ReplaceOne(ctx, bson.M{"_id": p.ID()}, toProductDoc(p))
	if isDuplicate(err) {
		return domain.ErrProductNameTaken()
	}
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return domain.ErrProductNotFound()
	}
	return nil
}

func pageOpts(page, limit int) *options.FindOptionsBuilder {
	return options.Find().SetSkip(int64((page - 1) * limit)).SetLimit(int64(limit))
}

func (r *productRepo) paged(ctx context.Context, filter any, sort any, page, limit int) ([]*domain.Product, int64, error) {
	total, err := r.c.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	items, err := r.many(ctx, filter, pageOpts(page, limit).SetSort(sort))
	return items, total, err
}

func (r *productRepo) ListPublished(ctx context.Context, q domain.ListQuery) ([]*domain.Product, int64, error) {
	sort := bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: 1}}
	switch q.Sort {
	case "price_asc":
		sort = bson.D{{Key: "price", Value: 1}, {Key: "_id", Value: 1}}
	case "price_desc":
		sort = bson.D{{Key: "price", Value: -1}, {Key: "_id", Value: 1}}
	}
	return r.paged(ctx, bson.M{"isPublished": true}, sort, q.Page, q.Limit)
}

// Search uses the text index (name weighs more than the description) and orders by relevance.
func (r *productRepo) Search(ctx context.Context, keyword string, page, limit int) ([]*domain.Product, int64, error) {
	filter := bson.M{"$text": bson.M{"$search": keyword}, "isPublished": true}
	total, err := r.c.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	items, err := r.many(ctx, filter, pageOpts(page, limit).
		SetProjection(bson.M{"score": bson.M{"$meta": "textScore"}}).
		SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}, {Key: "createdAt", Value: -1}}))
	return items, total, err
}

func (r *productRepo) ListByShop(ctx context.Context, shopID string, published bool, page, limit int) ([]*domain.Product, int64, error) {
	return r.paged(ctx, bson.M{"shopId": shopID, "isPublished": published}, bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: 1}}, page, limit)
}

func (r *productRepo) IDsByShop(ctx context.Context, shopID string) ([]string, error) {
	cur, err := r.c.Find(ctx, bson.M{"shopId": shopID, "isPublished": true}, options.Find().SetProjection(bson.M{"_id": 1}).SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	ids := make([]string, len(docs))
	for i, d := range docs {
		ids[i] = d.ID
	}
	return ids, nil
}
