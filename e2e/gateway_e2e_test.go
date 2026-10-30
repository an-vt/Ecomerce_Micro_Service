//go:build e2e

// End-to-end test of the public REST API: the real gateway container in front of the real auth-service, backed by
// PostgreSQL, Redis and Kafka. Start the stack (make start, or `make -C <svc>/infra/local up` for api-gateway and
// auth-service) and seed it (make seed) first.
//
//	GATEWAY_URL  default http://127.0.0.1:8088
//	E2E_REQUIRED=1 fails instead of skipping when the gateway is down
package e2e_test

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/an-vt/ecom/platform-go/e2ekit"
	"github.com/an-vt/ecom/platform-go/event"
)

func TestHealthAndDocsNeedNoAPIKey(t *testing.T) {
	c := e2ekit.NewClient(t)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/health/live", NoAPIKey: true}).Status)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/health/ready", NoAPIKey: true}).Status)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/docs/openapi.yaml", NoAPIKey: true}).Status)
	m := c.Do(t, e2ekit.Req{Method: "GET", Path: "/metrics", NoAPIKey: true})
	assert.True(t, strings.Contains(string(m.Body), "http_server_duration_seconds"), "REST metrics are exposed")
}

func TestPublicRoutesNeedAValidAPIKey(t *testing.T) {
	c := e2ekit.NewClient(t)
	r := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/login", NoAPIKey: true, Body: map[string]string{"email": "a@b.co", "password": "x"}})
	assert.Equal(t, 401, r.Status)
	assert.Equal(t, "API_KEY_REQUIRED", r.Code())

	r = c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/login", NoAPIKey: true, Headers: map[string]string{"x-api-key": "wrong"}, Body: map[string]string{"email": "a@b.co", "password": "x"}})
	assert.Equal(t, 401, r.Status)
	assert.Equal(t, "API_KEY_INVALID", r.Code())
}

func TestShopSessionLifecycleThroughTheGateway(t *testing.T) {
	c := e2ekit.NewClient(t)
	email := e2ekit.UniqueEmail("shop")
	body := map[string]string{"name": "E2E Shop", "email": email, "password": "correct-horse-battery"}

	signup := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/signup", Body: body})
	require.Equal(t, 201, signup.Status, string(signup.Body))
	assert.Equal(t, "Registered OK", signup.String("message"))
	assert.Equal(t, "SHOP", signup.String("metadata.account.type"))
	cookie := signup.Cookie("refreshToken")
	require.NotNil(t, cookie)
	assert.True(t, cookie.HttpOnly)
	assert.Equal(t, http.SameSiteStrictMode, cookie.SameSite)
	assert.NotEmpty(t, signup.Header.Get("x-request-id"))

	dup := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/signup", Body: body})
	assert.Equal(t, 409, dup.Status)
	assert.Equal(t, "EMAIL_TAKEN", dup.Code())

	bad := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/login", Body: map[string]string{"email": email, "password": "wrong-password"}})
	assert.Equal(t, 401, bad.Status)
	assert.Equal(t, "INVALID_CREDENTIALS", bad.Code())

	login := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/login", Body: map[string]string{"email": email, "password": "correct-horse-battery"}})
	require.Equal(t, 200, login.Status)
	access := login.String("metadata.tokens.accessToken")
	require.NotEmpty(t, access)

	// refresh via the cookie rotates it; replaying the old cookie is detected and kills the session
	first := login.Cookie("refreshToken")
	rotated := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/handleRefreshToken", Cookies: []*http.Cookie{first}})
	require.Equal(t, 200, rotated.Status, string(rotated.Body))
	newest := rotated.Cookie("refreshToken")
	require.NotNil(t, newest)
	assert.NotEqual(t, first.Value, newest.Value)

	replay := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/handleRefreshToken", Cookies: []*http.Cookie{first}})
	assert.Equal(t, 403, replay.Status)
	assert.Equal(t, "TOKEN_REUSED", replay.Code())
	dead := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/handleRefreshToken", Cookies: []*http.Cookie{newest}})
	assert.Equal(t, 401, dead.Status, "the whole session is gone, including the newest token")

	// the revoked session's access token stops working immediately (Redis denylist), not after 15 minutes
	revoked := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/logout", Token: rotated.String("metadata.tokens.accessToken")})
	assert.Equal(t, 401, revoked.Status)
	assert.Equal(t, "TOKEN_REVOKED", revoked.Code())

	// a different session of the same shop is unaffected, and logout ends it
	other := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/login", Body: map[string]string{"email": email, "password": "correct-horse-battery"}})
	tok := other.String("metadata.tokens.accessToken")
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/logout", Token: tok}).Status)
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/logout", Token: tok}).Status, "logged out")
}

