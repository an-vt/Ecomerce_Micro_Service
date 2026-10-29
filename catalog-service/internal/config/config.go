// Package config loads catalog-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8102
	DefaultGRPCPort = 9102
)

// Config of catalog-service.
type Config struct {
	platform.Base

	MongoURI    string
	MongoDB     string
	SkuCacheTTL time.Duration
	AutoMigrate bool
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:        platform.LoadBase(l, "catalog-service", DefaultHTTPPort, DefaultGRPCPort),
		MongoURI:    l.Required("MONGO_URI"),
		MongoDB:     l.String("MONGO_DB", "catalog_db"),
		SkuCacheTTL: time.Duration(l.Int("SKU_CACHE_TTL_SEC", 30)) * time.Second,
		AutoMigrate: l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
