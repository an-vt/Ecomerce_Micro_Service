package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/notification-service/internal/config"
	platform "github.com/an-vt/ecom/platform-go/config"
)

func TestLoad(t *testing.T) {
	l := func(m map[string]string) *platform.Loader {
		return platform.NewLoaderFrom(platform.Map(m), func(string) ([]byte, error) { return nil, assert.AnError })
	}
	c, err := config.Load(l(map[string]string{"MONGO_URI": "mongodb://x"}))
	require.NoError(t, err)
	assert.Equal(t, 8108, c.HTTPPort)
	assert.Equal(t, 9108, c.GRPCPort)
	assert.Equal(t, "notification_db", c.MongoDB)
	assert.Equal(t, "ecom-mailpit:1025", c.SMTPAddr())
	assert.Equal(t, 10*time.Second, c.SMTPTimeout)
	assert.Equal(t, "Ecom <no-reply@ecom.local>", c.MailFrom)
	assert.True(t, c.AutoMigrate)

	c, err = config.Load(l(map[string]string{"MONGO_URI": "m", "SMTP_HOST": "smtp.example.com", "SMTP_PORT": "587", "SMTP_USER": "u", "SMTP_PASSWORD": "p",
		"SMTP_TIMEOUT_MS": "500", "MAIL_FROM": "shop@example.com", "AUTO_MIGRATE": "false"}))
	require.NoError(t, err)
	assert.Equal(t, "smtp.example.com:587", c.SMTPAddr())
	assert.Equal(t, "u", c.SMTPUser)
	assert.Equal(t, "p", c.SMTPPass)
	assert.Equal(t, 500*time.Millisecond, c.SMTPTimeout)
	assert.False(t, c.AutoMigrate)

	_, err = config.Load(l(nil))
	assert.ErrorContains(t, err, "MONGO_URI")
	_, err = config.Load(l(map[string]string{"MONGO_URI": "m", "MAIL_FROM": "not an address"}))
	assert.ErrorContains(t, err, "MAIL_FROM")
}
