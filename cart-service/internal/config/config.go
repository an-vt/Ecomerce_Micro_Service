// Package config loads cart-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8104
	DefaultGRPCPort = 9104
)

// Config of cart-service.
type Config struct {
	platform.Base

	MongoURI      string
	MongoDB       string
	CatalogAddr   string
	InventoryAddr string
	AutoMigrate   bool
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:          platform.LoadBase(l, "cart-service", DefaultHTTPPort, DefaultGRPCPort),
		MongoURI:      l.Required("MONGO_URI"),
		MongoDB:       l.String("MONGO_DB", "cart_db"),
		CatalogAddr:   l.String("CATALOG_GRPC_ADDR", "ecom-catalog:9102"),
		InventoryAddr: l.String("INVENTORY_GRPC_ADDR", "ecom-inventory:9103"),
		AutoMigrate:   l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
