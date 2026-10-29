// Package app is the composition root of notification-service: it builds the adapters (MongoDB, the SMTP sender), wires
// the use cases and runs the gRPC server, the Kafka consumer and the ops HTTP server. The service publishes nothing, so
// it has no outbox.
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

	smtpclient "github.com/an-vt/ecom/notification-service/internal/client/smtp"
	"github.com/an-vt/ecom/notification-service/internal/config"
	_ "github.com/an-vt/ecom/notification-service/internal/database/migrations" // registers the migrations
	mongorepo "github.com/an-vt/ecom/notification-service/internal/repository/mongo"
	transport "github.com/an-vt/ecom/notification-service/internal/transport/grpc"
	consumer "github.com/an-vt/ecom/notification-service/internal/transport/kafka"
	"github.com/an-vt/ecom/notification-service/internal/usecase"
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
	notificationv1 "github.com/an-vt/ecom/proto/gen/go/notification/v1"
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

	producer, err := kafkax.NewProducer(cfg.KafkaBrokers) // the dead-letter writer
	if err != nil {
		return err
	}
	defer producer.Close()

	sender, err := smtpclient.New(smtpclient.Config{Addr: cfg.SMTPAddr(), From: cfg.MailFrom, Username: cfg.SMTPUser, Password: cfg.SMTPPass, Timeout: cfg.SMTPTimeout})
	if err != nil {
		return err
	}
	db := store.DB()
	uc := usecase.NewSet(&usecase.Deps{Notifications: mongorepo.NewNotifications(db), Templates: mongorepo.NewTemplates(db), Emails: mongorepo.NewEmails(db),
		Sender: sender, Clock: clock.System{}, IDs: idgen.ULID{}})
	// the emails the service sends on its own need their templates; an administrator's edits are never overwritten
	created, err := uc.EnsureTemplates.Execute(ctx)
	if err != nil {
		return fmt.Errorf("default templates: %w", err)
	}
	if created > 0 {
		log.Info("default email templates created", "count", created)
	}

	runner, err := kafkax.NewRunner(kafkax.RunnerConfig{
		Brokers: cfg.KafkaBrokers, Group: consumer.Consumer, Topics: consumer.Topics, Log: log, Handler: consumer.Handler(uc),
		Processor: kafkax.NewProcessor(kafkax.ProcessorConfig{DLQ: producer, Metrics: m, Log: log}),
	})
	if err != nil {
		return err
	}

	srv := grpcx.NewServer(cfg.ServiceName, m, log)
	notificationv1.RegisterNotificationServiceServer(srv, transport.NewServer(uc))
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
	log.Info("notification-service started", "env", cfg.Env, "smtp", cfg.SMTPAddr())
	err = g.Wait()
	log.Info("notification-service stopped")
	return err
}
