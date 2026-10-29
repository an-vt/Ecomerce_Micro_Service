package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/inventory-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func TestLoad(t *testing.T) {
	l := func(m map[string]string) *platform.Loader {
		return platform.NewLoaderFrom(platform.Map(m), func(string) ([]byte, error) { return nil, assert.AnError })
	}
	c, err := config.Load(l(map[string]string{"DATABASE_URL": "postgres://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8103, c.HTTPPort)
	assert.Equal(t, 9103, c.GRPCPort)
	assert.Equal(t, 15*time.Minute, c.ReservationTTL)
	assert.Equal(t, 30*time.Second, c.ExpiryInterval)
	c, err = config.Load(l(map[string]string{"DATABASE_URL": "x", "RESERVATION_TTL_MS": "60000", "RESERVATION_SWEEP_MS": "1000"}))
	require.NoError(t, err)
	assert.Equal(t, time.Minute, c.ReservationTTL)
	assert.Equal(t, time.Second, c.ExpiryInterval)
	_, err = config.Load(l(nil))
	assert.ErrorContains(t, err, "DATABASE_URL")
}
