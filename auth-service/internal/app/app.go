// Package app is the composition root of auth-service: it builds the adapters, wires them into the use cases and
// runs the gRPC server, the ops HTTP server and the outbox relay until the context is cancelled.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/an-vt/ecom/auth-service/internal/config"
	_ "github.com/an-vt/ecom/auth-service/internal/database/migrations" // registers the migrations
	"github.com/an-vt/ecom/auth-service/internal/repository/postgres"
	"github.com/an-vt/ecom/auth-service/internal/security"
	transport "github.com/an-vt/ecom/auth-service/internal/transport/grpc"
	"github.com/an-vt/ecom/auth-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/httpx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/jwtx"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
	"github.com/an-vt/ecom/platform-go/redisx"
	authv1 "github.com/an-vt/ecom/proto/gen/go/auth/v1"
)

// Run starts the service and blocks until ctx is cancelled or a component fails.
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

	store, err := pgdb.Open(ctx, pgdb.Config{DSN: cfg.DatabaseURL, DBName: "auth_db"})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if cfg.AutoMigrate {
		ran, err := pgmigrate.NewRunner(store.Raw(), "auth_db", nil).Migrate(ctx, false)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		log.Info("migrations applied", "count", len(ran))
	}

	rdb, err := redisx.Open(ctx, redisx.Config{Addr: cfg.RedisAddr})
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	key, err := jwtx.ParsePrivateKey([]byte(cfg.JWTPrivateKeyPEM))
	if err != nil {
		return fmt.Errorf("AUTH_JWT_PRIVATE_KEY: %w", err)
	}
	signer := jwtx.NewSigner(key, cfg.JWTIssuer, cfg.JWTAudience, cfg.AccessTTL, clock.System{})
	hasher, err := security.NewBcrypt(cfg.BcryptCost)
	if err != nil {
		return err
	}

	producer, err := kafkax.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	relay := outbox.NewRelay(pgoutbox.NewStore(store, cfg.ServiceName), producer, outbox.RelayOptions{Metrics: m, Log: log})

	repos := postgres.New(store)
	uc := usecase.NewSet(&usecase.Deps{
		Accounts: repos.Accounts, Sessions: repos.Sessions, Otps: repos.Otps, ApiKeys: repos.ApiKeys, Rbac: repos.Rbac,
		Hasher: hasher, Signer: signer, Secrets: security.Secrets{},
		Denylist: redisx.NewDenylist(rdb, "auth:revoked:"), Tx: pgdb.NewTxManager(store), Outbox: pgoutbox.NewWriter(store),
		Clock: clock.System{}, IDs: idgen.UUID{},
		Settings: usecase.Settings{RefreshTTL: cfg.RefreshTTL, OTPTTL: cfg.OTPTTL, VerifyLinkBase: cfg.VerifyLinkBase},
	})

	srv := grpcx.NewServer(cfg.ServiceName, m, log)
	authv1.RegisterAuthServiceServer(srv, transport.NewAuthServer(uc))
	authv1.RegisterRbacServiceServer(srv, transport.NewRbacServer(uc))
	lis, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.GRPCPort))
	if err != nil {
		return fmt.Errorf("grpc listen: %w", err)
	}

	ops := httpx.NewOpsHandler(m.Handler(), map[string]httpx.Check{
		"postgres": store.Ping,
		"redis":    func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		"kafka":    producer.Ping,
	})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		log.Info("grpc server listening", "addr", lis.Addr().String())
		if err := srv.Serve(lis); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})
	g.Go(func() error { return httpx.Serve(gctx, ":"+strconv.Itoa(cfg.HTTPPort), ops, log) })
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { // graceful stop: let in-flight RPCs finish, then release Serve
		<-gctx.Done()
		stopped := make(chan struct{})
		go func() { srv.GracefulStop(); close(stopped) }()
		select {
		case <-stopped:
		case <-time.After(10 * time.Second):
			srv.Stop()
		}
		return nil
	})
	log.Info("auth-service started", "env", cfg.Env)
	err = g.Wait()
	log.Info("auth-service stopped")
	return err
}
