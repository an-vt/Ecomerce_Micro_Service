package principal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/an-vt/ecom/platform-go/principal"
)

func TestContextRoundTrip(t *testing.T) {
	_, ok := principal.From(context.Background())
	assert.False(t, ok)
	_, ok = principal.From(principal.With(context.Background(), principal.Principal{}))
	assert.False(t, ok, "an empty id is not an authenticated caller")

	p := principal.Principal{ID: "u1", Type: principal.TypeUser, Roles: []string{"user"}, SessionID: "s1"}
	got, ok := principal.From(principal.With(context.Background(), p))
	assert.True(t, ok)
	assert.Equal(t, p, got)
	assert.True(t, got.IsUser())
	assert.False(t, got.IsShop())
	assert.True(t, got.HasRole("user"))
	assert.False(t, got.HasRole("admin"))
	assert.True(t, principal.Principal{Type: principal.TypeShop}.IsShop())
}
