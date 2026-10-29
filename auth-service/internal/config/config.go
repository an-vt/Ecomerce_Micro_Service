// Package config loads auth-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// Ports of this service.
const (
	DefaultHTTPPort = 8101
	DefaultGRPCPort = 9101
)

// Config of auth-service.
type Config struct {
	platform.Base

	DatabaseURL string

	// JWTPrivateKeyPEM is the RS256 signing key (AUTH_JWT_PRIVATE_KEY or AUTH_JWT_PRIVATE_KEY_FILE).
	JWTPrivateKeyPEM string
	JWTIssuer        string
	JWTAudience      string
	AccessTTL        time.Duration
	RefreshTTL       time.Duration
	OTPTTL           time.Duration
	VerifyLinkBase   string
	BcryptCost       int
	AutoMigrate      bool // run pending migrations on start (local default; production uses the console)

	// Seeding (console db:seed).
	SeedAPIKey        string
	SeedAdminEmail    string
	SeedAdminPassword string
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:              platform.LoadBase(l, "auth-service", DefaultHTTPPort, DefaultGRPCPort),
		DatabaseURL:       l.Required("DATABASE_URL"),
		JWTPrivateKeyPEM:  l.Required("AUTH_JWT_PRIVATE_KEY"),
		JWTIssuer:         l.String("AUTH_JWT_ISSUER", "ecom-auth"),
		JWTAudience:       l.String("AUTH_JWT_AUDIENCE", "ecom"),
		AccessTTL:         time.Duration(l.Int("ACCESS_TOKEN_TTL_SEC", 900)) * time.Second,
		RefreshTTL:        time.Duration(l.Int("REFRESH_TOKEN_TTL_SEC", 604800)) * time.Second,
		OTPTTL:            time.Duration(l.Int("OTP_TTL_SEC", 300)) * time.Second,
		VerifyLinkBase:    l.String("VERIFY_LINK_BASE", "http://localhost:3000/v1/api/user/welcome_back"),
		BcryptCost:        l.Int("BCRYPT_COST", 10),
		AutoMigrate:       l.Bool("AUTO_MIGRATE", true),
		SeedAPIKey:        l.String("SEED_API_KEY", ""),
		SeedAdminEmail:    l.String("SEED_ADMIN_EMAIL", ""),
		SeedAdminPassword: l.String("SEED_ADMIN_PASSWORD", ""),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// LoadConsole reads only what the console needs (no signing key).
func LoadConsole(l *platform.Loader) (Config, error) {
	c := Config{
		Base:              platform.LoadBase(l, "auth-service", DefaultHTTPPort, DefaultGRPCPort),
		DatabaseURL:       l.Required("DATABASE_URL"),
		BcryptCost:        l.Int("BCRYPT_COST", 10),
		SeedAPIKey:        l.String("SEED_API_KEY", ""),
		SeedAdminEmail:    l.String("SEED_ADMIN_EMAIL", ""),
		SeedAdminPassword: l.String("SEED_ADMIN_PASSWORD", ""),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
