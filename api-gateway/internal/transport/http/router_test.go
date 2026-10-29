package http_test

import (
	"errors"
	stdhttp "net/http"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/an-vt/ecom/api-gateway/api/openapi"
	"github.com/an-vt/ecom/platform-go/errs"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

// ---- API key ---------------------------------------------------------------------------------------------------

func TestAPIKeyIsRequiredAndChecked(t *testing.T) {
	g := newGateway(t)

	r := g.do(req{method: "POST", path: "/v1/api/shop/login", body: `{"email":"a@b.co","password":"x"}`})
	assert.Equal(t, 401, r.Code)
	assert.Equal(t, "API_KEY_REQUIRED", r.errCode())

	r = g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`, "x-api-key", "bad-key")
	assert.Equal(t, 401, r.Code)
	assert.Equal(t, "API_KEY_INVALID", r.errCode())

	assert.Equal(t, 200, g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`).Code)
}

func TestAPIKeyWithoutThePublicPermissionIsForbidden(t *testing.T) {
	g := newGateway(t)
	g.auth.validate = func(*authv1.ValidateApiKeyRequest) (*authv1.ValidateApiKeyResponse, error) {
		return &authv1.ValidateApiKeyResponse{Valid: true, Permissions: []string{"1111"}}, nil
	}
	r := g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`)
	assert.Equal(t, 403, r.Code)
	assert.Equal(t, "FORBIDDEN", r.errCode())
}

func TestAPIKeyResultsAreCachedByHashAndBadKeysTooBriefly(t *testing.T) {
	g := newGateway(t)
	for i := 0; i < 5; i++ {
		require.Equal(t, 200, g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`).Code)
	}
	assert.Equal(t, 1, g.auth.count("ValidateApiKey"), "five requests, one lookup")
	for _, k := range g.mr.Keys() {
		assert.NotContains(t, k, "good-key", "the raw key is never used as a cache key")
	}

	for i := 0; i < 5; i++ {
		g.call("POST", "/v1/api/shop/login", `{}`, "x-api-key", "bad-key")
	}
	assert.Equal(t, 2, g.auth.count("ValidateApiKey"), "unknown keys are negatively cached")
	g.mr.FastForward(11 * time.Second)
	g.call("POST", "/v1/api/shop/login", `{}`, "x-api-key", "bad-key")
	assert.Equal(t, 3, g.auth.count("ValidateApiKey"), "...but only for a few seconds")
}

