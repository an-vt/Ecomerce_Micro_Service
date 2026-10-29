# 12 — Decision log

Format: context → decision → consequences. All **Accepted** (2026-10-29) unless noted.

### D-001 Nine deployables: gateway + auth + seven domain services
- **Context:** The monolith mixes identity, catalog, stock, cart, discount, checkout, comments, notifications and an email worker in one Express app plus a RabbitMQ worker. Order placement and payment are missing.
- **Decision:** api-gateway, auth, catalog (incl. SPU/SKU and comments), inventory, cart, discount, order (saga orchestrator), payment (new), notification (incl. email, replacing the worker).
- **Consequences:** Independent deploy and failure; requires outbox/inbox, saga, breakers. Comments are not a separate service (low volume, only references products).

### D-002 Polyglot persistence, one database per service
- **Context:** Products vary a lot per type (free-form attributes, variations); money and stock need ACID and constraints. The monolith used MongoDB for everything and could not express "stock ≥ 0" or "one usage per order".
- **Decision:** PostgreSQL (via **uptrace/bun**) for auth, inventory, discount, order, payment. MongoDB (official `mongo-driver/v2`, replica set) for catalog, cart, notification. Redis for cache / rate limit / denylist.
- **Consequences:** Right tool per context; two outbox/inbox implementations in `platform-go`; developers need both stores locally (one compose).

### D-003 REST outside, gRPC inside
- **Context:** Clients expect REST (the monolith API). Internal calls need typed contracts and deadlines.
- **Decision:** Gateway exposes REST with **Gin** and translates to gRPC (`google.golang.org/grpc`). Contracts in `proto/`, generated with `buf`.
- **Consequences:** Gateway has explicit controllers per route (not a blind proxy) — more code, but validation, auth and error mapping in one place.

