// Package app is the composition root of payment-service: it builds the adapters (PostgreSQL, the mock payment provider
// behind its circuit breaker), wires the use cases and runs the Kafka consumer, the outbox relay and the ops HTTP
// server. The service has no gRPC server: it is driven only by payment.commands.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/an-vt/ecom/payment-service/internal/client/provider"
	"github.com/an-vt/ecom/payment-service/internal/config"
	_ "github.com/an-vt/ecom/payment-service/internal/database/migrations" // registers the migrations
	"github.com/an-vt/ecom/payment-service/internal/repository/postgres"
	consumer "github.com/an-vt/ecom/payment-service/internal/transport/kafka"
	"github.com/an-vt/ecom/payment-service/internal/usecase"
	"github.com/an-vt/ecom/platform-go/breaker"
	"github.com/an-vt/ecom/platform-go/clock"
	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
	"github.com/an-vt/ecom/platform-go/httpx"
	"github.com/an-vt/ecom/platform-go/idgen"
	"github.com/an-vt/ecom/platform-go/kafkax"
	"github.com/an-vt/ecom/platform-go/logger"
	"github.com/an-vt/ecom/platform-go/metrics"
	"github.com/an-vt/ecom/platform-go/otelx"
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/pgoutbox"
	"github.com/an-vt/ecom/platform-go/pgdb"
)

const dbName = "payment_db"

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

	// the provider is the one dependency that can hang: a deadline per call and a circuit breaker in front of it
	breakers := breaker.NewRegistry(breaker.Settings{
		ErrorRatio: cfg.BreakerError, MinRequests: uint32(cfg.BreakerMin), Window: cfg.BreakerWin, ResetTimeout: cfg.BreakerReset,
		IsFailure: provider.IsFailure, Observer: m.BreakerObserver(),
	})
	tx := pgdb.NewTxManager(store)
	uc := usecase.NewSet(&usecase.Deps{Payments: postgres.New(store), Provider: provider.NewGuard(provider.NewMock(), breakers, cfg.ProviderTimeout),
		Tx: tx, Outbox: pgoutbox.NewWriter(store), Clock: clock.System{}, IDs: idgen.UUID{}, Currency: cfg.Currency})

	runner, err := kafkax.NewRunner(kafkax.RunnerConfig{
		Brokers: cfg.KafkaBrokers, Group: consumer.Consumer, Topics: consumer.Topics, Log: log,
		Handler:   consumer.Handler(uc, tx, pgoutbox.NewGuard(store)),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: producer, Metrics: m, Log: log}),
	})
	if err != nil {
		return err
	}
	ops := httpx.NewOpsHandler(m.Handler(), map[string]httpx.Check{"postgres": store.Ping, "kafka": producer.Ping})

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return httpx.Serve(gctx, ":"+strconv.Itoa(cfg.HTTPPort), ops, log) })
	g.Go(func() error { return relay.Run(gctx) })
	g.Go(func() error { return runner.Run(gctx) })
	log.Info("payment-service started", "env", cfg.Env, "currency", cfg.Currency)
	err = g.Wait()
	log.Info("payment-service stopped")
	return err
}
