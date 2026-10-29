# 05 — API design

## 1. External REST API (api-gateway, port 8088)

Base path `/v1/api` (same as the monolith), served by **Gin**. OpenAPI spec at `api-gateway/api/openapi/openapi.yaml`, served at `/docs`.

### 1.1 Headers

| Header | Required | Meaning |
|---|---|---|
| `x-api-key` | every route except `/health`, `/metrics`, `/docs` | API key with permission `0000` (monolith behaviour) |
| `authorization: Bearer <access>` | routes marked 🔒 | RS256 access token from auth-service |
| `Idempotency-Key` | `POST /orders` | Client-chosen key; same key + same body → same order |
| `x-request-id` | optional | Propagated; generated when absent; returned in the response |

The monolith also required `x-client-id`; it is no longer needed because the token is verified with a public key.

### 1.2 Response envelope

Success (kept from monolith `SuccessResponse`):
```json
{ "message": "Created product", "status": 201, "metadata": { ... } }
```
Error:
```json
{ "status": "error", "code": 409, "error": "DUPLICATE_EMAIL", "message": "Shop already registered", "requestId": "01J…" }
```

### 1.3 gRPC status → HTTP

| gRPC | HTTP | | gRPC | HTTP |
|---|---|---|---|---|
| `INVALID_ARGUMENT` | 400 | | `FAILED_PRECONDITION` | 422 |
| `UNAUTHENTICATED` | 401 | | `RESOURCE_EXHAUSTED` | 429 |
| `PERMISSION_DENIED` | 403 | | `UNAVAILABLE` / breaker open | 503 + `Retry-After` |
| `NOT_FOUND` | 404 | | `DEADLINE_EXCEEDED` | 504 |
| `ALREADY_EXISTS` / `ABORTED` | 409 | | other | 500 |

The domain error code (e.g. `INSUFFICIENT_STOCK`) travels in gRPC metadata `x-error-code` and becomes `error` in the envelope.

### 1.4 Routes

Legend: 🔑 api key only · 🔒 any logged-in account · 🏪 shop account · 👤 user (buyer) account · 🛡 RBAC check.

**Auth — shop** (monolith `/v1/api/shop/*`, same paths)

| Method | Path | Guard | → gRPC |
|---|---|---|---|
| POST | `/shop/signup` `{name,email,password}` | 🔑 | `AuthService.SignUpShop` |
| POST | `/shop/login` `{email,password}` | 🔑 | `AuthService.Login` (type SHOP) |
| POST | `/shop/logout` | 🔒 | `AuthService.Logout` |
| POST | `/shop/handleRefreshToken` (cookie `refreshToken` or body `{refreshToken}`) | 🔑 | `AuthService.Refresh` |

