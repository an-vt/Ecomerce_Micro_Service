package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/order-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func TestLoad(t *testing.T) {
	l := func(m map[string]string) *platform.Loader {
		return platform.NewLoaderFrom(platform.Map(m), func(string) ([]byte, error) { return nil, assert.AnError })
	}
	c, err := config.Load(l(map[string]string{"DATABASE_URL": "postgres://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8106, c.HTTPPort)
	assert.Equal(t, 9106, c.GRPCPort)
	assert.Equal(t, 30*time.Second, c.StepTimeout)
	assert.Equal(t, 30*time.Second, c.CompensationRetry)
	assert.Equal(t, 5*time.Second, c.SweepInterval)
	assert.Equal(t, "ecom-catalog:9102", c.CatalogAddr)
	assert.Equal(t, "ecom-discount:9105", c.DiscountAddr)
	assert.EqualValues(t, 0, c.FeeShip)
	assert.True(t, c.AutoMigrate)

	c, err = config.Load(l(map[string]string{"DATABASE_URL": "p", "SAGA_STEP_TIMEOUT_MS": "2000", "SAGA_COMPENSATION_RETRY_MS": "1000", "SAGA_SWEEP_INTERVAL_MS": "200",
		"FEE_SHIP": "15000", "CATALOG_GRPC_ADDR": "localhost:1", "AUTO_MIGRATE": "false"}))
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, c.StepTimeout)
	assert.Equal(t, time.Second, c.CompensationRetry)
	assert.Equal(t, 200*time.Millisecond, c.SweepInterval)
	assert.EqualValues(t, 15000, c.FeeShip)
	assert.Equal(t, "localhost:1", c.CatalogAddr)
	assert.False(t, c.AutoMigrate)
	_, err = config.Load(l(nil))
	assert.ErrorContains(t, err, "DATABASE_URL")
}
