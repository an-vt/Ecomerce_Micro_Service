package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/payment-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func TestLoad(t *testing.T) {
	l := func(m map[string]string) *platform.Loader {
		return platform.NewLoaderFrom(platform.Map(m), func(string) ([]byte, error) { return nil, assert.AnError })
	}
	c, err := config.Load(l(map[string]string{"DATABASE_URL": "postgres://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8107, c.HTTPPort)
	assert.Equal(t, 3*time.Second, c.ProviderTimeout)
	assert.Equal(t, "VND", c.Currency)
	assert.True(t, c.AutoMigrate)
	c, err = config.Load(l(map[string]string{"DATABASE_URL": "p", "PAYMENT_PROVIDER_TIMEOUT_MS": "500", "PAYMENT_CURRENCY": "USD", "AUTO_MIGRATE": "false"}))
	require.NoError(t, err)
	assert.Equal(t, 500*time.Millisecond, c.ProviderTimeout)
	assert.Equal(t, "USD", c.Currency)
	assert.False(t, c.AutoMigrate)
	_, err = config.Load(l(nil))
	assert.ErrorContains(t, err, "DATABASE_URL")
	_, err = config.Load(l(map[string]string{"DATABASE_URL": "p", "PAYMENT_CURRENCY": "DONG"}))
	assert.ErrorContains(t, err, "PAYMENT_CURRENCY")
}
