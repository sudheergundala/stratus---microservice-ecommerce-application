# api-gateway

The single public entry point. Routes `/api/v1/*` requests to the internal
services, with a timeout on every upstream call and a request ID on every
request.

## Routes

| Public path | Forwarded to |
|---|---|
| `/api/v1/auth/*` | `USER_SERVICE_URL` + `/auth/*` |
| `/api/v1/products/*`, `/api/v1/search` | `CATALOG_SERVICE_URL` |
| `/api/v1/orders/*` | `ORDER_SERVICE_URL` + `/orders/*` |
| `GET /healthz` | liveness, never checks upstreams |
| `GET /readyz` | readiness, `503` while shutting down |

Upstream down: `502`. Upstream slower than the timeout: `504`. Body over 1 MiB: `413`.

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `USER_SERVICE_URL` | yes | none | e.g. `http://user-service:8081` |
| `CATALOG_SERVICE_URL` | yes | none | e.g. `http://catalog-service:8085` |
| `ORDER_SERVICE_URL` | yes | none | e.g. `http://order-service:8082` |
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
USER_SERVICE_URL=http://localhost:8081 CATALOG_SERVICE_URL=http://localhost:8085 \
ORDER_SERVICE_URL=http://localhost:8082 node dist/server.js
```

## Behaviour

- Logs are JSON lines on stdout (Fastify/pino). The `Authorization` header is redacted.
- Every response carries `x-request-id`, which is also forwarded upstream.
- On SIGTERM it fails readiness, finishes in-flight requests, and exits 0.

## Known limitations

- JWT verification and rate limiting arrive with user-service.
- No `/metrics` endpoint yet.