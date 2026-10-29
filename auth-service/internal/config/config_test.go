package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/auth-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func loader(env map[string]string, files map[string]string) *platform.Loader {
	return platform.NewLoaderFrom(platform.Map(env), func(p string) ([]byte, error) {
		if c, ok := files[p]; ok {
			return []byte(c), nil
		}
		return nil, assert.AnError
	})
}

func TestLoadDefaults(t *testing.T) {
	c, err := config.Load(loader(map[string]string{
		"DATABASE_URL": "postgres://x", "AUTH_JWT_PRIVATE_KEY_FILE": "/run/secrets/k",
	}, map[string]string{"/run/secrets/k": "PEM\n"}))
	require.NoError(t, err)
	assert.Equal(t, "auth-service", c.ServiceName)
	assert.Equal(t, 8101, c.HTTPPort)
	assert.Equal(t, 9101, c.GRPCPort)
	assert.Equal(t, "PEM", c.JWTPrivateKeyPEM, "the key is read from the *_FILE secret")
	assert.Equal(t, 15*time.Minute, c.AccessTTL)
	assert.Equal(t, 7*24*time.Hour, c.RefreshTTL)
	assert.Equal(t, 5*time.Minute, c.OTPTTL)
	assert.Equal(t, "ecom-auth", c.JWTIssuer)
	assert.Equal(t, "ecom", c.JWTAudience)
	assert.Equal(t, 10, c.BcryptCost)
	assert.True(t, c.AutoMigrate)
}

func TestLoadOverrides(t *testing.T) {
	c, err := config.Load(loader(map[string]string{
		"DATABASE_URL": "postgres://x", "AUTH_JWT_PRIVATE_KEY": "inline-pem", "ACCESS_TOKEN_TTL_SEC": "60", "AUTO_MIGRATE": "false",
		"VERIFY_LINK_BASE": "https://shop.example/verify", "SEED_API_KEY": "dev-key", "SEED_ADMIN_EMAIL": "root@example.com", "SEED_ADMIN_PASSWORD": "pw-pw-pw-pw",
	}, nil))
	require.NoError(t, err)
	assert.Equal(t, time.Minute, c.AccessTTL)
	assert.False(t, c.AutoMigrate)
	assert.Equal(t, "https://shop.example/verify", c.VerifyLinkBase)
	assert.Equal(t, "dev-key", c.SeedAPIKey)
	assert.Equal(t, "root@example.com", c.SeedAdminEmail)
}

func TestLoadReportsEveryMissingSetting(t *testing.T) {
	_, err := config.Load(loader(nil, nil))
	require.Error(t, err)
	assert.ErrorContains(t, err, "DATABASE_URL")
	assert.ErrorContains(t, err, "AUTH_JWT_PRIVATE_KEY")
}

func TestLoadConsoleNeedsNoSigningKey(t *testing.T) {
	c, err := config.LoadConsole(loader(map[string]string{"DATABASE_URL": "postgres://x", "APP_ENV": "production", "SEED_API_KEY": "k"}, nil))
	require.NoError(t, err)
	assert.True(t, c.IsProduction())
	assert.Equal(t, "k", c.SeedAPIKey)

	_, err = config.LoadConsole(loader(nil, nil))
	assert.ErrorContains(t, err, "DATABASE_URL")
}