func TestAPIKeyValidationFallsBackWhenRedisIsDown(t *testing.T) {
	g := newGateway(t)
	g.mr.Close()
	r := g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`)
	assert.Equal(t, 200, r.Code, "a cache outage must not take the API down")
}

// ---- JWT -------------------------------------------------------------------------------------------------------

func TestProtectedRoutesNeedAValidBearerToken(t *testing.T) {
	g := newGateway(t)
	for name, hdr := range map[string]string{"none": "", "no scheme": "abc", "empty bearer": "Bearer ", "garbage": "Bearer not.a.jwt"} {
		var r resp
		if hdr == "" {
			r = g.call("POST", "/v1/api/shop/logout", "")
		} else {
			r = g.call("POST", "/v1/api/shop/logout", "", "Authorization", hdr)
		}
		assert.Equal(t, 401, r.Code, name)
	}
	assert.Equal(t, "TOKEN_REQUIRED", g.call("POST", "/v1/api/shop/logout", "").errCode())
	assert.Equal(t, "TOKEN_INVALID", g.call("POST", "/v1/api/shop/logout", "", "Authorization", "Bearer not.a.jwt").errCode())

	assert.Equal(t, 200, g.as(g.token("shop-1", "SHOP", "shop"), "POST", "/v1/api/shop/logout", "").Code)
}

func TestExpiredTokenIsReportedAsExpired(t *testing.T) {
	g := newGateway(t)
	tok := g.token("shop-1", "SHOP", "shop")
	g.clk.Advance(16 * time.Minute)
	r := g.as(tok, "POST", "/v1/api/shop/logout", "")
	assert.Equal(t, 401, r.Code)
	assert.Equal(t, "TOKEN_EXPIRED", r.errCode())
}

func TestTokenOfARevokedSessionIsRefused(t *testing.T) {
	g := newGateway(t)
	tok := g.token("shop-1", "SHOP", "shop")
	require.Equal(t, 200, g.as(tok, "POST", "/v1/api/shop/logout", "").Code)

	require.NoError(t, g.mr.Set("auth:revoked:sid-shop-1", "1"))
	r := g.as(tok, "POST", "/v1/api/shop/logout", "")
	assert.Equal(t, 401, r.Code)
	assert.Equal(t, "TOKEN_REVOKED", r.errCode())
}

func TestRevocationCheckFailsClosedWhenRedisIsDown(t *testing.T) {
	g := newGateway(t)
	tok := g.token("shop-1", "SHOP", "shop")
	g.mr.Close()
	// the API key is served from the (lost) cache path, so use a request that gets past it via fallback
	r := g.as(tok, "POST", "/v1/api/shop/logout", "")
	assert.Equal(t, 503, r.Code, "if revocation cannot be checked, the request is refused")
	assert.Equal(t, 0, g.auth.count("Logout"))
}

func TestIdentityAndRequestIDReachTheService(t *testing.T) {
	g := newGateway(t)
	tok := g.token("shop-9", "SHOP", "shop", "writer")
	r := g.call("POST", "/v1/api/shop/logout", "", "Authorization", "Bearer "+tok, "x-request-id", "req-abc-123")
	require.Equal(t, 200, r.Code)
	assert.Equal(t, "req-abc-123", r.Header().Get("x-request-id"), "an incoming request id is kept and echoed")
	assert.Equal(t, "req-abc-123", g.auth.md("x-request-id"))
	assert.Equal(t, "shop-9", g.auth.md("x-account-id"))
	assert.Equal(t, "SHOP", g.auth.md("x-account-type"))
	assert.Equal(t, "shop,writer", g.auth.md("x-account-roles"))
	assert.Equal(t, "sid-shop-9", g.auth.md("x-session-id"))

	r = g.call("GET", "/v1/api/shop/nothing", "")
	assert.Len(t, r.Header().Get("x-request-id"), 26, "a ULID is generated when none is sent")
	r = g.call("GET", "/v1/api/shop/nothing", "", "x-request-id", "bad id with spaces")
	assert.NotEqual(t, "bad id with spaces", r.Header().Get("x-request-id"), "malformed ids are replaced")
}

// ---- RBAC ------------------------------------------------------------------------------------------------------

func TestRbacRoutesNeedTheAdminRole(t *testing.T) {
	g := newGateway(t)
	body := `{"name":"editor","grants":[{"resource":"profile","actions":["readOwn"]}]}`

	r := g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/rbac/role", body)
	assert.Equal(t, 403, r.Code)
	assert.Equal(t, "FORBIDDEN", r.errCode())
	assert.Equal(t, 403, g.as(g.token("s1", "SHOP", "shop"), "GET", "/v1/api/rbac/roles", "").Code)
	assert.Equal(t, 403, g.as(g.token("n1", "USER"), "GET", "/v1/api/rbac/roles", "").Code, "no roles at all")

	admin := g.token("a1", "USER", "user", "admin")
	r = g.as(admin, "POST", "/v1/api/rbac/role", body)
	require.Equal(t, 201, r.Code, r.Body.String())
	assert.Equal(t, "editor", r.json()["metadata"].(map[string]any)["name"])
	assert.Equal(t, 200, g.as(admin, "GET", "/v1/api/rbac/roles", "").Code)
	assert.Equal(t, 201, g.as(admin, "POST", "/v1/api/rbac/resource", `{"name":"product"}`).Code)
	assert.Equal(t, 200, g.as(admin, "GET", "/v1/api/rbac/resources", "").Code)
}

func TestRolesComeFromTheTokenNotTheQueryString(t *testing.T) {
	g := newGateway(t)
	// the monolith trusted ?role=admin
	r := g.as(g.token("u1", "USER", "user"), "GET", "/v1/api/rbac/roles?role=admin", "")
	assert.Equal(t, 403, r.Code)
}

func TestProfileRoutesDistinguishOwnFromAny(t *testing.T) {
	g := newGateway(t)
	user := g.token("u1", "USER", "user")
	assert.Equal(t, 200, g.as(user, "GET", "/v1/api/profile/viewOwn", "").Code)
	assert.Equal(t, 403, g.as(user, "GET", "/v1/api/profile/viewAny", "").Code, "readOwn does not grant viewAny")

	var asked string
	g.auth.getAccount = func(in *authv1.GetAccountRequest) (*authv1.Account, error) {
		asked = in.GetAccountId()
		return &authv1.Account{Id: "x", Type: authv1.AccountType_ACCOUNT_TYPE_USER}, nil
	}
	_ = g.as(user, "GET", "/v1/api/profile/viewOwn?accountId=someone-else", "")
	assert.Equal(t, "", asked, "with the own possession the accountId parameter is ignored")

	admin := g.token("a1", "USER", "admin")
	require.Equal(t, 200, g.as(admin, "GET", "/v1/api/profile/viewAny?accountId=someone-else", "").Code)
	assert.Equal(t, "someone-else", asked, "admins may pick an account")
}

func TestGrantsAreCachedAndStaleGrantsSurviveAnOutage(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.grantsTTL = time.Minute })
	admin := g.token("a1", "USER", "admin")
	calls := 0
	inner := g.rbac.grants
	g.rbac.grants = func() ([]*authv1.RoleGrant, error) { calls++; return inner() }

	for i := 0; i < 4; i++ {
		require.Equal(t, 200, g.as(admin, "GET", "/v1/api/rbac/roles", "").Code)
	}
	assert.Equal(t, 1, calls, "grants are cached")

	g.rbac.grants = func() ([]*authv1.RoleGrant, error) { return nil, errs.Unavailable("X", "auth down") }
	g.clk.Advance(2 * time.Minute)
	assert.Equal(t, 200, g.as(admin, "GET", "/v1/api/rbac/roles", "").Code, "auth being down does not lock admins out")
}

func TestGrantsUnavailableAtStartFailsClosed(t *testing.T) {
	g := newGateway(t)
	g.rbac.grants = func() ([]*authv1.RoleGrant, error) { return nil, errs.Unavailable("X", "auth down") }
	r := g.as(g.token("a1", "USER", "admin"), "GET", "/v1/api/rbac/roles", "")
	assert.GreaterOrEqual(t, r.Code, 500)
}

// ---- account type guards ---------------------------------------------------------------------------------------

func TestSetPasswordIsForBuyersOnly(t *testing.T) {
	g := newGateway(t)
	assert.Equal(t, 403, g.as(g.token("s1", "SHOP", "shop"), "POST", "/v1/api/user/password", `{"password":"long-enough-1"}`).Code)
	assert.Equal(t, 200, g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/user/password", `{"password":"long-enough-1"}`).Code)
	assert.Equal(t, 400, g.as(g.token("u1", "USER", "user"), "POST", "/v1/api/user/password", `{}`).Code)
}

// ---- auth handlers ---------------------------------------------------------------------------------------------

func TestSignUpReturnsTheMonolithEnvelopeAndSetsAHardenedCookie(t *testing.T) {
	g := newGateway(t)
	r := g.call("POST", "/v1/api/shop/signup", `{"name":"Acme","email":"o@acme.com","password":"correct-horse"}`)
	require.Equal(t, 201, r.Code, r.Body.String())
	body := r.json()
	assert.Equal(t, "Registered OK", body["message"])
	assert.EqualValues(t, 201, body["status"])
	meta := body["metadata"].(map[string]any)
	assert.Equal(t, "SHOP", meta["account"].(map[string]any)["type"])
	assert.Equal(t, "access-shop-1", meta["tokens"].(map[string]any)["accessToken"])
	assert.EqualValues(t, 900, meta["tokens"].(map[string]any)["expiresIn"])

	c := r.cookie("refreshToken")
	require.NotNil(t, c)
	assert.Equal(t, "refresh-shop-1", c.Value)
	assert.True(t, c.HttpOnly)
	assert.Equal(t, stdhttp.SameSiteStrictMode, c.SameSite)
	assert.Equal(t, "/v1/api", c.Path)
	assert.Equal(t, 7*24*3600, c.MaxAge, "7 days; the monolith's cookie lived 43 minutes by mistake")
}

func TestInputValidation(t *testing.T) {
	g := newGateway(t)
	for name, tc := range map[string]struct{ path, body string }{
		"signup missing field":  {"/v1/api/shop/signup", `{"name":"x"}`},
		"signup unknown field":  {"/v1/api/shop/signup", `{"name":"x","email":"a@b.co","password":"p","isAdmin":true}`},
		"signup not json":       {"/v1/api/shop/signup", `nope`},
		"login missing":         {"/v1/api/user/login", `{"email":"a@b.co"}`},
		"new_user missing":      {"/v1/api/user/new_user", `{}`},
		"shop login empty body": {"/v1/api/shop/login", ``},
	} {
		r := g.call("POST", tc.path, tc.body)
		assert.Equal(t, 400, r.Code, name)
		assert.Equal(t, "VALIDATION_FAILED", r.errCode(), name)
	}
	assert.Equal(t, 0, g.auth.count("SignUpShop")+g.auth.count("Login"), "invalid requests never reach the service")
	assert.Equal(t, 400, g.call("GET", "/v1/api/user/welcome_back", "").Code, "token query parameter is required")
}

func TestServiceErrorsKeepTheirCodeAndMapToHTTPStatus(t *testing.T) {
	g := newGateway(t)
	g.auth.signUp = func(*authv1.SignUpShopRequest) (*authv1.AuthResponse, error) {
		return nil, errs.AlreadyExists("EMAIL_TAKEN", "an account with this email already exists")
	}
	r := g.call("POST", "/v1/api/shop/signup", `{"name":"x","email":"a@b.co","password":"correct-horse"}`)
	assert.Equal(t, 409, r.Code)
	assert.Equal(t, "EMAIL_TAKEN", r.errCode())
	assert.Equal(t, "an account with this email already exists", r.json()["message"])
	assert.EqualValues(t, 409, r.json()["code"])
	assert.Equal(t, "error", r.json()["status"])

	g.auth.login = func(*authv1.LoginRequest) (*authv1.AuthResponse, error) {
		return nil, errs.Unauthenticated("INVALID_CREDENTIALS", "no")
	}
	assert.Equal(t, 401, g.call("POST", "/v1/api/user/login", `{"email":"a@b.co","password":"x"}`).Code)
	g.auth.login = func(*authv1.LoginRequest) (*authv1.AuthResponse, error) {
		return nil, errors.New("sql: connection refused to 10.0.0.5")
	}
	r = g.call("POST", "/v1/api/user/login", `{"email":"a@b.co","password":"x"}`)
	assert.Equal(t, 500, r.Code)
	assert.NotContains(t, r.Body.String(), "10.0.0.5", "internal details never reach the client")
}

func TestLoginTypeFollowsTheRoute(t *testing.T) {
	g := newGateway(t)
	var got authv1.AccountType
	g.auth.login = func(in *authv1.LoginRequest) (*authv1.AuthResponse, error) {
		got = in.GetType()
		return authResponse("x", in.GetType()), nil
	}
	g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`)
	assert.Equal(t, authv1.AccountType_ACCOUNT_TYPE_SHOP, got)
	g.call("POST", "/v1/api/user/login", `{"email":"a@b.co","password":"x"}`)
	assert.Equal(t, authv1.AccountType_ACCOUNT_TYPE_USER, got)
}

