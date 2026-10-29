# 06 — Checkout saga

The monolith's `orderByUser` (never routed) did: re-price → for each product `acquireLock` (which also decremented stock) → `releaseLock` → insert order. Stock was never given back on failure, discount usage was never recorded, there was no payment. Here checkout is an **orchestrated saga** run by order-service.

## 1. Entry point

`POST /v1/api/orders` (gateway) → `OrderService.PlaceOrder` (gRPC):

1. Look up `idempotency_keys(user_id, key)`. Same key + same request hash → return the existing order. Same key + different hash → `IDEMPOTENCY_CONFLICT` (409).
2. `ReviewCheckout` again on the server (prices from catalog via `GetProductsByIds`, discount preview via `CalculateDiscount`). The client's prices are never trusted.
3. In **one transaction**: insert `orders` (`PENDING`) + `order_items` + `saga_instances` (`RESERVING_STOCK`) + `idempotency_keys` + outbox `ReserveStock` command.
4. Return `202 {orderId, status: "PENDING"}`. The client polls `GET /orders/:id`.

## 2. State machines

### 2.1 Saga (`CheckoutSaga`, pure domain object)

```
                ┌──────────────── StockReservationFailed ─────────────────────────────┐
                │                                                                       ▼
RESERVING_STOCK ──StockReserved──▶ APPLYING_DISCOUNT ──DiscountsApplied──▶ PROCESSING_PAYMENT ──PaymentCompleted──▶ COMMITTING_STOCK ──StockCommitted──▶ COMPLETED
       │   (no discounts: skip ─────────────────────────────────────────▶)       │                                     │ timeout: re-send CommitStock
       │                                 │ DiscountsRejected / timeout          │ PaymentFailed / timeout               │ (payment is the pivot —
       │ timeout                         ▼                                      ▼                                       │  only forward recovery after it)
       └──────────────────────────▶ COMPENSATING ◀──────────────────────────────┘
                                         │ all compensation replies received
                                         ▼
                                      FAILED
```

`onReply(reply)` and `onTimeout(now)` return `{ state, commands[], orderTransition }`. They have no I/O, so every branch is unit-tested ([10](10-testing.md)).

### 2.2 Order

| Saga event | Order status |
|---|---|
| saga started | `PENDING` |
| `StockReserved` | `STOCK_RESERVED` |
| `DiscountsApplied` | `DISCOUNT_APPLIED` |
| entering `PROCESSING_PAYMENT` | `PAYMENT_PROCESSING` |
| `COMPLETED` | `CONFIRMED` → event `order.order.confirmed` |
| entering `COMPENSATING` | `CANCELLING` (with `cancel_reason`) |
| `FAILED` | `CANCELLED` → event `order.order.cancelled` |

Transitions not in the table throw `INVALID_STATE` (guarded in the `Order` aggregate).

## 3. Steps

| # | Command (topic) | Participant does | Success reply | Failure reply | Compensation (command → reply) |
|---|---|---|---|---|---|
| 1 | `ReserveStock{orderId, items}` (`inventory.commands`) | For each item, in one tx: `UPDATE inventories SET stock=stock-q, reserved=reserved+q WHERE product_id=$1 AND stock>=q`; insert `reservations` (`RESERVED`, expires in 15 min). All or nothing. | `StockReserved` | `StockReservationFailed{reason, productId}` | `ReleaseStock{orderId}` → `StockReleased` |
| 2 | `ApplyDiscounts{orderId, userId, discounts[]}` (`discount.commands`) — skipped when the order has no codes | For each code, lock the discount row (`FOR UPDATE`), re-check rules (active, dates, max uses, per-user uses, min order value), compute amount, insert `discount_usages` (`APPLIED`), `used_count + 1`. All or nothing. | `DiscountsApplied{totalDiscount}` | `DiscountsRejected{reason, code}` | `ReleaseDiscounts{orderId}` → `DiscountsReleased` |
| 3 | `ProcessPayment{orderId, userId, amount, method, token}` (`payment.commands`) | Insert `payments` (`PENDING`), call provider through the breaker, set `SUCCEEDED` / `FAILED`. | `PaymentCompleted{paymentId}` | `PaymentFailed{reason}` | `RefundPayment{orderId}` → `PaymentRefunded` |
| 4 | `CommitStock{orderId}` (`inventory.commands`) | Reservations `RESERVED → COMMITTED`, `reserved - q`. | `StockCommitted` | — (cannot fail after reservation: the hold outlives the saga timeouts. If it somehow did lapse, the command is a permanent failure and lands in `inventory.commands.dlq` for a human; the DLQ alert fires) | — |

