# api-gateway

The single public entry point. Routes `/api/v1/*` requests to the internal
services, verifies access tokens, enforces who may call what, rate-limits
clients, and puts a timeout and a request ID on every upstream call.

## Routes and access

| Public path | Access | Forwarded to |
|---|---|---|
| `POST /api/v1/auth/login`, `POST /api/v1/auth/register` | public, **5/min per IP** | `USER_SERVICE_URL` |
| `/api/v1/auth/*` (other) | public | `USER_SERVICE_URL` + `/auth/*` |
| `GET`/`HEAD /api/v1/products/*` (search: `?q=`) | public | `CATALOG_SERVICE_URL` |
| `PUT`/`POST`/`DELETE /api/v1/products/*` | **admin role** | `CATALOG_SERVICE_URL` |
| `/api/v1/orders/*` | **signed-in user** | `ORDER_SERVICE_URL` + `/orders/*` |
| `GET /healthz` | liveness, never checks upstreams, not rate-limited | |
| `GET /readyz` | readiness, `503` while shutting down, not rate-limited | |

Every route is also limited to `RATE_LIMIT_PER_MINUTE` requests per client IP.

| Situation | Response |
|---|---|
| No token, or a bad token (expired, forged, wrong issuer/audience, `alg: none`) | `401` + `WWW-Authenticate: Bearer`; the reason is logged, never returned |
| Valid token, missing role | `403` |
| Signing keys can't be fetched and none are cached | `503` + `Retry-After: 1` |
| Too many requests | `429` + `Retry-After` |
| Upstream down / slower than the timeout | `502` / `504` |
| Body over 1 MiB | `413` |

## How tokens are checked

user-service signs RS256 tokens and publishes its public key at
`/.well-known/jwks.json`. The gateway fetches that key set once, caches it
(`JWKS_CACHE_MAX_AGE_SECONDS`), and verifies each token locally: signature,
`iss`, `aud`, `exp` (30 s clock tolerance), algorithm RS256 only. A token with
an unknown key id triggers one refetch (at most every 30 s), so key rotation
works without a restart. While keys are cached, tokens keep verifying even if
user-service is down.

Upstream services receive the verified identity as `X-User-Id` and
`X-User-Role`. Clients can't set these: only listed headers
(`authorization`, `content-type`, `idempotency-key`) are forwarded.

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `USER_SERVICE_URL` | yes | none | e.g. `http://user-service:8081` |
| `CATALOG_SERVICE_URL` | yes | none | e.g. `http://catalog-service:8085` |
| `ORDER_SERVICE_URL` | yes | none | e.g. `http://order-service:8082` |
| `JWKS_URL` | yes | none | e.g. `http://user-service:8081/.well-known/jwks.json` |
| `JWT_ISSUER` | no | `stratus-user-service` | Must match user-service |
| `JWT_AUDIENCE` | no | `stratus-api` | Must match user-service |
| `JWKS_CACHE_MAX_AGE_SECONDS` | no | `600` | How long keys are trusted before refetching |
| `RATE_LIMIT_PER_MINUTE` | no | `120` | Per client IP, every route |
| `AUTH_RATE_LIMIT_PER_MINUTE` | no | `5` | Per client IP, login and register |
| `TRUST_PROXY_HOPS` | no | `0` | Proxies in front of the gateway (`1` behind the ALB) |
| `PORT` | no | `8080` | Listen port |
| `UPSTREAM_TIMEOUT_MS` | no | `3000` | Maximum wait for any upstream |
| `SHUTDOWN_TIMEOUT_MS` | no | `20000` | Time allowed for in-flight requests after SIGTERM |

Missing or invalid configuration exits with code 2.

## Develop

Requires Node.js 24.

```
npm ci            # install exactly what package-lock.json lists
npm test          # compile, then run tests
npm run build     # compile TypeScript from src/ to dist/
```

## Behaviour

- Logs are JSON lines on stdout (Fastify/pino). The `Authorization` header is redacted.
- Every response carries `x-request-id`, which is also forwarded upstream.
- `Content-Type`, `Cache-Control` and `Retry-After` from upstream responses reach the client.
- On SIGTERM it fails readiness, finishes in-flight requests, and exits 0.

## Known limitations

- Rate-limit counters are in memory: each replica counts separately, so the
  effective limit is the limit times the replica count. In AWS the first line
  is an AWS WAF rate-based rule on the ALB; a shared store (Redis) is the
  option if exact per-replica-independent limits are needed.
- user-service issues every token with role `customer`; until it can issue
  `admin`, catalog writes are denied for everyone at the gateway (safe default).
- No `/metrics` endpoint yet.