func TestRefreshAcceptsCookieOrBody(t *testing.T) {
	g := newGateway(t)
	var seen string
	g.auth.refresh = func(in *authv1.RefreshRequest) (*authv1.AuthResponse, error) {
		seen = in.GetRefreshToken()
		return authResponse("shop-1", authv1.AccountType_ACCOUNT_TYPE_SHOP), nil
	}
	r := g.do(req{method: "POST", path: "/v1/api/shop/handleRefreshToken", headers: map[string]string{"x-api-key": "good-key"},
		cookies: []*stdhttp.Cookie{{Name: "refreshToken", Value: "from-cookie"}}})
	require.Equal(t, 200, r.Code)
	assert.Equal(t, "from-cookie", seen)
	assert.Equal(t, "refresh-shop-1", r.cookie("refreshToken").Value, "the cookie is rotated")

	r = g.call("POST", "/v1/api/user/handleRefreshToken", `{"refreshToken":"from-body"}`)
	require.Equal(t, 200, r.Code)
	assert.Equal(t, "from-body", seen)

	r = g.call("POST", "/v1/api/shop/handleRefreshToken", ``)
	assert.Equal(t, 401, r.Code)
	assert.Equal(t, "TOKEN_INVALID", r.errCode())
}

func TestReusedRefreshTokenClearsTheCookieAndReturns403(t *testing.T) {
	g := newGateway(t)
	g.auth.refresh = func(*authv1.RefreshRequest) (*authv1.AuthResponse, error) {
		return nil, errs.PermissionDenied("TOKEN_REUSED", "this refresh token was already used")
	}
	r := g.do(req{method: "POST", path: "/v1/api/shop/handleRefreshToken", headers: map[string]string{"x-api-key": "good-key"},
		cookies: []*stdhttp.Cookie{{Name: "refreshToken", Value: "stolen"}}})
	assert.Equal(t, 403, r.Code)
	assert.Equal(t, "TOKEN_REUSED", r.errCode())
	c := r.cookie("refreshToken")
	require.NotNil(t, c)
	assert.Equal(t, "", c.Value)
	assert.Less(t, c.MaxAge, 0, "the browser is told to drop it")
}

