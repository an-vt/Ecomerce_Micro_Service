// Package app is the composition root of catalog-service: it builds the adapters, wires them into the use cases and
// runs the gRPC server, the Kafka consumer, the outbox relay and the ops HTTP server until the context is cancelled.
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

	"github.com/an-vt/ecom/catalog-service/internal/cache"
	"github.com/an-vt/ecom/catalog-service/internal/client/media"
	"github.com/an-vt/ecom/catalog-service/internal/config"
	_ "github.com/an-vt/ecom/catalog-service/internal/database/migrations" // registers the migrations
	"github.com/an-vt/ecom/catalog-service/internal/domain"
	mongorepo "github.com/an-vt/ecom/catalog-service/internal/repository/mongo"
	transport "github.com/an-vt/ecom/catalog-service/internal/transport/grpc"
	consumer "github.com/an-vt/ecom/catalog-service/internal/transport/kafka"
	"github.com/an-vt/ecom/catalog-service/internal/usecase"
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
	"github.com/an-vt/ecom/platform-go/outbox"
	"github.com/an-vt/ecom/platform-go/outbox/mongooutbox"
	"github.com/an-vt/ecom/platform-go/redisx"
	catalogv1 "github.com/an-vt/ecom/proto/gen/go/catalog/v1"
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

	rdb, err := redisx.Open(ctx, redisx.Config{Addr: cfg.RedisAddr})
	if err != nil {
		return err
	}
	defer func() { _ = rdb.Close() }()

	producer, err := kafkax.NewProducer(cfg.KafkaBrokers)
	if err != nil {
		return err
	}
	defer producer.Close()
	relay := outbox.NewRelay(mongooutbox.NewStore(store.DB()), producer, outbox.RelayOptions{Metrics: m, Log: log})

	repos := mongorepo.New(store)
	tx := mongox.NewTxManager(store)
	deps := &usecase.Deps{
		Products: repos.Products, Spus: repos.Spus, Comments: repos.Comments, Shops: repos.Shops,
		Types: domain.DefaultTypes(), Tx: tx, Outbox: mongooutbox.NewWriter(store.DB()),
		SkuCache: cache.NewSku(redisx.NewCache(rdb, "sku:"), cfg.SkuCacheTTL),
		Clock:    clock.System{}, IDs: idgen.ULID{},
		MediaPolicy: domain.MediaPolicy{MaxBytes: cfg.MediaMaxBytes, TTL: cfg.MediaURLTTL},
	}
	if cfg.MediaEnabled() {
		store, err := media.New(media.Config{Endpoint: cfg.MediaEndpoint, Bucket: cfg.MediaBucket, Region: cfg.MediaRegion, AccessKey: cfg.MediaAccessKey,
			SecretKey: cfg.MediaSecretKey, PathStyle: cfg.MediaPathStyle, PublicBaseURL: cfg.MediaPublicURL}, clock.System{})
		if err != nil {
			return fmt.Errorf("media: %w", err)
		}
		deps.Media = store
		log.Info("product image uploads enabled", "bucket", cfg.MediaBucket, "endpoint", cfg.MediaEndpoint)
	}
	uc := usecase.NewSet(deps)

	runner, err := kafkax.NewRunner(kafkax.RunnerConfig{
		Brokers: cfg.KafkaBrokers, Group: consumer.Consumer, Topics: consumer.Topics, Log: log,
		Handler:   consumer.Handler(uc, tx, mongooutbox.NewGuard(store.DB())),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: producer, Metrics: m, Log: log}),
	})
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(cfg.ServiceName, m, log)
	catalogv1.RegisterCatalogServiceServer(srv, transport.NewServer(uc))
	lis, err := net.Listen("tcp", ":"+strconv.Itoa(cfg.GRPCPort))
	if err != nil {
		return fmt.Errorf("grpc listen: %w", err)
	}
	ops := httpx.NewOpsHandler(m.Handler(), map[string]httpx.Check{
		"mongodb": store.Ping,
		"redis":   func(ctx context.Context) error { return rdb.Ping(ctx).Err() },
		"kafka":   producer.Ping,
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
	log.Info("catalog-service started", "env", cfg.Env)
	err = g.Wait()
	log.Info("catalog-service stopped")
	return err
}
