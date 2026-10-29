package console

import (
	"bytes"
	"fmt"
	"go/format"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"
)

// Kind selects the templates (and driver imports) for make:migration / make:seeder.
type Kind string

const (
	KindPostgres Kind = "pg"
	KindMongo    Kind = "mongo"
)

var snake = regexp.MustCompile(`^[a-z][a-z0-9]*(_[a-z0-9]+)*$`)

func camel(snakeName string, upperFirst bool) string {
	parts := strings.Split(snakeName, "_")
	for i, p := range parts {
		if i == 0 && !upperFirst {
			continue
		}
		parts[i] = strings.ToUpper(p[:1]) + p[1:]
	}
	return strings.Join(parts, "")
}

type migrationData struct {
	Name, Struct, Table, Mode, Pkg string // Mode: create | table | blank
}

func makeMigration(o Options, pos []string, f flagValues) error {
	if len(pos) != 1 || !snake.MatchString(pos[0]) {
		return fmt.Errorf("%w: make:migration needs a snake_case name, e.g. create_accounts_table", errUsage)
	}
	if f.create != "" && f.table != "" {
		return fmt.Errorf("%w: use either --create or --table", errUsage)
	}
	d := migrationData{Struct: camel(pos[0], false), Mode: "blank", Pkg: "migrations"}
	switch {
	case f.create != "":
		d.Mode, d.Table = "create", f.create
	case f.table != "":
		d.Mode, d.Table = "table", f.table
	case strings.HasPrefix(pos[0], "create_") && strings.HasSuffix(pos[0], "_table"):
		d.Mode, d.Table = "create", strings.TrimSuffix(strings.TrimPrefix(pos[0], "create_"), "_table")
	}
	d.Name = o.Now().UTC().Format("2006_01_02_150405") + "_" + pos[0]
	src, err := render(migrationTemplate(o.Kind), d)
	if err != nil {
		return err
	}
	path := filepath.Join(o.MigrationsDir, d.Name+".go")
	if err := writeNew(path, src); err != nil {
		return err
	}
	pf(o.Out, "Created Migration: %s\n", path)
	return nil
}

type seederData struct{ Name, Pkg string }

func makeSeeder(o Options, pos []string) error {
	if len(pos) != 1 || !regexp.MustCompile(`^[A-Z][A-Za-z0-9]*Seeder$`).MatchString(pos[0]) {
		return fmt.Errorf("%w: make:seeder needs a CamelCase name ending in Seeder, e.g. RbacSeeder", errUsage)
	}
	src, err := render(seederTemplate(o.Kind), seederData{Name: pos[0], Pkg: "seeders"})
	if err != nil {
		return err
	}
	path := filepath.Join(o.SeedersDir, pos[0]+".go")
	if err := writeNew(path, src); err != nil {
		return err
	}
	pf(o.Out, "Created Seeder: %s\n", path)
	return nil
}

func render(tpl string, data any) ([]byte, error) {
	t, err := template.New("gen").Parse(tpl)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return format.Source(buf.Bytes())
}

func migrationTemplate(k Kind) string {
	if k == KindMongo {
		return mongoMigrationTpl
	}
	return pgMigrationTpl
}

func seederTemplate(k Kind) string {
	if k == KindMongo {
		return mongoSeederTpl
	}
	return pgSeederTpl
}

const pgMigrationTpl = `package {{.Pkg}}

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&{{.Struct}}{}) }

type {{.Struct}} struct{}

func (m *{{.Struct}}) Name() string { return "{{.Name}}" }

func (m *{{.Struct}}) Up(ctx context.Context, db bun.IDB) error {
{{- if eq .Mode "create"}}
	_, err := db.ExecContext(ctx, ` + "`" + `
		CREATE TABLE {{.Table}} (
			id         UUID PRIMARY KEY,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)` + "`" + `)
	return err
{{- else if eq .Mode "table"}}
	_, err := db.ExecContext(ctx, ` + "`" + `ALTER TABLE {{.Table}} ADD COLUMN example VARCHAR(64)` + "`" + `)
	return err
{{- else}}
	// TODO: write the change.
	return nil
{{- end}}
}

func (m *{{.Struct}}) Down(ctx context.Context, db bun.IDB) error {
{{- if eq .Mode "create"}}
	_, err := db.ExecContext(ctx, ` + "`" + `DROP TABLE IF EXISTS {{.Table}}` + "`" + `)
	return err
{{- else if eq .Mode "table"}}
	_, err := db.ExecContext(ctx, ` + "`" + `ALTER TABLE {{.Table}} DROP COLUMN IF EXISTS example` + "`" + `)
	return err
{{- else}}
	// TODO: undo the change.
	return nil
{{- end}}
}
`

const mongoMigrationTpl = `package {{.Pkg}}

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
)

func init() { mongomigrate.Register(&{{.Struct}}{}) }

type {{.Struct}} struct{}

func (m *{{.Struct}}) Name() string { return "{{.Name}}" }

func (m *{{.Struct}}) Up(ctx context.Context, db *mongo.Database) error {
{{- if eq .Mode "create"}}
	if err := db.CreateCollection(ctx, "{{.Table}}"); err != nil {
		return err
	}
	_, err := db.Collection("{{.Table}}").Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{"{{"}}Key: "createdAt", Value: 1{{"}}"}}})
	return err
{{- else}}
	// TODO: write the change (indexes, validators, backfills).
	return nil
{{- end}}
}

func (m *{{.Struct}}) Down(ctx context.Context, db *mongo.Database) error {
{{- if eq .Mode "create"}}
	return db.Collection("{{.Table}}").Drop(ctx)
{{- else}}
	// TODO: undo the change.
	return nil
{{- end}}
}
`

const pgSeederTpl = `package {{.Pkg}}

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
)

func init() { pgmigrate.RegisterSeeder(&{{.Name}}{}) }

type {{.Name}} struct{}

func (s *{{.Name}}) Name() string { return "{{.Name}}" }

// Run must be idempotent: use INSERT ... ON CONFLICT DO UPDATE / DO NOTHING.
func (s *{{.Name}}) Run(ctx context.Context, db bun.IDB, set *seeder.Set[bun.IDB]) error {
	return nil
}
`

const mongoSeederTpl = `package {{.Pkg}}

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/database/seeder"
)

func init() { mongomigrate.RegisterSeeder(&{{.Name}}{}) }

type {{.Name}} struct{}

func (s *{{.Name}}) Name() string { return "{{.Name}}" }

// Run must be idempotent: use upserts.
func (s *{{.Name}}) Run(ctx context.Context, db *mongo.Database, set *seeder.Set[*mongo.Database]) error {
	return nil
}
`