func TestLogoutClearsTheCookie(t *testing.T) {
	g := newGateway(t)
	r := g.as(g.token("shop-1", "SHOP", "shop"), "POST", "/v1/api/shop/logout", "")
	require.Equal(t, 200, r.Code)
	c := r.cookie("refreshToken")
	require.NotNil(t, c)
	assert.Less(t, c.MaxAge, 0)
}

func TestBuyerRegistrationRoutes(t *testing.T) {
	g := newGateway(t)
	var mailed, token string
	g.auth.requestMail = func(in *authv1.RequestEmailVerificationRequest) error { mailed = in.GetEmail(); return nil }
	g.auth.verify = func(in *authv1.VerifyEmailRequest) (*authv1.AuthResponse, error) {
		token = in.GetToken()
		return authResponse("user-1", authv1.AccountType_ACCOUNT_TYPE_USER), nil
	}
	assert.Equal(t, 200, g.call("POST", "/v1/api/user/new_user", `{"email":"buyer@example.com"}`).Code)
	assert.Equal(t, "buyer@example.com", mailed)
	r := g.call("GET", "/v1/api/user/welcome_back?token=abc%2B123", "")
	require.Equal(t, 200, r.Code)
	assert.Equal(t, "abc+123", token, "the token is decoded exactly once")
	assert.NotNil(t, r.cookie("refreshToken"))

	g.auth.verify = func(*authv1.VerifyEmailRequest) (*authv1.AuthResponse, error) {
		return nil, errs.Unauthenticated("TOKEN_INVALID", "x")
	}
	assert.Equal(t, 401, g.call("GET", "/v1/api/user/welcome_back?token=used", "").Code)
}

