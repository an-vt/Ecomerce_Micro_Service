# 03 — Project structure

Go microservices in one repository, laid out like `payment_gateway_golang`: a `go.work` workspace, one module per service, a shared `platform-go` library, a `proto` module, `shared-infra/infra/local` for data stores and one `infra/local` folder per service.

## 1. Repository layout

```
Ecomerce_Micro_Service/
├── README.md  Makefile  go.work  go.work.sum  .golangci.yml  .editorconfig
├── build/ci/github-actions.yml
├── docs/                         numbered design docs + PLAN.md
├── scripts/                      lib.sh start-all.sh stop-all.sh status.sh migrate-all.sh seed-all.sh coverage.sh gen-keys.sh check-infra.sh
├── shared-infra/infra/local/     data stores, broker, observability (compose project `ecom-shared`)
├── platform-go/                  shared library module (see §3)
├── proto/                        module: <svc>/v1/<svc>.proto, buf.yaml, buf.gen.yaml, gen/go/ (generated, committed)
├── api-gateway/                  ┐
├── auth-service/                 │
├── catalog-service/              │
├── inventory-service/            │ one Go module per service, see §1.1
├── cart-service/                 │
├── discount-service/             │
├── order-service/                │
├── payment-service/              │
├── notification-service/         ┘
├── e2e/                          module: black-box tests against the running stack (build tag `e2e`)
└── tools/newservice/             templates used by `make new-service`
```

Module path prefix: `github.com/an-vt/ecom/` (`.../platform-go`, `.../proto`, `.../order-service`, …). `go.work` lists every module, so cross-module imports resolve locally without `replace` directives.

### 1.1 Service module

```
<svc>/
├── go.mod
├── cmd/
│   ├── <svc>/main.go          server binary (gRPC + Kafka consumers + ops HTTP; gateway: Gin REST)
│   └── console/main.go        Laravel-style CLI: make:migration, migrate, migrate:rollback, migrate:fresh, migrate:status, db:seed
├── build/Dockerfile           multi-stage, ARG TARGET=<svc>|console
├── infra/local/               docker-compose.yml  Makefile  .env  .env.example
├── internal/
│   ├── domain/                aggregates, entities, value objects, domain events, domain errors, repository interfaces
│   ├── usecase/               one file per command / query; ports.go (interfaces for everything outside the process); set.go groups use cases
│   ├── repository/postgres/   bun repositories (or repository/mongo/ for Mongo services) + row models and mappers
│   ├── client/<peer>/         gRPC clients to other services (wrapped by gobreaker)
│   ├── transport/grpc/        gRPC server handlers
│   ├── transport/kafka/       consumers (+ command handlers for saga participants)
│   ├── transport/http/        Gin handlers (api-gateway only); other services: ops endpoints from platform-go
│   ├── config/                typed env config
│   ├── app/                   composition root: builds adapters, wires them into use cases, starts/stops everything
│   ├── database/
│   │   ├── migrations/        2026_10_30_000010_create_accounts_table.go …  (Laravel-style, see §4)
│   │   └── seeders/           DatabaseSeeder.go, RbacSeeder.go …
│   └── testenv/               fully wired service on a real DB with fakes for peers (integration tests)
└── test/
    └── integration/           (optional extra black-box tests per service)
```

Unit tests are `*_test.go` next to the code. Integration tests are `*_integration_test.go` with `//go:build integration`.

## 2. DDD layering

```
transport (grpc, kafka, http) ──▶ usecase ──▶ domain
        └── wired in cmd/*/main.go via internal/app ──▶ repository/*, client/*, platform-go adapters
```

| Package | May import | Must not import |
|---|---|---|
| `internal/domain/**` | std lib, `platform-go/{money,errs,idgen,clock}` | `database/sql`, `bun`, `mongo-driver`, `net/http`, `gin`, `grpc`, `franz-go`, `redis`, `usecase`, `repository`, `transport`, `client` |
| `internal/usecase/**` | `domain`, std lib, `platform-go/{errs,clock,idgen,event}` | `bun`, `mongo-driver`, `database/sql`, `net/http`, `gin`, `grpc`, `franz-go`, `redis`, `repository`, `transport`, `client` |
| `internal/repository/**`, `internal/client/**` | `domain`, `usecase` (to implement ports), `platform-go/*` | `transport` |
| `internal/transport/**` | `usecase`, `domain` (DTO mapping), `platform-go/*` | `repository`, `client` (go through use-case ports) |

Enforced by `depguard` in `.golangci.yml` (same approach as the reference project); `make lint` fails on a violation. Concrete types are created only in `cmd/*/main.go` and `internal/app`.

### 2.1 Building blocks

