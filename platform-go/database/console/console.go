// Package console implements the Laravel-style command line shared by every service's `console` binary
// (docs/03-project-structure.md §4):
//
//	console migrate [--pretend] [--force]
//	console migrate:rollback [--step=N] [--force]     console migrate:reset [--force]
//	console migrate:refresh [--seed] [--force]        console migrate:fresh [--seed]      (never in production)
//	console migrate:status
//	console db:seed [--class=X] [--force]
//	console make:migration <name> [--create=table | --table=table]
//	console make:seeder <Name>
package console

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/an-vt/ecom/platform-go/database/migration"
)

// Migrator is what the migrate:* commands need (satisfied by *migration.Runner[T]).
type Migrator interface {
	Migrate(ctx context.Context, pretend bool) ([]string, error)
	Rollback(ctx context.Context, step int) ([]string, error)
	Reset(ctx context.Context) ([]string, error)
	Refresh(ctx context.Context) (rolledBack, migrated []string, err error)
	Fresh(ctx context.Context) ([]string, error)
	Status(ctx context.Context) ([]migration.StatusRow, error)
}

// Seeder runs seeders (satisfied by an adapter over *seeder.Set[T]).
type Seeder interface {
	Seed(ctx context.Context, class string) ([]string, error)
}

// Options configures Run.
type Options struct {
	Env           string // APP_ENV: local | staging | production
	Kind          Kind   // which templates make:* uses
	MigrationsDir string // default internal/database/migrations
	SeedersDir    string // default internal/database/seeders
	Out           io.Writer
	Err           io.Writer
	Now           func() time.Time
}

func (o *Options) defaults() {
	if o.MigrationsDir == "" {
		o.MigrationsDir = "internal/database/migrations"
	}
	if o.SeedersDir == "" {
		o.SeedersDir = "internal/database/seeders"
	}
	if o.Out == nil {
		o.Out = os.Stdout
	}
	if o.Err == nil {
		o.Err = os.Stderr
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Kind == "" {
		o.Kind = KindPostgres
	}
}

// Run executes one command and returns the process exit code.
func Run(ctx context.Context, args []string, m Migrator, s Seeder, o Options) int {
	o.defaults()
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage(o.Out)
		return 0
	}
	cmd, rest := args[0], args[1:]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(o.Err)
	force := fs.Bool("force", false, "run in production")
	pretend := fs.Bool("pretend", false, "list pending migrations without running them")
	step := fs.Int("step", 1, "number of batches to roll back")
	withSeed := fs.Bool("seed", false, "run the seeders afterwards")
	class := fs.String("class", "", "seeder class to run")
	create := fs.String("create", "", "table to create")
	table := fs.String("table", "", "table to alter")

	// Allow the Laravel order `make:migration name --create=x` (flags after the positional argument).
	positional, flagArgs := splitArgs(rest)
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}

	err := dispatch(ctx, cmd, positional, o, m, s, flagValues{
		force: *force, pretend: *pretend, step: *step, seed: *withSeed, class: *class, create: *create, table: *table,
	})
	if err != nil {
		pl(o.Err, "error:", err)
		if errors.Is(err, errUsage) {
			return 2
		}
		return 1
	}
	return 0
}

type flagValues struct {
	force, pretend, seed bool
	step                 int
	class, create, table string
}

var errUsage = errors.New("usage")

// command is one console command; ctx / args / flags are everything a command may need.
type command func(c *cmdEnv) error

type cmdEnv struct {
	ctx  context.Context
	name string
	pos  []string
	o    Options
	m    Migrator
	s    Seeder
	f    flagValues
}

func (c *cmdEnv) production() bool { return strings.EqualFold(c.o.Env, "production") }

// guard enforces --force for destructive commands in production.
func (c *cmdEnv) guard() error {
	if c.production() && !c.f.force {
		return fmt.Errorf("APP_ENV=production: pass --force to run %s", c.name)
	}
	return nil
}

func (c *cmdEnv) list(verb string, names []string) {
	if len(names) == 0 {
		pf(c.o.Out, "Nothing to %s.\n", verb)
		return
	}
	for _, n := range names {
		pf(c.o.Out, "%s  %s\n", verb, n)
	}
}

// guarded wraps a command with the production guard.
func guarded(fn command) command {
	return func(c *cmdEnv) error {
		if err := c.guard(); err != nil {
			return err
		}
		return fn(c)
	}
}