// ---- resilience ------------------------------------------------------------------------------------------------

func TestUpstreamOutageOpensTheBreakerAndFailsFast(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.breakerMin = 4; o.breakerReset = 400 * time.Millisecond })
	var healthy bool
	g.auth.login = func(in *authv1.LoginRequest) (*authv1.AuthResponse, error) {
		if healthy {
			return authResponse("acc-1", in.GetType()), nil
		}
		return nil, errs.Unavailable("X", "auth is down")
	}
	login := func() resp { return g.call("POST", "/v1/api/user/login", `{"email":"a@b.co","password":"x"}`) }

	for i := 0; i < 4; i++ {
		assert.Equal(t, 503, login().Code)
	}
	callsBefore := g.auth.count("Login")

	start := time.Now()
	r := login()
	elapsed := time.Since(start)
	assert.Equal(t, 503, r.Code)
	assert.Equal(t, "UPSTREAM_UNAVAILABLE", r.errCode())
	assert.NotEmpty(t, r.Header().Get("Retry-After"), "clients are told when to come back")
	assert.Less(t, elapsed, 50*time.Millisecond, "an open circuit fails fast")
	assert.Equal(t, callsBefore, g.auth.count("Login"), "the service was not called")

	// other routes, whose upstream methods are healthy, keep working
	assert.Equal(t, 201, g.call("POST", "/v1/api/shop/signup", `{"name":"x","email":"a@b.co","password":"correct-horse"}`).Code)

	healthy = true
	require.Eventually(t, func() bool { return login().Code == 200 }, 3*time.Second, 50*time.Millisecond, "the circuit closes after the reset timeout")
	assert.Greater(t, testutil.ToFloat64(g.metrics.BreakerEvents.WithLabelValues("auth", "Login", "reject")), 0.0)
}

