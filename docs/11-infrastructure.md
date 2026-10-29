# 11 — Infrastructure (local)

Same layout as `payment_gateway_golang`: shared stores in `shared-infra/infra/local`, one compose project per service in `<svc>/infra/local`, all joined to the external Docker network **`ecom_net`**.

## 1. Shared infra — `shared-infra/infra/local` (compose project `ecom-shared`)

| Container | Image | Host port | Notes |
|---|---|---|---|
| `ecom-postgres` | `postgres:17-alpine` | 5432 | `postgres/init/01-databases.sh` creates `<svc>_db` + user `<svc>` per PG service; each user owns only its DB |
| `ecom-mongo` | `mongo:8.2` | 27017 | `--replSet rs0 --keyFile` (transactions need a replica set); `mongo/init-users.js` creates `<svc>` users on `<svc>_db`; healthcheck runs `rs.initiate` once |
| `ecom-redis` | `redis:7.4-alpine` | 6379 | AOF on |
| `ecom-kafka` | `apache/kafka:3.9.1` | 9094 (host listener) | KRaft single node; internal listener `ecom-kafka:9092`; `auto.create.topics.enable=false` |
| `ecom-kafka-init` | `apache/kafka:3.9.1` | – | one-shot: `kafka/create-topics.sh` (topics + `.dlq`) |
| `ecom-kafka-ui` | `provectuslabs/kafka-ui:v0.7.2` | 8080 | browse topics / consumer lag |
| `ecom-mailpit` | `axllent/mailpit:v1.27.0` | 8025 (UI/API), 1025 (SMTP) | catches verification emails |

Profile **`observability`** (on by default in `make up`, `OBSERVABILITY=0` to skip):

| Container | Image | Port | Role |
|---|---|---|---|
| `ecom-prometheus` | `prom/prometheus:v3.1.0` | 9090 | scrapes services + exporters; rules in `observability/prometheus/rules/` |
| `ecom-alertmanager` | `prom/alertmanager:v0.28.0` | 9093 | alert routing (local: log only) |
| `ecom-grafana` | `grafana/grafana:11.4.0` | 3001 | provisioned datasources + dashboards |
| `ecom-loki` | `grafana/loki:3.3.2` | 3100 | logs |
| `ecom-tempo` | `grafana/tempo:2.7.1` | 3200 | traces |
| `ecom-alloy` | `grafana/alloy:v1.5.1` | 4317 / 4318 (OTLP), 12345 (UI) | collects container logs → Loki; receives OTLP → Tempo |
| `ecom-postgres-exporter`, `ecom-mongodb-exporter`, `ecom-redis-exporter`, `ecom-kafka-exporter` | official exporters | – | DB / broker metrics |

`shared-infra/infra/local/Makefile`: `env`, `network`, `up`, `down`, `reset` (`down -v`), `logs`, `ps`, `psql`, `mongosh`, `topics`.

## 2. Per-service infra — `<svc>/infra/local`

```
<svc>/infra/local/
├── docker-compose.yml   name: ecom-<svc>; build context = repo root, dockerfile <svc>/Dockerfile
├── .env.example         copied to .env by `make env`
└── Makefile             env, build, up (creates ecom_net if missing, `up -d --build --wait`), down, restart, logs, ps, migrate, seed, shell
```

Container name `ecom-<short>` (`ecom-gateway`, `ecom-auth`, `ecom-catalog`, `ecom-inventory`, `ecom-cart`, `ecom-discount`, `ecom-order`, `ecom-payment`, `ecom-notification`). Healthcheck: `/app -healthcheck`.

### 2.1 Dockerfile (all services)

Multi-stage, build context = repository root (`go.work` needs the sibling modules), `ARG TARGET=<svc>|console`:
1. `build`: `golang:1.26-alpine`; copy `go.work`, `go.work.sum`, `platform-go/`, `proto/`, `<svc>/` → `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/app ./<svc>/cmd/$TARGET`.
2. `runtime`: `gcr.io/distroless/static-debian12:nonroot`, copy `/out/app`; the binary supports `-healthcheck` (calls its own `/health/ready`) so the image needs no `wget`.

Healthcheck in compose: `["CMD", "/app", "-healthcheck"]`.

## 3. Environment

### 3.1 Common to every service

