package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/discount-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func TestLoad(t *testing.T) {
	l := func(m map[string]string) *platform.Loader {
		return platform.NewLoaderFrom(platform.Map(m), func(string) ([]byte, error) { return nil, assert.AnError })
	}
	c, err := config.Load(l(map[string]string{"DATABASE_URL": "postgres://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8105, c.HTTPPort)
	assert.Equal(t, 9105, c.GRPCPort)
	assert.Equal(t, "ecom-catalog:9102", c.CatalogAddr)
	assert.True(t, c.AutoMigrate)
	c, err = config.Load(l(map[string]string{"DATABASE_URL": "p", "CATALOG_GRPC_ADDR": "localhost:1", "AUTO_MIGRATE": "false"}))
	require.NoError(t, err)
	assert.Equal(t, "localhost:1", c.CatalogAddr)
	assert.False(t, c.AutoMigrate)
	_, err = config.Load(l(nil))
	assert.ErrorContains(t, err, "DATABASE_URL")
}