| Block | Convention | Examples |
|---|---|---|
| Aggregate root | struct with unexported fields; `New…` (new) and `Restore…` (from persistence) constructors; methods enforce invariants and append to `events`; `PullEvents()` | `Order`, `Inventory`, `Discount`, `Account`, `Session`, `Product`, `Cart`, `Payment` |
| Value object | immutable type with validating constructor, `Equals` | `Money`, `Email`, `Quantity`, `DateRange` |
| Domain event | plain struct implementing `event.Event` (`Type()`, `AggregateID()`) | `ProductCreated` |
| Repository | interface in `domain/` (or `usecase/ports.go` when it serves a single use case) | `OrderRepository` |
| Domain error | `errs.New(code, msg)`; codes map to gRPC status and HTTP status | `INSUFFICIENT_STOCK` |
| Use case | struct with a method `Execute(ctx, Input) (Output, error)`; depends only on ports | `PlaceOrder` |
| Port | small interface in `usecase/ports.go` | `Catalog`, `TxManager`, `Outbox`, `Clock` |
| State machine | transition table + guard method | `Order`, `CheckoutSaga`, `Payment`, `Reservation` |

### 2.2 Unit of Work and outbox

The transaction travels in the `context.Context`, so use cases and repositories never see `bun.Tx` or a Mongo session:

```go
err := uc.tx.WithinTx(ctx, func(ctx context.Context) error {
    o, err := domain.PlaceOrder(...)
    if err != nil { return err }
    if err := uc.orders.Save(ctx, o); err != nil { return err }      // repository reads the tx from ctx
    return uc.outbox.Add(ctx, event.FromDomain(o.PullEvents())...)    // same transaction
})
```

- PostgreSQL: `pgdb.TxManager` implements `WithinTx` with `db.RunInTx`; `pgdb.DB(ctx)` returns the `bun.IDB` (tx when present). Retries on serialization failure / deadlock (`40001`, `40P01`).
- MongoDB: `mongox.TxManager` uses `client.UseSession` + `WithTransaction`; the session context is passed on.

## 3. Shared library — `platform-go`

