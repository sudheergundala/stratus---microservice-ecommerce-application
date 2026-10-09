"""Train the fraud model and write it as a JSON artifact.

    uv run --group train python training/train.py --version fraud-lr-v1

Stratus has no real fraud history yet, so this generates a synthetic,
labelled dataset with a fixed seed: the same command always produces a
byte-identical model file (same SHA-256). With real data, only load_data()
changes.
"""

import argparse
import hashlib
import json
import math
import sys
from pathlib import Path

import numpy as np
import sklearn
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import average_precision_score, precision_score, recall_score, roc_auc_score
from sklearn.model_selection import train_test_split
from sklearn.preprocessing import StandardScaler

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from app.model import FEATURES  # noqa: E402  (the single source of truth for feature order)

SEED = 42


def load_data(rows: int, rng: np.random.Generator) -> tuple[np.ndarray, np.ndarray]:
    """Synthetic orders. Fraud is more likely for new accounts, bursts of orders,
    mismatched addresses or card country, large baskets and night orders."""
    amount_dollars = rng.lognormal(mean=3.8, sigma=0.9, size=rows)       # median about $45
    item_count = rng.integers(1, 8, size=rows)
    account_age_days = rng.exponential(scale=400, size=rows).astype(int)
    orders_last_24h = rng.poisson(lam=0.3, size=rows)
    address_mismatch = rng.random(rows) < 0.08
    card_country_mismatch = rng.random(rows) < 0.04
    order_hour = rng.integers(0, 24, size=rows)

    x = np.column_stack([
        np.log1p(amount_dollars),
        item_count,
        np.log1p(account_age_days),
        orders_last_24h,
        address_mismatch,
        card_country_mismatch,
        order_hour < 6,
    ]).astype(float)

    # The hidden "true" risk the model has to learn.
    z = (-4.6 + 0.55 * (x[:, 0] - 3.8) + 0.10 * x[:, 1] - 0.55 * (x[:, 2] - 5.5)
         + 0.90 * x[:, 3] + 1.6 * x[:, 4] + 2.0 * x[:, 5] + 0.7 * x[:, 6])
    y = rng.random(rows) < 1 / (1 + np.exp(-z))
    return x, y.astype(int)


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--version", required=True, help="model version, e.g. fraud-lr-v1")
    parser.add_argument("--rows", type=int, default=50_000)
    parser.add_argument("--out", default="model/model.json")
    args = parser.parse_args()

    rng = np.random.default_rng(SEED)
    x, y = load_data(args.rows, rng)
    x_train, x_test, y_train, y_test = train_test_split(x, y, test_size=0.2, random_state=SEED, stratify=y)

    scaler = StandardScaler().fit(x_train)
    model = LogisticRegression(max_iter=1000).fit(scaler.transform(x_train), y_train)

    p_test = model.predict_proba(scaler.transform(x_test))[:, 1]
    metrics = {
        "fraud_rate": round(float(y.mean()), 4),
        "roc_auc": round(float(roc_auc_score(y_test, p_test)), 4),
        "pr_auc": round(float(average_precision_score(y_test, p_test)), 4),
    }
    for threshold in (0.30, 0.80):
        flagged = p_test >= threshold
        metrics[f"at_{threshold:.2f}"] = {
            "flagged_share": round(float(flagged.mean()), 4),
            "precision": round(float(precision_score(y_test, flagged, zero_division=0)), 4),
            "recall": round(float(recall_score(y_test, flagged)), 4),
        }

    artifact = {
        "model_version": args.version,
        "algorithm": "logistic_regression",
        "features": list(FEATURES),
        "mean": [round(float(v), 8) for v in scaler.mean_],
        "scale": [round(float(v), 8) for v in scaler.scale_],
        "coef": [round(float(v), 8) for v in model.coef_[0]],
        "intercept": round(float(model.intercept_[0]), 8),
        "training": {
            "rows": args.rows,
            "seed": SEED,
            "data": "synthetic",
            "scikit_learn": sklearn.__version__,
        },
        "metrics": metrics,
    }
    out = Path(args.out)
    out.parent.mkdir(parents=True, exist_ok=True)
    out.write_text(json.dumps(artifact, indent=2) + "\n")
    print(json.dumps({"written": str(out), "sha256": hashlib.sha256(out.read_bytes()).hexdigest(),
                      "metrics": metrics}, indent=2))
    if not math.isfinite(artifact["intercept"]) or metrics["roc_auc"] < 0.75:
        sys.exit("model quality gate failed: roc_auc below 0.75")


if __name__ == "__main__":
    main()