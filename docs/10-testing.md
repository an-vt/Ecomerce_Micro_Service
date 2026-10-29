# 10 — Testing

Three levels, each with its own command. The monolith had no automated tests (`"test": "echo \"Error: no test specified\""`).

| Level | Files | Runs against | Command |
|---|---|---|---|
| Unit | `*_test.go` next to the code | nothing external — fakes only | `make test` |
| Integration | `*_integration_test.go` with `//go:build integration` | real PostgreSQL / MongoDB (replica set) / Kafka / Redis started by **testcontainers-go** | `make test-integration` |
| End-to-end | `e2e/*_test.go` with `//go:build e2e` | the full stack started by `scripts/start-all.sh`, through the gateway only | `make test-e2e` |

## 1. Unit tests

Table-driven, with `testify` for assertions, `t.Parallel()` where safe, and `-race` always on.

What they cover:

- **Domain**: every aggregate method and invariant (`Order` transitions, `CheckoutSaga.OnReply/OnTimeout` for every branch of [06 §4](06-checkout-saga.md#4-compensation), `Inventory.Reserve`, `Discount.Calculate` for fixed / percent / all / specific / min order / max uses / per-user, `Session.Rotate` + reuse detection, nested-set comment arithmetic, product type strategies).
- **Use cases**: each with in-memory fake repositories, `FakeTx` (just calls the function), `FakeOutbox` (records which messages were written), `FixedClock`, stub ports. Fakes live in `internal/usecase/fakes_test.go`; repository fakes must pass the same contract suite as the real repository (`repotest`) so they cannot drift.
- **Transport**: gRPC handlers (request ↔ use case mapping, error mapping with `bufconn`), Kafka handlers (dispatch by `type`), gateway middleware (api-key, JWT, RBAC, revoked session, rate limit), `errs` → status mapping.
- **platform-go**: breaker failure classification and state transitions (fake clock), retry backoff, consumer runner retry → DLQ (fake client), envelope builder, config loading, `jwtx`, `rbac`, migration runner against `sqlmock`/in-memory fake.

Rules: no network, no Docker, no `time.Sleep` (inject `clock.Clock`).

### 1.1 Coverage gate (≥ 80 %)

`scripts/coverage.sh` runs `go test -race -coverprofile` for every module and computes the total of the module's `internal/` (or the library's) packages **excluding** code that is wiring or generated:

```
excluded: cmd/**  internal/app/**  internal/database/migrations/**  internal/database/seeders/**
          internal/testenv/**  **/fake/**  proto/gen/**  *.pb.go
```

The script prints a table per module and fails if any module is below **80 %**; it also fails if `internal/domain` or `internal/usecase` of any service is below 80 % on its own. Integration tests are included in the profile when run with `COVER_INTEGRATION=1` (CI does), so repository code is covered by real-database tests.

```
MODULE                 DOMAIN  USECASE  TOTAL   RESULT
auth-service            91.2    88.4    84.0    ok
…
```

## 2. Integration tests

`platform-go/testkit` (build tag `integration`) starts one container per test binary with `sync.Once`, and isolates each test:

| Dependency | Container | Isolation per test |
|---|---|---|
| PostgreSQL | `postgres:17-alpine` | fresh database `t_<random>`; migrations run through the same runner as production (`testkit.MigrateUp`) |
| MongoDB | `mongo:8` single-node replica set (`testcontainers-go/modules/mongodb`) | fresh database name; migrations run |
| Kafka | `apache/kafka:3.9.1` (KRaft, `modules/kafka`) | unique topic / consumer-group suffix (`testkit.Suffix(t)`) |
| Redis | `redis:7-alpine` | unique key prefix |

What they cover per service:

- Repositories against the real database (mapping, unique constraints, conditional updates, `FOR UPDATE SKIP LOCKED`), and the repository **contract suite** shared with the fakes.
- Migrations: `migrate` → `rollback` → `migrate` leaves the schema identical; `migrate:fresh` works.
- `TxManager` + outbox: an error after `outbox.Add` leaves no outbox row; commit + relay → message on Kafka with headers.
- Consumer + inbox: the same message delivered twice changes state once.
- gRPC server in-process with a real client (proto, metadata, error mapping).
- `testenv`: the whole service wired on a real database with fake peers; each use case exercised end to end.
- Inventory: 50 concurrent reservations for 10 units → exactly 10 succeed.
- Order: the saga driven by a real Kafka and scripted participants — happy path, each failure branch, redelivery, timeout sweep.
- Gateway: `httptest` against the Gin engine with stub gRPC servers; breaker opens after failures → 503 fast → closes after reset.

Requires Docker. The Ryuk reaper removes containers when the test binary exits.

## 3. End-to-end tests

Module `e2e/` (std `testing` + `net/http`, helpers in `platform-go/e2ekit`). Precondition: `make start`. With `E2E_REQUIRED=1` a missing stack fails the test instead of skipping it. The suite talks only to `http://localhost:3000`, plus the Mailpit API (to read the verification link), the Prometheus / Tempo HTTP APIs, and the Docker CLI (for the breaker scenario).

| # | Scenario | Expectation |
|---|---|---|
| 1 | Shop signup → login → refresh → reuse old refresh token → logout | tokens issued; reuse → 403 `TOKEN_REUSED` and session revoked; after logout the access token → 401 |
| 2 | Shop creates product (Clothing) | 201; `GET /inventory/:id` eventually shows stock = `product_quantity`; publish → visible in public list and search |
| 3 | Buyer `new_user` → read link from Mailpit → `welcome_back` → set password → login | tokens issued; second `welcome_back` with same token → 401 |
| 4 | Buyer adds to cart, updates, reviews checkout with a shop discount | totals equal server-side prices minus discount |
| 5 | Place order (`tok_ok`) | 202; polling reaches `CONFIRMED` < 15 s; stock decreased; discount `used_count` +1; item removed from cart; `ORDER-001` notification |
| 6 | Place order (`tok_fail`) | reaches `CANCELLED`; stock back to previous value; discount usage released; `ORDER-002` notification |
| 7 | Same `Idempotency-Key` twice | same `orderId`; different body with same key → 409 |
| 8 | Order more than stock | `CANCELLED` with reason `INSUFFICIENT_STOCK`; stock unchanged |
| 9 | Stop `ecom-inventory`, call `GET /inventory/:id` repeatedly | after the threshold, 503 with `Retry-After` in < 50 ms; start container, after reset → 200 |
| 10 | Observability | every service's `/metrics` responds; Prometheus `up{job=~".*-service"} == 1`; Tempo search finds a trace containing spans from order, inventory and payment for scenario 5 |

## 4. CI

`build/ci/github-actions.yml`: `go mod download` → `golangci-lint` → `go vet` → unit tests with `-race` + coverage gate → integration tests (Docker is available on GitHub runners) → `make check-infra`. E2E runs on manual dispatch (needs the full stack).
