# 02 — Architecture

## 1. Services

| # | Service | Port (REST or gRPC / HTTP ops) | Store | Responsibility |
|---|---|---|---|---|
| 1 | **api-gateway** | 3000 (REST) | Redis | The only public entry (Gin). API-key check, JWT (RS256) verify, session-revocation check, RBAC, rate limit, request id, REST → gRPC translation, one circuit breaker per upstream, Swagger. |
| 2 | **auth-service** | 9101 / 8101 | PostgreSQL `auth_db` | Shops, users, sessions (refresh rotation + reuse detection), API keys (hashed), roles / resources / grants, OTP, JWKS. |
| 3 | **catalog-service** | 9102 / 8102 | MongoDB `catalog_db`, Redis | Typed products (strategy factory), SPU / SKU, search, publish, SKU cache, shop projection, comments (nested set). |
| 4 | **inventory-service** | 9103 / 8103 | PostgreSQL `inventory_db` | Stock per product, reservations per order (reserve / release / commit), reservation expiry. |
| 5 | **cart-service** | 9104 / 8104 | MongoDB `cart_db` | Cart per user with price snapshots. |
| 6 | **discount-service** | 9105 / 8105 | PostgreSQL `discount_db` | Discount codes, amount calculation, usage records. |
| 7 | **order-service** | 9106 / 8106 | PostgreSQL `order_db` | Checkout review, orders, **checkout saga orchestrator**, idempotency keys. |
| 8 | **payment-service** | 9107 / 8107 | PostgreSQL `payment_db` | Payments, mock provider adapter, refunds. |
| 9 | **notification-service** | 9108 / 8108 | MongoDB `notification_db` | In-app notifications, email templates, email delivery (SMTP → Mailpit locally). |

The ops HTTP port of every internal service serves `/health/live`, `/health/ready`, `/metrics` only.

Infrastructure: PostgreSQL 17, MongoDB 8.2 (single-node replica set `rs0`), Redis 7, Kafka (KRaft), Mailpit, Prometheus, Grafana, Loki, Tempo, Grafana Alloy, Alertmanager.

### 1.1 Why these boundaries

- **Auth split** — identity is needed by every flow; the gateway must verify tokens without a database. The monolith stored a per-user HS256 secret, forcing a DB read per request.
- **Catalog vs inventory** — the monolith wrote inventory rows from the product service (`insertInventory`). Stock changes every order; product data rarely. Different consistency needs, different stores.
- **Cart split** — high write rate, short-lived, document-shaped, owned by the buyer.
- **Discount split** — has its own invariants (max uses, per-user uses) that must be checked and recorded atomically; the monolith never recorded usage.
- **Order is the orchestrator** — it owns the business process "checkout" and its state. Other services expose commands, not workflow.
- **Payment split** — external provider, slow and unreliable; isolated behind a breaker.
- **Notification split** — replaces the separate RabbitMQ worker; slow SMTP must never block business flows.
- **Not split**: comments stay in catalog (they only reference products, low volume), SPU / SKU stay with products, RBAC stays in auth.

## 2. Context map

```
                    ┌────────────────────┐
                    │      Identity      │  (auth-service)  — upstream for everyone
                    │ shops, users, RBAC │
                    └─────────┬──────────┘
          auth.shop.registered │  auth.user.registered, auth.email-verification.requested
             ┌─────────────────┼───────────────────────────┐
             ▼                 ▼                           ▼
     ┌──────────────┐   ┌──────────────┐           ┌───────────────┐
     │   Catalog    │──▶│  Inventory   │           │ Notification  │◀── order.*, discount.*, catalog.*
     │ products,SKU │   │ stock, resv. │           │ in-app, email │
     └──────┬───────┘   └──────▲───────┘           └───────────────┘
 gRPC prices│  catalog.product.created (conformist)
            ▼                  │ inventory.commands
     ┌──────────────┐   ┌──────┴───────┐   discount.commands   ┌──────────────┐
     │     Cart     │◀──│    Order     │──────────────────────▶│   Discount   │
     │              │   │ saga orchestr│   payment.commands    └──────────────┘
     └──────────────┘   │              │──────────────────────▶┌──────────────┐
      order.order.confirmed            │◀── order.saga.replies─│   Payment    │
                        └──────────────┘                       └──────────────┘
```

