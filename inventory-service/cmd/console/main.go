// Command console is the Laravel-style CLI of inventory-service: migrate, migrate:rollback, migrate:fresh,
// migrate:status, make:migration (this service has no seeders) (docs/03-project-structure.md §4).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/an-vt/ecom/inventory-service/internal/config"
	_ "github.com/an-vt/ecom/inventory-service/internal/database/migrations"
	platform "github.com/an-vt/ecom/platform-go/config"
	"github.com/an-vt/ecom/platform-go/database/console"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	args := os.Args[1:]
	opts := console.Options{Env: platform.NewLoader().String("APP_ENV", "local"), Kind: console.KindPostgres}

	// Generators and help need no database.
	if len(args) == 0 || strings.HasPrefix(args[0], "make:") || strings.HasPrefix(args[0], "-") || args[0] == "help" {
		return console.Run(ctx, args, nil, nil, opts)
	}
	cfg, err := config.Load(platform.NewLoader())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: cfg.DatabaseURL, DBName: "inventory_db"})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer func() { _ = store.Close() }()
	opts.Env = cfg.Env
	migrator, seeder := pgmigrate.Console(store.Raw(), "inventory_db")
	return console.Run(ctx, args, migrator, seeder, opts)
}
