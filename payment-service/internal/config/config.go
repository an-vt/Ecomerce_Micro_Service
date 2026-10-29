// Package config loads payment-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"fmt"
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// DefaultHTTPPort is the ops port of this service. It has no gRPC server: it is driven only by payment.commands.
const DefaultHTTPPort = 8107

// Config of payment-service.
type Config struct {
	platform.Base

	DatabaseURL     string
	ProviderTimeout time.Duration
	Currency        string
	AutoMigrate     bool
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:            platform.LoadBase(l, "payment-service", DefaultHTTPPort, 0),
		DatabaseURL:     l.Required("DATABASE_URL"),
		ProviderTimeout: l.DurationMS("PAYMENT_PROVIDER_TIMEOUT_MS", 3*time.Second),
		Currency:        l.String("PAYMENT_CURRENCY", "VND"),
		AutoMigrate:     l.Bool("AUTO_MIGRATE", true),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	if len(c.Currency) != 3 {
		return Config{}, fmt.Errorf("PAYMENT_CURRENCY: %q is not a 3-letter currency code", c.Currency)
	}
	return c, nil
}