var commands = map[string]command{
	"migrate": guarded(func(c *cmdEnv) error {
		names, err := c.m.Migrate(c.ctx, c.f.pretend)
		verb := "migrated"
		if c.f.pretend {
			verb = "pending"
		}
		c.list(verb, names)
		return err
	}),
	"migrate:rollback": guarded(func(c *cmdEnv) error {
		names, err := c.m.Rollback(c.ctx, c.f.step)
		c.list("rolled back", names)
		return err
	}),
	"migrate:reset": guarded(func(c *cmdEnv) error {
		names, err := c.m.Reset(c.ctx)
		c.list("rolled back", names)
		return err
	}),
	"migrate:refresh": guarded(func(c *cmdEnv) error {
		back, ran, err := c.m.Refresh(c.ctx)
		c.list("rolled back", back)
		c.list("migrated", ran)
		if err != nil {
			return err
		}
		return maybeSeed(c.ctx, c.o, c.s, c.f)
	}),
	"migrate:fresh": func(c *cmdEnv) error {
		if c.production() {
			return migration.ErrProduction // never, not even with --force
		}
		names, err := c.m.Fresh(c.ctx)
		c.list("migrated", names)
		if err != nil {
			return err
		}
		return maybeSeed(c.ctx, c.o, c.s, c.f)
	},
	"migrate:status": func(c *cmdEnv) error {
		rows, err := c.m.Status(c.ctx)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(c.o.Out, 0, 4, 2, ' ', 0)
		pl(w, "RAN\tBATCH\tMIGRATION")
		for _, r := range rows {
			ran, batch := "No", "-"
			if r.Ran {
				ran, batch = "Yes", fmt.Sprint(r.Batch)
			}
			pf(w, "%s\t%s\t%s\n", ran, batch, r.Name)
		}
		return w.Flush()
	},
	"db:seed":        guarded(func(c *cmdEnv) error { return seed(c.ctx, c.o, c.s, c.f.class) }),
	"make:migration": func(c *cmdEnv) error { return makeMigration(c.o, c.pos, c.f) },
	"make:seeder":    func(c *cmdEnv) error { return makeSeeder(c.o, c.pos) },
}

func dispatch(ctx context.Context, name string, pos []string, o Options, m Migrator, s Seeder, f flagValues) error {
	cmd, ok := commands[name]
	if !ok {
		usage(o.Err)
		return fmt.Errorf("%w: unknown command %q", errUsage, name)
	}
	return cmd(&cmdEnv{ctx: ctx, name: name, pos: pos, o: o, m: m, s: s, f: f})
}

// pf / pl write to w and deliberately ignore the error: if stdout / stderr is gone there is nowhere to report it.
func pf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }
func pl(w io.Writer, a ...any)                { _, _ = fmt.Fprintln(w, a...) }

func maybeSeed(ctx context.Context, o Options, s Seeder, f flagValues) error {
	if !f.seed {
		return nil
	}
	return seed(ctx, o, s, f.class)
}

func seed(ctx context.Context, o Options, s Seeder, class string) error {
	if s == nil {
		return errors.New("this service has no seeders")
	}
	names, err := s.Seed(ctx, class)
	for _, n := range names {
		pf(o.Out, "seeded  %s\n", n)
	}
	return err
}

// splitArgs separates positional arguments from flags so that both `name --create=x` and `--create=x name` work.
func splitArgs(args []string) (positional, flags []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			positional = append(positional, a)
		}
	}
	return positional, flags
}

func usage(w io.Writer) {
	pf(w, "%s", `Usage: console <command> [options]

  migrate [--pretend] [--force]        run pending migrations in a new batch
  migrate:rollback [--step=N]          roll back the last N batches (default 1)
  migrate:reset                        roll back everything
  migrate:refresh [--seed]             reset, then migrate
  migrate:fresh [--seed]               drop everything, then migrate (not in production)
  migrate:status                       show which migrations ran
  db:seed [--class=X]                  run the seeders
  make:migration <name> [--create=t | --table=t]
  make:seeder <Name>

In production (APP_ENV=production) destructive commands need --force; migrate:fresh is refused.
`)
}

func writeNew(path string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { //nolint:gosec // generated source tree, not secrets
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // source files are world-readable
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", path)
		}
		return err
	}
	if _, err := f.Write(content); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
