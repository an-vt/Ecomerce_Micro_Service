// Package mongorepo implements the cart repository with the official MongoDB driver (docs/04 §4).
package mongorepo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/cart-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/money"
	"github.com/an-vt/ecom/platform-go/mongox"
)

type itemDoc struct {
	ProductID string `bson:"productId"`
	ShopID    string `bson:"shopId"`
	Name      string `bson:"name"`
	Price     int64  `bson:"price"`
	Quantity  int64  `bson:"quantity"`
}

type cartDoc struct {
	ID           string    `bson:"_id"`
	UserID       string    `bson:"userId"`
	State        string    `bson:"state"`
	Items        []itemDoc `bson:"items"`
	CountProduct int       `bson:"countProduct"`
	Version      int64     `bson:"version"`
	CreatedAt    time.Time `bson:"createdAt"`
	UpdatedAt    time.Time `bson:"updatedAt"`
}

// Carts implements domain.CartRepository.
type Carts struct{ c *mongo.Collection }

// New creates the repository over a store.
func New(s *mongox.Store) *Carts { return &Carts{c: s.DB().Collection("carts")} }

func toDoc(c *domain.Cart) cartDoc {
	s := c.Snapshot()
	items := make([]itemDoc, len(s.Items))
	for i, it := range s.Items {
		items[i] = itemDoc{ProductID: it.ProductID, ShopID: it.ShopID, Name: it.Name, Price: int64(it.Price), Quantity: it.Quantity}
	}
	return cartDoc{ID: s.ID, UserID: s.UserID, State: string(s.State), Items: items, CountProduct: len(items), Version: s.Version, CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt}
}

func (d cartDoc) toDomain() *domain.Cart {
	items := make([]domain.Item, len(d.Items))
	for i, it := range d.Items {
		items[i] = domain.Item{ProductID: it.ProductID, ShopID: it.ShopID, Name: it.Name, Price: money.Amount(it.Price), Quantity: it.Quantity}
	}
	return domain.RestoreCart(domain.CartSnapshot{ID: d.ID, UserID: d.UserID, State: domain.State(d.State), Items: items, Version: d.Version,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC()})
}

// FindByUser implements domain.CartRepository.
func (r *Carts) FindByUser(ctx context.Context, userID string) (*domain.Cart, error) {
	var d cartDoc
	err := r.c.FindOne(ctx, bson.M{"userId": userID}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return d.toDomain(), nil
}

func conflict() error {
	return errs.Aborted(domain.CodeCartConflict, "the cart was changed concurrently")
}

// Save implements domain.CartRepository. A new cart (version 0) is inserted — the unique index on userId turns a
// race of two first saves into one winner and a conflict; an existing cart is replaced only if the stored version is
// still the one this copy was loaded at.
func (r *Carts) Save(ctx context.Context, c *domain.Cart) error {
	doc := toDoc(c)
	if c.Version() == 0 {
		doc.Version = 1 // the stored version must already be the new one: the next save checks against it
		if _, err := r.c.InsertOne(ctx, doc); err != nil {
			if mongox.IsDuplicateKey(err) {
				return conflict()
			}
			return err
		}
	} else {
		doc.Version = c.Version() + 1
		res, err := r.c.ReplaceOne(ctx, bson.M{"userId": c.UserID(), "version": c.Version()}, doc)
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return conflict()
		}
	}
	c.Bump()
	return nil
}
