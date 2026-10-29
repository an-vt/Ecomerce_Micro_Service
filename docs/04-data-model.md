# 04 — Data model

One database per service. A service never reads another service's database; it calls its gRPC API, consumes its events, or keeps a projection.

| Service | Store | Why |
|---|---|---|
| auth | PostgreSQL `auth_db` | Unique constraints (email), FK between roles / grants, transactional token rotation |
| catalog | MongoDB `catalog_db` | Product attributes differ per type and per shop; SPU variations are nested arrays; text index |
| inventory | PostgreSQL `inventory_db` | Stock must never go negative: `UPDATE … SET stock = stock - $q WHERE stock >= $q` + `CHECK (stock >= 0)` |
| cart | MongoDB `cart_db` | One document per user with embedded items; read/write whole document |
| discount | PostgreSQL `discount_db` | Usage counting must be atomic with the usage record; unique `(discount_id, order_id)` makes apply idempotent |
| order | PostgreSQL `order_db` | Order + items + saga state + outbox in one ACID transaction |
| payment | PostgreSQL `payment_db` | Money; unique `order_id` prevents double charge |
| notification | MongoDB `notification_db` | Append-heavy, flexible `options` payload |

Common to all: `outbox_messages` and `inbox_messages` (PostgreSQL tables or Mongo collections), see [08](08-messaging.md).

**Money** is an integer amount in the smallest unit (VND has no minor unit, so `price = 150000` means 150 000 ₫). No floats anywhere. `money.Amount` value object in `platform-go/money` (int64).

**Ids**: PostgreSQL rows use `uuid` (v4 generated in the domain). Mongo documents use string ids (ULID) so that every id crossing a boundary is a string.

## 1. auth_db (PostgreSQL)

| Table | Columns | Constraints / indexes |
|---|---|---|
| `accounts` | `id uuid PK`, `type varchar(8)` (`SHOP`\|`USER`), `email varchar(320)`, `name varchar(150)`, `password_hash varchar(100) NULL`, `status varchar(16)` (`active`\|`inactive`\|`pending`\|`blocked`), `verified bool`, `roles text[]`, `created_at`, `updated_at` | `UNIQUE(type, email)` |
| `sessions` | `id uuid PK`, `account_id uuid FK`, `refresh_token_hash char(64)`, `used_refresh_hashes text[]`, `expires_at`, `revoked_at NULL`, `revoke_reason NULL`, `created_at`, `updated_at` | `INDEX(account_id)`, `UNIQUE(refresh_token_hash)` |
| `api_keys` | `id uuid PK`, `name`, `key_hash char(64)`, `permissions text[]`, `active bool`, `created_at` | `UNIQUE(key_hash)` |
| `roles` | `id uuid PK`, `name varchar(32)` (`user`\|`shop`\|`admin`), `slug`, `status` (`active`\|`block`\|`pending`), `description`, timestamps | `UNIQUE(name)` |
| `resources` | `id uuid PK`, `name`, `slug`, `description`, timestamps | `UNIQUE(name)` |
| `grants` | `id uuid PK`, `role_id FK`, `resource_id FK`, `actions text[]`, `attributes varchar default '*'` | `UNIQUE(role_id, resource_id)` |
| `otps` | `id uuid PK`, `email`, `token_hash char(64)`, `status` (`pending`\|`used`), `expires_at`, `created_at` | `UNIQUE(token_hash)` |

Monolith mapping: `Shops` + `Users` → `accounts` (one model, `type` discriminator); `Keys` (one row per shop holding the HS256 secrets) → `sessions` (one row per login, only a hash of the opaque refresh token); `Apikeys.key` (plain text) → `api_keys.key_hash`; `Roles.rol_grants[]` → `grants`; `otp_logs` → `otps` (token stored hashed).

## 2. catalog_db (MongoDB)

| Collection | Fields | Indexes |
|---|---|---|
| `products` | `_id` (ULID), `name`, `description`, `slug`, `price`, `quantity` (initial stock, informational), `type` (`Electronic`\|`Clothing`\|`Furniture`), `thumb`, `shopId`, `attributes` (object, validated by the type strategy), `ratingsAverage`, `variations`, `isDraft`, `isPublished`, `createdAt`, `updatedAt` | `{name:1}` unique, text `{name, description}`, `{shopId:1, isDraft:1}`, `{shopId:1, isPublished:1}` |
| `spus` | `_id` (ULID), `name`, `thumb`, `description`, `slug`, `price`, `categories[]`, `quantity`, `shopId`, `attributes`, `ratingsAverage`, `variations: [{name, options[], images[]}]`, `isDraft`, `isPublished` | `{name:1}` unique |
| `skus` | `_id` = `skuId` (`<spuId>.<n>`), `spuId`, `tierIdx[]`, `isDefault`, `slug`, `sort`, `price`, `stock`, `isDraft`, `isPublished`, `isDeleted` | `{spuId:1}` |
| `comments` | `_id`, `productId`, `userId`, `content`, `left`, `right`, `parentId`, `isDeleted`, timestamps | `{productId:1, left:1}`, `{productId:1, right:1}` |
| `shops` | `_id` = shopId, `name`, `email` | projection from `auth.shop.registered` |

