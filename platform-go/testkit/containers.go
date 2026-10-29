//go:build integration

// Package testkit starts real PostgreSQL / MongoDB / Kafka / Redis with testcontainers-go for integration tests
// (docs/10-testing.md §2). One container per test binary (sync.Once); every test gets its own database / topic
// suffix, so tests are isolated and can run in any order. The Ryuk reaper removes containers when the binary exits.
package testkit

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tckafka "github.com/testcontainers/testcontainers-go/modules/kafka"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/uptrace/bun/driver/pgdriver"
)

// Suffix returns a short unique string for topics, consumer groups, key prefixes and database names.
func Suffix(t testing.TB) string {
	t.Helper()
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

var (
	pgOnce sync.Once
	pgBase string // DSN of the maintenance database
	pgErr  error
)

func startPostgres() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("postgres"), tcpostgres.WithUsername("postgres"), tcpostgres.WithPassword("postgres"),
		tcpostgres.BasicWaitStrategies(),
		testcontainers.WithCmd("postgres", "-c", "max_connections=300"))
	if err != nil {
		return "", err
	}
	return c.ConnectionString(ctx, "sslmode=disable")
}

// PostgresDSN creates a fresh, empty database on a shared PostgreSQL container and returns its DSN.
// The database is dropped when the test ends.
func PostgresDSN(t testing.TB) string {
	t.Helper()
	pgOnce.Do(func() { pgBase, pgErr = startPostgres() })
	if pgErr != nil {
		t.Fatalf("start postgres container: %v", pgErr)
	}
	name := "t_" + Suffix(t)
	admin := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(pgBase)))
	defer func() { _ = admin.Close() }()
	if _, err := admin.Exec(fmt.Sprintf(`CREATE DATABASE %q`, name)); err != nil {
		t.Fatalf("create database: %v", err)
	}
	t.Cleanup(func() {
		a := sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(pgBase)))
		defer func() { _ = a.Close() }()
		_, _ = a.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS %q WITH (FORCE)`, name))
	})
	u, err := url.Parse(pgBase)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.Path = "/" + strings.TrimPrefix(name, "/")
	return u.String()
}

var (
	mongoOnce sync.Once
	mongoBase string
	mongoErr  error
)

func startMongo() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// mongo:8.2 — 8.0 refuses to start on Linux kernels >= 6.19 (SERVER-121912).
	c, err := tcmongo.Run(ctx, "mongo:8.2", tcmongo.WithReplicaSet("rs0"))
	if err != nil {
		return "", err
	}
	uri, err := c.ConnectionString(ctx)
	if err != nil {
		return "", err
	}
	// The replica set advertises the container's internal address, which the host cannot reach: connect to the
	// mapped port directly instead of discovering members. Transactions work on a directly-connected primary.
	sep := "?"
	if strings.Contains(uri, "?") {
		sep = "&"
	} else if !strings.HasSuffix(uri, "/") {
		sep = "/?"
	}
	return uri + sep + "directConnection=true", nil
}

// MongoURI returns a connection string (replica set, so transactions work) and the name of a fresh database.
// The database is dropped when the test ends.
func MongoURI(t testing.TB) (uri, database string) {
	t.Helper()
	mongoOnce.Do(func() { mongoBase, mongoErr = startMongo() })
	if mongoErr != nil {
		t.Fatalf("start mongodb container: %v", mongoErr)
	}
	return mongoBase, "t_" + Suffix(t)
}

var (
	kafkaOnce    sync.Once
	kafkaBrokers []string
	kafkaErr     error
)

func startKafka() ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	c, err := tckafka.Run(ctx, "confluentinc/confluent-local:7.8.0", tckafka.WithClusterID("ecom-test")) // the module only supports Confluent images
	if err != nil {
		return nil, err
	}
	return c.Brokers(ctx)
}

// KafkaBrokers returns the bootstrap servers of a shared single-broker Kafka (auto topic creation is on).
// Use Suffix to make topic and consumer-group names unique per test.
func KafkaBrokers(t testing.TB) []string {
	t.Helper()
	kafkaOnce.Do(func() { kafkaBrokers, kafkaErr = startKafka() })
	if kafkaErr != nil {
		t.Fatalf("start kafka container: %v", kafkaErr)
	}
	return kafkaBrokers
}

var (
	redisOnce sync.Once
	redisAddr string
	redisErr  error
)

func startRedis() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	c, err := tcredis.Run(ctx, "redis:7.4-alpine")
	if err != nil {
		return "", err
	}
	uri, err := c.ConnectionString(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(uri, "redis://"), nil
}

// RedisAddr returns host:port of a shared Redis. Use Suffix as a key prefix to isolate tests.
func RedisAddr(t testing.TB) string {
	t.Helper()
	redisOnce.Do(func() { redisAddr, redisErr = startRedis() })
	if redisErr != nil {
		t.Fatalf("start redis container: %v", redisErr)
	}
	return redisAddr
}

// CreateTopics creates topics (3 partitions) and their .dlq, like scripts in shared-infra do. The production
// brokers have auto-creation disabled, so tests must create what they use.
func CreateTopics(t testing.TB, topics ...string) {
	t.Helper()
	brokers := KafkaBrokers(t)
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		t.Fatalf("kafka admin client: %v", err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	all := make([]string, 0, len(topics)*2)
	for _, tp := range topics {
		all = append(all, tp, tp+".dlq")
	}
	res, err := adm.CreateTopics(ctx, 3, 1, nil, all...)
	if err != nil {
		t.Fatalf("create topics: %v", err)
	}
	for _, r := range res {
		if r.Err != nil {
			t.Fatalf("create topic %s: %v", r.Topic, r.Err)
		}
	}
}

// ConsumeN reads topic from the beginning with a throw-away consumer and returns the first n records (fails the
// test after timeout). Use it to assert what was really published.
func ConsumeN(t testing.TB, topic string, n int, timeout time.Duration) []*kgo.Record {
	t.Helper()
	cl, err := kgo.NewClient(kgo.SeedBrokers(KafkaBrokers(t)...), kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatalf("kafka consumer: %v", err)
	}
	defer cl.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var out []*kgo.Record
	for len(out) < n && ctx.Err() == nil {
		cl.PollFetches(ctx).EachRecord(func(r *kgo.Record) { out = append(out, r) })
	}
	if len(out) < n {
		t.Fatalf("read %d of %d records from %s before the timeout", len(out), n, topic)
	}
	return out
}

// Header returns the value of a record header, or "".
func Header(r *kgo.Record, key string) string {
	for _, h := range r.Headers {
		if h.Key == key {
			return string(h.Value)
		}
	}
	return ""
}
