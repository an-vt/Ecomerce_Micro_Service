# 01 — Requirements

Source system: `Ecomerce_project/Ecomerce_BE` (Express + MongoDB monolith) and `Ecomerce_project/System_message_queue_Ecomerce_BE` (RabbitMQ email worker). This project rebuilds them as **Go** microservices. **Backend only.**

## 1. Functional requirements

### 1.1 Ported from the monolith

| # | Area | Requirement | Owner service |
|---|---|---|---|
| F-01 | Shop identity | Shop signup, login, logout, refresh-token rotation with reuse detection | auth-service |
| F-02 | Buyer identity | Register by email OTP link (`new_user` → `welcome_back`), set password, login | auth-service |
| F-03 | API keys | Every public request carries `x-api-key`; key must be active and hold permission `0000` | auth-service + api-gateway |
| F-04 | RBAC | Roles, resources, grants (`accesscontrol` vocabulary, evaluated by `platform-go/rbac`); `profile` resource guarded by `readAny` / `readOwn` | auth-service + api-gateway |
| F-05 | Catalog | Create typed products (Electronic / Clothing / Furniture) through a strategy factory; update (partial, nested attributes); publish / unpublish; drafts and published lists per shop; public list, detail, full-text search | catalog-service |
| F-06 | SPU / SKU | Create SPU with tier variations and its SKU list; read SPU with SKUs; read one SKU (cached, null-cached) | catalog-service |
| F-07 | Comments | Nested-set comments per product: create root / reply, list by parent, delete subtree | catalog-service |
| F-08 | Inventory | Inventory row created when a product is created; shop adds stock; stock check | inventory-service |
| F-09 | Cart | Add item (price/name snapshot, stock pre-check), update quantities (v2 diff semantics), remove item, list | cart-service |
| F-10 | Discount | Create (date/duplicate validation), update, list for shop, list products for a code, compute discount amount (fixed / percent, all / specific products, min order value, max uses, max uses per user) | discount-service |
| F-11 | Checkout review | Re-price items from catalog, apply one discount per shop, return totals | order-service |
| F-12 | Notifications | `SHOP-001` on new product, `PROMOTION-001` on new discount, list notifications for the caller | notification-service |
| F-13 | Email | Templates (`new_template`), verification email through a queue with retries | notification-service |

### 1.2 New (missing in the monolith)

| # | Requirement | Owner |
|---|---|---|
| F-20 | **Place order** (`POST /v1/api/orders`, `Idempotency-Key`) runs the checkout saga: reserve stock → apply discounts → pay → commit stock → confirm. Any failure compensates completed steps | order-service |
| F-21 | Order status query and list for the buyer | order-service |
| F-22 | **Payment** with a mock provider: `tok_ok` succeeds, `tok_fail` declines, `tok_timeout` times out (exercises the breaker). Refund on compensation | payment-service |
| F-23 | Cart items bought in a confirmed order are removed | cart-service |
| F-24 | `ORDER-001` (confirmed) and `ORDER-002` (cancelled) notifications | notification-service |
| F-25 | Inventory reservations expire if never committed or released | inventory-service |

## 2. Non-functional requirements

| # | Requirement | How |
|---|---|---|
| N-01 | Microservices, one database per service | [02](02-architecture.md), [04](04-data-model.md) |
| N-02 | Public API is REST; internal sync calls are gRPC | [05](05-api-design.md) |
| N-03 | Async integration through Kafka, at-least-once with idempotent consumers | [08](08-messaging.md) |
| N-04 | No dual writes: state change and its event are committed together (transactional outbox) | [08 §3](08-messaging.md#3-transactional-outbox) |
| N-05 | Distributed transaction for checkout is an orchestrated saga with compensation | [06](06-checkout-saga.md) |
| N-06 | Circuit breaker + deadline on every synchronous remote call | [09](09-resilience.md) |
| N-07 | DDD layering enforced by lint (`depguard`) | [03 §2](03-project-structure.md#2-ddd-layering) |
| N-08 | Unit + integration + e2e tests; **≥ 80 % statement coverage** per service module (and per `domain` / `usecase` package set) | [10](10-testing.md) |
| N-09 | Metrics (Prometheus), dashboards (Grafana), logs (Loki), traces (Tempo), alerts | [14](14-observability.md) |
| N-10 | Stateless gateway token verification (no DB hit per request) | [07](07-auth-security.md) |
| N-11 | Secrets only through env / files; nothing hardcoded (monolith hardcoded `amqp://localhost`, CORS origin, links) | [11](11-infrastructure.md) |
| N-12 | Local stack starts with one command and reports health | `scripts/start-all.sh` |
| N-13 | Laravel-style migrations and seeders per service (`console migrate`, `db:seed`) | [03 §4](03-project-structure.md#4-database-migrations-laravel-style) |

## 3. Out of scope

- Frontend.
- Real payment providers, shipping, tax.
- Elasticsearch sync (the monolith only has a comment about it). Mongo text index is kept.
- Discord request logging (replaced by Loki).
- Production / Kubernetes manifests (only `infra/local`).