Product types (strategy registry, ported from `ProductFactory.registerProductType`):

| Type | Required attributes |
|---|---|
| `Electronic` | `manufacturer`, `model`, `color` |
| `Clothing` | `brand`, `size`, `material` |
| `Furniture` | `brand`, `size`, `material` |

The monolith stored attributes twice (subtype collection + `product_attributes`). Here the product is **one aggregate, one document** (see [12 D-009](12-decisions.md)).

SKU cache: Redis key `sku:<skuId>`, TTL 30 s, `null` cached too (cache-penetration guard, as in the monolith).

## 3. inventory_db (PostgreSQL)

| Table | Columns | Constraints |
|---|---|---|
| `inventories` | `id uuid PK`, `product_id varchar(64)`, `shop_id uuid`, `location varchar`, `stock int`, `reserved int`, `version int`, timestamps | `UNIQUE(product_id)`, `CHECK (stock >= 0)`, `CHECK (reserved >= 0)` |
| `reservations` | `id uuid PK`, `order_id uuid`, `product_id`, `quantity int`, `status` (`RESERVED`\|`COMMITTED`\|`RELEASED`\|`EXPIRED`), `expires_at`, timestamps | `UNIQUE(order_id, product_id)`, `INDEX(status, expires_at)` |

`stock` = available units. Reserve moves units `stock → reserved`; commit removes them from `reserved`; release / expire moves them back.

## 4. cart_db (MongoDB)

`carts`: `_id` (ULID), `userId` (unique), `state` (`active`\|`completed`\|`failed`\|`pending`), `items: [{productId, shopId, quantity, price, name}]`, `countProduct`, timestamps. Index `{userId:1}` unique.

## 5. discount_db (PostgreSQL)

| Table | Columns | Constraints |
|---|---|---|
| `discounts` | `id uuid PK`, `shop_id`, `code`, `name`, `description`, `type` (`fixed_amount`\|`percent`), `value int`, `start_date`, `end_date`, `max_uses int`, `used_count int`, `max_uses_per_user int`, `min_order_value int`, `is_active bool`, `applies_to` (`all`\|`specific`), `product_ids text[]`, timestamps | `UNIQUE(shop_id, code)`, `CHECK (used_count <= max_uses)` |
| `discount_usages` | `id uuid PK`, `discount_id FK`, `user_id`, `order_id`, `amount int`, `status` (`APPLIED`\|`RELEASED`), timestamps | `UNIQUE(discount_id, order_id)`, `INDEX(discount_id, user_id)` |

`discount_users_used[]` of the monolith becomes rows in `discount_usages`; per-user limit = count of `APPLIED` rows for that user.

## 6. order_db (PostgreSQL)

| Table | Columns | Constraints |
|---|---|---|
| `orders` | `id uuid PK`, `user_id`, `status`, `total_price`, `total_discount`, `fee_ship`, `total_checkout`, `shipping jsonb`, `payment_method varchar`, `payment_token varchar`, `discounts jsonb` (`[{shopId, code}]`), `tracking_number`, `cancel_reason NULL`, `version int`, timestamps | `INDEX(user_id, created_at)` |
| `order_items` | `id uuid PK`, `order_id FK`, `product_id`, `shop_id`, `name`, `price`, `quantity` | `INDEX(order_id)` |
| `saga_instances` | `id uuid PK` (= order id), `type` (`checkout`), `state`, `step_deadline_at`, `data jsonb` (completed steps, payment id, discount amount), `version int`, timestamps | `INDEX(state, step_deadline_at)` |
| `idempotency_keys` | `user_id`, `key`, `request_hash char(64)`, `order_id`, `created_at` | `PRIMARY KEY(user_id, key)` |

## 7. payment_db (PostgreSQL)

`payments`: `id uuid PK`, `order_id uuid`, `user_id`, `amount int`, `currency char(3)`, `method`, `status` (`PENDING`\|`SUCCEEDED`\|`FAILED`\|`REFUNDED`), `provider_ref NULL`, `failure_reason NULL`, timestamps. `UNIQUE(order_id)`.

## 8. notification_db (MongoDB)

| Collection | Fields |
|---|---|
| `notifications` | `_id`, `type` (`ORDER-001`\|`ORDER-002`\|`PROMOTION-001`\|`SHOP-001`), `senderId`, `receiverId` (`*` = broadcast), `content`, `options`, `isRead`, timestamps; index `{receiverId:1, createdAt:-1}` |
| `templates` | `_id`, `name` (unique), `html`, `status` |
| `emails` | `_id`, `to`, `subject`, `template`, `status` (`SENT`\|`FAILED`), `attempts`, `lastError`, timestamps |

## 9. Outbox / inbox (every service)

| Store | Shape |
|---|---|
| `outbox_messages` | `id uuid PK`, `topic`, `key`, `type`, `payload jsonb`, `headers jsonb`, `status` (`PENDING`\|`SENT`), `attempts`, `available_at`, `created_at`, `sent_at`; `INDEX(status, available_at)` |
| `inbox_messages` | `message_id`, `consumer`, `processed_at`; `PRIMARY KEY(message_id, consumer)` |

Mongo services use collections with the same fields (`_id` = message id for outbox; `_id` = `<consumer>:<messageId>` for inbox).
