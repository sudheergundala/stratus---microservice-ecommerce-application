# fraud-service

Scores each order for fraud risk before payment. Returns a score from 0 to 1,
a decision (`approve`, `review`, `decline`) and the main reasons.

## API

| Method and path | Purpose |
|---|---|
| `POST /score` | Score an order (body below). `200`, or `422` for invalid or unknown fields |
| `GET /healthz` | Liveness |
| `GET /readyz` | Readiness: `200` only once the model is loaded; includes `model_version` |

```json
{"order_id": "ord_1", "amount_cents": 4599, "item_count": 2, "account_age_days": 900,
 "orders_last_24h": 0, "address_mismatch": false, "card_country_mismatch": false, "order_hour": 14}
```

## The model

- `model/model.json`: logistic regression weights as plain JSON. No pickle, no
  scikit-learn at runtime: loading it can't execute code, and the image stays small.
- Trained by `training/train.py` on a synthetic dataset with a fixed seed.
  Re-running training on the same machine produces the same file (same SHA-256).
- Training refuses to write a model whose ROC AUC is below 0.75 (quality gate).
- The service logs `model_version` and `model_sha256` at startup, and every
  response includes `model_version`.

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `MODEL_PATH` | `model/model.json` | Model artifact to load |
| `REVIEW_THRESHOLD` | `0.30` | Score at or above this: `review` |
| `DECLINE_THRESHOLD` | `0.80` | Score at or above this: `decline` |
| `PORT` | `8086` | Listen port |
| `SHUTDOWN_TIMEOUT_SECONDS` | `20` | Time for in-flight requests after SIGTERM |
| `LOG_LEVEL` | `INFO` | Log level |

Invalid configuration or a missing or invalid model exits with code 2.

## Develop and test (uv)

```
uv sync                                                     # create .venv from uv.lock
uv run pytest -q                                            # tests
uv run --group train python training/train.py --version fraud-lr-v1   # retrain
uv run python -m app                                        # run on :8086
```