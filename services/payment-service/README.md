# payment-service

Captures card payments for orders. Retries are safe: requests carry an
`Idempotency-Key`, and a repeated key returns the original payment instead of
charging again.

## Layout

```
cmd/server/          entry point: config, HTTP server, graceful shutdown
internal/handler/    HTTP endpoints and health probes
internal/model/      payment types and validation
internal/repo/       storage (in-memory today, PostgreSQL next)
```

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `PROVIDER_API_KEY` | yes | none | Payment provider credential. Never commit it. |
| `PROVIDER_URL` | no | sandbox URL | Payment provider endpoint |
| `PROVIDER_TIMEOUT` | no | `5s` | Maximum time to wait for the provider |
| `PORT` | no | `8083` | Listen port |
| `SHUTDOWN_TIMEOUT` | no | `20s` | Time allowed to finish in-flight requests after SIGTERM |

The service exits with code 2 at startup if configuration is missing or invalid.

## Run

```
PROVIDER_API_KEY=local-dev-key go run ./cmd/server
```

## API

| Method and path | Purpose |
|---|---|
| `POST /payments` | Capture a payment. Requires the `Idempotency-Key` header. `201` created, `200` replay, `400`/`422` invalid input |
| `GET /payments/{id}` | Fetch a payment |
| `GET /healthz` | Liveness. `200` while the process runs; never checks dependencies |
| `GET /readyz` | Readiness. `503` while shutting down |

```
curl -X POST localhost:8083/payments \
  -H 'Idempotency-Key: 3f1c9a' \
  -d '{"order_id":"ord_1","amount_cents":4999,"currency":"USD","card_number":"4111111111111111","email":"sam@example.com"}'
```

Amounts are integer cents. Card numbers and emails are never logged.

## Behaviour

- Logs are JSON lines on stdout. No files are written.
- On SIGTERM it fails readiness, finishes in-flight requests, and exits 0.

## Known limitations

- Payments are held in memory: run a single replica until the PostgreSQL repo lands.
- No `/metrics` endpoint yet; it arrives with the Prometheus client library.

## Test

```
go test -race ./...
```