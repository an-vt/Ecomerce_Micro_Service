// Package config loads inventory-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8103
	DefaultGRPCPort = 9103
)

// Config of inventory-service.
type Config struct {
	platform.Base

	DatabaseURL    string
	ReservationTTL time.Duration
	ExpiryInterval time.Duration
	AutoMigrate    bool
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:           platform.LoadBase(l, "inventory-service", DefaultHTTPPort, DefaultGRPCPort),
		DatabaseURL:    l.Required("DATABASE_URL"),
		ReservationTTL: l.DurationMS("RESERVATION_TTL_MS", 15*time.Minute),
		ExpiryInterval: l.DurationMS("RESERVATION_SWEEP_MS", 30*time.Second),
		AutoMigrate:    l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
