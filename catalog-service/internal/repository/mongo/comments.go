package mongorepo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/catalog-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/mongox"
)

type commentDoc struct {
	ID        string    `bson:"_id"`
	ProductID string    `bson:"productId"`
	UserID    string    `bson:"userId"`
	Content   string    `bson:"content"`
	Left      int64     `bson:"left"`
	Right     int64     `bson:"right"`
	ParentID  string    `bson:"parentId"`
	IsDeleted bool      `bson:"isDeleted"`
	CreatedAt time.Time `bson:"createdAt"`
}

func (d commentDoc) toDomain() *domain.Comment {
	return &domain.Comment{ID: d.ID, ProductID: d.ProductID, UserID: d.UserID, Content: d.Content, Left: d.Left, Right: d.Right,
		ParentID: d.ParentID, IsDeleted: d.IsDeleted, CreatedAt: d.CreatedAt.UTC()}
}

func toCommentDoc(c *domain.Comment) commentDoc {
	return commentDoc{ID: c.ID, ProductID: c.ProductID, UserID: c.UserID, Content: c.Content, Left: c.Left, Right: c.Right,
		ParentID: c.ParentID, IsDeleted: c.IsDeleted, CreatedAt: c.CreatedAt}
}

// commentRepo keeps each product's comments as a nested set. Every operation that moves nodes runs in a
// transaction (joining the caller's when there is one): two concurrent replies touch the same documents, so one
// of them gets a write conflict and the driver re-runs it on the updated tree.
type commentRepo struct {
	c  *mongo.Collection
	tx *mongox.TxManager
}

func (r *commentRepo) FindByID(ctx context.Context, id string) (*domain.Comment, error) {
	var d commentDoc
	err := r.c.FindOne(ctx, bson.M{"_id": id}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrCommentNotFound()
	}
	if err != nil {
		return nil, err
	}
	return d.toDomain(), nil
}

func (r *commentRepo) AddRoot(ctx context.Context, c *domain.Comment) error {
	return r.tx.WithinTx(ctx, func(ctx context.Context) error {
		var last commentDoc
		var maxRight int64
		err := r.c.FindOne(ctx, bson.M{"productId": c.ProductID}, options.FindOne().SetSort(bson.D{{Key: "right", Value: -1}})).Decode(&last)
		switch {
		case err == nil:
			maxRight = last.Right
		case !errors.Is(err, mongo.ErrNoDocuments):
			return err
		}
		p := domain.PlanRoot(maxRight)
		c.Left, c.Right = p.Left, p.Right
		_, err = r.c.InsertOne(ctx, toCommentDoc(c))
		return err
	})
}

func (r *commentRepo) AddReply(ctx context.Context, parent, c *domain.Comment) error {
	return r.tx.WithinTx(ctx, func(ctx context.Context) error {
		cur, err := r.FindByID(ctx, parent.ID) // the caller's copy may be stale: the tree moves
		if err != nil {
			return err
		}
		p := domain.PlanReply(cur.Right)
		if _, err := r.c.UpdateMany(ctx, bson.M{"productId": c.ProductID, "right": bson.M{"$gte": p.ShiftRightsFrom}}, bson.M{"$inc": bson.M{"right": 2}}); err != nil {
			return err
		}
		if _, err := r.c.UpdateMany(ctx, bson.M{"productId": c.ProductID, "left": bson.M{"$gt": p.ShiftLeftsAfter}}, bson.M{"$inc": bson.M{"left": 2}}); err != nil {
			return err
		}
		c.Left, c.Right, c.ParentID = p.Left, p.Right, parent.ID
		_, err = r.c.InsertOne(ctx, toCommentDoc(c))
		return err
	})
}

func (r *commentRepo) DeleteSubtree(ctx context.Context, c *domain.Comment) error {
	return r.tx.WithinTx(ctx, func(ctx context.Context) error {
		cur, err := r.FindByID(ctx, c.ID)
		if err != nil {
			return err
		}
		rm := domain.PlanDelete(*cur)
		if _, err := r.c.DeleteMany(ctx, bson.M{"productId": cur.ProductID, "left": bson.M{"$gte": rm.Left}, "right": bson.M{"$lte": rm.Right}}); err != nil {
			return err
		}
		if _, err := r.c.UpdateMany(ctx, bson.M{"productId": cur.ProductID, "left": bson.M{"$gt": rm.Right}}, bson.M{"$inc": bson.M{"left": -rm.Width}}); err != nil {
			return err
		}
		_, err = r.c.UpdateMany(ctx, bson.M{"productId": cur.ProductID, "right": bson.M{"$gt": rm.Right}}, bson.M{"$inc": bson.M{"right": -rm.Width}})
		return err
	})
}

func (r *commentRepo) List(ctx context.Context, productID, parentID string, limit, skip int) ([]*domain.Comment, error) {
	cur, err := r.c.Find(ctx, bson.M{"productId": productID, "parentId": parentID},
		options.Find().SetSort(bson.D{{Key: "left", Value: 1}}).SetSkip(int64(skip)).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []commentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]*domain.Comment, len(docs))
	for i := range docs {
		out[i] = docs[i].toDomain()
	}
	return out, nil
}
