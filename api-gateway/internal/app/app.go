// Package app is the composition root of api-gateway: it dials the upstream services through circuit breakers,
// builds the guards and the router, and serves the public REST API until the context is cancelled.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/an-vt/ecom/api-gateway/internal/config"
	transport "github.com/an-vt/ecom/api-gateway/internal/transport/http"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/errs"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/httpx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/rbac"
	"github.com/an-vt/ecom/platform-go/redisx"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
	cartv1 "github.com/an-vt/ecom/proto/gen/go/cart/v1"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
	notificationv1 "github.com/an-vt/ecom/proto/gen/go/notification/v1"
	orderv1 "github.com/an-vt/ecom/proto/gen/go/order/v1"
)

// Run starts the gateway and blocks until ctx is cancelled or the server fails.
func Run(ctx context.Context, cfg config.Config) error {
	log := logger.New(logger.Options{Service: cfg.ServiceName, Level: cfg.LogLevel})
	slog.SetDefault(log)

	shutdownTracing, err := otelx.Init(ctx, otelx.Config{Service: cfg.ServiceName, Env: cfg.Env, Endpoint: cfg.OTLPEndpoint})
	if err != nil {
		return fmt.Errorf("tracing: %w", err)
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(c)
	}()

	m := metrics.New()
	rdb, err := redisx.Open(ctx, redisx.Config{Addr: cfg.RedisAddr})
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	// One breaker registry for every upstream: a failing method of one service never trips another.
	breakers := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{
		ErrorRatio: cfg.BreakerError, MinRequests: uint32(cfg.BreakerMin), Window: cfg.BreakerWin, ResetTimeout: cfg.BreakerReset,
		Observer: m.BreakerObserver(),
	}))
	up, closeUpstreams, err := dialUpstreams(cfg, breakers, m)
	if err != nil {
		return err
	}
	defer closeUpstreams()
	authClient, rbacClient := up.Auth, up.Rbac

	// Tokens are verified locally with the public key published by auth-service (refetched on key rotation).
	verifier := jwtx.NewVerifier(cfg.JWTIssuer, cfg.JWTAudience, clock.System{}, func(ctx context.Context) (jwtx.JWKS, error) {
		r, err := authClient.GetJwks(ctx, &emptypb.Empty{})
		if err != nil {
			return jwtx.JWKS{}, err
		}
		set := jwtx.JWKS{}
		for _, k := range r.GetKeys() {
			set.Keys = append(set.Keys, jwtx.JWK{Kty: k.GetKty(), Kid: k.GetKid(), Alg: k.GetAlg(), Use: k.GetUse(), N: k.GetN(), E: k.GetE()})
		}
		return set, nil
	})
	grants := transport.NewGrantsCache(func(ctx context.Context) ([]rbac.Grant, error) {
		r, err := rbacClient.GetGrants(ctx, &emptypb.Empty{})
		if err != nil {
			return nil, err
		}
		out := make([]rbac.Grant, 0, len(r.GetGrants()))
		for _, g := range r.GetGrants() {
			out = append(out, rbac.Grant{Role: g.GetRole(), Resource: g.GetResource(), Actions: g.GetActions(), Attributes: g.GetAttributes()})
		}
		return out, nil
	}, cfg.GrantsCacheTTL, clock.System{})

	router := transport.NewRouter(transport.Options{
		Upstreams: up,
		Verifier:  verifier, Denylist: redisx.NewDenylist(rdb, "auth:revoked:"), Limiter: redisx.NewLimiter(rdb, "rl:"),
		APICache: redisx.NewCache(rdb, "apikey:"), Grants: grants, Metrics: m, IDs: idgen.ULID{},
		Checks: map[string]httpx.Check{
			"redis": func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
			// ready means "can serve traffic": no upstream circuit may be open
			"upstreams": func(context.Context) error {
				for name, st := range breakers.States() {
					if st == breaker.Open {
						return errs.Unavailable(errs.CodeUnavailable, "circuit open: "+name)
					}
				}
				return nil
			},
		},
		CORSOrigins: cfg.CORSOrigins, TrustedProxies: cfg.TrustedProxies, CookieSecure: cfg.CookieSecure, CookieTTL: cfg.RefreshCookieTTL,
		RateLimit: cfg.RateLimitPerMin, AuthRateLimit: cfg.AuthRatePerMin, BodyLimit: cfg.BodyLimitBytes,
		APIKeyTTL: cfg.APIKeyCacheTTL, APIKeyNegTTL: cfg.APIKeyNegativeTTL,
	})

	log.Info("api-gateway starting", "env", cfg.Env, "auth", cfg.AuthAddr)
	err = httpx.Serve(ctx, ":"+strconv.Itoa(cfg.HTTPPort), router, log)
	log.Info("api-gateway stopped")
	return err
}

// upstream is one backend service of the gateway.
type upstream struct {
	name, addr string
	bind       func(*grpc.ClientConn, *transport.Upstreams)
}

// dialUpstreams opens one gRPC channel per backend, each with its own deadline and its own breakers in the shared
// registry (the bulkhead of docs/09). The returned function closes them all.
func dialUpstreams(cfg config.Config, breakers *breaker.Registry, m *metrics.Metrics) (transport.Upstreams, func(), error) {
	list := []upstream{
		{"auth", cfg.AuthAddr, func(c *grpc.ClientConn, u *transport.Upstreams) {
			u.Auth, u.Rbac = authv1.NewAuthServiceClient(c), authv1.NewRbacServiceClient(c)
		}},
		{"catalog", cfg.CatalogAddr, func(c *grpc.ClientConn, u *transport.Upstreams) { u.Catalog = catalogv1.NewCatalogServiceClient(c) }},
		{"inventory", cfg.InventoryAddr, func(c *grpc.ClientConn, u *transport.Upstreams) {
			u.Inventory = inventoryv1.NewInventoryServiceClient(c)
		}},
		{"cart", cfg.CartAddr, func(c *grpc.ClientConn, u *transport.Upstreams) { u.Cart = cartv1.NewCartServiceClient(c) }},
		{"discount", cfg.DiscountAddr, func(c *grpc.ClientConn, u *transport.Upstreams) { u.Discount = discountv1.NewDiscountServiceClient(c) }},
		{"order", cfg.OrderAddr, func(c *grpc.ClientConn, u *transport.Upstreams) { u.Order = orderv1.NewOrderServiceClient(c) }},
		{"notification", cfg.NotificationAddr, func(c *grpc.ClientConn, u *transport.Upstreams) {
			u.Notification = notificationv1.NewNotificationServiceClient(c)
		}},
	}
	var conns []*grpc.ClientConn
	closeAll := func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}
	var out transport.Upstreams
	for _, u := range list {
		conn, err := grpcx.Dial(grpcx.ClientConfig{Target: u.addr, Name: u.name, Timeout: cfg.GRPCTimeout, Breakers: breakers, Metrics: m})
		if err != nil {
			closeAll()
			return transport.Upstreams{}, nil, err
		}
		conns = append(conns, conn)
		u.bind(conn, &out)
	}
	return out, closeAll, nil
}
