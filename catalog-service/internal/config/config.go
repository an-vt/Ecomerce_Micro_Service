// Package config loads catalog-service settings from the environment (docs/11-infrastructure.md §3.2).
package config

import (
	"fmt"
	"strings"
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

	// Product images (optional, P13). Uploads go straight to an S3-compatible store through presigned URLs; this
	// service only signs. The feature is on when the four required settings are all present.
	MediaEndpoint  string // the address CLIENTS use to reach the store: the signature covers the host
	MediaBucket    string
	MediaRegion    string
	MediaAccessKey string
	MediaSecretKey string // MEDIA_SECRET_KEY or MEDIA_SECRET_KEY_FILE
	MediaPublicURL string // where images are served from (a CDN); default: the object's own URL
	MediaPathStyle bool
	MediaURLTTL    time.Duration
	MediaMaxBytes  int64
}

// MediaEnabled reports whether image uploads are configured.
func (c Config) MediaEnabled() bool { return c.MediaEndpoint != "" }

// Load reads and validates the configuration.
func Load(l *platform.Loader) (Config, error) {
	c := Config{
		Base:        platform.LoadBase(l, "catalog-service", DefaultHTTPPort, DefaultGRPCPort),
		MongoURI:    l.Required("MONGO_URI"),
		MongoDB:     l.String("MONGO_DB", "catalog_db"),
		SkuCacheTTL: time.Duration(l.Int("SKU_CACHE_TTL_SEC", 30)) * time.Second,
		AutoMigrate: l.Bool("AUTO_MIGRATE", true),

		MediaEndpoint:  l.String("MEDIA_ENDPOINT", ""),
		MediaBucket:    l.String("MEDIA_BUCKET", ""),
		MediaRegion:    l.String("MEDIA_REGION", "us-east-1"),
		MediaAccessKey: l.String("MEDIA_ACCESS_KEY", ""),
		MediaSecretKey: l.String("MEDIA_SECRET_KEY", ""),
		MediaPublicURL: l.String("MEDIA_PUBLIC_BASE_URL", ""),
		MediaPathStyle: l.Bool("MEDIA_PATH_STYLE", true),
		MediaURLTTL:    time.Duration(l.Int("MEDIA_URL_TTL_SEC", 300)) * time.Second,
		MediaMaxBytes:  int64(l.Int("MEDIA_MAX_BYTES", 5<<20)),
	}
	if err := l.Err(); err != nil {
		return Config{}, err
	}
	if err := c.validateMedia(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// validateMedia refuses a half-configured store: a missing key would otherwise surface as a signing error on the first
// upload, in production, instead of at start-up.
func (c Config) validateMedia() error {
	set := map[string]bool{"MEDIA_ENDPOINT": c.MediaEndpoint != "", "MEDIA_BUCKET": c.MediaBucket != "", "MEDIA_ACCESS_KEY": c.MediaAccessKey != "", "MEDIA_SECRET_KEY": c.MediaSecretKey != ""}
	var missing []string
	for _, k := range []string{"MEDIA_ENDPOINT", "MEDIA_BUCKET", "MEDIA_ACCESS_KEY", "MEDIA_SECRET_KEY"} {
		if !set[k] {
			missing = append(missing, k)
		}
	}
	if len(missing) == len(set) {
		return nil // not configured: uploads are off
	}
	if len(missing) > 0 {
		return fmt.Errorf("incomplete media configuration, missing %s", strings.Join(missing, ", "))
	}
	if c.MediaURLTTL < 30*time.Second || c.MediaURLTTL > time.Hour {
		return fmt.Errorf("MEDIA_URL_TTL_SEC must be between 30 and 3600")
	}
	if c.MediaMaxBytes <= 0 {
		return fmt.Errorf("MEDIA_MAX_BYTES must be positive")
	}
	return nil
}
