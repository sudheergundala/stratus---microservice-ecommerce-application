import json

import pytest
from fastapi.testclient import TestClient

from app.config import ConfigError, Settings
from app.main import create_app
from app.model import FEATURES, FraudModel, ModelError

MODEL_PATH = "model/model.json"

SAFE = {
    "order_id": "ord_1", "amount_cents": 4599, "item_count": 2, "account_age_days": 900,
    "orders_last_24h": 0, "address_mismatch": False, "card_country_mismatch": False, "order_hour": 14,
}
RISKY = {
    "order_id": "ord_2", "amount_cents": 250_000, "item_count": 6, "account_age_days": 0,
    "orders_last_24h": 6, "address_mismatch": True, "card_country_mismatch": True, "order_hour": 3,
}


@pytest.fixture
def client():
    with TestClient(create_app(Settings(model_path=MODEL_PATH))) as c:
        yield c


def test_safe_order_is_approved(client):
    body = client.post("/score", json=SAFE).json()
    assert body["decision"] == "approve"
    assert 0 <= body["score"] < 0.30
    assert body["model_version"] == "fraud-lr-v1"


def test_risky_order_is_declined_with_reasons(client):
    body = client.post("/score", json=RISKY).json()
    assert body["decision"] == "decline"
    assert body["score"] >= 0.80
    assert "many orders in the last 24 hours" in body["reasons"]
    assert len(body["reasons"]) <= 3


def test_scoring_is_deterministic(client):
    assert client.post("/score", json=RISKY).json() == client.post("/score", json=RISKY).json()


@pytest.mark.parametrize("change", [
    {"amount_cents": -1},
    {"item_count": 0},
    {"order_hour": 24},
    {"order_id": ""},
    {"unexpected_field": 1},
])
def test_invalid_requests_are_rejected(client, change):
    assert client.post("/score", json={**SAFE, **change}).status_code == 422


def test_probes(client):
    assert client.get("/healthz").json() == {"status": "ok"}
    assert client.get("/readyz").json()["model_version"] == "fraud-lr-v1"


def test_model_with_wrong_features_is_refused(tmp_path):
    doc = json.loads(open(MODEL_PATH).read())
    doc["features"] = list(reversed(FEATURES))
    bad = tmp_path / "model.json"
    bad.write_text(json.dumps(doc))
    with pytest.raises(ModelError, match="do not match"):
        FraudModel.load(str(bad))


def test_missing_model_file_is_refused():
    with pytest.raises(ModelError, match="cannot read"):
        FraudModel.load("does/not/exist.json")


def test_thresholds_must_be_ordered(monkeypatch):
    monkeypatch.setenv("REVIEW_THRESHOLD", "0.9")
    monkeypatch.setenv("DECLINE_THRESHOLD", "0.5")
    with pytest.raises(ConfigError):
        Settings.from_env()