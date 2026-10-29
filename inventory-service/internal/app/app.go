// Package app is the composition root of inventory-service: it builds the adapters, wires the use cases and runs the
// gRPC server, the Kafka consumer, the outbox relay, the reservation sweeper and the ops HTTP server.
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

	"github.com/an-vt/ecom/inventory-service/internal/config"
	_ "github.com/an-vt/ecom/inventory-service/internal/database/migrations" // registers the migrations
	"github.com/an-vt/ecom/inventory-service/internal/repository/postgres"
	transport "github.com/an-vt/ecom/inventory-service/internal/transport/grpc"
	consumer "github.com/an-vt/ecom/inventory-service/internal/transport/kafka"
	"github.com/an-vt/ecom/inventory-service/internal/usecase"
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
	store, err := pgdb.Open(ctx, pgdb.Config{DSN: cfg.DatabaseURL, DBName: "inventory_db"})
	if err != nil {
		return err
	}
	defer func() { _ = store.Close() }()
	if cfg.AutoMigrate {
		ran, err := pgmigrate.NewRunner(store.Raw(), "inventory_db", nil).Migrate(ctx, false)
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

	repos := postgres.New(store)
	tx := pgdb.NewTxManager(store)
	uc := usecase.NewSet(&usecase.Deps{Inventories: repos.Inventories, Reservations: repos.Reservations, Tx: tx,
		Outbox: pgoutbox.NewWriter(store), Clock: clock.System{}, IDs: idgen.UUID{}, ReservationTTL: cfg.ReservationTTL})

	runner, err := kafkax.NewRunner(kafkax.RunnerConfig{
		Brokers: cfg.KafkaBrokers, Group: consumer.Consumer, Topics: consumer.Topics, Log: log,
		Handler:   consumer.Handler(uc, tx, pgoutbox.NewGuard(store)),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: producer, Metrics: m, Log: log}),
	})
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(cfg.ServiceName, m, log)
	inventoryv1.RegisterInventoryServiceServer(srv, transport.NewServer(uc))
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
	g.Go(func() error { return sweep(gctx, uc, cfg.ExpiryInterval, log) })
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
	log.Info("inventory-service started", "env", cfg.Env)
	err = g.Wait()
	log.Info("inventory-service stopped")
	return err
}

// sweep returns the stock of overdue holds to the shelf, every interval, until ctx ends.
func sweep(ctx context.Context, uc *usecase.Set, every time.Duration, log *slog.Logger) error {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			n, err := uc.ExpireReservations.Execute(ctx, 100)
			switch {
			case err != nil && ctx.Err() == nil:
				log.Error("reservation sweep failed", "error", err.Error())
			case n > 0:
				log.Info("expired stock holds returned to the shelf", "orders", n)
			}
		}
	}
}
