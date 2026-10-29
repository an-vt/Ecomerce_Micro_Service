package seeders

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/repository/postgres"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

func init() { pgmigrate.RegisterSeeder(&RbacSeeder{}) }

// RbacSeeder creates the resources and the three roles of docs/07 §4. It is an upsert: running it again restores
// the defaults without touching other roles.
type RbacSeeder struct{}

func (s *RbacSeeder) Name() string { return "RbacSeeder" }

var resources = []struct{ name, description string }{
	{"profile", "an account's own profile"},
	{"rbac", "roles and resources administration"},
	{"template", "email templates"},
}

var roles = []struct {
	name, description string
	grants            []domain.Grant
}{
	{"user", "buyers", []domain.Grant{{Resource: "profile", Actions: []string{"readOwn", "updateOwn"}}}},
	{"shop", "sellers", []domain.Grant{{Resource: "profile", Actions: []string{"readOwn", "updateOwn"}}}},
	{"admin", "operators", []domain.Grant{
		{Resource: "profile", Actions: []string{"readAny", "updateAny", "readOwn", "updateOwn"}},
		{Resource: "rbac", Actions: []string{"createAny", "readAny", "updateAny", "deleteAny"}},
		{Resource: "template", Actions: []string{"createAny", "readAny"}},
	}},
}

// store builds the repositories over the console's connection.
func store(db bun.IDB) (postgres.Repositories, error) {
	raw, ok := db.(*bun.DB)
	if !ok {
		return postgres.Repositories{}, errNotPool
	}
	return postgres.New(pgdb.NewStore(raw)), nil
}

func (s *RbacSeeder) Run(ctx context.Context, db bun.IDB, _ *seeder.Set[bun.IDB]) error {
	repos, err := store(db)
	if err != nil {
		return err
	}
	ids := idgen.UUID{}
	for _, r := range resources {
		res, err := domain.NewResource(ids.New(), r.name, "", r.description)
		if err != nil {
			return err
		}
		if err := repos.Rbac.UpsertResource(ctx, res); err != nil {
			return err
		}
	}
	for _, r := range roles {
		role, err := domain.NewRole(ids.New(), r.name, "", domain.RoleActive, r.description, r.grants)
		if err != nil {
			return err
		}
		if err := repos.Rbac.UpsertRole(ctx, role); err != nil {
			return err
		}
	}
	return nil
}