### D-004 Kafka replaces RabbitMQ
- **Context:** Need durable, ordered-per-key, replayable events plus saga command channels.
- **Decision:** Kafka (KRaft). Key = aggregate id. **franz-go** client.
- **Consequences:** Per-key ordering makes forward/compensation ordering safe ([06 §4.1](06-checkout-saga.md#41-why-this-is-safe)); no per-message delay → retry design in D-006.

### D-005 Transactional outbox + inbox
- **Context:** The monolith published after commit (or not at all) — lost messages.
- **Decision:** Outbox row in the same transaction as the state change; relay publishes; consumers dedupe with an inbox row in their own transaction.
- **Consequences:** At-least-once + idempotency = effectively once. Relay latency ~200 ms.

### D-006 In-process retry then DLQ (no retry topics)
- **Context:** Kafka cannot delay a single message; retry topics break per-key ordering for saga traffic.
- **Decision:** Exponential backoff inside the consumer (max 5 attempts), then `<topic>.dlq`; non-retryable domain errors go to DLQ immediately.
- **Consequences:** A poison message blocks its partition for at most ~10 s. DLQ alert + manual replay.

### D-007 Orchestrated (not choreographed) checkout saga
- **Context:** Checkout touches inventory, discount, payment with compensation rules and a pivot (payment).
- **Decision:** order-service owns a `CheckoutSaga` state machine, sends commands, receives replies.
- **Consequences:** The whole flow is visible in one place and unit-testable; order-service knows the participants' commands (acceptable — it owns the process).

### D-008 Circuit breaker per (upstream, method) with sony/gobreaker
- **Context:** One slow dependency must not exhaust the gateway or callers.
- **Decision:** a gRPC client interceptor wraps every RPC; only infrastructure errors count; open → 503 fast.
- **Consequences:** Clear failure mode; business errors never trip breakers.

### D-009 Product = one aggregate, one document
- **Context:** The monolith wrote a subtype document (`clothes`/`electronic`/`furniture`) and a parent `Products` document with the same `_id`, non-transactionally; attributes were duplicated.
- **Decision:** Keep the strategy registry (`ProductTypeRegistry`) for per-type validation and behaviour, but store attributes inside the product document.
- **Consequences:** Atomic writes, one read per product; type-specific queries use `attributes.<field>` indexes if needed.

### D-010 RS256 access tokens + opaque rotating refresh tokens
- **Context:** Per-user HS256 secrets in DB force a DB read per request and cannot be verified by a gateway.
- **Decision:** auth signs with one RS256 key (kid), gateway verifies via JWKS; refresh tokens opaque, hashed, rotated with reuse detection; logout → Redis denylist by `sid`.
- **Consequences:** Stateless verification; revocation latency ≤ one Redis lookup.

### D-011 Stock reservation by conditional UPDATE instead of Redis lock
- **Context:** Monolith `acquireLock` used `SETNX` and only set the TTL after a successful reservation — a failed reservation leaked the lock forever; the reservation field name was wrong so nothing was recorded.
- **Decision:** PostgreSQL `UPDATE inventories SET stock = stock - q, reserved = reserved + q WHERE product_id = $1 AND stock >= q` + `reservations` rows with TTL.
- **Consequences:** No distributed lock; correctness from the DB; proven by a concurrency integration test.

### D-012 Trust boundary at the gateway (no mTLS yet)
- **Context:** Local / single-host deployment.
- **Decision:** Internal ports only on `ecom_net`; principal forwarded in gRPC metadata.
- **Consequences:** Simple. **Future:** mTLS or signed internal tokens before multi-host deployment.

### D-013 Go workspace, one module per service
- **Context:** Services must be independently buildable; shared code must not be copy-pasted; the reference project (`payment_gateway_golang`) already uses this layout.
- **Decision:** `go.work` with one module per service plus `platform-go` and `proto`. Service folders follow `cmd/ internal/{domain,usecase,repository,client,transport,app,config,database} infra/local`.
- **Consequences:** Each service has its own `go.mod` and Dockerfile; local builds use the workspace, Docker builds copy only `platform-go`, `proto` and the service.

### D-014 Observability: OpenTelemetry + Prometheus + Loki + Tempo + Grafana
- **Context:** A saga crosses 4 services and Kafka; debugging needs one trace and correlated logs.
- **Decision:** OTel SDK with `otelgrpc`, `otelhttp`, `bunotel`, `otelmongo`, `kotel`, `redisotel`; trace context in Kafka headers and outbox rows; slog JSON logs with `trace_id`; Prometheus client metrics; Alloy ships logs and traces.
- **Consequences:** Trace ↔ logs ↔ metrics navigation in Grafana. Small runtime overhead.

### D-015 Go as implementation language (replaces an earlier NestJS draft)
- **Context:** First draft targeted NestJS; the project owner chose Go, matching `payment_gateway_golang` (go.work, `platform-go`, gobreaker, testcontainers-go, buf/sqlc style tooling). Architecture decisions D-001…D-012 are language-independent and unchanged.
- **Decision:** Go 1.26 toolchain; Gin (gateway), bun + pgdriver (PostgreSQL), `mongo-driver/v2`, franz-go, go-redis v9, gobreaker v2, golang-jwt v5, testify, testcontainers-go.
- **Consequences:** Single static binaries and small images; DDD boundaries enforced with depguard instead of ESLint.

### D-016 Laravel-style migrations and seeders
- **Context:** The team knows `php artisan migrate`; the reference project provides the same workflow for Go.
- **Decision:** Timestamped Go migration files (`2026_10_30_000010_create_accounts_table.go`) with `Up` / `Down`, batches in a `migrations` table, a `console` binary per service (`make:migration`, `migrate`, `migrate:rollback`, `migrate:fresh`, `migrate:status`, `db:seed`). Each PostgreSQL migration runs in its own transaction; Mongo services use the same runner for collections and indexes.
- **Consequences:** Uniform operations across 8 stateful services; migrations are code, so they are covered by the integration tests (up → down → up).
