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

```bash
make start      # shared infra + every implemented service (needs Docker)
make status     # health table
make test       # unit tests (-race)
make cover      # coverage table, fails under 80 %
make test-integration   # testcontainers
make test-e2e           # against the running stack
make help
```

Requirements: Go ≥ 1.26, Docker, `buf`, `golangci-lint`.
