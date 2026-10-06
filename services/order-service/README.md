# order-service

Places orders: validates the request, computes the total, charges it through
payment-service exactly once per order, and returns the confirmed order.
Retries are safe: a repeated `Idempotency-Key` returns the original order.

## Layout

```
src/main/java/com/stratus/order/
  OrderServiceApplication.java   entry point
  api/       controller, request validation, JSON error responses
  domain/    Order, OrderItem, OrderStatus
  service/   order workflow: idempotency, totals, payment
  repo/      storage (in-memory today, PostgreSQL next)
  payment/   payment-service client with timeouts
src/main/resources/application.yml   configuration, read from env vars
src/test/java/...                    API tests against a fake payment-service
```

## Configuration

| Variable | Required | Default | Purpose |
|---|---|---|---|
| `PAYMENT_SERVICE_URL` | yes | none | e.g. `http://payment-service:8083` |
| `PAYMENT_TIMEOUT` | no | `2s` | Maximum wait for payment-service |
| `PORT` | no | `8082` | Listen port |
| `SHUTDOWN_TIMEOUT` | no | `20s` | Time allowed for in-flight requests after SIGTERM |

A missing `PAYMENT_SERVICE_URL` stops startup.

## API

| Method and path | Result |
|---|---|
| `POST /orders` | Requires `Idempotency-Key`. `201` created, `200` replay, `400` bad request, `422` invalid order or reused key, `503` payment unavailable (retry with the same key) |
| `GET /orders/{id}` | `200` or `404` |
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness; `DOWN` during shutdown |

## Behaviour

- JSON logs (ECS format) on stdout. Card numbers and emails are never logged or stored.
- On SIGTERM: readiness goes DOWN, in-flight requests finish (up to `SHUTDOWN_TIMEOUT`), then it exits.

## Known limitations

- Orders are held in memory: run a single replica until the PostgreSQL repo lands.
- Prices come from the client; catalog-service will supply them.
- Inventory reservation and the fraud check join the flow when those services exist.

## Build and test

```
mvn -B test        # tests
mvn -B package     # builds target/order-service.jar
```