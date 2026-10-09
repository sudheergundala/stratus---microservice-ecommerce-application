"""The fraud model: a logistic regression stored as plain JSON.

Serving needs no scikit-learn and no pickle: the artifact is data, not code,
so loading it can never execute anything. It's small enough to review in a
pull request, and its SHA-256 is logged at startup for traceability.
"""

import hashlib
import json
import math
from dataclasses import dataclass
from pathlib import Path

# Features, in the order the model expects them. Must match training/train.py.
FEATURES = (
    "log_amount",
    "item_count",
    "log_account_age_days",
    "orders_last_24h",
    "address_mismatch",
    "card_country_mismatch",
    "night_order",
)

# Human-readable reasons, shown to support staff for "review" decisions.
REASONS = {
    "log_amount": "high order amount",
    "item_count": "many items",
    "log_account_age_days": "new account",
    "orders_last_24h": "many orders in the last 24 hours",
    "address_mismatch": "shipping and billing addresses differ",
    "card_country_mismatch": "card issued in a different country",
    "night_order": "placed between midnight and 6 am",
}


class ModelError(ValueError):
    pass


@dataclass(frozen=True)
class Score:
    probability: float
    reasons: list[str]


@dataclass(frozen=True)
class FraudModel:
    version: str
    sha256: str
    mean: tuple[float, ...]
    scale: tuple[float, ...]
    coef: tuple[float, ...]
    intercept: float

    @classmethod
    def load(cls, path: str) -> "FraudModel":
        try:
            raw = Path(path).read_bytes()
        except OSError as exc:
            raise ModelError(f"cannot read model file {path}: {exc}") from exc
        try:
            doc = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise ModelError(f"model file is not valid JSON: {exc}") from exc

        if doc.get("algorithm") != "logistic_regression":
            raise ModelError("unsupported algorithm (expected logistic_regression)")
        if tuple(doc.get("features", ())) != FEATURES:
            raise ModelError(f"model features {doc.get('features')} do not match the service's {list(FEATURES)}")
        arrays = {name: doc.get(name) for name in ("mean", "scale", "coef")}
        for name, values in arrays.items():
            if not isinstance(values, list) or len(values) != len(FEATURES):
                raise ModelError(f"'{name}' must be a list of {len(FEATURES)} numbers")
        if any(s <= 0 for s in arrays["scale"]):
            raise ModelError("'scale' values must be positive")

        return cls(
            version=str(doc.get("model_version", "unknown")),
            sha256=hashlib.sha256(raw).hexdigest(),
            mean=tuple(float(v) for v in arrays["mean"]),
            scale=tuple(float(v) for v in arrays["scale"]),
            coef=tuple(float(v) for v in arrays["coef"]),
            intercept=float(doc["intercept"]),
        )

    def score(self, features: dict[str, float]) -> Score:
        contributions = [
            c * (features[name] - m) / s
            for name, m, s, c in zip(FEATURES, self.mean, self.scale, self.coef, strict=True)
        ]
        z = self.intercept + sum(contributions)
        probability = 1.0 / (1.0 + math.exp(-z))
        # The features that pushed the score up the most.
        risky = sorted(
            ((value, name) for value, name in zip(contributions, FEATURES) if value > 0.25),
            reverse=True,
        )
        return Score(probability=probability, reasons=[REASONS[name] for _, name in risky[:3]])


def to_features(amount_cents: int, item_count: int, account_age_days: int, orders_last_24h: int,
                address_mismatch: bool, card_country_mismatch: bool, order_hour: int) -> dict[str, float]:
    """Turns an order into model features. Must match training/train.py."""
    return {
        "log_amount": math.log1p(amount_cents / 100),
        "item_count": float(item_count),
        "log_account_age_days": math.log1p(account_age_days),
        "orders_last_24h": float(orders_last_24h),
        "address_mismatch": float(address_mismatch),
        "card_country_mismatch": float(card_country_mismatch),
        "night_order": 1.0 if order_hour < 6 else 0.0,
    }