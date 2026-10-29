// Package config loads api-gateway settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"time"

	platform "github.com/an-vt/ecom/platform-go/config"
)

// DefaultHTTPPort is the public REST port.
const DefaultHTTPPort = 8088

// Upstream names the gRPC services the gateway talks to.
type Upstream struct {
	Name string // breaker / metrics label
	Addr string
}

// Config of api-gateway.
type Config struct {
	platform.Base

	JWTIssuer   string
	JWTAudience string

	AuthAddr         string
	CatalogAddr      string
	InventoryAddr    string
	CartAddr         string
	DiscountAddr     string
	OrderAddr        string
	NotificationAddr string

	CORSOrigins       []string
	CookieSecure      bool
	TrustedProxies    []string
	RateLimitPerMin   int // public routes, per IP
	AuthRatePerMin    int // login / registration routes, per IP
	BodyLimitBytes    int64
	APIKeyCacheTTL    time.Duration
	APIKeyNegativeTTL time.Duration
	GrantsCacheTTL    time.Duration
	RefreshCookieTTL  time.Duration
}

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:        platform.LoadBase(l, "api-gateway", DefaultHTTPPort, 0),
		JWTIssuer:   l.String("AUTH_JWT_ISSUER", "ecom-auth"),
		JWTAudience: l.String("AUTH_JWT_AUDIENCE", "ecom"),

		AuthAddr:         l.String("AUTH_GRPC_ADDR", "ecom-auth:9101"),
		CatalogAddr:      l.String("CATALOG_GRPC_ADDR", "ecom-catalog:9102"),
		InventoryAddr:    l.String("INVENTORY_GRPC_ADDR", "ecom-inventory:9103"),
		CartAddr:         l.String("CART_GRPC_ADDR", "ecom-cart:9104"),
		DiscountAddr:     l.String("DISCOUNT_GRPC_ADDR", "ecom-discount:9105"),
		OrderAddr:        l.String("ORDER_GRPC_ADDR", "ecom-order:9106"),
		NotificationAddr: l.String("NOTIFICATION_GRPC_ADDR", "ecom-notification:9108"),

		CORSOrigins:       l.Strings("CORS_ORIGINS", nil),
		CookieSecure:      l.Bool("COOKIE_SECURE", false),
		TrustedProxies:    l.Strings("TRUSTED_PROXIES", nil),
		RateLimitPerMin:   l.Int("RATE_LIMIT_PER_MIN", 100),
		AuthRatePerMin:    l.Int("AUTH_RATE_LIMIT_PER_MIN", 10),
		BodyLimitBytes:    int64(l.Int("BODY_LIMIT_BYTES", 100*1024)),
		APIKeyCacheTTL:    time.Duration(l.Int("APIKEY_CACHE_TTL_SEC", 60)) * time.Second,
		APIKeyNegativeTTL: time.Duration(l.Int("APIKEY_NEGATIVE_TTL_SEC", 10)) * time.Second,
		GrantsCacheTTL:    time.Duration(l.Int("GRANTS_CACHE_TTL_SEC", 60)) * time.Second,
		RefreshCookieTTL:  time.Duration(l.Int("REFRESH_TOKEN_TTL_SEC", 604800)) * time.Second,
	}
	if c.RateLimitPerMin < 1 || c.AuthRatePerMin < 1 {
		l.Required("RATE_LIMIT_PER_MIN must be >= 1 (and AUTH_RATE_LIMIT_PER_MIN)")
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	return c, nil
}
