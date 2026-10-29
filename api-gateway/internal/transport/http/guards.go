package http

import (
	"context"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/principal"
	"github.com/an-vt/ecom/platform-go/rbac"
	"github.com/an-vt/ecom/platform-go/redisx"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

// Error codes produced by the guards.
const (
	CodeAPIKeyRequired = "API_KEY_REQUIRED"
	CodeAPIKeyInvalid  = "API_KEY_INVALID"
	CodeTokenRequired  = "TOKEN_REQUIRED"
	CodeForbidden      = "FORBIDDEN"
)

const ctxClaims = "claims"
const ctxPossession = "possession"

// ---- API key --------------------------------------------------------------------------------------------------

type apiKeyInfo struct {
	Permissions []string `json:"permissions"`
}

// apiKey requires a valid x-api-key holding the permission every public route needs (the monolith's '0000').
// Validation results are cached in Redis by the SHA-256 of the key — the key itself is never stored — for
// positiveTTL, and unknown keys for negativeTTL, so a flood of bad keys does not reach auth-service.
func apiKey(auth authv1.AuthServiceClient, cache *redisx.Cache, positiveTTL, negativeTTL time.Duration, permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.GetHeader("x-api-key")
		if key == "" {
			fail(c, errs.Unauthenticated(CodeAPIKeyRequired, "x-api-key header is required"))
			return
		}
		info, found, _, err := redisx.GetOrLoad(c.Request.Context(), cache, hashKey(key), positiveTTL, negativeTTL,
			func(ctx context.Context) (apiKeyInfo, bool, error) {
				r, err := auth.ValidateApiKey(ctx, &authv1.ValidateApiKeyRequest{Key: key})
				if err != nil {
					return apiKeyInfo{}, false, err
				}
				if !r.GetValid() {
					return apiKeyInfo{}, false, nil
				}
				return apiKeyInfo{Permissions: r.GetPermissions()}, true, nil
			})
		switch {
		case err != nil:
			fail(c, err)
		case !found:
			fail(c, errs.Unauthenticated(CodeAPIKeyInvalid, "the API key is not valid"))
		case !slices.Contains(info.Permissions, permission):
			fail(c, errs.PermissionDenied(CodeForbidden, "the API key lacks the required permission"))
		default:
			c.Next()
		}
	}
}

// ---- JWT ------------------------------------------------------------------------------------------------------

// jwtAuth verifies the bearer token locally and rejects tokens of revoked sessions. If the revocation list cannot
// be read the request is refused (fail closed): revocation is a security control, unlike rate limiting.
func jwtAuth(v *jwtx.Verifier, deny *redisx.Denylist) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.GetHeader("Authorization")
		token, hasBearer := strings.CutPrefix(h, "Bearer ")
		if !hasBearer || token == "" {
			fail(c, errs.Unauthenticated(CodeTokenRequired, "an Authorization: Bearer token is required"))
			return
		}
		claims, err := v.Verify(c.Request.Context(), token)
		if err != nil {
			fail(c, err)
			return
		}
		revoked, err := deny.IsRevoked(c.Request.Context(), claims.SessionID)
		if err != nil {
			slog.Default().ErrorContext(c.Request.Context(), "revocation list unavailable, refusing the request", "error", err.Error())
			fail(c, errs.Unavailable(errs.CodeUnavailable, "cannot verify the session right now"))
			return
		}
		if revoked {
			fail(c, errs.Unauthenticated("TOKEN_REVOKED", "this session has ended"))
			return
		}
		c.Request = c.Request.WithContext(principal.With(c.Request.Context(), principal.Principal{
			ID: claims.AccountID(), Type: principal.Type(claims.Type), Roles: claims.Roles, SessionID: claims.SessionID,
		}))
		c.Set(ctxClaims, claims)
		c.Next()
	}
}

func callerOf(c *gin.Context) principal.Principal {
	p, _ := principal.From(c.Request.Context())
	return p
}

// requireType allows only one account type (shops for product routes, buyers for carts and orders).
func requireType(t principal.Type) gin.HandlerFunc {
	return func(c *gin.Context) {
		if callerOf(c).Type != t {
			fail(c, errs.PermissionDenied(CodeForbidden, "this route is for "+strings.ToLower(string(t))+" accounts"))
			return
		}
		c.Next()
	}
}

func userOnly() gin.HandlerFunc { return requireType(principal.TypeUser) }

// ---- RBAC -----------------------------------------------------------------------------------------------------

// GrantSource loads every grant (auth-service RbacService.GetGrants).
type GrantSource func(ctx context.Context) ([]rbac.Grant, error)

// GrantsCache keeps the evaluator for ttl. If a refresh fails the previous grants keep being served: auth-service
// being briefly down must not lock every administrator out.
type GrantsCache struct {
	fetch GrantSource
	ttl   time.Duration
	clk   clock.Clock

	mu sync.Mutex
	ev *rbac.Evaluator
	at time.Time
}

// NewGrantsCache creates the cache.
func NewGrantsCache(fetch GrantSource, ttl time.Duration, clk clock.Clock) *GrantsCache {
	if clk == nil {
		clk = clock.System{}
	}
	return &GrantsCache{fetch: fetch, ttl: ttl, clk: clk}
}

// Evaluator returns a fresh-enough evaluator.
func (g *GrantsCache) Evaluator(ctx context.Context) (*rbac.Evaluator, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.clk.Now()
	if g.ev != nil && now.Sub(g.at) < g.ttl {
		return g.ev, nil
	}
	grants, err := g.fetch(ctx)
	if err != nil {
		if g.ev != nil {
			slog.Default().WarnContext(ctx, "could not refresh RBAC grants, using the cached ones", "error", err.Error())
			return g.ev, nil
		}
		return nil, err
	}
	g.ev, g.at = rbac.New(grants), now
	return g.ev, nil
}

// requirePermission checks the caller's roles against the grants. verb is create/read/update/delete; need says
// whether the route needs the "any" grant or accepts "own" (the handler then restricts data to the caller's own).
func requirePermission(grants *GrantsCache, verb, resource string, need rbac.Possession) gin.HandlerFunc {
	return func(c *gin.Context) {
		ev, err := grants.Evaluator(c.Request.Context())
		if err != nil {
			fail(c, err)
			return
		}
		got := ev.Check(callerOf(c).Roles, verb, resource)
		if got == rbac.None || (need == rbac.Any && got != rbac.Any) {
			fail(c, errs.PermissionDenied(CodeForbidden, "you are not allowed to "+verb+" "+resource))
			return
		}
		c.Set(ctxPossession, got)
		c.Next()
	}
}
