// Package mongorepo implements the notification repositories with the official MongoDB driver (docs/04 §8).
package mongorepo

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	"github.com/an-vt/ecom/platform-go/mongox"
)

// ---- notifications ------------------------------------------------------------------------------------------------

type notificationDoc struct {
	ID         string    `bson:"_id"`
	Type       string    `bson:"type"`
	SenderID   string    `bson:"senderId"`
	ReceiverID string    `bson:"receiverId"`
	Content    string    `bson:"content"`
	Options    bson.Raw  `bson:"options"`
	IsRead     bool      `bson:"isRead"`
	DedupeKey  string    `bson:"dedupeKey"`
	CreatedAt  time.Time `bson:"createdAt"`
}

func (d notificationDoc) toDomain() (*domain.Notification, error) {
	opts := map[string]any{}
	if len(d.Options) > 0 {
		// relaxed extended JSON turns every BSON number into a plain JSON number, whatever width it was stored with
		js, err := bson.MarshalExtJSON(d.Options, false, false)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(js, &opts); err != nil {
			return nil, err
		}
	}
	return &domain.Notification{ID: d.ID, Type: domain.Type(d.Type), SenderID: d.SenderID, ReceiverID: d.ReceiverID, Content: d.Content,
		Options: opts, IsRead: d.IsRead, DedupeKey: d.DedupeKey, CreatedAt: d.CreatedAt.UTC()}, nil
}

// Notifications implements domain.NotificationRepository.
type Notifications struct{ c *mongo.Collection }

// NewNotifications creates the repository over a database.
func NewNotifications(db *mongo.Database) *Notifications {
	return &Notifications{c: db.Collection("notifications")}
}

// Insert implements domain.NotificationRepository. The unique index on dedupeKey is what makes a redelivered message
// harmless.
func (r *Notifications) Insert(ctx context.Context, n *domain.Notification) (bool, error) {
	opts, err := bson.Marshal(n.Options)
	if err != nil {
		return false, err
	}
	_, err = r.c.InsertOne(ctx, notificationDoc{ID: n.ID, Type: string(n.Type), SenderID: n.SenderID, ReceiverID: n.ReceiverID, Content: n.Content,
		Options: opts, IsRead: n.IsRead, DedupeKey: n.DedupeKey, CreatedAt: n.CreatedAt})
	if mongox.IsDuplicateKey(err) {
		return false, nil
	}
	return err == nil, err
}

// List implements domain.NotificationRepository.
func (r *Notifications) List(ctx context.Context, f domain.ListFilter, offset, limit int) ([]*domain.Notification, int64, error) {
	filter := bson.M{"receiverId": bson.M{"$in": bson.A{f.ReceiverID, domain.Broadcast}}}
	if f.Type != "" {
		filter["type"] = string(f.Type)
	}
	if f.IsRead != nil {
		filter["isRead"] = *f.IsRead
	}
	total, err := r.c.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	cur, err := r.c.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(offset)).SetLimit(int64(limit)))
	if err != nil {
		return nil, 0, err
	}
	var docs []notificationDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, 0, err
	}
	out := make([]*domain.Notification, 0, len(docs))
	for _, d := range docs {
		n, err := d.toDomain()
		if err != nil {
			return nil, 0, err
		}
		out = append(out, n)
	}
	return out, total, nil
}

// ---- templates ----------------------------------------------------------------------------------------------------

type templateDoc struct {
	ID        string    `bson:"_id"`
	Name      string    `bson:"name"`
	HTML      string    `bson:"html"`
	Status    string    `bson:"status"`
	CreatedAt time.Time `bson:"createdAt"`
}

// Templates implements domain.TemplateRepository.
type Templates struct{ c *mongo.Collection }

// NewTemplates creates the repository over a database.
func NewTemplates(db *mongo.Database) *Templates { return &Templates{c: db.Collection("templates")} }

func (r *Templates) insert(ctx context.Context, t *domain.Template) error {
	_, err := r.c.InsertOne(ctx, templateDoc{ID: t.ID, Name: t.Name, HTML: t.HTML, Status: t.Status, CreatedAt: t.CreatedAt})
	return err
}

// Insert implements domain.TemplateRepository.
func (r *Templates) Insert(ctx context.Context, t *domain.Template) error {
	if err := r.insert(ctx, t); mongox.IsDuplicateKey(err) {
		return domain.ErrTemplateExists()
	} else {
		return err
	}
}

// InsertIfAbsent implements domain.TemplateRepository.
func (r *Templates) InsertIfAbsent(ctx context.Context, t *domain.Template) (bool, error) {
	err := r.insert(ctx, t)
	if mongox.IsDuplicateKey(err) {
		return false, nil
	}
	return err == nil, err
}

// FindByName implements domain.TemplateRepository.
func (r *Templates) FindByName(ctx context.Context, name string) (*domain.Template, error) {
	var d templateDoc
	err := r.c.FindOne(ctx, bson.M{"name": name}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrTemplateNotFound(name)
	}
	if err != nil {
		return nil, err
	}
	return &domain.Template{ID: d.ID, Name: d.Name, HTML: d.HTML, Status: d.Status, CreatedAt: d.CreatedAt.UTC()}, nil
}

// ---- emails -------------------------------------------------------------------------------------------------------

type emailDoc struct {
	ID        string    `bson:"_id"`
	DedupeKey string    `bson:"dedupeKey"`
	To        string    `bson:"to"`
	Subject   string    `bson:"subject"`
	Template  string    `bson:"template"`
	Status    string    `bson:"status"`
	Attempts  int       `bson:"attempts"`
	LastError string    `bson:"lastError"`
	CreatedAt time.Time `bson:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

func (d emailDoc) toDomain() *domain.Email {
	return &domain.Email{ID: d.ID, DedupeKey: d.DedupeKey, To: d.To, Subject: d.Subject, Template: d.Template, Status: domain.EmailStatus(d.Status),
		Attempts: d.Attempts, LastError: d.LastError, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC()}
}

// Emails implements domain.EmailRepository.
type Emails struct{ c *mongo.Collection }

// NewEmails creates the repository over a database.
func NewEmails(db *mongo.Database) *Emails { return &Emails{c: db.Collection("emails")} }

// Begin implements domain.EmailRepository: the unique dedupeKey turns a concurrent or repeated start into one record.
func (r *Emails) Begin(ctx context.Context, e *domain.Email) (*domain.Email, error) {
	_, err := r.c.InsertOne(ctx, emailDoc{ID: e.ID, DedupeKey: e.DedupeKey, To: e.To, Subject: e.Subject, Template: e.Template, Status: string(e.Status),
		Attempts: e.Attempts, LastError: e.LastError, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt})
	if err == nil {
		c := *e
		return &c, nil
	}
	if !mongox.IsDuplicateKey(err) {
		return nil, err
	}
	var d emailDoc
	if err := r.c.FindOne(ctx, bson.M{"dedupeKey": e.DedupeKey}).Decode(&d); err != nil {
		return nil, err
	}
	return d.toDomain(), nil
}

// Save implements domain.EmailRepository.
func (r *Emails) Save(ctx context.Context, e *domain.Email) error {
	res, err := r.c.UpdateOne(ctx, bson.M{"dedupeKey": e.DedupeKey}, bson.M{"$set": bson.M{
		"status": string(e.Status), "attempts": e.Attempts, "lastError": e.LastError, "updatedAt": e.UpdatedAt}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return mongo.ErrNoDocuments
	}
	return nil
}
