# Ecomerce Micro Service

E-commerce backend as **Go microservices**, ported from the `Ecomerce_project` Express/MongoDB monolith.
API gateway (Gin, REST) · auth · catalog · inventory · cart · discount · order (checkout **saga**) · payment · notification.
REST outside, **gRPC** inside, **Kafka** for events and saga messages, PostgreSQL (**bun**) + MongoDB, Redis, **DDD** layering,
**circuit breakers** (gobreaker), **Prometheus / Grafana / Loki / Tempo**, Laravel-style migrations, unit + integration + e2e tests (≥ 80 % coverage).

## Documentation

| Doc | What it answers |
|---|---|
| [01 Requirements](docs/01-requirements.md) | What is ported, what is new, non-functional goals |
| [02 Architecture](docs/02-architecture.md) | Services, boundaries, context map, communication, patterns |
| [03 Project structure](docs/03-project-structure.md) | Repo layout, DDD layering rules, `platform-go`, Laravel-style migrations |
| [04 Data model](docs/04-data-model.md) | Tables / collections per service |
| [05 API design](docs/05-api-design.md) | REST routes (monolith → new), error envelope, gRPC services |
| [06 Checkout saga](docs/06-checkout-saga.md) | Order state machine, steps, compensation, timeouts |
| [07 Auth & security](docs/07-auth-security.md) | RS256 tokens, refresh rotation, API keys, RBAC |
| [08 Messaging](docs/08-messaging.md) | Kafka topics, envelope, outbox / inbox, retry / DLQ |
| [09 Resilience](docs/09-resilience.md) | Circuit breaker, timeouts, retries |
| [10 Testing](docs/10-testing.md) | Unit / integration / e2e, coverage gate |
| [11 Infrastructure](docs/11-infrastructure.md) | Compose layout, env, ports, startup order |
| [12 Decisions](docs/12-decisions.md) | Why (decision log) |
| [13 Monolith migration](docs/13-monolith-migration.md) | Module → service map, monolith bugs fixed |
| [14 Observability](docs/14-observability.md) | Metrics, logs, traces, dashboards, alerts |
| [PLAN](docs/PLAN.md) | Phased implementation plan with acceptance tests |

## Quick start

Requirements: Go ≥ 1.26, Docker (with Compose), `buf`, `golangci-lint`, `openssl`, `python3`.

```bash
make start      # RS256 keys, shared infra (+ observability), the nine services, seed data; takes a few minutes the first time
make status     # health table with every URL
```

Then:

| What | URL |
|---|---|
| REST API · OpenAPI docs | http://localhost:8088/v1/api · http://localhost:8088/docs |
| Grafana (admin / admin) | http://localhost:3001 — six dashboards in the *Ecom* folder |
| Prometheus · Alertmanager | http://localhost:9090 · http://localhost:9093 |
| Kafka UI · Mailpit | http://localhost:8080 · http://localhost:8025 |

Every call needs the API key (`x-api-key: dev-api-key-change-me`, seeded). A checkout, end to end:

```bash
KEY=dev-api-key-change-me; API=http://localhost:8088/v1/api
curl -s -H "x-api-key: $KEY" -H 'content-type: application/json' -d '{"name":"Acme","email":"shop@example.com","password":"correct-horse"}' $API/shop/signup   # -> shop tokens
# create + publish a product, then as a buyer: POST /user/new_user, open the link in Mailpit, add to /cart, POST /checkout/review,
# POST /orders (header Idempotency-Key) -> 202, then poll GET /orders/:id until CONFIRMED or CANCELLED
```

`e2e/order_e2e_test.go` is that walk-through as code.

## Testing

```bash
make lint               # golangci-lint on every module, incl. the DDD layering rules (depguard)
make test               # unit tests, -race, no Docker
make test-integration   # testcontainers: real PostgreSQL, MongoDB, Kafka, Redis
make cover              # unit + integration coverage per module, fails under 80 %
make test-e2e           # black-box scenarios against the running stack (make start first)
make check-infra        # compose files, Prometheus / Alertmanager config, alert rules, dashboards
make help
```

The e2e suite (`e2e/`) covers the scenarios of [docs/10](docs/10-testing.md#3-end-to-end-tests), including a service killed
mid-checkout (SIGTERM must lose and duplicate nothing), an open circuit breaker answering 503 fast and recovering, and one
checkout found as a single trace across seven services in Tempo. CI (`.github/workflows/ci.yml`) runs `verify` on every push
and `e2e` against the full stack after it.

`make stop` stops everything (`VOLUMES=1` also wipes the data). `make dashboards` regenerates the Grafana dashboards.