| Var | Example | Meaning |
|---|---|---|
| `SERVICE_NAME` | `order-service` | logs, metrics, traces |
| `HTTP_PORT` | `8106` | ops port (gateway: public REST port 3000) |
| `GRPC_PORT` | `9106` | gRPC bind (not for gateway) |
| `LOG_LEVEL` | `info` | slog level |
| `KAFKA_BROKERS` | `ecom-kafka:9092` | |
| `REDIS_URL` | `redis://ecom-redis:6379` | where used |
| `OTEL_ENABLED` | `true` | |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | `http://ecom-alloy:4317` | |
| `GRPC_TIMEOUT_MS`, `BREAKER_*` | see [09](09-resilience.md) | |

### 3.2 Per service

| Service | Vars |
|---|---|
| api-gateway | `AUTH_GRPC_URL=ecom-auth:9101`, `CATALOG_GRPC_URL`, `INVENTORY_GRPC_URL`, `CART_GRPC_URL`, `DISCOUNT_GRPC_URL`, `ORDER_GRPC_URL`, `NOTIFICATION_GRPC_URL`, `CORS_ORIGINS`, `RATE_LIMIT_PER_MIN`, `COOKIE_SECURE` |
| auth-service | `DATABASE_URL=postgres://auth:auth@ecom-postgres:5432/auth_db`, `AUTH_JWT_PRIVATE_KEY_FILE`, `AUTH_JWT_PUBLIC_KEY_FILE`, `ACCESS_TOKEN_TTL_SEC=900`, `REFRESH_TOKEN_TTL_SEC=604800`, `OTP_TTL_SEC=300`, `VERIFY_LINK_BASE=http://localhost:3000/v1/api/user/welcome_back`, `SEED_API_KEY`, `SEED_ADMIN_EMAIL`, `SEED_ADMIN_PASSWORD` |
| catalog-service | `MONGO_URI=mongodb://catalog:catalog@ecom-mongo:27017/catalog_db?replicaSet=rs0&authSource=catalog_db`, `SKU_CACHE_TTL_SEC=30` |
| inventory-service | `DATABASE_URL=…/inventory_db`, `CATALOG_GRPC_URL`, `RESERVATION_TTL_MS=900000` |
| cart-service | `MONGO_URI=…/cart_db…`, `CATALOG_GRPC_URL`, `INVENTORY_GRPC_URL` |
| discount-service | `DATABASE_URL=…/discount_db`, `CATALOG_GRPC_URL` |
| order-service | `DATABASE_URL=…/order_db`, `CATALOG_GRPC_URL`, `CART_GRPC_URL`, `DISCOUNT_GRPC_URL`, `SAGA_STEP_TIMEOUT_MS`, `SAGA_SWEEP_INTERVAL_MS` |
| payment-service | `DATABASE_URL=…/payment_db`, `PAYMENT_PROVIDER_TIMEOUT_MS=3000`, `PAYMENT_CURRENCY=VND` |
| notification-service | `MONGO_URI=…/notification_db…`, `SMTP_HOST=ecom-mailpit`, `SMTP_PORT=1025`, `MAIL_FROM` |

Secrets accept `<VAR>_FILE`.

## 4. Startup

`scripts/start-all.sh` (order from `scripts/lib.sh`):

1. `ensure_network ecom_net`; `scripts/gen-keys.sh` if `secrets/jwt-private.pem` is missing.
2. Shared infra `make up` → wait until postgres, mongo (rs primary), kafka, kafka-init done.
3. For each service in `SERVICES=(auth-service catalog-service inventory-service cart-service discount-service payment-service order-service notification-service api-gateway)`: `make env` if `.env` missing → `make up` (migrations run on boot for PG services; Mongo indexes are synced on boot) → `wait_for_healthy`.
4. `seed-all.sh` (API key, admin, roles / grants).
5. `status.sh` prints a table of containers, health, and URLs.

`scripts/stop-all.sh` stops in reverse order; `--volumes` also wipes data.

## 5. URLs (local)

| What | URL |
|---|---|
| REST API / OpenAPI | http://localhost:3000/v1/api · http://localhost:3000/docs |
| Grafana | http://localhost:3001 (admin / admin) |
| Prometheus | http://localhost:9090 |
| Kafka UI | http://localhost:8080 |
| Mailpit | http://localhost:8025 |
