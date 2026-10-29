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

type variationDoc struct {
	Name    string   `bson:"name"`
	Options []string `bson:"options"`
	Images  []string `bson:"images"`
}

type spuDoc struct {
	ID             string         `bson:"_id"`
	ShopID         string         `bson:"shopId"`
	Name           string         `bson:"name"`
	Thumb          string         `bson:"thumb"`
	Description    string         `bson:"description"`
	Slug           string         `bson:"slug"`
	Price          int64          `bson:"price"`
	Categories     []string       `bson:"categories"`
	Quantity       int64          `bson:"quantity"`
	Attributes     map[string]any `bson:"attributes"`
	RatingsAverage float64        `bson:"ratingsAverage"`
	Variations     []variationDoc `bson:"variations"`
	IsDraft        bool           `bson:"isDraft"`
	IsPublished    bool           `bson:"isPublished"`
	CreatedAt      time.Time      `bson:"createdAt"`
}

type skuDoc struct {
	ID        string `bson:"_id"`
	SpuID     string `bson:"spuId"`
	TierIdx   []int  `bson:"tierIdx"`
	IsDefault bool   `bson:"isDefault"`
	Slug      string `bson:"slug"`
	Sort      int    `bson:"sort"`
	Price     int64  `bson:"price"`
	Stock     int64  `bson:"stock"`
}

func (d spuDoc) toDomain() *domain.Spu {
	vars := make([]domain.Variation, len(d.Variations))
	for i, v := range d.Variations {
		vars[i] = domain.Variation{Name: v.Name, Options: v.Options, Images: v.Images}
	}
	return &domain.Spu{ID: d.ID, ShopID: d.ShopID, Name: d.Name, Thumb: d.Thumb, Description: d.Description, Slug: d.Slug,
		Price: money.Amount(d.Price), Categories: d.Categories, Quantity: d.Quantity, Attributes: plainMap(d.Attributes),
		RatingsAverage: d.RatingsAverage, Variations: vars, IsDraft: d.IsDraft, IsPublished: d.IsPublished, CreatedAt: d.CreatedAt.UTC()}
}

func (d skuDoc) toDomain() *domain.Sku {
	return &domain.Sku{ID: d.ID, SpuID: d.SpuID, TierIdx: d.TierIdx, IsDefault: d.IsDefault, Slug: d.Slug, Sort: d.Sort,
		Price: money.Amount(d.Price), Stock: d.Stock}
}

type spuRepo struct{ spus, skus *mongo.Collection }

// Create inserts the SPU and its SKUs; the use case runs it in a transaction, so a failure leaves nothing behind.
func (r *spuRepo) Create(ctx context.Context, spu *domain.Spu, skus []*domain.Sku) error {
	vars := make([]variationDoc, len(spu.Variations))
	for i, v := range spu.Variations {
		vars[i] = variationDoc{Name: v.Name, Options: v.Options, Images: v.Images}
	}
	_, err := r.spus.InsertOne(ctx, spuDoc{ID: spu.ID, ShopID: spu.ShopID, Name: spu.Name, Thumb: spu.Thumb, Description: spu.Description,
		Slug: spu.Slug, Price: int64(spu.Price), Categories: spu.Categories, Quantity: spu.Quantity, Attributes: spu.Attributes,
		RatingsAverage: spu.RatingsAverage, Variations: vars, IsDraft: spu.IsDraft, IsPublished: spu.IsPublished, CreatedAt: spu.CreatedAt})
	if isDuplicate(err) {
		return domain.ErrSpuNameTaken()
	}
	if err != nil {
		return err
	}
	if len(skus) == 0 {
		return nil
	}
	docs := make([]any, len(skus))
	for i, s := range skus {
		docs[i] = skuDoc{ID: s.ID, SpuID: s.SpuID, TierIdx: s.TierIdx, IsDefault: s.IsDefault, Slug: s.Slug, Sort: s.Sort, Price: int64(s.Price), Stock: s.Stock}
	}
	_, err = r.skus.InsertMany(ctx, docs)
	return err
}

func (r *spuRepo) FindByID(ctx context.Context, id string) (*domain.Spu, error) {
	var d spuDoc
	err := r.spus.FindOne(ctx, bson.M{"_id": id}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrSpuNotFound()
	}
	if err != nil {
		return nil, err
	}
	return d.toDomain(), nil
}

func (r *spuRepo) SkusOf(ctx context.Context, spuID string) ([]*domain.Sku, error) {
	cur, err := r.skus.Find(ctx, bson.M{"spuId": spuID}, options.Find().SetSort(bson.D{{Key: "sort", Value: 1}, {Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	var docs []skuDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]*domain.Sku, len(docs))
	for i := range docs {
		out[i] = docs[i].toDomain()
	}
	return out, nil
}

func (r *spuRepo) FindSku(ctx context.Context, skuID, spuID string) (*domain.Sku, error) {
	var d skuDoc
	err := r.skus.FindOne(ctx, bson.M{"_id": skuID, "spuId": spuID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrSkuNotFound()
	}
	if err != nil {
		return nil, err
	}
	return d.toDomain(), nil
}
