package config_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/cart-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func TestLoad(t *testing.T) {
	l := func(m map[string]string) *platform.Loader {
		return platform.NewLoaderFrom(platform.Map(m), func(string) ([]byte, error) { return nil, assert.AnError })
	}
	c, err := config.Load(l(map[string]string{"MONGO_URI": "mongodb://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8104, c.HTTPPort)
	assert.Equal(t, 9104, c.GRPCPort)
	assert.Equal(t, "cart_db", c.MongoDB)
	assert.Equal(t, "ecom-catalog:9102", c.CatalogAddr)
	assert.Equal(t, "ecom-inventory:9103", c.InventoryAddr)
	c, err = config.Load(l(map[string]string{"MONGO_URI": "m", "CATALOG_GRPC_ADDR": "localhost:1", "AUTO_MIGRATE": "false"}))
	require.NoError(t, err)
	assert.Equal(t, "localhost:1", c.CatalogAddr)
	assert.False(t, c.AutoMigrate)
	_, err = config.Load(l(nil))
	assert.ErrorContains(t, err, "MONGO_URI")
}
