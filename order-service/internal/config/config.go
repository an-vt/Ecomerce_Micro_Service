// Package config loads order-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8106
	DefaultGRPCPort = 9106
)

// Config of order-service.
type Config struct {
	platform.Base

	DatabaseURL       string
	CatalogAddr       string
	DiscountAddr      string
	StepTimeout       time.Duration // SAGA_STEP_TIMEOUT_MS: a forward step that does not answer is given up on
	CompensationRetry time.Duration // SAGA_COMPENSATION_RETRY_MS: compensations are re-sent this often until acknowledged
	SweepInterval     time.Duration // SAGA_SWEEP_INTERVAL_MS: how often overdue sagas are looked for
	FeeShip           int64         // FEE_SHIP: flat shipping fee per order, minor units
	AutoMigrate       bool
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:              platform.LoadBase(l, "order-service", DefaultHTTPPort, DefaultGRPCPort),
		DatabaseURL:       l.Required("DATABASE_URL"),
		CatalogAddr:       l.String("CATALOG_GRPC_ADDR", "ecom-catalog:9102"),
		DiscountAddr:      l.String("DISCOUNT_GRPC_ADDR", "ecom-discount:9105"),
		StepTimeout:       l.DurationMS("SAGA_STEP_TIMEOUT_MS", 30*time.Second),
		CompensationRetry: l.DurationMS("SAGA_COMPENSATION_RETRY_MS", 30*time.Second),
		SweepInterval:     l.DurationMS("SAGA_SWEEP_INTERVAL_MS", 5*time.Second),
		FeeShip:           int64(l.Int("FEE_SHIP", 0)),
		AutoMigrate:       l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
