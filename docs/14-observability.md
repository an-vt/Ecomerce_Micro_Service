# 14 — Observability

Three signals, one correlation id.

```
service ──/metrics (pull)──────────────▶ Prometheus ──▶ Alertmanager
service ──stdout JSON logs──▶ Docker ──▶ Alloy ──────▶ Loki
service ──OTLP gRPC :4317──▶ Alloy ───────────────────▶ Tempo
                                  Grafana: Prometheus + Loki + Tempo (trace ⇄ logs ⇄ metrics via traceId)
```

## 1. Logs

Go `log/slog` JSON handler (`platform-go/logger`), to stdout:

```json
{"time":"2026-10-29T10:00:00.123Z","level":"INFO","service":"order-service","trace_id":"4bf9…","span_id":"00f0…","request_id":"01JD…","msg":"saga step completed","order_id":"6f1c…","step":"RESERVING_STOCK"}
```

- Levels via `LOG_LEVEL`. Redacted: `authorization`, `cookie`, `password`, `token`, `x-api-key`, `payment.token`.
- `traceId` / `spanId` injected from the active OTel span by the slog handler, `requestId` from gRPC metadata / HTTP header / Kafka header.
- Alloy discovers every compose container, adds labels `service`, `container`, `env` and the low-cardinality JSON field `level`; the Loki derived field links `trace_id` → Tempo.

## 2. Metrics

Each service exposes `GET /metrics` on its ops port (gateway on 8088): the Go runtime and process collectors (`go_*`, `process_*`) plus:

| Metric | Type | Labels | From |
|---|---|---|---|
| `http_server_duration_seconds` | histogram | `method,route,status` | gateway |
| `grpc_server_duration_seconds` | histogram | `service,method,code` | every gRPC service (interceptor) |
| `grpc_client_duration_seconds` | histogram | `target,method,code` | `ResilientGrpcClient` |
| `circuit_breaker_state` | gauge | `target,method` (0/1/2) | `ResilientGrpcClient` |
| `circuit_breaker_events_total` | counter | `target,method,event` | `ResilientGrpcClient` |
| `outbox_pending` / `outbox_oldest_age_seconds` | gauge | – | outbox relay |
| `outbox_published_total` / `outbox_publish_errors_total` | counter | `topic` | outbox relay |
| `kafka_messages_consumed_total` | counter | `topic,result` (`ok`,`duplicate`,`retry`,`dlq`) | consumer runner |
| `kafka_dlq_total` | counter | `topic` | consumer runner |
| `saga_started_total`, `saga_completed_total` | counter | – | order |
| `saga_failed_total` | counter | `reason` | order |
| `saga_compensations_total`, `saga_step_timeouts_total` | counter | `step` | order |
| `saga_stale_replies_total` | counter | – | order |
| `saga_duration_seconds` | histogram | `outcome` | order |
| `auth_refresh_reuse_detected_total` | counter | – | auth |
| `rate_limited_total` | counter | `route` | gateway |

There are deliberately **no** per-feature business counters (logins, payments, reservations, cache hits): the Business dashboard derives them from `grpc_server_duration_seconds_count{service,method,code}` (for example `method="Login"` split by `code`) and from the `saga_*` series, so a new feature is visible without new instrumentation. Add a counter only for something calls cannot show, like `auth_refresh_reuse_detected_total` (a security signal) and `rate_limited_total` (abuse).

Exporters: postgres, mongodb, redis, kafka (lag via `kafka_consumergroup_lag`). `scripts/check-observability.py` (part of `make check-infra`) fails when a dashboard or an alert uses a metric that nothing exports, and executes every expression against a running Prometheus, so a panel cannot silently say "No data" because a name drifted.

## 3. Traces

`platform-go/otel.Init` (first call in `main`) sets up the tracer provider with the OTLP gRPC exporter. Instrumentation: `otelgrpc` (gRPC server/client stats handlers), `otelhttp` + `otelgin` (gateway), `bunotel` (PostgreSQL), `otelmongo` (MongoDB), `kotel` (franz-go), `redisotel` (go-redis). Resource attributes: `service.name`, `deployment.environment`.

- **gRPC / HTTP**: W3C `traceparent` propagated automatically.
- **Kafka**: `kotel` propagates through headers; additionally the outbox row stores `traceparent` of the producing request, and the relay publishes with that header, so the consumer span is a child of the original request even though publishing happens later in another process.
- Result: one `POST /orders` trace shows gateway → order → (outbox) → inventory → order → discount → … → payment → inventory → order.

Behaviour worth knowing:

