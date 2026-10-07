# notification-service

Background worker. Reads order events from SQS and sends one email per event
through Amazon SES. It serves no user traffic; its HTTP port exists only for
Kubernetes probes.

## Event contract (SQS `stratus-notifications`, fed by SNS `stratus-orders`)

```jsonc
{
  "event_id": "0f8c…",            // unique per event; used for deduplication
  "event_type": "OrderPlaced",    // or "OrderFailed"; other types are ignored
  "order_id": "ord_42",
  "customer_email": "ada@example.com",
  "total_cents": 4599,
  "currency": "USD",
  "reason": "payment declined"    // OrderFailed only
}
```

The SNS subscription should use raw message delivery. An SNS envelope is also accepted.

## What happens to each message

| Outcome | When | Message |
|---|---|---|
| Sent | Email sent and recorded | Deleted |
| Duplicate | `event_id` already recorded (SQS redelivery) | Deleted, no email |
| Ignored | Event type this service doesn't email about | Deleted |
| Failed | SES or DynamoDB error, timeout | Kept: retried after the visibility timeout |
| Invalid | Not JSON, missing fields, bad address | Kept: moves to the DLQ after `maxReceiveCount` |

Delivery is at-least-once. A crash between sending and recording can send one
duplicate email; everything else is deduplicated.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `SQS_QUEUE_URL` | *(required)* | Queue to consume |
| `EMAIL_FROM` | *(required)* | Sender address (a verified SES identity in AWS) |
| `DEDUPE_TABLE` | `stratus-notification-dedupe` | DynamoDB table, partition key `pk` (string), TTL on `expires_at` |
| `AWS_REGION` | `us-east-1` | AWS region |
| `SQS_ENDPOINT`, `SES_ENDPOINT`, `DYNAMODB_ENDPOINT` | *(unset in AWS)* | Local emulators only |
| `ASPNETCORE_HTTP_PORTS` | `8087` (set in the image) | Probe port |
| `LIVENESS_STALL_SECONDS` | `90` | `/healthz` fails if the poll loop hasn't turned for this long |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | Time to finish the current message after SIGTERM |

## Probes

- `GET /healthz`: liveness. 503 when the poll loop is stuck. An SQS outage does **not** fail it (the loop keeps turning and backing off), so pods are not restarted for problems a restart can't fix.
- `GET /readyz`: 503 while shutting down.

## Shutdown

SIGTERM stops polling immediately, finishes the message in progress (15 s max),
and returns any other received messages to the queue at once.

## Test

```
docker run --rm -v "$PWD":/src -w /src -v stratus-nuget:/root/.nuget/packages \
  mcr.microsoft.com/dotnet/sdk:10.0 dotnet test NotificationService.slnx
```