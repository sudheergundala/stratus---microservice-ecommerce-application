# inventory-service

Tracks stock per SKU and holds stock for orders. Reservations are
all-or-nothing and can never oversell, because every change is a DynamoDB
conditional transaction.

## API

| Method and path | Purpose |
|---|---|
| `PUT /stock/{sku}` | Set available stock: `{"available": 10}` |
| `GET /stock/{sku}` | `{"sku","available","reserved"}` or `404` |
| `POST /reservations` | `{"order_id","items":[{"sku","quantity"}]}` → `201` held, `200` replay, `409` insufficient stock (nothing held), `422` invalid or conflicting |
| `POST /reservations/{order_id}/commit` | Order paid: reserved stock is consumed. `204` / `404` |
| `DELETE /reservations/{order_id}` | Order failed: reserved stock returns to available. `204` / `404` |
| `GET /healthz`, `GET /readyz` | Liveness; readiness (`503` while shutting down) |

## Data model (one DynamoDB table, partition key `pk`)

| `pk` | Attributes |
|---|---|
| `SKU#<sku>` | `available`, `reserved` |
| `RES#<order_id>` | `items`, `created_at` |

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `TABLE_NAME` | `stratus-inventory` | DynamoDB table |
| `AWS_REGION` | `us-east-1` | AWS region |
| `DYNAMODB_ENDPOINT` | *(unset = real AWS)* | e.g. `http://localhost:8000` for DynamoDB Local |
| `DYNAMODB_TIMEOUT` | `2s` | Upper bound for each request's database work |
| `PORT` | `8084` | Listen port |
| `SHUTDOWN_TIMEOUT` | `20s` | Time for in-flight requests after SIGTERM |

Credentials come from the standard AWS chain: EKS Pod Identity in the cluster,
environment variables locally. None are stored in code or the image.

## Test

```
go test -race ./...                       # unit tests (fake store)
DYNAMODB_ENDPOINT=http://localhost:8000 AWS_REGION=us-east-1 \
AWS_ACCESS_KEY_ID=local AWS_SECRET_ACCESS_KEY=local \
go test ./internal/repo/ -v               # integration tests (DynamoDB Local)
```

## Known limitations

- Abandoned reservations are not expired automatically yet; order-service
  releases them when an order fails.