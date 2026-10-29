# 08 — Messaging (Kafka)

Client library: **franz-go** (`github.com/twmb/franz-go`) with the `kotel` plugin for OpenTelemetry.

The monolith used RabbitMQ only for verification emails (producer opened a new connection per message, the exchange was asserted on every send, and the producer lived under `src/tests/`). This system uses **Kafka (KRaft, single broker locally)** for all asynchronous integration.

## 1. Topics

Created by `shared-infra/infra/local/kafka/create-topics.sh` (3 partitions, RF 1 locally). Auto-create is disabled.

### 1.1 Domain events

| Topic | Producer | Payload | Consumers |
|---|---|---|---|
| `auth.shop.registered` | auth | `{shopId, name, email}` | catalog (shop projection) |
| `auth.user.registered` | auth | `{userId, email}` | notification (welcome) |
| `auth.email-verification.requested` | auth | `{email, link, expiresAt}` | notification (email) |
| `catalog.product.created` | catalog | `{productId, shopId, name, price, quantity, type}` | inventory (create stock), notification (`SHOP-001`) |
| `catalog.product.updated` | catalog | `{productId, shopId, name, price, quantity?}` | inventory (set stock if `quantity` given — monolith `updateInventory`) |
| `discount.discount.created` | discount | `{discountId, shopId, code, name, value, type}` | notification (`PROMOTION-001`) |
| `order.order.confirmed` | order | `{orderId, userId, items[], totalCheckout}` | cart, notification |
| `order.order.cancelled` | order | `{orderId, userId, reason}` | notification |

### 1.2 Saga commands and replies

| Topic | Producer → consumer | Messages |
|---|---|---|
| `inventory.commands` | order → inventory | `ReserveStock`, `ReleaseStock`, `CommitStock` |
| `discount.commands` | order → discount | `ApplyDiscounts`, `ReleaseDiscounts` |
| `payment.commands` | order → payment | `ProcessPayment`, `RefundPayment` |
| `order.saga.replies` | inventory, discount, payment → order | `StockReserved`, `StockReservationFailed`, `StockReleased`, `StockCommitted`, `DiscountsApplied`, `DiscountsRejected`, `DiscountsReleased`, `PaymentCompleted`, `PaymentFailed`, `PaymentRefunded` |

### 1.3 Dead-letter topics

Every consumed topic `T` has `T.dlq`. See §5.

## 2. Envelope

Kafka message value (JSON):

```json
{
  "messageId": "01JD…",           // ULID, unique per message, used by the inbox
  "type": "ReserveStock",          // event or command name
  "occurredAt": "2026-10-29T10:00:00.000Z",
  "aggregateId": "6f1c…",          // also the Kafka key
  "correlationId": "6f1c…",        // orderId for saga traffic
  "payload": { … }
}
```

Kafka headers: `traceparent`, `tracestate` (W3C trace context), `x-request-id`, and on DLQ messages `x-error`, `x-attempts`, `x-original-topic`.

Key = `aggregateId` → all messages for one order / product land on the same partition, in order.

## 3. Transactional outbox

Producers never call Kafka inside a use case. They write the message in the **same database transaction** as the state change:

```
uow.run(tx => { repo.save(aggregate, tx); outbox.add(messages, tx); })
```

A relay in each service publishes pending rows:

| Store | Claim strategy |
|---|---|
| PostgreSQL | In one transaction: `pg_try_advisory_xact_lock` (only one relay replica is active at a time — parallel claiming could reorder two messages of one aggregate), then `SELECT … WHERE status='PENDING' AND available_at <= now() ORDER BY created_at LIMIT 100 FOR UPDATE SKIP LOCKED`; produce the batch (`acks=all`, idempotent producer); `UPDATE status='SENT'`; commit. On a produce error: `attempts+1`, `available_at = now() + backoff`, and later messages of the same key wait. |
| MongoDB | A lease document in `outbox_lease` (15 s, renewed every poll, taken over when expired) elects the active relay; it reads the due messages in `created_at` order, produces, and marks them `SENT`. (`mongo-driver/v2`) |

The relay polls every 200 ms (`OUTBOX_POLL_MS`) and goes straight on when a batch was full; sent rows older than 7 days are purged hourly. The producer continues the trace stored in the row (`traceparent` header) and the bookkeeping writes survive a shutdown, so a message that reached Kafka is not published twice. Metrics: `outbox_pending`, `outbox_oldest_age_seconds`, `outbox_published_total`, `outbox_publish_errors_total`.

Ordering: messages for one aggregate are inserted in commit order and the relay sorts by `created_at`; with a single relay per service per poll the per-key order is kept. Duplicates are possible (crash between produce and mark) and are handled by the inbox.

## 4. Inbox (idempotent consumer)

```
consume(msg):
  uow.run(tx => {
    if (!inbox.tryInsert(msg.messageId, consumerName, tx)) return;   // duplicate → skip
    handler(msg, tx);                                                 // state change + outbox replies, same tx
  })
  commit offset
```

PostgreSQL: `INSERT … ON CONFLICT DO NOTHING` and check `rowCount`. Mongo: insert `{_id: consumer:messageId}` in the session; duplicate key → skip.

## 5. Consumer runner, retries, DLQ

`kafkax.ConsumerRunner` in `platform-go`:

- `eachMessage` with manual offset commit after the handler's transaction commits (`autoCommit: false`).
- Failure → retry in-process with exponential backoff and jitter (`200 ms × 2^n`, max 5 s) up to `KAFKA_MAX_ATTEMPTS` (default 5).
- `DomainError` with a non-retryable code (validation, not found) → straight to DLQ (no point retrying).
- After the last attempt → produce to `<topic>.dlq` with the original headers + `x-error`, `x-attempts`, then commit the offset so the partition is not blocked.
- Metrics: `kafka_messages_consumed_total{topic,result}`, `kafka_message_retries_total`, `kafka_dlq_total{topic}`; alert on any DLQ message ([14](14-observability.md)).

Why not a separate retry topic (like the monolith's `x-retry-count` re-queue)? Kafka has no per-message delay; a retry topic needs a paused consumer and adds ordering problems for saga traffic. In-process backoff keeps per-key order; poison messages are parked in the DLQ ([12 D-006](12-decisions.md)).

## 6. Consumer groups

| Group | Topics |
|---|---|
| `catalog-service` | `auth.shop.registered` |
| `inventory-service` | `catalog.product.created`, `catalog.product.updated`, `inventory.commands` |
| `cart-service` | `order.order.confirmed` |
| `discount-service` | `discount.commands` |
| `order-service` | `order.saga.replies` |
| `payment-service` | `payment.commands` |
| `notification-service` | `auth.user.registered`, `auth.email-verification.requested`, `catalog.product.created`, `discount.discount.created`, `order.order.confirmed`, `order.order.cancelled` |