// ---- rate limiting -----------------------------------------------------------------------------------------------

func TestRateLimitPerIP(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.rateLimit = 5 })
	for i := 1; i <= 5; i++ {
		r := g.call("GET", "/v1/api/user/welcome_back?token=t", "")
		require.Equal(t, 200, r.Code, "request %d", i)
		assert.Equal(t, "5", r.Header().Get("X-RateLimit-Limit"))
	}
	r := g.call("GET", "/v1/api/user/welcome_back?token=t", "")
	assert.Equal(t, 429, r.Code)
	assert.Equal(t, "RATE_LIMITED", r.errCode())
	assert.NotEmpty(t, r.Header().Get("Retry-After"))
	assert.Equal(t, "0", r.Header().Get("X-RateLimit-Remaining"))
}

func TestLoginRoutesHaveAStricterLimit(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.authRateLimit = 3 })
	for i := 0; i < 3; i++ {
		require.Equal(t, 200, g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`).Code)
	}
	assert.Equal(t, 429, g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`).Code)
	assert.Equal(t, 200, g.as(g.token("s1", "SHOP", "shop"), "POST", "/v1/api/shop/logout", "").Code, "other routes use the general budget")
}

func TestRateLimiterOutageLetsRequestsThrough(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.rateLimit = 1 })
	g.mr.Close()
	assert.Equal(t, 200, g.call("POST", "/v1/api/shop/login", `{"email":"a@b.co","password":"x"}`).Code)
}

// ---- cross-cutting -----------------------------------------------------------------------------------------------

func TestSecurityHeadersAndCORS(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.corsOrigins = []string{"https://shop.example"} })
	r := g.call("GET", "/v1/api/user/welcome_back?token=t", "", "Origin", "https://shop.example")
	assert.Equal(t, "nosniff", r.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "DENY", r.Header().Get("X-Frame-Options"))
	assert.Equal(t, "no-store", r.Header().Get("Cache-Control"))
	assert.Equal(t, "https://shop.example", r.Header().Get("Access-Control-Allow-Origin"))
	assert.Equal(t, "true", r.Header().Get("Access-Control-Allow-Credentials"))

	r = g.call("GET", "/v1/api/user/welcome_back?token=t", "", "Origin", "https://evil.example")
	assert.Empty(t, r.Header().Get("Access-Control-Allow-Origin"), "unlisted origins get no CORS headers")

	pre := g.do(req{method: "OPTIONS", path: "/v1/api/shop/login", headers: map[string]string{"Origin": "https://shop.example", "Access-Control-Request-Method": "POST"}})
	assert.Equal(t, 204, pre.Code)
	assert.Contains(t, pre.Header().Get("Access-Control-Allow-Headers"), "x-api-key")
	pre = g.do(req{method: "OPTIONS", path: "/v1/api/shop/login", headers: map[string]string{"Origin": "https://evil.example"}})
	assert.NotEqual(t, 204, pre.Code)
}

