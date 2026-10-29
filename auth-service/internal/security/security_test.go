package security_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/auth-service/internal/domain"
	"github.com/an-vt/ecom/auth-service/internal/security"
)

func TestBcryptHashAndCompare(t *testing.T) {
	b, err := security.NewBcrypt(4)
	require.NoError(t, err)
	h, err := b.Hash("correct-horse")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(h, "$2"), "a bcrypt hash")
	assert.NotContains(t, h, "correct-horse")
	assert.NoError(t, b.Compare(h, "correct-horse"))
	assert.Error(t, b.Compare(h, "wrong"))

	h2, _ := b.Hash("correct-horse")
	assert.NotEqual(t, h, h2, "salted: equal passwords hash differently")
	assert.Error(t, b.Compare("not-a-hash", "x"))
}

func TestBcryptCostValidation(t *testing.T) {
	d, err := security.NewBcrypt(0)
	require.NoError(t, err)
	assert.Equal(t, 10, d.Cost, "default cost")
	for _, bad := range []int{-1, 3, 32} {
		_, err := security.NewBcrypt(bad)
		assert.Error(t, err, bad)
	}
	_, err = security.NewBcrypt(4)
	assert.NoError(t, err)
	_, err = security.NewBcrypt(31)
	assert.NoError(t, err)
}

func TestSecretsAreRandomUrlSafeAndHashed(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		secret, hash, err := security.Secrets{}.New()
		require.NoError(t, err)
		assert.Len(t, secret, 43, "32 bytes in base64url without padding")
		assert.Regexp(t, `^[A-Za-z0-9_-]+$`, secret)
		assert.Equal(t, domain.HashToken(secret), hash)
		assert.NotEqual(t, secret, hash)
		require.False(t, seen[secret], "secrets must not repeat")
		seen[secret] = true
	}
}