- **The trace starts at the gateway.** A server span `POST /v1/api/orders` (named after the route, never the path or the query string) roots every API request; its trace id is returned in the `X-Trace-Id` response header, and the access log carries it, so a report "my order was slow" can be followed in Tempo and in Loki. The gateway does **not** adopt a client's `traceparent`: it is the public edge, and honouring it would let anyone force sampling or graft requests onto another trace.
- **Background work is not traced** (`otelx.Untraced`): health checks, `/metrics`, unmatched paths (scanners) and the outbox relay's polling would otherwise create hundreds of one-span root traces a minute and bury the real ones. Publishing a message still continues the trace stored in its outbox row. The two sweepers (saga timeouts, stock expiry) stay traced on purpose: a timeout they handle starts a trace worth seeing.
- Verified on a real checkout: one trace of ~130 spans across 7 services, gateway → order → catalog → discount → Kafka → inventory → discount → payment → … → notification, with the compensation commands of a declined card in the same trace.
- Logs: `order-service` writes one `saga step` line per transition (`order_id`, `saga_state`, `order_status`, `commands`, `reason`; WARN for compensations and re-sent commands). Searching Loki by `trace_id` returns the gateway request line and every saga step of that order.

Sampling: 100 % locally (`OTEL_TRACES_SAMPLER=parentbased_always_on`), configurable.

## 4. Dashboards (provisioned in `observability/grafana/dashboards/`)

Generated by `observability/grafana/gen/dashboards.py` (`make dashboards`); `make check-infra` fails when the committed JSON differs from the generator.

| Dashboard (uid) | Panels |
|---|---|
| Services overview (`ecom-services`) | services up / down and a target table; API request rate by status, 5xx ratio, p50/p95/p99, slowest routes; gRPC calls, failing ratio, p95 and answers by code per service; CPU, memory, goroutines. Variable `service` |
| Circuit breakers (`ecom-breakers`) | open / half-open counts and a state table; calls by result, fast-failed calls (open circuit), failures; client latency p95 and calls by target and code |
| Checkout saga (`ecom-saga`) | started / confirmed / cancelled / confirmed share / in flight / stale (1 h); flow rate; why orders are cancelled (by reason); duration p50/p95 by outcome; compensations by command, step timeouts by step, stale replies |
| Kafka & outbox (`ecom-kafka`) | brokers, dead-lettered (1 h), total lag, group members; consumer lag; consumed by result, retries, dead letters by topic; outbox pending, oldest pending row, published, publish errors per service |
| Databases (`ecom-databases`) | PostgreSQL connections, TPS, rollbacks, locks by mode, deadlocks, cache hit ratio, longest open transaction; MongoDB connections, ops, replica-set state; Redis memory, clients, hit ratio, commands |
| Business (`ecom-business`) | orders placed / confirmed / cancelled, declined cards, provider trouble, out of stock, cancel reasons; sign-ups, logins by answer, refresh-token reuse; products created / published, carts, stock added; discounts created, code previews by answer, notifications read; requests refused by the rate limiter |

Data sources: Prometheus (default), Loki, Tempo with `tracesToLogs` (by `trace_id`) and `tracesToMetrics`; Loki derived field `trace_id` → Tempo.

## 5. Alerts (`observability/prometheus/rules/alerts.yml`)

Severity follows the Alertmanager routing: **P1** act now (paged), **P2** look soon (notified), **P3** ticket. A `ServiceDown` inhibits the alerts a dead service would explain (outbox backlog, consumer lag, stuck sagas).

| Alert | Expression (sketch) | For | Severity |
|---|---|---|---|
| `ServiceDown` | `up{job="ecom-services"} == 0` | 1m | P1 |
| `GatewayHighErrorRate` | 5xx share of API requests > 5 % (and more than ~3 requests a minute) | 5m | P2 |
| `GrpcHighErrorRate` | per service: `Unavailable`, `Internal`, `Unknown`, `DeadlineExceeded`, `DataLoss`, `ResourceExhausted` > 5 % of calls. Business answers (`NotFound`, `FailedPrecondition`...) never count | 5m | P2 |
| `GatewayHighLatency` / `GrpcHighLatency` | p95 per route / per service > 1 s | 5m | P2 |
| `CircuitBreakerOpen` | `circuit_breaker_state == 2` | 1m | P2 |
| `DlqMessages` | `increase(kafka_dlq_total[5m]) > 0` | 0m | P1 |
| `ConsumerLagHigh` | `kafka_consumergroup_lag` > 1000 per group and topic | 5m | P2 |
| `OutboxBacklog` | `outbox_oldest_age_seconds > 60` | 2m | P1 |
| `SagaCompensationRatioHigh` | `rate(saga_failed_total) / rate(saga_started_total)` > 0.2 over 15 m | 10m | P2 |
| `SagaStuck` | `increase(saga_step_timeouts_total[10m]) > 5` | 0m | P1 |
| `RefreshTokenReuse` | `increase(auth_refresh_reuse_detected_total[5m]) > 3` | 0m | P2 |
| `RateLimitedSpike` | `increase(rate_limited_total[5m]) > 100` | 0m | P3 |

`make check-infra` validates the compose files, `promtool check config` / `check rules` and `amtool check-config` (run in the same images the stack uses), that the dashboards match their generator, and runs `scripts/check-observability.py`. Alertmanager locally only groups and shows alerts (`receivers` without integrations).
