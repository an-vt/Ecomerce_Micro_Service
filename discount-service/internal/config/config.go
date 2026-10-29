// Package config loads discount-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8105
	DefaultGRPCPort = 9105
)

// Config of discount-service.
type Config struct {
	platform.Base

	DatabaseURL string
	CatalogAddr string
	AutoMigrate bool
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:        platform.LoadBase(l, "discount-service", DefaultHTTPPort, DefaultGRPCPort),
		DatabaseURL: l.Required("DATABASE_URL"),
		CatalogAddr: l.String("CATALOG_GRPC_ADDR", "ecom-catalog:9102"),
		AutoMigrate: l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
