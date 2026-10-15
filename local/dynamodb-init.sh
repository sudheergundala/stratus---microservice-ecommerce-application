#!/usr/bin/env bash
# LOCAL ONLY. Creates the DynamoDB Local tables that Terraform creates in AWS.
# Safe to re-run: existing tables are left alone.
set -euo pipefail

EP=http://dynamodb-local:8000

# DynamoDB Local takes a few seconds to start.
for i in $(seq 1 30); do
  if aws dynamodb list-tables --endpoint-url "$EP" > /dev/null 2>&1; then break; fi
  echo "waiting for dynamodb-local ($i/30)"
  sleep 1
done

create_table() {
  local table=$1
  if aws dynamodb describe-table --endpoint-url "$EP" --table-name "$table" > /dev/null 2>&1; then
    echo "exists:  $table"
    return
  fi
  aws dynamodb create-table --endpoint-url "$EP" --table-name "$table" \
    --attribute-definitions AttributeName=pk,AttributeType=S \
    --key-schema AttributeName=pk,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST > /dev/null
  echo "created: $table"
}

create_table stratus-inventory
create_table stratus-notification-dedupe
aws dynamodb update-time-to-live --endpoint-url "$EP" --table-name stratus-notification-dedupe \
  --time-to-live-specification "Enabled=true,AttributeName=expires_at" > /dev/null || true
echo "dynamodb-init done"