func TestBuyerRegistrationThroughTheMailedLink(t *testing.T) {
	c := e2ekit.NewClient(t)
	email := e2ekit.UniqueEmail("buyer")
	require.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/new_user", Body: map[string]string{"email": email}}).Status)

	// notification-service will mail this event; here the test reads the link from Kafka
	ev := e2ekit.WaitForEvent(t, "auth.email-verification.requested", func(e event.Envelope) bool {
		var p struct{ Email string }
		return e.Decode(&p) == nil && p.Email == email
	})
	var p struct{ Link string }
	require.NoError(t, ev.Decode(&p))
	u, err := url.Parse(p.Link)
	require.NoError(t, err)
	assert.Equal(t, "/v1/api/user/welcome_back", u.Path)
	token := u.Query().Get("token")

	verified := c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/user/welcome_back?token=" + url.QueryEscape(token)})
	require.Equal(t, 200, verified.Status, string(verified.Body))
	assert.Equal(t, "USER", verified.String("metadata.account.type"))
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/user/welcome_back?token=" + url.QueryEscape(token)}).Status, "the link works once")

	access := verified.String("metadata.tokens.accessToken")
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/password", Token: access, Body: map[string]string{"password": "buyer-password-1"}}).Status)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/login", Body: map[string]string{"email": email, "password": "buyer-password-1"}}).Status)

	// shops cannot use buyer-only routes
	shop := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/signup", Body: map[string]string{"name": "S", "email": e2ekit.UniqueEmail("s"), "password": "correct-horse-battery"}})
	forbidden := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/password", Token: shop.String("metadata.tokens.accessToken"), Body: map[string]string{"password": "long-enough-pw"}})
	assert.Equal(t, 403, forbidden.Status)
}

func TestRbacAndProfileThroughTheGateway(t *testing.T) {
	c := e2ekit.NewClient(t)
	admin := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/user/login", Body: map[string]string{
		"email": e2ekit.Env("SEED_ADMIN_EMAIL", "admin@ecom.local"), "password": e2ekit.Env("SEED_ADMIN_PASSWORD", "admin-change-me-123")}})
	require.Equal(t, 200, admin.Status, "run `make seed` first: %s", string(admin.Body))
	adminTok := admin.String("metadata.tokens.accessToken")

	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/rbac/roles", Token: adminTok}).Status)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/rbac/resources", Token: adminTok}).Status)
	name := fmt.Sprintf("e2e%d", time.Now().UnixNano()%1_000_000_000) // low-order digits: unique per run
	created := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/rbac/role", Token: adminTok, Body: map[string]any{
		"name": name, "description": "e2e", "grants": []map[string]any{{"resource": "profile", "actions": []string{"readOwn"}}}}})
	assert.Equal(t, 201, created.Status, string(created.Body))
	again := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/rbac/role", Token: adminTok, Body: map[string]any{"name": name}})
	assert.Equal(t, 409, again.Status)
	assert.Equal(t, "ROLE_EXISTS", again.Code())

	// a shop is not an administrator, whatever it puts in the query string
	shop := c.Do(t, e2ekit.Req{Method: "POST", Path: "/v1/api/shop/signup", Body: map[string]string{"name": "S", "email": e2ekit.UniqueEmail("s"), "password": "correct-horse-battery"}})
	shopTok := shop.String("metadata.tokens.accessToken")
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/rbac/roles?role=admin", Token: shopTok}).Status)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/profile/viewOwn", Token: shopTok}).Status)
	assert.Equal(t, 403, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/profile/viewAny", Token: shopTok}).Status)
	assert.Equal(t, 200, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/profile/viewAny?accountId=" + shop.String("metadata.account.id"), Token: adminTok}).Status)

	// no token at all
	assert.Equal(t, 401, c.Do(t, e2ekit.Req{Method: "GET", Path: "/v1/api/rbac/roles"}).Status)
}
