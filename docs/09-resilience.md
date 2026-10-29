# 09 — Resilience

## 1. Circuit breaker (sony/gobreaker)

Every synchronous remote call goes through the gRPC client interceptor in `platform-go/grpcx`, which keeps one `gobreaker.CircuitBreaker` per (target, method) in `platform-go/breaker`'s registry. A failing `CatalogService.GetSku` cannot trip `CatalogService.GetProductsByIds`, and one failing upstream cannot trip another.

| Setting | Default | Env |
|---|---|---|
| Call deadline (`context.WithTimeout`) | 2000 ms | `GRPC_TIMEOUT_MS` |
| `ReadyToTrip`: failure ratio ≥ | 50 % | `BREAKER_ERROR_PCT` |
| `ReadyToTrip`: min requests in the window | 10 | `BREAKER_VOLUME` |
| `Interval` (counts reset while closed) | 10 000 ms | `BREAKER_WINDOW_MS` |
| `Timeout` (open → half-open) | 30 000 ms | `BREAKER_RESET_MS` |
| `MaxRequests` (trial calls in half-open) | 1 | – |

```
 CLOSED ──(≥ volume calls, ≥ 50 % failures in window)──▶ OPEN ──(resetTimeout)──▶ HALF_OPEN
   ▲                                                       │ fail fast:              │ 1 trial call
   └──────────────────── trial succeeds ◀──────────────────┴── BREAKER_OPEN (503) ◀──┘ trial fails → OPEN
```

### 1.1 What counts as a failure

Only errors that say "the dependency is unhealthy" open the circuit. Business errors are the dependency working correctly.

| gRPC status | Failure? |
|---|---|
| `UNAVAILABLE`, `DEADLINE_EXCEEDED`, `INTERNAL`, `UNKNOWN`, `RESOURCE_EXHAUSTED` | **yes** |
| `INVALID_ARGUMENT`, `NOT_FOUND`, `ALREADY_EXISTS`, `FAILED_PRECONDITION`, `PERMISSION_DENIED`, `UNAUTHENTICATED`, `ABORTED` | no (passed through, `IsSuccessful` returns true) |

### 1.2 Open circuit behaviour

| Caller | Behaviour when open |
|---|---|
| api-gateway | `503 SERVICE_UNAVAILABLE`, `Retry-After: <resetTimeout s>`, `error: "UPSTREAM_UNAVAILABLE"` — in < 5 ms, no network call |
| cart → catalog / inventory | `UNAVAILABLE` propagated (gateway → 503); cart is not modified |
| order (review, place) → catalog / discount | `UNAVAILABLE` propagated; no order is created |
| discount → catalog (`list_product_code`) | `UNAVAILABLE` propagated |
| payment → mock provider | `PaymentFailed{reason:"PROVIDER_UNAVAILABLE"}` → saga compensates |

No silent fallbacks with fake data: for commerce, a stale price or a fake "in stock" is worse than a clear 503.

## 2. Retries

| Where | Policy |
|---|---|
| gRPC **reads** (`Get*`, `List*`, `Check*`, `Calculate*`, `Review*`) | up to 2 retries, full jitter, base 100 ms, only on `UNAVAILABLE`; every attempt goes through the breaker, so retries count towards it and an open circuit stops the retry loop immediately |
| gRPC **writes** | no retry (not idempotent from the caller's view) |
| Kafka consumer | in-process backoff then DLQ ([08 §5](08-messaging.md#5-consumer-runner-retries-dlq)) |
| Outbox relay | per-row backoff, forever (alert on backlog age) |
| Saga steps | timeout → compensation; compensations re-sent until acknowledged ([06 §5](06-checkout-saga.md#5-timeouts)) |

## 3. Timeouts everywhere

| Hop | Timeout |
|---|---|
| Client → gateway | `server.requestTimeout` 15 s |
| Gateway → service | 2 s deadline |
| Service → service (gRPC) | 2 s deadline (shorter than the caller's remaining budget) |
| Payment provider | 3 s (`PAYMENT_PROVIDER_TIMEOUT_MS`) |
| DB queries | PostgreSQL `statement_timeout` 5 s; Mongo `maxTimeMS` 5 s |
| Redis | `commandTimeout` 500 ms; cache failures are logged and treated as cache misses |

## 4. Other measures

| Measure | Where |
|---|---|
| Bulkhead: each upstream has its own gRPC channel and breaker | gateway, cart, order |
| Graceful shutdown: stop consuming, finish in-flight handler, flush producer, close DB (`enableShutdownHooks`) | all |
| Readiness vs liveness: `/health/ready` checks DB, Redis, Kafka producer connected; `/health/live` only the event loop | all |
| Idempotency: inbox for messages, `Idempotency-Key` for `PlaceOrder`, unique constraints for reservations / usages / payments | see [08](08-messaging.md), [06](06-checkout-saga.md) |
| Inventory never oversells: conditional `UPDATE … WHERE stock >= q` + `CHECK (stock >= 0)` (monolith: Redis `SETNX` lock that leaked when reservation failed) | inventory |

## 5. Metrics

`circuit_breaker_state{target,method}` (0 closed, 1 half-open, 2 open), `circuit_breaker_events_total{target,method,event}` (`success`, `failure`, `reject`, `timeout`, `open`, `close`, `halfOpen`), `grpc_client_duration_seconds{target,method,code}`. Alert `CircuitBreakerOpen` when state = 2 for 1 minute ([14](14-observability.md)).
