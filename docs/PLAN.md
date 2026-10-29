# Implementation Plan

Implement phases in order, task by task. Each task names what to build and the doc section it implements. A phase is done only when its acceptance tests pass and the Global Definition of Done holds.

Conventions: `<svc>` = service folder; module path prefix `github.com/an-vt/ecom/`; "design §" links point to the docs. Stack: Go 1.26, Gin, bun (PostgreSQL), mongo-driver/v2, franz-go, go-redis v9, gobreaker v2, gRPC + buf, testcontainers-go ([12 D-015](12-decisions.md)).

## Global Definition of Done (every phase)

- [ ] `make lint` clean (`golangci-lint` incl. `depguard` layer rules, `go vet`).
- [ ] Unit tests (`-race`) pass; **coverage ≥ 80 %** for each touched module (`make cover`), and `domain` / `usecase` ≥ 80 % on their own.
- [ ] Integration tests (testcontainers) for repositories, migrations (up → down → up), outbox / inbox, consumers, gRPC handlers.
- [ ] `.proto` updated and regenerated (`make gen-proto`); error codes from [05 §2.1](05-api-design.md#21-errors).
- [ ] Laravel-style migrations (+ seeders where needed) exist for every new table / collection; `make fresh` works locally ([03 §4](03-project-structure.md#4-database-migrations-laravel-style)).
- [ ] `<svc>/infra/local` (compose, Makefile, `.env.example`) updated; `/health/*` and `/metrics` work; traces flow ([14](14-observability.md)).
- [ ] Logs structured and redacted.
- [ ] `./scripts/start-all.sh` still brings the whole stack up healthy.
- [ ] This file ticked and docs updated when behaviour changed.

## Phase overview

| Phase | Name | Depends on | Outcome |
|---|---|---|---|
| P0 | Docs & repo foundations | — | Docs, `go.work`, lint config, Makefile, scripts, CI, service template |
| P1 | Shared infra | P0 | `make up` starts Postgres, Mongo rs, Redis, Kafka, Mailpit, observability stack |
| P2 | `platform-go` | P0 | Config, logger, otel, errs, breaker, grpcx, httpx, pgdb, mongox, redisx, kafkax, outbox, inbox, migration runner, rbac, jwtx, testkit |
| P3 | auth-service | P1, P2 | Accounts, sessions, OTP, API keys, RBAC, JWKS |
| P4 | api-gateway | P3 | Gin REST → gRPC, middleware, rate limit, breaker → 503, OpenAPI (routes added per phase) |
| P5 | catalog-service | P2, P3 | Products, SPU/SKU, search, comments, events |
| P6 | inventory-service | P5 | Stock, reservations, saga participant |
| P7 | cart-service | P5, P6 | Cart CRUD, clears on confirmed order |
| P8 | discount-service | P5 | Discounts, calculation, saga participant |
| P9 | payment-service | P2 | Mock provider + breaker, saga participant |
| P10 | order-service | P5–P9 | Review, place order, `CheckoutSaga`, sweeper |
| P11 | notification-service | P3, P5, P8, P10 | In-app + email notifications |
| P12 | Dashboards & alerts | P2–P11 | Grafana dashboards, Prometheus rules validated |
| P13 | Media (optional) | P5 | Presigned upload via S3 port (S3-compatible store chosen then) |
| P14 | E2E & hardening | all | `e2e/` suites, coverage gate, CI, README |

---

## P0 — Docs & repo foundations

**Goal:** a skeleton every service copies.

### Tasks
- [x] **P0.1 Docs** — `docs/01…14` and this plan.
- [x] **P0.2 Repo skeleton** — `README.md` (doc index with a "What it answers" column), `go.work`, `.golangci.yml` (depguard rules from [03 §2](03-project-structure.md#2-ddd-layering)), `.editorconfig`, `.gitignore`.
- [x] **P0.3 Makefile** — `install lint fmt test test-integration test-e2e cover gen-proto check-infra start stop status seed new-service`; each target loops over the modules in `go.work`.
- [x] **P0.4 Scripts** — `scripts/{lib,start-all,stop-all,status,migrate-all,seed-all,coverage,gen-keys,check-infra}.sh` (written).
- [x] **P0.5 Service template** — `tools/newservice/templates` + `make new-service NAME= SHORT= HTTP= GRPC= DB=pg|mongo|none` generating `go.mod`, `cmd/<svc>`, `cmd/console`, `build/Dockerfile`, `infra/local/*`, empty `internal/*` tree.
- [x] **P0.6 CI** — `build/ci/github-actions.yml`: lint, vet, unit + coverage gate, integration, check-infra.
- [x] **P0.7 Proto module** — `proto/go.mod`, `buf.yaml`, `buf.gen.yaml` (local plugins via `go run`), `make gen-proto`.

### Acceptance
| # | Check | Expectation |
|---|---|---|
| 1 | `make new-service` then `go build ./...` in the new module | builds |
| 2 | A `domain` file importing `github.com/uptrace/bun` | `make lint` fails (depguard) |

---

## P1 — Shared infra

### Tasks ([11 §1](11-infrastructure.md#1-shared-infra--shared-infralocal-compose-project-ecom-shared))
- [x] **P1.1** `shared-infra/infra/local/docker-compose.yml` — postgres, mongo (keyfile, rs0), redis, kafka (KRaft), kafka-init, kafka-ui, mailpit; healthchecks; external network `ecom_net`.
- [x] **P1.2** `postgres/init/01-databases.sh` (DB + user per PG service, identifiers quoted — `order` is reserved), `mongo/init-replica.sh` + `init-users.js`, `kafka/create-topics.sh` (all topics + `.dlq`, [08 §1](08-messaging.md#1-topics)).
- [x] **P1.3** Observability profile — prometheus (scrape config + rules dir), alertmanager, grafana (datasource provisioning), loki, tempo, alloy (docker log discovery + OTLP), exporters.
- [x] **P1.4** `.env.example`, `Makefile` (`env network up down reset logs ps psql mongosh topics`).

### Acceptance
| # | Check | Expectation |
|---|---|---|
| 1 | `make -C shared-infra/infra/local up` | all containers healthy |
| 2 | `docker exec ecom-mongo mongosh --eval 'rs.status().ok'` | `1` |
| 3 | topics listing | every topic and DLQ present |
| 4 | Grafana datasources | Prometheus, Loki, Tempo healthy |

---

## P2 — `platform-go`

### Tasks ([03 §3](03-project-structure.md#3-shared-library--platform-go), [08](08-messaging.md), [09](09-resilience.md))
- [x] **P2.1 Basics** — `errs` (codes → gRPC → HTTP), `event` (envelope), `money`, `idgen`, `clock`, `retry`, `pagination`.
- [x] **P2.2 Config & logger** — env + `_FILE` loading with validation; slog JSON handler with redaction and trace fields.
- [x] **P2.3 Observability** — `otel.Init`, `metrics` registry + standard metrics, `httpx` ops server (`/health/*`, `/metrics`).
- [x] **P2.4 Breaker + grpcx** — `breaker` registry (gobreaker v2, failure classifier, state gauge); gRPC server / client bootstrap with interceptors (deadline, tracing, logging, metrics, error mapping, principal metadata, breaker, read retries).
- [ ] **P2.5 Databases** *(pgdb done; mongox, redisx pending)* — `pgdb` (bun open, `TxManager`, `DB(ctx)`, `bunotel`), `mongox` (client, `TxManager`, otel), `redisx`.
- [ ] **P2.6 Laravel-style migrations** *(engine, console, seeders, PostgreSQL store done; Mongo store pending)* — `database/migration` (registry, runner, batches, advisory lock, per-migration tx, `--pretend`, status, rollback, fresh), `database/seeder`, `console` command framework shared by all services, generator for `make:migration` / `make:seeder`.
- [ ] **P2.7 Messaging** — `kafkax` producer + `ConsumerRunner` (manual commit, retry/backoff, DLQ, non-retryable errors, `kotel`); `outbox` (PG + Mongo writers and relays); `inbox` (PG + Mongo).
- [ ] **P2.8 Security helpers** — `jwtx` (RS256 sign/verify, JWKS), `rbac` evaluator.
- [ ] **P2.9 `testkit`** *(PostgreSQL container done; Mongo / Kafka / Redis pending)* — container singletons, fresh DB per test, `MigrateUp`, topic suffix, fakes, in-process gRPC helper; `e2ekit`.

### Acceptance (integration)
| # | Scenario | Expectation |
|---|---|---|
| 1 | PG `WithinTx`: return error after `outbox.Add` | no outbox row |
| 2 | PG relay: commit then relay | message on Kafka with `traceparent`, row `SENT` |
| 3 | Two relays on one table | each row published once |
| 4 | Mongo tx + relay | same as 1–3 |
| 5 | Same `messageId` consumed twice | handler effect once, second counted `duplicate` |
| 6 | Handler fails 5× | message on `<topic>.dlq` with `x-error`; offset committed |
| 7 | Breaker: 10 `UNAVAILABLE` then call | `ErrOpenState` without network; `NOT_FOUND` never opens |
| 8 | Migrations: `migrate`, `rollback`, `migrate` | same schema; two concurrent `migrate` runs → one applies |

---

## P3 — auth-service

### Tasks ([07](07-auth-security.md), [04 §1](04-data-model.md#1-auth_db-postgresql))
- [ ] **P3.1 Domain** — `Account` (shop / user, password, status), `Session` (rotate, reuse detection, revoke), `Otp`, `ApiKey`, `Role` / `Resource` / `Grant`; value objects `Email`, `Password`.
- [ ] **P3.2 Use cases** — `SignUpShop`, `Login`, `Refresh`, `Logout`, `RequestEmailVerification`, `VerifyEmail`, `SetPassword`, `GetAccount`, `ValidateApiKey`, `GetJwks`, RBAC CRUD + `GetGrants`.
- [ ] **P3.3 Persistence** — bun repositories + row models + mappers; migrations `2026_10_30_0000NN_create_*_table.go`; seeders (`RbacSeeder`, `AdminSeeder`, `ApiKeySeeder`).
- [ ] **P3.4 Transport** — `proto/auth/v1/auth.proto` (`AuthService`, `RbacService`), gRPC handlers, `cmd/auth-service`, `cmd/console`.
- [ ] **P3.5 Events** — `auth.shop.registered`, `auth.user.registered`, `auth.email-verification.requested` via outbox.
- [ ] **P3.6 Infra** — `auth-service/infra/local`.

### Acceptance
| # | Scenario | Expectation |
|---|---|---|
| 1 | Signup then login | tokens; access token verifies against JWKS |
| 2 | Refresh twice with the same token | second → `TOKEN_REUSED`, session revoked |
| 3 | Two concurrent refreshes with one token | exactly one succeeds |
| 4 | OTP flow | link token single use; expired → `TOKEN_INVALID` |
| 5 | API key | hashed in DB; inactive key rejected |
| 6 | Signup | `auth.shop.registered` in outbox in the same tx |

---

## P4 — api-gateway

### Tasks ([05](05-api-design.md), [07](07-auth-security.md))
- [ ] **P4.1 Bootstrap** — Gin engine, recovery, request id, access log, CORS, security headers, body limit, cookie handling, OpenAPI file served at `/docs`, error → envelope, ops endpoints.
- [ ] **P4.2 Middleware** — `APIKey` (Redis-cached), `JWT` (JWKS cache, `kid` refresh, denylist), `RBAC` (`Rbac("readAny","profile")`), `ShopOnly` / `UserOnly`, rate limit.
- [ ] **P4.3 gRPC clients** — one client per upstream through the breaker interceptor; principal metadata injection; breaker open → 503 + `Retry-After`.
- [ ] **P4.4 Handlers** — auth (shop / user / rbac / profile) in this phase; catalog, inventory, cart, discount, checkout / orders, notification added in P5–P11.
- [ ] **P4.5 Infra** — `api-gateway/infra/local`.

### Acceptance (integration, stub gRPC servers)
| # | Scenario | Expectation |
|---|---|---|
| 1 | No `x-api-key` | 401 |
| 2 | Expired / wrong-signature / revoked-session token | 401 |
| 3 | `Rbac` with wrong role | 403 |
| 4 | Upstream returns `UNAVAILABLE` ×10 | next calls 503 in < 50 ms, then recover after reset |
| 5 | 101st request in a minute | 429 |

---

## P5 — catalog-service

### Tasks ([04 §2](04-data-model.md#2-catalog_db-mongodb))
- [ ] **P5.1 Domain** — `Product` aggregate, `ProductTypeRegistry` (Electronic / Clothing / Furniture strategies), `Spu`, `Sku`, `Comment` (nested-set ops).
- [ ] **P5.2 Use cases** — create / update / publish / unpublish / list / search / get product; create SPU, get SPU, get SKU (cache-aside + null cache); comments; `GetProductsByIds`, `ListProductIdsByShop`.
- [ ] **P5.3 Persistence** — Mongo repositories, Redis cache, shop projection; migrations (`create_products_collection`, indexes…).
- [ ] **P5.4 Transport** — `proto/catalog/v1`, gRPC handlers, `auth.shop.registered` consumer.
- [ ] **P5.5 Events** — `catalog.product.created|updated`.
- [ ] **P5.6 Gateway routes** — catalog + comment handlers.

### Acceptance
| # | Scenario | Expectation |
|---|---|---|
| 1 | Create Clothing without `size` | `VALIDATION_FAILED` |
| 2 | Create product | one document; outbox `catalog.product.created` |
| 3 | Update with nested attributes | partial update, others kept |
| 4 | Unpublished product | not in public list, in shop drafts |
| 5 | SKU lookup twice / unknown id twice | second served from cache; unknown id null-cached |
| 6 | Comments: reply, delete subtree | left / right values consistent (property test) |

---

## P6 — inventory-service

### Tasks ([06 §3](06-checkout-saga.md#3-steps), [04 §3](04-data-model.md#3-inventory_db-postgresql))
- [ ] **P6.1 Domain** — `Inventory` (`AddStock`, `Reserve`, `Release`, `Commit`), `Reservation`.
- [ ] **P6.2 Use cases** — `CreateInventoryForProduct`, `AddStock` (owner check), `GetStock`, `CheckStock`, `ReserveStock`, `ReleaseStock`, `CommitStock`, `ExpireReservations` (job).
- [ ] **P6.3 Persistence** — migrations with `CHECK` constraints; conditional-UPDATE repository.
- [ ] **P6.4 Transport** — `proto/inventory/v1`, gRPC handlers, consumers for `catalog.product.*` and `inventory.commands`, replies to `order.saga.replies`.

### Acceptance
| # | Scenario | Expectation |
|---|---|---|
| 1 | 50 concurrent reserves of 1 on stock 10 | exactly 10 succeed; stock never < 0 |
| 2 | Reserve 2 items where the second lacks stock | none reserved |
| 3 | `ReserveStock` delivered twice | one reservation (inbox + unique) |
| 4 | `ReleaseStock` with no reservation | `StockReleased` reply |
| 5 | Expired reservation | stock returned, status `EXPIRED` |

---

## P7 — cart-service

- [ ] **P7.1 Domain / use cases** — `Cart` (add, update diff, remove, quantity 0 removes), `AddToCart` (catalog price snapshot + `CheckStock`), `UpdateCart`, `RemoveItem`, `GetCart`, `ClearOrderedItems`.
- [ ] **P7.2 Persistence / transport** — Mongo repository, migrations, `proto/cart/v1`, `order.order.confirmed` consumer, gateway routes.

| # | Acceptance | Expectation |
|---|---|---|
| 1 | Add item over stock | `INSUFFICIENT_STOCK` |
| 2 | Add same product twice | quantity summed |
| 3 | Catalog breaker open | `UNAVAILABLE`, cart unchanged |
| 4 | `order.order.confirmed` twice | items removed once, no error |

---

## P8 — discount-service

- [ ] **P8.1 Domain** — `Discount.Calculate(items)` (fixed / percent, all / specific, min order), `CanBeUsedBy(userID, usedByUser)`, date validation.
- [ ] **P8.2 Use cases** — create / update / list; `ListProductsForCode`; `CalculateDiscount`; `ApplyDiscounts` / `ReleaseDiscounts` (saga, `FOR UPDATE`).
- [ ] **P8.3 Persistence / transport** — bun repositories, migrations, `proto/discount/v1`, `discount.commands` consumer, `discount.discount.created` event, gateway routes.

| # | Acceptance | Expectation |
|---|---|---|
| 1 | Percent / fixed / specific products | amounts match the table in unit tests |
| 2 | `max_uses` 1, two concurrent applies | one succeeds |
| 3 | Per-user limit | second use by same user rejected |
| 4 | `ApplyDiscounts` redelivered | single usage row |
| 5 | Release | `used_count` decremented once |

---

## P9 — payment-service

- [ ] **P9.1 Domain** — `Payment` (PENDING → SUCCEEDED / FAILED → REFUNDED).
- [ ] **P9.2 Provider port + mock adapter** — token `tok_ok` / `tok_fail` / `tok_timeout`; decorated with a gobreaker guard.
- [ ] **P9.3 Use cases / consumer** — `ProcessPayment`, `RefundPayment` from `payment.commands`; replies via outbox.

| # | Acceptance | Expectation |
|---|---|---|
| 1 | `tok_ok` | `PaymentCompleted` |
| 2 | `tok_fail` | `PaymentFailed{CARD_DECLINED}` |
| 3 | `tok_timeout` ×N | breaker opens → `PaymentFailed{PROVIDER_UNAVAILABLE}` without calling provider |
| 4 | Refund before charge | payment recorded as cancelled; later `ProcessPayment` refused |
| 5 | Duplicate `ProcessPayment` | one charge |

---

## P10 — order-service

### Tasks ([06](06-checkout-saga.md))
- [ ] **P10.1 Domain** — `Order` aggregate + status guard, `CheckoutSaga` pure state machine (`OnReply`, `OnTimeout`), command builders.
- [ ] **P10.2 Use cases** — `ReviewCheckout` (catalog re-price + discount calc), `PlaceOrder` (idempotency), `HandleSagaReply`, `SweepSagaTimeouts`, `GetOrder`, `ListOrders`.
- [ ] **P10.3 Persistence / transport** — bun repositories, migrations, `proto/order/v1`, `order.saga.replies` consumer, sweeper job, gateway routes, events `order.order.confirmed|cancelled`.

| # | Acceptance | Expectation |
|---|---|---|
| 1 | Unit: every row of [06 §4](06-checkout-saga.md#4-compensation) | correct compensation commands |
| 2 | Same `Idempotency-Key` | same order; different body → 409 |
| 3 | Reply arrives after timeout | ignored, `saga_stale_replies_total` +1 |
| 4 | Sweeper on two replicas | step timeout handled once |
| 5 | Integration with real Kafka and scripted participants | happy path reaches `CONFIRMED`; payment failure reaches `CANCELLED` with all compensations |

---

## P11 — notification-service

- [ ] **P11.1 Domain / use cases** — `Notification`, `Template` (renderer replaces **all** placeholders), `SendEmail` (retry on SMTP failure), `ListNotifications`, `CreateTemplate`.
- [ ] **P11.2 Consumers** — `auth.email-verification.requested` → email; `catalog.product.created` → `SHOP-001`; `discount.discount.created` → `PROMOTION-001`; `order.order.confirmed|cancelled` → `ORDER-001|002`.
- [ ] **P11.3 Persistence / transport** — Mongo repositories, migrations + `TemplateSeeder`, SMTP sender (Mailpit), `proto/notification/v1`, gateway routes.

| # | Acceptance | Expectation |
|---|---|---|
| 1 | Template with 3 placeholders | all replaced |
| 2 | Verification event | email in Mailpit with the link |
| 3 | SMTP down | retried, then DLQ; no duplicate notification on redelivery |
| 4 | List | receiver's notifications + broadcasts, newest first |

---

## P12 — Dashboards & alerts

- [ ] **P12.1** Six Grafana dashboards ([14 §4](14-observability.md#4-dashboards-provisioned-in-observabilitygrafanadashboards)) as JSON, provisioned.
- [ ] **P12.2** `alerts.yml` ([14 §5](14-observability.md#5-alerts-observabilityprometheusrulesalertsyml)); `make check-infra` runs `promtool check rules` and `docker compose config -q` for every compose file.

| # | Acceptance | Expectation |
|---|---|---|
| 1 | `make check-infra` | passes |
| 2 | Stop a service | `ServiceDown` fires in Prometheus |
| 3 | Run a checkout | Grafana shows the trace and its logs |

---

## P13 — Media (optional)

- [ ] **P13.1** `MediaStorage` port in catalog; S3 adapter (S3-compatible store for local, chosen in this phase); `POST /product/upload-url` returns a presigned PUT URL (auth required); unit + integration tests.

---

## P14 — E2E & hardening

- [ ] **P14.1** `e2e/` suites for the ten scenarios in [10 §3](10-testing.md#3-end-to-end-tests).
- [ ] **P14.2** `scripts/coverage.sh` prints a per-module table and fails under 80 %.
- [ ] **P14.3** Graceful-shutdown test (SIGTERM during consume).
- [ ] **P14.4** CI workflow complete; README quick start verified from a clean checkout.
