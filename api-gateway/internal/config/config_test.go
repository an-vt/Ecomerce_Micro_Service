package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/api-gateway/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func loader(env map[string]string) *platform.Loader {
	return platform.NewLoaderFrom(platform.Map(env), func(string) ([]byte, error) { return nil, assert.AnError })
}

func TestDefaults(t *testing.T) {
	c, err := config.Load(loader(nil))
	require.NoError(t, err)
	assert.Equal(t, 8088, c.HTTPPort)
	assert.Equal(t, "ecom-auth:9101", c.AuthAddr)
	assert.Equal(t, "ecom-notification:9108", c.NotificationAddr)
	assert.Equal(t, 100, c.RateLimitPerMin)
	assert.Equal(t, 10, c.AuthRatePerMin)
	assert.Equal(t, int64(100*1024), c.BodyLimitBytes)
	assert.Equal(t, time.Minute, c.APIKeyCacheTTL)
	assert.Equal(t, 10*time.Second, c.APIKeyNegativeTTL)
	assert.False(t, c.CookieSecure)
	assert.Empty(t, c.CORSOrigins)
	assert.Equal(t, "ecom-auth", c.JWTIssuer)
}

func TestOverrides(t *testing.T) {
	c, err := config.Load(loader(map[string]string{
		"CORS_ORIGINS": "http://localhost:5173, https://shop.example", "COOKIE_SECURE": "true", "RATE_LIMIT_PER_MIN": "5",
		"AUTH_GRPC_ADDR": "localhost:9101", "TRUSTED_PROXIES": "10.0.0.0/8",
	}))
	require.NoError(t, err)
	assert.Equal(t, []string{"http://localhost:5173", "https://shop.example"}, c.CORSOrigins)
	assert.True(t, c.CookieSecure)
	assert.Equal(t, 5, c.RateLimitPerMin)
	assert.Equal(t, "localhost:9101", c.AuthAddr)
	assert.Equal(t, []string{"10.0.0.0/8"}, c.TrustedProxies)
}

func TestRejectsNonsenseLimits(t *testing.T) {
	_, err := config.Load(loader(map[string]string{"RATE_LIMIT_PER_MIN": "0"}))
	assert.ErrorContains(t, err, "RATE_LIMIT_PER_MIN")
	_, err = config.Load(loader(map[string]string{"RATE_LIMIT_PER_MIN": "abc"}))
	assert.Error(t, err)
}