`amount = totalPrice − totalDiscount + feeShip`, with `totalDiscount` taken from `DiscountsApplied` (not from the review).

All replies go to `order.saga.replies`, key = `orderId`.

## 4. Compensation

On failure at step *n*, the saga sends compensations for every step that **may** have happened (completed steps and the step that failed by timeout), in reverse order, all at once:

| Failure | Compensations sent |
|---|---|
| `StockReservationFailed` | none (participant already rolled back) |
| step 1 timeout | `ReleaseStock` |
| `DiscountsRejected` | `ReleaseStock` |
| step 2 timeout | `ReleaseDiscounts`, `ReleaseStock` |
| `PaymentFailed` | `ReleaseDiscounts` (if step 2 ran), `ReleaseStock` |
| step 3 timeout | `RefundPayment`, `ReleaseDiscounts` (if step 2 ran), `ReleaseStock` |

The saga reaches `FAILED` when every compensation has been acknowledged.

### 4.1 Why this is safe

- **Compensations are idempotent and tolerate "nothing to undo"**: `ReleaseStock` for an order with no reservation replies `StockReleased`; `RefundPayment` for an order never charged marks a `FAILED` / cancelled payment record so a late `ProcessPayment` is refused.
- **Per-order ordering**: forward command and its compensation go to the same topic with the same key (`orderId`), so the participant always handles `ReserveStock` before `ReleaseStock`. A delayed forward command can never be applied after its compensation.
- **Every participant handler is inbox-guarded** and its reply is written to the outbox in the same transaction as its state change.
- **Stale replies** (a reply that does not match the current saga state, e.g. `StockReserved` arriving after a timeout) are ignored and counted in `saga_stale_replies_total`.
- **Pivot**: once payment succeeded, the saga only moves forward (re-sends `CommitStock` on timeout) — undoing a successful payment because a stock commit was slow would be worse.

## 5. Timeouts

| Setting | Default | Env |
|---|---|---|
| Forward step deadline | 30 s | `SAGA_STEP_TIMEOUT_MS` |
| Compensation re-send interval | 30 s (forever, alert after 3) | `SAGA_COMPENSATION_RETRY_MS` |
| Sweeper interval | 5 s | `SAGA_SWEEP_INTERVAL_MS` |
| Reservation TTL (inventory, second safety net) | 15 min | `RESERVATION_TTL_MS` |

The sweeper selects `saga_instances WHERE state NOT IN ('COMPLETED','FAILED') AND step_deadline_at < now() FOR UPDATE SKIP LOCKED LIMIT 50`, so several order-service replicas can run it.

## 6. Side effects of the outcome

| Event | Consumer | Effect |
|---|---|---|
| `order.order.confirmed{orderId, userId, items, totalCheckout}` | cart-service | Remove bought items from the user's cart (F-23) |
| | notification-service | `ORDER-001` to the buyer |
| `order.order.cancelled{orderId, userId, reason}` | notification-service | `ORDER-002` to the buyer |

## 7. Observability

Metrics: `saga_started_total`, `saga_completed_total`, `saga_failed_total{reason}`, `saga_compensations_total{step}`, `saga_step_timeouts_total{step}`, `saga_duration_seconds`. The whole saga is one trace: the outbox stores `traceparent`, each consumer continues it ([14](14-observability.md)).
