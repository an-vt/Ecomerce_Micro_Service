// Package app is the composition root of order-service: it builds the adapters (PostgreSQL, circuit-breaker-protected
// gRPC clients), wires the use cases and runs the gRPC server, the saga reply consumer, the outbox relay, the timeout
// sweeper and the ops HTTP server.
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
	"google.golang.org/grpc"

	catalogclient "github.com/an-vt/ecom/order-service/internal/client/catalog"
	discountclient "github.com/an-vt/ecom/order-service/internal/client/discount"
	"github.com/an-vt/ecom/order-service/internal/config"
	_ "github.com/an-vt/ecom/order-service/internal/database/migrations" // registers the migrations
	"github.com/an-vt/ecom/order-service/internal/domain"
	"github.com/an-vt/ecom/order-service/internal/repository/postgres"
	transport "github.com/an-vt/ecom/order-service/internal/transport/grpc"
	consumer "github.com/an-vt/ecom/order-service/internal/transport/kafka"
	"github.com/an-vt/ecom/order-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/httpx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
	discountv1 "github.com/an-vt/ecom/proto/gen/go/discount/v1"
	orderv1 "github.com/an-vt/ecom/proto/gen/go/order/v1"
)

const dbName = "order_db"

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
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: cfg.DatabaseURL, DBName: dbName})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if cfg.AutoMigrate {
		ran, err := pgmigrate.NewRunner(store.Raw(), dbName, nil).Migrate(ctx, false)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		log.Info("migrations applied", "count", len(ran))
	}

	producer, err := kafkax.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	relay := outbox.NewRelay(pgoutbox.NewStore(store, cfg.ServiceName), producer, outbox.RelayOptions{Metrics: m, Log: log})

	breakers := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{
		ErrorRatio: cfg.BreakerError, MinRequests: uint32(cfg.BreakerMin), Window: cfg.BreakerWin, ResetTimeout: cfg.BreakerReset,
		Observer: m.BreakerObserver(),
	}))
	dial := func(name, addr string) (*grpc.ClientConn, error) {
		return grpcx.Dial(grpcx.ClientConfig{Target: addr, Name: name, Timeout: cfg.GRPCTimeout, Breakers: breakers, Metrics: m})
	}
	catalogConn, err := dial("catalog", cfg.CatalogAddr)
	if err != nil {
		return err
	}
	defer func() { _ = catalogConn.Close() }()
	discountConn, err := dial("discount", cfg.DiscountAddr)
	if err != nil {
		return err
	}
	defer func() { _ = discountConn.Close() }()

	repos := postgres.New(store)
	tx := pgdb.NewTxManager(store)
	uc := usecase.NewSet(&usecase.Deps{
		Orders: repos.Orders, Sagas: repos.Sagas, Keys: repos.Keys,
		Catalog:   catalogclient.New(catalogv1.NewCatalogServiceClient(catalogConn)),
		Discounts: discountclient.New(discountv1.NewDiscountServiceClient(discountConn)),
		Tx:        tx, Outbox: pgoutbox.NewWriter(store), Clock: clock.System{}, IDs: idgen.UUID{}, Metrics: newSagaMetrics(m.Registry),
		Timeouts: domain.Timeouts{Step: cfg.StepTimeout, Compensation: cfg.CompensationRetry}, FeeShip: cfg.FeeShip,
	})

	runner, err := kafkax.NewRunner(kafkax.RunnerConfig{
		Brokers: cfg.KafkaBrokers, Group: consumer.Consumer, Topics: consumer.Topics, Log: log,
		Handler:   consumer.Handler(uc, tx, pgoutbox.NewGuard(store)),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: producer, Metrics: m, Log: log}),
	})
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(cfg.ServiceName, m, log)
	orderv1.RegisterOrderServiceServer(srv, transport.NewServer(uc))
	lis, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.GRPCPort))
	if err != nil {
		return fmt.Errorf("grpc listen: %w", err)
	}
	ops := httpx.NewOpsHandler(m.Handler(), map[string]httpx.Check{"postgres": store.Ping, "kafka": producer.Ping})

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
	g.Go(func() error { return runner.Run(gctx) })
	g.Go(func() error { return sweep(gctx, uc, cfg.SweepInterval, log) })
	g.Go(func() error {
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
	log.Info("order-service started", "env", cfg.Env, "stepTimeout", cfg.StepTimeout.String())
	err = g.Wait()
	log.Info("order-service stopped")
	return err
}

// sweep gives up on checkout steps that missed their deadline, every interval, until ctx ends. Every replica runs it:
// the sweeper takes sagas with SKIP LOCKED, so a timeout is handled by exactly one of them.
func sweep(ctx context.Context, uc *usecase.Set, every time.Duration, log *slog.Logger) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n, err := uc.SweepSagaTimeouts.Execute(ctx, 50)
			switch {
			case err != nil && ctx.Err() == nil:
				log.Error("saga sweep failed", "error", err.Error())
			case n > 0:
				log.Warn("checkout steps timed out and were acted on", "sagas", n)
			}
		}
	}
}
