# 07 — Authentication, authorization, security

## 1. What changes from the monolith

| Monolith | Problem | New design |
|---|---|---|
| Per-shop random 64-byte hex "public/private key" used as **HS256 secrets**, stored in `Keys` | Every request reads the DB to get the secret; gateway can't verify | One **RS256** key pair owned by auth-service; gateway verifies with the public key (JWKS) |
| One `Keys` document per shop, upserted on login | Second device logs the first one out | One `sessions` row per login |
| Refresh token = JWT stored in clear | DB leak = session takeover | Opaque random 32-byte token; only SHA-256 stored |
| `authenticationV2` accepts the refresh cookie as an access credential | Long-lived token used for every call | Refresh token only accepted by `/handleRefreshToken` |
| API key stored in plain text, DB hit per request | Leak + latency | SHA-256 hash; gateway caches validation 60 s in Redis |
| RBAC role from `req.query.role` | Anyone can claim `admin` | Roles come from the verified token |
| Buyer password = `bcrypt(email)`, `usr_id: 1` hardcoded | Broken | Buyer created on OTP verification, sets a password, logs in |
| `userId` from body/query for cart, checkout, notification | IDOR | `sub` from the token only |

## 2. Tokens

| Token | Format | Lifetime | Where |
|---|---|---|---|
| Access | JWT RS256, `kid` header, claims `{sub, typ: "SHOP"\|"USER", email, roles[], sid, iat, exp, iss: "ecom-auth", aud: "ecom"}` | 15 min (`ACCESS_TOKEN_TTL`) | `Authorization: Bearer` |
| Refresh | 32 random bytes, base64url | 7 days (`REFRESH_TOKEN_TTL`) | httpOnly, `SameSite=Strict` cookie `refreshToken` (or body) |

Keys: `AUTH_JWT_PRIVATE_KEY_FILE` / `AUTH_JWT_PUBLIC_KEY_FILE` (PEM). `scripts/gen-keys.sh` generates a local pair into `secrets/` (git-ignored). `GetJwks` returns the public key as JWK; the gateway caches it and refetches on unknown `kid` (key rotation).

### 2.1 Refresh rotation and reuse detection (port of `handleRefetchTokenV2`)

```
Refresh(token):
  h = sha256(token)
  session = sessions.findActiveByHash(h)          -- current token
  if !session:
      reused = sessions.findByUsedHash(h)          -- token was already rotated away
      if reused: revoke(reused, "REUSE_DETECTED"); publish denylist sid; throw TOKEN_REUSED   (monolith: deleteKeyById + ForBiddenError)
      throw TOKEN_INVALID
  if session.expired or session.revoked: throw TOKEN_INVALID
  account = accounts.get(session.accountId); if !account.active: throw FORBIDDEN
  newToken = random(); session.rotate(h -> sha256(newToken))   -- pushes h into used_refresh_hashes
  return { access: sign(account, session.id), refresh: newToken }
```

Rotation is a single `UPDATE … WHERE refresh_token_hash = $old` so two concurrent refreshes with the same token cannot both win.

### 2.2 Logout and revocation

`Logout(sid)` sets `revoked_at` and writes Redis `auth:revoked:<sid>` with TTL = access-token TTL. The gateway rejects tokens whose `sid` is in that set. Reuse detection does the same.

## 3. API keys

`x-api-key` is checked by the gateway on every route except ops routes:
1. Redis `auth:apikey:<sha256>` hit → use cached `{active, permissions}`.
2. Miss → `AuthService.ValidateApiKey` → cache 60 s (negative results 10 s).
3. Must be active and include permission `0000` (monolith `permission('0000')`).

`console db:seed` in auth-service (`make seed`) creates the dev key from `SEED_API_KEY`.

## 4. RBAC

Model (ported from `rbac.service.js`): `roles` ⟶ `grants(resource, actions[], attributes)` ⟶ `resources`. Actions use the `accesscontrol` vocabulary (implemented in `platform-go/rbac`): `createAny`, `readAny`, `updateAny`, `deleteAny`, `createOwn`, `readOwn`, `updateOwn`, `deleteOwn`.

- Gateway loads all grants with `RbacService.GetGrants` (cached 60 s), builds an in-memory `rbac.Evaluator`.
- `@Rbac('readAny', 'profile')` on a route: allowed if **any** of the token's roles is granted.
- Seeded roles: `user` (readOwn profile), `shop` (readOwn profile, CRUD own products / discounts), `admin` (everything, incl. `rbac`, `template`).
- Coarse guards `@ShopOnly()` / `@UserOnly()` check `typ`. Ownership (product belongs to shop, order belongs to user) is checked **inside the owning service** using the `sub` the gateway forwards in gRPC metadata `x-account-id` / `x-account-type`.

## 5. Service-to-service trust

Internal gRPC ports are only on the Docker network `ecom_net` (not published to the host except for debugging). The gateway forwards the verified principal in metadata; services trust it because only the gateway is exposed. (mTLS between services is listed as future work in [12 D-012](12-decisions.md).)

## 6. Other controls

| Control | Where |
|---|---|
| Security headers middleware, CORS allow-list from `CORS_ORIGINS` (monolith hardcoded `http://localhost:5173`) | gateway |
| Rate limit: 100 req/min per IP on public routes, 10 req/min on `/shop/login`, `/user/login`, `/user/new_user` (Redis fixed window) | gateway |
| Input validation (`go-playground/validator` via Gin binding, unknown JSON fields rejected) | gateway DTOs; domain value objects re-validate |
| Password hashing: bcrypt cost 10 (`golang.org/x/crypto/bcrypt`) | auth |
| OTP: 32 random bytes, stored hashed, 5 min TTL (monolith: `randomInt` 60 s), single use | auth |
| No secrets in logs: slog redaction for `authorization`, `cookie`, `password`, `token`, `x-api-key` | all |
| Body size limit 100 kB | gateway |
