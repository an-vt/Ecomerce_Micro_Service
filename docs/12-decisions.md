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

### D-017 gRPC ports 9101–9108 (not the 50051 convention)
- **Context:** On macOS, `launchd` holds a socket on `127.0.0.1:50051` (Apple's `gamepolicyd`), so a container publishing 50051 is shadowed on loopback: connects succeed but reach macOS, not the service. 50051–50058 also sit in the OS ephemeral port range.
- **Decision:** gRPC ports are 9101 (auth) … 9108 (notification), pairing with the ops HTTP ports 8101–8108.
- **Consequences:** Works on developer laptops and CI alike; the "50051" gRPC convention is not used anywhere in this repo.

### D-018 Public gateway port 8088 (not 3000)
- **Context:** Port 3000 is the default of most JavaScript dev servers (Next.js, Remotion Studio, CRA…). On a developer machine it is usually taken, and a published container port is then silently shadowed on loopback: requests reach the other process (found while testing: `/health/ready` answered with a Remotion Studio page).
- **Decision:** the gateway listens on 8088 (container and host). Grafana keeps 3001 for the same reason.
- **Consequences:** no clash with common tooling; `VERIFY_LINK_BASE`, OpenAPI `servers`, scripts and docs use 8088.

### D-019 Payment: the charge happens inside the message transaction, keyed by the order id
- **Context:** `ProcessPayment` must not charge twice, whatever Kafka redelivers or two replicas do, yet a provider call cannot be rolled back with a database transaction.
- **Decision:** the handler runs in one transaction: `INSERT … ON CONFLICT (order_id) DO NOTHING` first (a concurrent copy waits on the unique key, then reads the winner's outcome), then the provider call with the **order id as the provider's idempotency key**, then the status update and the saga reply. A crash anywhere rolls the whole transaction back and the retry asks the provider again with the same key, which a real provider answers with the same charge. A refund that arrives before the charge writes a `CANCELLED` tombstone so the late charge is refused. A declined card is an answer (`PaymentFailed{CARD_DECLINED}`); a provider that cannot be used (timeout, open circuit) is `PaymentFailed{PROVIDER_UNAVAILABLE}`; only our own infrastructure errors make the handler fail and retry. A refund the provider cannot do also fails and retries, because a compensation must never give up and leave a charge standing.
- **Consequences:** a database connection is held for up to the provider timeout (3 s) per payment — acceptable at this scale. **Known limit:** when the provider times out, the real outcome is unknown (the card may have been charged); with a real provider this needs a reconciliation job or a provider-side void. The mock cannot be in that state, so it is out of scope for now ([01 non-goals](01-requirements.md)).

### D-020 The saga decides, the use case executes — in one transaction
- **Context:** The checkout saga must be testable branch by branch, and a crash must never leave a command unsent, an event unannounced, or an order and its saga disagreeing.
- **Decision:** `CheckoutSaga` is a pure state machine: `OnReply` / `OnTimeout` return a `Result` (commands to send, order transitions to apply, whether the reply was stale) and touch no I/O. The use case locks the saga row and the order row, applies the result to the `Order` aggregate (whose status guard rejects any transition the saga should not make), and stores order, saga, outbox commands and the final order event in **one** transaction. Replies and the sweeper therefore serialise per order on the saga row lock; the sweeper takes sagas with `FOR UPDATE SKIP LOCKED` so several replicas handle each timeout once. Commands carry data from the order (items, the amount to charge after the discount `discount-service` actually applied, the payment token), not from the saga, which stays small. `PlaceOrder` claims the `Idempotency-Key` first (`ON CONFLICT DO NOTHING`), then writes order, saga and `ReserveStock` together.
- **Consequences:** Every row of [06 §4](06-checkout-saga.md#4-compensation) is a unit test, and each test also replays the result against the order guard. An order id that is not a UUID cannot exist, so it is "not found" / a permanent failure instead of a database error (found in the first run against real containers: old replies with non-UUID ids were retried five times each).

### D-021 Notification: idempotency by natural keys, the email record before the SMTP call
- **Context:** Kafka delivers at least once; an email cannot be rolled back, and a notification must not appear twice. The monolith's worker re-queued failures forever and its template renderer applied only the last placeholder.
- **Decision:** no inbox. A notification carries a unique `dedupeKey` (causing message id + type + receiver) and an email record a unique `dedupeKey` (message id + template). `SendEmail` writes the record first (`PENDING`), renders, calls SMTP, then marks `SENT` or `FAILED`: a redelivery of a sent message finds `SENT` and does nothing; a failed or unfinished one is retried on the same record. Bad addresses (CR/LF, lists), a missing template or a missing placeholder value are permanent errors (dead-letter at once); an SMTP error is retried by the consumer's backoff and then dead-lettered. `Render` fills **all** placeholders in a single pass (a value is never re-expanded), refuses a placeholder with no value instead of mailing a literal `{{token}}`, and HTML-escapes values in HTML bodies. Verification links that already expired are not sent (the topic is replayed from the start on a first run).
- **Consequences:** email delivery is at-least-once: a crash between the SMTP server accepting a message and the `SENT` mark can send it once more (the alternative, marking first, can lose it). Mailpit catches everything locally, so the whole path is tested end to end.

### D-022 Observability: derive business signals, root traces at the edge, do not trace the noise
- **Context:** Writing the dashboards and alerts against the running stack found that [14](14-observability.md) listed metrics nobody exported (`payments_total`, `auth_logins_total`, `inventory_reservations_total`, `cache_requests_total`), that Tempo was filled with one-span root traces from health checks and the outbox relay's polling (hundreds a minute per service, burying real traces), and that the gateway emitted no HTTP span, so a trace began at a gRPC client call and the access log had no `trace_id`.
- **Decision:** (1) No per-feature business counters: the Business dashboard derives logins, sign-ups, orders, payments and stock from `grpc_server_duration_seconds_count{service,method,code}` and the `saga_*` series; only signals calls cannot show get a counter (`auth_refresh_reuse_detected_total`, `rate_limited_total`). (2) The gateway roots each API trace with a server span named after the route and returns `X-Trace-Id`; it ignores a client's `traceparent` (public edge). (3) `otelx.Untraced` marks health checks, `/metrics`, unmatched paths and the relay's polling as not to be traced; the loggers print no trace id for them. (4) `scripts/check-observability.py` (in `make check-infra`) fails when a dashboard or alert uses a metric nothing exports and runs every query against a live Prometheus; the dashboards are generated from one Python file and checked against it.
- **Consequences:** Dashboards cannot silently rot, a checkout is one searchable trace with its logs, and Tempo holds traces worth reading. Business panels depend on RPC names, so renaming an RPC means updating `gen/dashboards.py` (the live check catches a query that returns an error, not one that returns nothing).