Login / signup / refresh return `{ account, tokens: { accessToken, refreshToken, expiresIn } }` and set the httpOnly `refreshToken` cookie (path `/v1/api`, 7 days — the monolith's cookie lived ~43 min by mistake).

**Auth — user (buyer)**

| Method | Path | Guard | → gRPC |
|---|---|---|---|
| POST | `/user/new_user` `{email}` | 🔑 | `AuthService.RequestEmailVerification` |
| GET | `/user/welcome_back?token=` | 🔑 | `AuthService.VerifyEmail` → creates user, returns tokens |
| POST | `/user/password` `{password}` | 👤 | `AuthService.SetPassword` |
| POST | `/user/login` `{email,password}` | 🔑 | `AuthService.Login` (type USER) |
| POST | `/user/logout` | 🔒 | `AuthService.Logout` |
| POST | `/user/handleRefreshToken` | 🔑 | `AuthService.Refresh` |

**RBAC** (monolith had no auth on these; now admin only)

| Method | Path | Guard | → gRPC |
|---|---|---|---|
| POST | `/rbac/role` | 🛡 `createAny:rbac` | `RbacService.CreateRole` |
| GET | `/rbac/roles` | 🛡 `readAny:rbac` | `RbacService.ListRoles` |
| POST | `/rbac/resource` | 🛡 `createAny:rbac` | `RbacService.CreateResource` |
| GET | `/rbac/resources` | 🛡 `readAny:rbac` | `RbacService.ListResources` |
| GET | `/profile/viewAny` | 🛡 `readAny:profile` | gateway-local (returns accounts summary via `AuthService.GetAccount`) |
| GET | `/profile/viewOwn` | 🛡 `readOwn:profile` | `AuthService.GetAccount(sub)` |

**Catalog**

| Method | Path | Guard | → gRPC `CatalogService.` |
|---|---|---|---|
| GET | `/product?limit&page&sort` | 🔑 | `ListProducts` |
| GET | `/product/search/:keySearch` | 🔑 | `SearchProducts` |
| GET | `/product/:productId` | 🔑 | `GetProduct` |
| POST | `/product` `{product_type, product_name, product_price, product_quantity, product_attributes, …}` | 🏪 | `CreateProduct` |
| PATCH | `/product/:productId` | 🏪 | `UpdateProduct` |
| POST | `/product/publish/:id` · `/product/unpublish/:id` | 🏪 | `PublishProduct` · `UnpublishProduct` |
| GET | `/product/drafts/all` · `/product/published/all` | 🏪 | `ListShopProducts{published}` |
| POST | `/product/spu/new` | 🏪 | `CreateSpu` |
| GET | `/product/spu/get_spu_info?product_id` | 🔑 | `GetSpu` |
| GET | `/product/sku/select_variation?sku_id&product_id` | 🔑 | `GetSku` (cached) |
| GET | `/comment/list?productId&parentId&limit&skip` | 🔑 | `ListComments` |
| POST | `/comment` `{productId, content, parentCommentId?}` | 🔒 | `CreateComment` |
| DELETE | `/comment?id&productId` | 🔒 | `DeleteComment` (author only) |

**Inventory**

| Method | Path | Guard | → gRPC `InventoryService.` |
|---|---|---|---|
| POST | `/inventory` `{productId, stock, location?}` | 🏪 (owner) | `AddStock` |
| GET | `/inventory/:productId` | 🔑 | `GetStock` |

**Cart** (user id always from the token; the monolith trusted `userId` from the body)

| Method | Path | Guard | → gRPC `CartService.` |
|---|---|---|---|
| POST | `/cart` `{product:{productId, shopId, quantity}}` | 👤 | `AddToCart` |
| POST | `/cart/update` `{shop_order_ids:[{shopId, item_products:[{productId, shopId, quantity, old_quantity}]}]}` | 👤 | `UpdateCart` |
| DELETE | `/cart` `{productId}` | 👤 | `RemoveItem` |
| GET | `/cart` | 👤 | `GetCart` |

**Discount**

| Method | Path | Guard | → gRPC `DiscountService.` |
|---|---|---|---|
| POST | `/discount` | 🏪 | `CreateDiscount` |
| PATCH | `/discount/:discountId` | 🏪 (owner) | `UpdateDiscount` |
| GET | `/discount?limit&page` | 🏪 | `ListShopDiscounts` |
| GET | `/discount/list_product_code?code&shopId&limit&page` | 🔑 | `ListProductsForCode` |
| POST | `/discount/amount` `{code, shopId, products:[{productId, price, quantity}]}` | 👤 | `CalculateDiscount` |

**Checkout / orders**

| Method | Path | Guard | → gRPC `OrderService.` |
|---|---|---|---|
| POST | `/checkout/review` `{shop_order_ids:[{shopId, shop_discounts:[{codeId}], item_products:[{productId, quantity}]}]}` | 👤 | `ReviewCheckout` |
| POST | `/orders` `{shop_order_ids, shipping:{address,…}, payment:{method:"card", token}}` + `Idempotency-Key` | 👤 | `PlaceOrder` → **202** `{orderId, status}` |
| GET | `/orders/:orderId` | 👤 (owner) | `GetOrder` |
| GET | `/orders?limit&page` | 👤 | `ListOrders` |

**Notification**

| Method | Path | Guard | → gRPC `NotificationService.` |
|---|---|---|---|
| GET | `/notification?type&isRead` | 🔒 | `ListNotifications` (receiver = token sub, plus broadcasts) |
| POST | `/email/new_template` `{name, html}` | 🛡 `createAny:template` | `CreateTemplate` |

**Ops**: `GET /health/live`, `GET /health/ready` (checks Redis + upstream breakers), `GET /metrics`.

## 2. Internal gRPC

Contracts live in `proto/<svc>/v1/<svc>.proto`, package `ecom.<svc>.v1`. Code is generated with `buf generate` into `proto/gen/go/` (committed). Free-form JSON (product attributes, variations) travels as `google.protobuf.Struct` / `bytes *_json` fields.

| Service | RPCs | Called by |
|---|---|---|
| `auth.v1.AuthService` | `SignUpShop`, `Login`, `Refresh`, `Logout`, `RequestEmailVerification`, `VerifyEmail`, `SetPassword`, `GetAccount`, `ValidateApiKey`, `GetJwks` | gateway |
| `auth.v1.RbacService` | `CreateRole`, `ListRoles`, `CreateResource`, `ListResources`, `GetGrants` | gateway |
| `catalog.v1.CatalogService` | products, SPU/SKU, comments (above) + `GetProductsByIds` (re-pricing), `ListProductIdsByShop` | gateway, cart, order, discount |
| `inventory.v1.InventoryService` | `AddStock`, `GetStock`, `CheckStock{items}` | gateway, cart |
| `cart.v1.CartService` | `AddToCart`, `UpdateCart`, `RemoveItem`, `GetCart` | gateway, order |
| `discount.v1.DiscountService` | `CreateDiscount`, `UpdateDiscount`, `ListShopDiscounts`, `ListProductsForCode`, `CalculateDiscount` | gateway, order |
| `order.v1.OrderService` | `ReviewCheckout`, `PlaceOrder`, `GetOrder`, `ListOrders` | gateway |
| `notification.v1.NotificationService` | `ListNotifications`, `CreateTemplate` | gateway |

payment-service has **no** synchronous API: it is driven only by `payment.commands`.

Every call carries metadata `x-request-id` and `traceparent`; the caller sets a deadline (default 2 s) and goes through its circuit breaker ([09](09-resilience.md)).

### 2.1 Errors

Use cases return `errs.Error{Code}`; the gRPC error interceptor in `platform-go/grpcx` maps the code to a gRPC status:

| Domain code (examples) | gRPC status |
|---|---|
| `VALIDATION_FAILED` | `INVALID_ARGUMENT` |
| `*_NOT_FOUND` | `NOT_FOUND` |
| `DUPLICATE_*`, `ALREADY_*` | `ALREADY_EXISTS` |
| `INVALID_CREDENTIALS`, `TOKEN_*` | `UNAUTHENTICATED` |
| `FORBIDDEN`, `NOT_OWNER` | `PERMISSION_DENIED` |
| `INSUFFICIENT_STOCK`, `DISCOUNT_*`, `INVALID_STATE` | `FAILED_PRECONDITION` |
| `IDEMPOTENCY_CONFLICT` | `ABORTED` |
