// Package app is the composition root of cart-service: it builds the adapters (MongoDB, circuit-breaker-protected gRPC
// clients), wires the use cases and runs the gRPC server, the Kafka consumer, the outbox relay and the ops server.
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

	catalogclient "github.com/an-vt/ecom/cart-service/internal/client/catalog"
	inventoryclient "github.com/an-vt/ecom/cart-service/internal/client/inventory"
	"github.com/an-vt/ecom/cart-service/internal/config"
	_ "github.com/an-vt/ecom/cart-service/internal/database/migrations" // registers the migrations
	mongorepo "github.com/an-vt/ecom/cart-service/internal/repository/mongo"
	transport "github.com/an-vt/ecom/cart-service/internal/transport/grpc"
	consumer "github.com/an-vt/ecom/cart-service/internal/transport/kafka"
	"github.com/an-vt/ecom/cart-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/mongomigrate"
	"github.com/an-vt/ecom/platform-go/grpcx"
	"github.com/an-vt/ecom/platform-go/httpx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/mongox"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/outbox/mongooutbox"
	cartv1 "github.com/an-vt/ecom/proto/gen/go/cart/v1"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
	inventoryv1 "github.com/an-vt/ecom/proto/gen/go/inventory/v1"
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
	store, err := mongox.Open(ctx, mongox.Config{URI: cfg.MongoURI, Database: cfg.MongoDB})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close(context.Background()) }()
	if cfg.AutoMigrate {
		ran, err := mongomigrate.NewRunner(store.DB(), nil).Migrate(ctx, false)
		if err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		log.Info("migrations applied", "count", len(ran))
	}

	producer, err := kafkax.NewProducer(cfg.KafkaBrokers) // DLQ writer
	if err != nil {
		return err
	}
	defer producer.Close()

	breakers := breaker.NewRegistry(grpcx.BreakerSettings(breaker.Settings{
		ErrorRatio: cfg.BreakerError, MinRequests: uint32(cfg.BreakerMin), Window: cfg.BreakerWin, ResetTimeout: cfg.BreakerReset,
		Observer: m.BreakerObserver(),
	}))
	dial := func(name, addr string) (*grpcConn, error) {
		return dialConn(grpcx.ClientConfig{Target: addr, Name: name, Timeout: cfg.GRPCTimeout, Breakers: breakers, Metrics: m})
	}
	catalogConn, err := dial("catalog", cfg.CatalogAddr)
	if err != nil {
		return err
	}
	defer func() { _ = catalogConn.Close() }()
	inventoryConn, err := dial("inventory", cfg.InventoryAddr)
	if err != nil {
		return err
	}
	defer func() { _ = inventoryConn.Close() }()

	tx := mongox.NewTxManager(store)
	uc := usecase.NewSet(&usecase.Deps{
		Carts: mongorepo.New(store), Catalog: catalogclient.New(catalogv1.NewCatalogServiceClient(catalogConn)),
		Stock: inventoryclient.New(inventoryv1.NewInventoryServiceClient(inventoryConn)), Clock: clock.System{}, IDs: idgen.ULID{},
	})

	runner, err := kafkax.NewRunner(kafkax.RunnerConfig{
		Brokers: cfg.KafkaBrokers, Group: consumer.Consumer, Topics: consumer.Topics, Log: log,
		Handler:   consumer.Handler(uc, tx, mongooutbox.NewGuard(store.DB())),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: producer, Metrics: m, Log: log}),
	})
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(cfg.ServiceName, m, log)
	cartv1.RegisterCartServiceServer(srv, transport.NewServer(uc))
	lis, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.GRPCPort))
	if err != nil {
		return fmt.Errorf("grpc listen: %w", err)
	}
	ops := httpx.NewOpsHandler(m.Handler(), map[string]httpx.Check{"mongodb": store.Ping, "kafka": producer.Ping})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		log.Info("grpc server listening", "addr", lis.Addr().String())
		if err := srv.Serve(lis); err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
		return nil
	})
	g.Go(func() error { return httpx.Serve(gctx, ":"+strconv.Itoa(cfg.HTTPPort), ops, log) })
	g.Go(func() error { return runner.Run(gctx) })
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
	log.Info("cart-service started", "env", cfg.Env)
	err = g.Wait()
	log.Info("cart-service stopped")
	return err
}