Relationships:
- Catalog keeps a **shop projection** (`shopId → name, email`) built from `auth.shop.registered` instead of the monolith's `populate('product_shop')` join.
- Inventory is a **conformist** of catalog: it creates stock rows from `catalog.product.created`.
- Order is **customer** of catalog, cart, discount (gRPC for review) and orchestrator of inventory, discount, payment (Kafka commands).

## 3. Communication

```
Client ──REST/JSON──▶ api-gateway ──gRPC (deadline + gobreaker)──▶ auth / catalog / inventory / cart / discount / order / notification
                                                                     cart ──gRPC──▶ catalog, inventory
                                                                    order ──gRPC──▶ catalog, cart, discount        (review only)
                                                                 discount ──gRPC──▶ catalog

every service ──(DB tx: state + outbox row)──▶ outbox relay ──▶ Kafka ──▶ consumer ──(DB tx: inbox row + effect)
```

| Need | Style | Why |
|---|---|---|
| Client API | REST via gateway | Simple for clients, OpenAPI, cookies for refresh token |
| Query / pre-check needing an answer now | gRPC | Typed contracts, deadlines, low latency |
| State change that other contexts react to | Kafka event | Decoupled, replayable, no temporal coupling |
| Step of a distributed transaction | Kafka command + reply | Saga steps survive restarts of either side |

Details: [05](05-api-design.md) (REST + gRPC), [08](08-messaging.md) (Kafka).

## 4. Patterns

| Pattern | Where | Doc |
|---|---|---|
| API Gateway | `api-gateway` | [05 §1](05-api-design.md) |
| Database per service, polyglot persistence | all | [04](04-data-model.md), [12 D-002](12-decisions.md) |
| DDD (aggregates, value objects, domain events, repositories) + layered architecture | all | [03 §2](03-project-structure.md#2-ddd-layering) |
| Orchestrated saga | order-service | [06](06-checkout-saga.md) |
| Transactional outbox / idempotent consumer (inbox) | all producers / consumers | [08](08-messaging.md) |
| Circuit breaker, timeout, retry with jitter | gRPC clients, payment provider | [09](09-resilience.md) |
| Strategy + registry (product types) | catalog `ProductFactory` | [04 §3](04-data-model.md) |
| State machine | Order, Saga, Payment, Reservation | [06 §2](06-checkout-saga.md) |
| Cache-aside with null caching | catalog SKU | [04 §3](04-data-model.md) |
| CQRS-lite projection | catalog shop projection | §2 |
| Observability: RED metrics, structured logs, distributed tracing | all | [14](14-observability.md) |

## 5. Checkout flow (summary)

```
Buyer          Gateway        Order              Inventory     Discount      Payment      Cart / Notification
  │ POST /orders │              │                    │             │             │              │
  │─────────────▶│── PlaceOrder▶│ tx: order PENDING  │             │             │              │
  │              │              │   + saga + outbox  │             │             │              │
  │◀── 202 ──────│◀─────────────│ ReserveStock ─────▶│             │             │              │
  │              │              │◀─ StockReserved ───│             │             │              │
  │              │              │ ApplyDiscounts ───────────────▶  │             │              │
  │              │              │◀─ DiscountsApplied ──────────────│             │              │
  │              │              │ ProcessPayment ─────────────────────────────▶  │              │
  │              │              │◀─ PaymentCompleted ────────────────────────────│              │
  │              │              │ CommitStock ──────▶│             │             │              │
  │              │              │ order CONFIRMED ── order.order.confirmed ─────────────────────▶│
  │ GET /orders/:id ──────────▶ │ CONFIRMED          │             │             │              │
```

Failure at any step → compensation in reverse order → `CANCELLED`. Full detail in [06](06-checkout-saga.md).
