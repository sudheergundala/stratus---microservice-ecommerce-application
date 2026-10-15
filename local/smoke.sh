#!/usr/bin/env bash
# LOCAL ONLY. End-to-end smoke test of the running compose stack.
#   ./local/smoke.sh        exit code 0 = every check passed
# Public paths go through the gateway on 127.0.0.1:8080. Internal services are
# not published, so those checks run from a throwaway curl container on the
# backend network (the same view another pod would have).
# Works with macOS's bash 3.2: JSON bodies are built with printf into
# variables, never with escaped quotes inside "$( )".
set -uo pipefail

GW=http://127.0.0.1:8080
NET=stratus-backend
CURL_IMAGE=curlimages/curl:8.22.0
AWS_IMAGE=amazon/aws-cli:2.37.6
NL=$'\n'
passed=0
failed=0

pass() { printf 'PASS  %s\n' "$1"; passed=$((passed + 1)); }
fail() { printf 'FAIL  %s\n' "$1"; failed=$((failed + 1)); }

# Sends a request and checks the status code; on failure prints the response
# body too, so the reason is visible without a second request.
#   check_http <name> <expected status> <public|internal> <curl args...>
check_http() {
  local name=$1 expected=$2 where=$3 out code body
  shift 3
  if [[ $where == internal ]]; then
    out=$(docker run --rm --network "$NET" "$CURL_IMAGE" -s --max-time 5 -w "${NL}%{http_code}" "$@")
  else
    out=$(curl -s --max-time 5 -w "${NL}%{http_code}" "$@")
  fi
  code=${out##*"$NL"}
  body=${out%"$NL"*}
  if [[ $code == "$expected" ]]; then
    pass "$name"
  else
    fail "$name (expected $expected, got $code: ${body:0:200})"
  fi
}

check_value() { # name, expected, actual
  if [[ $2 == "$3" ]]; then pass "$1"; else fail "$1 (expected $2, got $3)"; fi
}

json() { curl -s --max-time 5 -H 'content-type: application/json' "$@"; }
email_count() {
  curl -s --max-time 5 http://127.0.0.1:8005/store | python3 -c 'import sys, json; print(len(json.load(sys.stdin).get("emails", [])))'
}

echo "== Public routes (through the gateway)"
# The gateway may have just been (re)started: wait until it reports ready.
for _ in $(seq 1 30); do
  [[ $(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$GW/readyz") == 200 ]] && break
  sleep 1
done
check_http "gateway /healthz" 200 public "$GW/healthz"

email="smoke-$(date +%s)@example.com"
creds=$(printf '{"email":"%s","password":"correct-horse-battery"}' "$email")
check_http "register (gateway -> user-service -> Postgres)" 201 public \
  -X POST "$GW/api/v1/auth/register" -H 'content-type: application/json' -d "$creds"
token=$(json -X POST "$GW/api/v1/auth/login" -d "$creds" | python3 -c 'import sys, json; print(json.load(sys.stdin).get("access_token", ""))')
check_value "login returns a JWT" yes "$([[ $token == ey* ]] && echo yes || echo no)"

check_http "browse products (gateway -> catalog-service)" 200 public "$GW/api/v1/products?limit=3"
first=$(curl -s --max-time 5 "$GW/api/v1/products?q=waterproof" | python3 -c 'import sys, json; i = json.load(sys.stdin)["items"]; print(i[0]["sku"] if i else "none")')
check_value "search 'waterproof' ranks BTS-002 first" BTS-002 "$first"

check_http "orders without a token are refused at the gateway" 401 public "$GW/api/v1/orders/smoke-1"
order_status=$(curl -s -o /dev/null -w '%{http_code}' --max-time 5 -H "Authorization: Bearer $token" "$GW/api/v1/orders/smoke-1")
check_value "orders with a token reach order-service (not 401/502/504)" yes \
  "$([[ $order_status != 401 && $order_status != 502 && $order_status != 504 && $order_status != 000 ]] && echo yes || echo "no ($order_status)")"
check_http "a customer can't change the catalog" 403 public -X PUT "$GW/api/v1/products/JKT-001" \
  -H "Authorization: Bearer $token" -H 'content-type: application/json' -d '{}'

echo "== Internal services (from inside the backend network)"
payment='{"order_id":"smoke_1","amount_cents":4999,"currency":"USD","card_number":"4111111111111111","email":"sam@example.com"}'
key="smoke-$(date +%s)"
check_http "payment: capture" 201 internal -X POST http://payment-service:8083/payments \
  -H "Idempotency-Key: $key" -H 'content-type: application/json' -d "$payment"
check_http "payment: same Idempotency-Key replays, no second charge" 200 internal -X POST http://payment-service:8083/payments \
  -H "Idempotency-Key: $key" -H 'content-type: application/json' -d "$payment"

sku="SMOKE-$(date +%s)"
reserve1=$(printf '{"order_id":"o1-%s","items":[{"sku":"%s","quantity":2}]}' "$sku" "$sku")
reserve2=$(printf '{"order_id":"o2-%s","items":[{"sku":"%s","quantity":2}]}' "$sku" "$sku")
check_http "inventory: set stock to 3" 200 internal -X PUT "http://inventory-service:8084/stock/$sku" \
  -H 'content-type: application/json' -d '{"available":3}'
check_http "inventory: reserve 2" 201 internal -X POST http://inventory-service:8084/reservations \
  -H 'content-type: application/json' -d "$reserve1"
check_http "inventory: reserve 2 more is refused (no overselling)" 409 internal -X POST http://inventory-service:8084/reservations \
  -H 'content-type: application/json' -d "$reserve2"

score='{"order_id":"smoke_1","amount_cents":4599,"item_count":2,"account_age_days":900,"orders_last_24h":0,"address_mismatch":false,"card_country_mismatch":false,"order_hour":14}'
check_http "fraud: score an order" 200 internal -X POST http://fraud-service:8086/score \
  -H 'content-type: application/json' -d "$score"

echo "== Event path (SQS -> notification-service -> SES)"
before=$(email_count)
event=$(printf '{"event_id":"evt-smoke-%s","event_type":"OrderPlaced","order_id":"smoke_1","customer_email":"sam@example.com","total_cents":4999,"currency":"USD"}' "$(date +%s)")
docker run --rm --network "$NET" -e AWS_ACCESS_KEY_ID=local -e AWS_SECRET_ACCESS_KEY=local -e AWS_REGION=us-east-1 "$AWS_IMAGE" \
  sqs send-message --endpoint-url http://sqs-local:9324 \
  --queue-url http://sqs-local:9324/000000000000/stratus-notifications \
  --message-body "$event" > /dev/null
after=$before
for _ in $(seq 1 15); do
  sleep 1
  after=$(email_count)
  [[ $after -gt $before ]] && break
done
check_value "OrderPlaced event delivered as one email" $((before + 1)) "$after"

echo "== $passed passed, $failed failed"
[[ $failed -eq 0 ]]