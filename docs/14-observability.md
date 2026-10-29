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

Each service exposes `GET /metrics` on its ops port (gateway on 8088). Default Node metrics (`process_*`, `nodejs_*`) plus:

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
| `inventory_reservations_total` | counter | `result` | inventory |
| `payments_total` | counter | `result` | payment |
| `auth_logins_total` | counter | `type,result` | auth |
| `auth_refresh_reuse_detected_total` | counter | – | auth |
| `cache_requests_total` | counter | `cache,result` (`hit`,`miss`,`null`) | catalog |
| `rate_limited_total` | counter | `route` | gateway |

Exporters: postgres, mongodb, redis, kafka (lag via `kafka_consumergroup_lag`).

## 3. Traces

`platform-go/otel.Init` (first call in `main`) sets up the tracer provider with the OTLP gRPC exporter. Instrumentation: `otelgrpc` (gRPC server/client stats handlers), `otelhttp` + `otelgin` (gateway), `bunotel` (PostgreSQL), `otelmongo` (MongoDB), `kotel` (franz-go), `redisotel` (go-redis). Resource attributes: `service.name`, `deployment.environment`.

- **gRPC / HTTP**: W3C `traceparent` propagated automatically.
- **Kafka**: `kotel` propagates through headers; additionally the outbox row stores `traceparent` of the producing request, and the relay publishes with that header, so the consumer span is a child of the original request even though publishing happens later in another process.
- Result: one `POST /orders` trace shows gateway → order → (outbox) → inventory → order → discount → … → payment → inventory → order.

Sampling: 100 % locally (`OTEL_TRACES_SAMPLER=parentbased_always_on`), configurable.

## 4. Dashboards (provisioned in `observability/grafana/dashboards/`)

| Dashboard | Panels |
|---|---|
| Services overview | per service: request rate, error rate (5xx / non-OK gRPC), p50/p95/p99 latency, up, CPU, memory, event-loop lag |
| Circuit breakers | state per target/method, events rate (failure / reject / timeout), gRPC client latency |
| Checkout saga | started / completed / failed rate, failure reasons, compensations by step, step timeouts, duration p95, stale replies |
| Kafka & outbox | consumer lag per group, consumed rate by result, DLQ count, outbox pending and oldest age per service |
| Databases | PostgreSQL connections / TPS / locks / slow queries, MongoDB ops / connections / replication, Redis memory / hit ratio |
| Business | logins, signups, products created, orders by status, payments by result, reservations by result |

Data sources: Prometheus (default), Loki, Tempo with `tracesToLogs` (by `trace_id`) and `tracesToMetrics`; Loki derived field `traceId` → Tempo.

## 5. Alerts (`observability/prometheus/rules/alerts.yml`)

| Alert | Expression (sketch) | For | Severity |
|---|---|---|---|
| `ServiceDown` | `up == 0` | 1m | critical |
| `HighErrorRate` | 5xx or non-OK gRPC ratio > 5 % | 5m | warning |
| `HighLatency` | p95 > 1 s | 5m | warning |
| `CircuitBreakerOpen` | `circuit_breaker_state == 2` | 1m | warning |
| `DlqMessages` | `increase(kafka_dlq_total[5m]) > 0` | 0m | critical |
| `ConsumerLagHigh` | `kafka_consumergroup_lag > 1000` | 5m | warning |
| `OutboxBacklog` | `outbox_oldest_age_seconds > 60` | 2m | critical |
| `SagaCompensationRatioHigh` | `rate(saga_failed_total[15m]) / rate(saga_started_total[15m]) > 0.2` | 10m | warning |
| `SagaStuck` | `increase(saga_step_timeouts_total[10m]) > 5` | 0m | critical |
| `RefreshTokenReuse` | `increase(auth_refresh_reuse_detected_total[5m]) > 3` | 0m | warning |

Rules are validated in CI with `promtool check rules` (run through Docker `prom/prometheus` image: `make check-infra`). Alertmanager locally only logs (`receivers: [{name: default}]`).
