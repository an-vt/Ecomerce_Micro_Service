//go:build integration

package mongorepo_test

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	_ "github.com/an-vt/ecom/notification-service/internal/database/migrations"
	"github.com/an-vt/ecom/notification-service/internal/database/seeders"
	"github.com/an-vt/ecom/notification-service/internal/domain"
	mongorepo "github.com/an-vt/ecom/notification-service/internal/repository/mongo"
	"github.com/an-vt/ecom/notification-service/internal/repotest"
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/mongox"
	"github.com/an-vt/ecom/platform-go/testkit"
)

func openMigrated(t *testing.T) *mongox.Store {
	t.Helper()
	ctx := context.Background()
	uri, name := testkit.MongoURI(t)
	store, err := mongox.Open(ctx, mongox.Config{URI: uri, Database: name})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.DB().Drop(ctx); _ = store.Close(ctx) })
	_, err = mongomigrate.NewRunner(store.DB(), nil).Migrate(ctx, false)
	require.NoError(t, err)
	return store
}

func TestMongoSatisfiesTheRepositoryContract(t *testing.T) {
	repotest.Run(t, func(t *testing.T) repotest.Repos {
		db := openMigrated(t).DB()
		return repotest.Repos{Notifications: mongorepo.NewNotifications(db), Templates: mongorepo.NewTemplates(db), Emails: mongorepo.NewEmails(db)}
	})
}

func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := openMigrated(t)
	runner := mongomigrate.NewRunner(store.DB(), nil)
	rows, err := runner.Status(ctx)
	require.NoError(t, err)
	assert.Len(t, rows, 3)
	names, _ := store.DB().ListCollectionNames(ctx, bson.M{})
	for _, c := range []string{"notifications", "templates", "emails"} {
		assert.True(t, slices.Contains(names, c), c)
	}
	back, err := runner.Reset(ctx)
	require.NoError(t, err)
	assert.Len(t, back, 3)
	ran, err := runner.Migrate(ctx, false)
	require.NoError(t, err)
	assert.Len(t, ran, 3, "up -> down -> up")
}

// The seeder only adds what is missing: running it twice, or after an administrator edited a template, changes nothing.
func TestTemplateSeederIsIdempotentAndKeepsEdits(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t).DB()
	repo := mongorepo.NewTemplates(db)
	edited, _ := domain.NewTemplate("x", domain.TemplateWelcome, "<p>edited {{email}}</p>", repotest.Base)
	require.NoError(t, repo.Insert(ctx, edited))

	require.NoError(t, (&seeders.DatabaseSeeder{}).Run(ctx, db, mongomigrate.Seeders))
	require.NoError(t, (&seeders.TemplateSeeder{}).Run(ctx, db, nil))
	n, err := db.Collection("templates").CountDocuments(ctx, bson.M{})
	require.NoError(t, err)
	assert.EqualValues(t, len(domain.DefaultTemplates()), n)
	got, err := repo.FindByName(ctx, domain.TemplateWelcome)
	require.NoError(t, err)
	assert.Equal(t, "<p>edited {{email}}</p>", got.HTML, "an edited template survives db:seed")
	_, err = repo.FindByName(ctx, domain.TemplateEmailVerification)
	require.NoError(t, err, "the missing one was created")
}

// Acceptance #3 at database level: many copies of one message create one notification and one email record.
func TestRacingDuplicatesCollapseToOne(t *testing.T) {
	ctx := context.Background()
	db := openMigrated(t).DB()
	notes, emails := mongorepo.NewNotifications(db), mongorepo.NewEmails(db)

	var wg sync.WaitGroup
	var inserted, firstWins sync.Map
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, err := domain.NewNotification("n"+string(rune('a'+i)), domain.TypeOrderConfirmed, "s", "alice", "c", nil, "same-message", time.Now())
			if assert.NoError(t, err) {
				ok, err := notes.Insert(ctx, n)
				assert.NoError(t, err)
				inserted.Store(i, ok)
			}
			e, _ := domain.NewEmail("e"+string(rune('a'+i)), "same-message:welcome", "an@example.com", "s", "welcome", time.Now())
			got, err := emails.Begin(ctx, e)
			if assert.NoError(t, err) {
				firstWins.Store(i, got.ID)
			}
		}(i)
	}
	wg.Wait()

	wins := 0
	inserted.Range(func(_, v any) bool {
		if v.(bool) {
			wins++
		}
		return true
	})
	assert.Equal(t, 1, wins, "exactly one insert won")
	_, total, err := notes.List(ctx, domain.ListFilter{ReceiverID: "alice"}, 0, 50)
	require.NoError(t, err)
	assert.EqualValues(t, 1, total)

	ids := map[string]bool{}
	firstWins.Range(func(_, v any) bool { ids[v.(string)] = true; return true })
	assert.Len(t, ids, 1, "every Begin returned the same record")
}