| Package | Contents |
|---|---|
| `config` | env + `<NAME>_FILE` secret loading into typed structs, validation |
| `logger` | `slog` JSON handler, redaction, context fields (`trace_id`, `span_id`, `request_id`, `service`) |
| `otelx` | tracer provider + OTLP exporter, W3C propagator, `Inject` / `Extract` of trace context for Kafka and outbox headers |
| `httpx` | ops server (`/health/live`, `/health/ready`, `/metrics`), graceful `Serve`, `-healthcheck` flag support. (Gin middleware and the JSON envelope live in `api-gateway/internal/transport/http`, the only Gin user.) |
| `grpcx` | gRPC server / client bootstrap: server chain (recover, request id + principal, metrics, error mapping, logging), client interceptor (deadline, breaker per attempt, read retries, status → `errs`), `ToStatus` / `FromStatus` carrying the domain code in `ErrorInfo` |
| `principal` | authenticated caller (`ID`, `Type`, `Roles`, `SessionID`) in `context.Context`; safe to import from use cases |
| `breaker` | `sony/gobreaker/v2` wrapper: per-(target, method) registry, failure classifier, state gauge |
| `pgdb` | bun open (pgdialect + pgdriver), pool config, `TxManager`, `DB(ctx)`, error helpers (`IsUniqueViolation`), otel hook (`bunotel`) |
| `mongox` | client open, `TxManager`, `DB(ctx)`, error helpers (`IsDuplicateKey`), otel monitor |
| `redisx` | go-redis client, `SetNX`, fixed-window rate limiter, cache helpers |
| `kafkax` | franz-go producer (idempotent) and consumer runner (manual commit, retry, DLQ, `kotel` tracing), topic constants |
| `outbox` | `Message`, `Writer`, `Relay` (storage-agnostic loop); `outbox/pgoutbox` and `outbox/mongooutbox` hold the stores, writers, inbox guards and the `CreateTables` / `CreateCollections` migrations |
| `inbox` | `Guard.Once(ctx, consumer, messageID, fn)` |
| `database/migration` | Laravel-style migration engine (generic), `pgmigrate` (PostgreSQL store + registry), `mongomigrate` (MongoDB store + registry) (§4) |
| `database/seeder` | seeder registry (`DatabaseSeeder` entry point, `Call`, `--class`) |
| `database/console` | the `console` command line: migrate:*, db:seed, make:migration, make:seeder |
| `errs` | typed error codes → gRPC status → HTTP status |
| `event` | message envelope, builders |
| `money`, `idgen` (ULID/UUID), `clock`, `retry` (backoff + jitter), `rbac` (grants evaluator, `accesscontrol` vocabulary), `jwtx` (RS256 sign / verify / JWKS), `pagination` |
| `metrics` | Prometheus registry and the standard metrics from [14 §2](14-observability.md#2-metrics) |
| `testkit` (build tag `integration`) | testcontainers singletons (postgres, mongo replica set, kafka, redis), `testkit.PG(t)` / `testkit.Mongo(t)` fresh DB per test, `testkit.Topic(t)`, fakes (`FakeTx`, `FakeOutbox`, `FixedClock`), in-process gRPC server helper |

## 4. Database migrations (Laravel-style)

Same developer experience as the reference project's `console` binary, which mirrors `php artisan`:

| Laravel | Ours (`console` binary inside each service) |
|---|---|
| `make:migration create_accounts_table` | `console make:migration create_accounts_table` (`--create=accounts` / `--table=accounts`) |
| `migrate` | `console migrate` (`--force` required when `APP_ENV=production`, `--pretend` lists the pending migrations without running them) |
| `migrate:rollback --step=2` | `console migrate:rollback [--step=N]` (default: last batch) |
| `migrate:reset` / `refresh` / `fresh` | `console migrate:reset` / `migrate:refresh [--seed]` / `migrate:fresh [--seed]` (fresh refused in production) |
| `migrate:status` | `console migrate:status` |
| `make:seeder RbacSeeder` / `db:seed --class=X` | `console make:seeder RbacSeeder` / `console db:seed [--class=X]` |

Locally: `make -C auth-service/infra/local migrate | rollback STEP=1 | fresh | seed CLASS=RbacSeeder`.

### 4.1 File naming and registration

- Migrations are **Go files** in `internal/database/migrations/`, named `YYYY_MM_DD_HHMMSS_<snake_name>.go` (e.g. `2026_10_30_000010_create_accounts_table.go`). The file name (without `.go`) is the migration name; lexical order = run order.
- Each file has a struct with `Name()`, `Up` and `Down`, and registers itself in `init()`.
- Tracking table (PostgreSQL; a collection with the same fields in Mongo services):
  ```sql
  CREATE TABLE migrations (
    id        BIGSERIAL PRIMARY KEY,
    migration VARCHAR(255) NOT NULL UNIQUE,
    batch     INT NOT NULL,
    ran_at    TIMESTAMPTZ NOT NULL DEFAULT now()
  );
  ```
- `migrate` runs all pending migrations in one **batch** (`max(batch)+1`); `rollback` runs `Down` of the last batch in reverse order.
- `pg_advisory_lock(hashtext('<db>:migrations'))` prevents two containers migrating at once (Mongo services: a lock document with TTL).
- PostgreSQL DDL is transactional, so **each migration runs in its own transaction** together with its `migrations` row: it fully applies or not at all (the reference project's MySQL cannot do this). Statements that cannot run in a transaction (`CREATE INDEX CONCURRENTLY`) set `NoTransaction() bool { return true }`.
- Production: migrations run as a one-off container (`<svc>-console migrate --force`) **before** new app containers start; they must be backward compatible with the previous release.

### 4.2 Example (PostgreSQL, bun)

```go
// auth-service/internal/database/migrations/2026_10_30_000010_create_accounts_table.go
package migrations

import (
	"context"

	"github.com/uptrace/bun"

	"github.com/an-vt/ecom/platform-go/database/migration/pgmigrate"
)

func init() { pgmigrate.Register(&createAccountsTable{}) }

type createAccountsTable struct{}

func (m *createAccountsTable) Name() string { return "2026_10_30_000010_create_accounts_table" }

func (m *createAccountsTable) Up(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE accounts (
			id            UUID PRIMARY KEY,
			type          VARCHAR(8)   NOT NULL,
			email         VARCHAR(320) NOT NULL,
			name          VARCHAR(150) NOT NULL,
			password_hash VARCHAR(100),
			status        VARCHAR(16)  NOT NULL DEFAULT 'pending',
			verified      BOOLEAN      NOT NULL DEFAULT FALSE,
			roles         TEXT[]       NOT NULL DEFAULT '{}',
			created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
			updated_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
			CONSTRAINT uq_accounts_type_email UNIQUE (type, email)
		)`)
	return err
}

func (m *createAccountsTable) Down(ctx context.Context, db bun.IDB) error {
	_, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS accounts`)
	return err
}
```

Mongo services use the same shape (`mongomigrate.Register`, `Up(ctx, db *mongo.Database)`) — creating collections, validators, indexes (`createIndexes` is idempotent) — so every service has the same `console` commands.

## 5. Naming

| Item | Convention |
|---|---|
| Go packages | short, lowercase, no underscores |
| Files | `snake_case.go`; tests `<file>_test.go`; integration `<file>_integration_test.go` |
| gRPC package | `ecom.<svc>.v1`, service `<Svc>Service` |
| Kafka topic | events `<context>.<aggregate>.<past-tense>`; commands `<context>.commands`; replies `<orchestrator>.saga.replies`; DLQ `<topic>.dlq` |
| DB | `<svc>_db`, user `<svc>`; tables snake_case plural |
| Env | `UPPER_SNAKE`, secrets also accept `<NAME>_FILE` |
| Binaries / containers | `<svc>`, `console`; containers `ecom-<short>` |