func TestBodyLimit(t *testing.T) {
	g := newGateway(t, func(o *gwOpts) { o.bodyLimit = 200 })
	big := `{"name":"` + strings.Repeat("x", 500) + `","email":"a@b.co","password":"correct-horse"}`
	r := g.call("POST", "/v1/api/shop/signup", big)
	assert.Equal(t, 400, r.Code)
	assert.Equal(t, "PAYLOAD_TOO_LARGE", r.errCode())
	assert.Equal(t, 0, g.auth.count("SignUpShop"))
}

func TestUnknownRoutesAndMethodsUseTheErrorEnvelope(t *testing.T) {
	g := newGateway(t)
	r := g.do(req{method: "GET", path: "/nope"})
	assert.Equal(t, 404, r.Code)
	assert.Equal(t, "ROUTE_NOT_FOUND", r.errCode())
	r = g.call("GET", "/v1/api/shop/login", "")
	assert.Equal(t, 400, r.Code, "wrong method on a known path")
	assert.Equal(t, "METHOD_NOT_ALLOWED", r.errCode())
}

func TestOperationalRoutesNeedNoAPIKey(t *testing.T) {
	g := newGateway(t)
	assert.Equal(t, 200, g.do(req{method: "GET", path: "/health/live"}).Code)
	assert.Equal(t, 200, g.do(req{method: "GET", path: "/health/ready"}).Code)
	m := g.do(req{method: "GET", path: "/metrics"})
	assert.Equal(t, 200, m.Code)
	g.call("GET", "/v1/api/user/welcome_back?token=t", "")
	m = g.do(req{method: "GET", path: "/metrics"})
	assert.Contains(t, m.Body.String(), `http_server_duration_seconds_bucket{method="GET",route="/v1/api/user/welcome_back"`)
	assert.Contains(t, m.Body.String(), "circuit_breaker_state")
}

func TestDocsAreServed(t *testing.T) {
	g := newGateway(t)
	page := g.do(req{method: "GET", path: "/docs"})
	assert.Equal(t, 200, page.Code)
	assert.Contains(t, page.Body.String(), "SwaggerUIBundle")
	assert.Contains(t, page.Header().Get("Content-Security-Policy"), "unpkg.com")
	spec := g.do(req{method: "GET", path: "/docs/openapi.yaml"})
	assert.Equal(t, 200, spec.Code)
	assert.Contains(t, spec.Body.String(), "openapi: 3.0.3")
}

func TestEveryRegisteredRouteIsDocumentedInTheOpenAPISpec(t *testing.T) {
	g := newGateway(t)
	var doc struct {
		Paths map[string]map[string]any `yaml:"paths"`
	}
	require.NoError(t, yaml.Unmarshal(openapi.Spec, &doc))

	registered := map[string]bool{}
	for _, ri := range g.engine.Routes() {
		if !strings.HasPrefix(ri.Path, "/v1/api/") {
			continue // health, metrics and docs are not part of the public API
		}
		path := ri.Path
		for _, seg := range strings.Split(path, "/") {
			if strings.HasPrefix(seg, ":") {
				path = strings.Replace(path, seg, "{"+seg[1:]+"}", 1)
			}
		}
		registered[strings.ToLower(ri.Method)+" "+path] = true
		ops, ok := doc.Paths[path]
		require.True(t, ok, "%s %s is not in the OpenAPI spec", ri.Method, path)
		_, ok = ops[strings.ToLower(ri.Method)]
		assert.True(t, ok, "%s %s is not documented", ri.Method, path)
	}
	for path, ops := range doc.Paths {
		for method := range ops {
			assert.True(t, registered[method+" "+path], "the spec describes %s %s but no such route is registered", method, path)
		}
	}
}
