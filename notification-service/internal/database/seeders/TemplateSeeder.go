// Package seeders holds the Laravel-style seeders of notification-service (docs/03 §4).
package seeders

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/notification-service/internal/domain"
	mongorepo "github.com/an-vt/ecom/notification-service/internal/repository/mongo"
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
	"github.com/an-vt/ecom/platform-go/idgen"
)

func init() { mongomigrate.RegisterSeeder(&TemplateSeeder{}) }

// TemplateSeeder creates the email templates the service needs. It only adds what is missing, so running it again never
// overwrites a template an administrator edited.
type TemplateSeeder struct{}

func (s *TemplateSeeder) Name() string { return "TemplateSeeder" }

func (s *TemplateSeeder) Run(ctx context.Context, db *mongo.Database, _ *seeder.Set[*mongo.Database]) error {
	repo := mongorepo.NewTemplates(db)
	for _, dt := range domain.DefaultTemplates() {
		t, err := domain.NewTemplate(idgen.ULID{}.New(), dt.Name, dt.HTML, time.Now().UTC())
		if err != nil {
			return err
		}
		if _, err := repo.InsertIfAbsent(ctx, t); err != nil {
			return fmt.Errorf("template %s: %w", dt.Name, err)
		}
	}
	return nil
}
