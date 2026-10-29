# 13 — Monolith migration map

Source: `Ecomerce_project/Ecomerce_BE/src` and `System_message_queue_Ecomerce_BE/src`.

## 1. Module → service

| Monolith | Target |
|---|---|
| `services/access.service.js`, `keyToken.service.js`, `shop.service.js`, `auth/authUtils.js` | auth-service (`accounts`, `sessions`, token issuing), gateway (verification) |
| `services/user.service.js`, `otp.service.js` | auth-service (`otps`, email verification) |
| `services/email.service.js`, `template.service.js`, worker `consumerQueue.service.js` | notification-service (templates, SMTP, retry) |
| `services/apiKey.service.js`, `auth/checkAuth.js` | auth-service (`api_keys`), gateway guard |
| `services/rbac.service.js`, `middlewares/rbac.js`, `role.model.js`, `resource.model.js` | auth-service (`roles`, `resources`, `grants`), gateway guard |
| `services/product.service.xxx.js`, `product.model.js`, `product.repo.js` | catalog-service (`products`, type registry) |
| `services/spu.service.js`, `sku.service.js`, `cache.repo.js`, `readCache.middleware.js` | catalog-service (`spus`, `skus`, Redis cache) |
| `services/comment.service.js` | catalog-service (`comments`) |
| `services/inventory.service.js`, `inventory.repo.js`, `redis.service.js` | inventory-service |
| `services/cart.service.js`, `cart.repo.js` | cart-service |
| `services/discount.service.js`, `discount.repo.js` | discount-service |
| `services/checkout.service.js` (`checkoutReview`, `orderByUser`), `order.model.js` | order-service |
| *(none)* | payment-service (new) |
| `services/notification.service.js` | notification-service |
| `services/*.upload.service.js`, `configs/{s3,cloudinary,multer}.config.js` | catalog-service media adapter (optional phase P13) |
| `logger/discord*.js` | removed → slog + Loki |
| `tests/message_queue/*`, `tests/*.test.js` (manual scripts) | removed → real test suites ([10](10-testing.md)) |
| `sql/*.sql` (MySQL RBAC experiment) | dropped |

## 2. Route mapping

All paths keep the `/v1/api` prefix. Changes are listed; unchanged routes are in [05 §1.4](05-api-design.md#14-routes).

| Monolith | New | Change |
|---|---|---|
| `POST /v1/api/shop/signup`, `/login`, `/logout`, `/handleRefreshToken` | same | tokens RS256; response adds `expiresIn` |
| `POST /v1/api/user/new_user`, `GET /user/welcome_back` | same | + `POST /user/password`, `POST /user/login`, `POST /user/logout` |
| `POST /v1/api/email/new_template` | same | admin RBAC |
| `POST/GET /v1/api/rbac/*` | same | admin RBAC (was open) |
| `GET /v1/api/profile/viewAny|viewOwn` | same | role from token, not `?role=` |
| `POST /v1/api/inventory/` | same | owner check; was crashing (missing import) |
| `POST /v1/api/cart`, `/cart/update`, `DELETE /cart`, `GET /cart?userId` | same, no `userId` | user from token |
| `POST /v1/api/checkout/review` | same | user from token |
| *(not routed)* `orderByUser` | `POST /v1/api/orders`, `GET /orders`, `GET /orders/:id` | new, saga |
| `GET /v1/api/notification?userId` | `GET /notification` | user from token |
| `GET /v1/api/discount/list_product_Code` | `GET /discount/list_product_code` | lowercase path |
| `POST /v1/api/upload/*` | deferred (P13) | was unauthenticated |

## 3. Monolith defects and how they are fixed

| # | Defect (file) | Fix |
|---|---|---|
| 1 | `checkout.service.js`: `findCartById` not awaited → cart check always passes; `orderByUser` destructures the wrong field and does not await `checkoutReview` | `ReviewCheckout` use case awaited and typed; cart loaded via `CartService.GetCart`, items verified against it |
| 2 | `orderByUser` acquires and immediately releases the lock, so nothing protects the order; `order_payment: userId` | Saga with real reservation; `payments` table |
| 3 | `redis.service.js` `acquireLock`: lock key has no TTL when reservation fails (leak) | Removed; conditional `UPDATE` in PostgreSQL |
| 4 | `inventory.repo.js` `reservationInventory`: pushes to `inven_reservations` but schema field is `inven_reservation` → reservation not stored | `reservations` table |
| 5 | `discount.service.js` `getDiscountAmount`: never increments `used_count` or records the user; `max_uses_per_user` never enforced; amount computed only if `min_order_value > 0`; `ForBiddenError` not imported | `ApplyDiscounts` records usage atomically; amount always computed |
| 6 | `discount.service.js` `cancelDiscountCode` calls `findByIdAndDelete`; `deleteDiscountCode` unrouted | `ReleaseDiscounts` (marks usage released); delete is not exposed |
| 7 | `access.service.js` `logout`: `deleteOne(id)` with bare id, controller reads `tokens` from a delete result | `Logout(sid)` revokes session + denylist |
| 8 | `authUtils.js`: refresh cookie accepted as access credential; secret lookup per request | Separate paths; RS256 |
| 9 | Cookie `maxAge` `3*24*60*60*10` ms (~43 min) | 7 days |
| 10 | `access.controller` / `user.service`: buyer created with `usr_id:1`, `usr_password: bcrypt(email)`, hardcoded role id; `Key.user` is an ObjectId ref to Shop but is given a number | Buyer is an `accounts` row (type USER); password set explicitly |
| 11 | `utils/index.js` `replacePlaceholder` uses `template.replace` each loop → only the last key replaced | Fixed in notification template renderer (test covers multiple placeholders) |
| 12 | `email.service`: verification link host hardcoded to `localhost:3000`; OTP TTL 60 s | `VERIFY_LINK_BASE`, 5 min |
| 13 | `rbac.js`: grants reloaded from DB per request; role from query string | Cached grants; role from token |
| 14 | `cart/checkout/notification/comment` take `userId` from body/query (IDOR) | Principal from token |
| 15 | `cart.service.js` `addToCartV2`: `quantity === 0` branch is an empty stub | Quantity 0 removes the item |
| 16 | `notification.service.js` ↔ `product.service.xxx.js` circular require; `createComment` creates notifications | Event-driven; method renamed |
| 17 | Producer for production email lives in `src/tests/…`; new connection per message; hardcoded `amqp://localhost` | Outbox + Kafka |
| 18 | `product.service.xxx.js` creates subtype doc, product doc, inventory in separate non-transactional writes and fires notification directly | Single product aggregate; event → inventory + notification |
| 19 | `controllers/inventory.controller.js` uses `SuccessResponse` without import | New code, tested |
| 20 | API keys committed in `src/postman/*.http`; keys plain text | Seeded from env; hashed |
| 21 | No `package.json` test script / no tests | Unit + integration + e2e, 80 % gate |
| 22 | `docker-compose` has no app images; worker compose ports mismatch code | Dockerfile + compose per service |

## 4. Data migration

Out of scope for this backend rewrite (greenfield databases). If needed later: a one-off script reading the Mongo `Shops`, `Products`, `Discounts` and writing through the new gRPC APIs, mapping ObjectIds to new ids and keeping a `legacy_id` field.
