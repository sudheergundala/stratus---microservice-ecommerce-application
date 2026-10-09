"""HTTP API: POST /score, plus health probes."""

import logging
from contextlib import asynccontextmanager
from typing import Literal

from fastapi import FastAPI
from pydantic import BaseModel, ConfigDict, Field

from app.config import Settings
from app.model import FraudModel, to_features

log = logging.getLogger("fraud")


class ScoreRequest(BaseModel):
    # Unknown fields are rejected: callers can't send data we'd silently ignore.
    model_config = ConfigDict(extra="forbid")

    order_id: str = Field(min_length=1, max_length=100)
    amount_cents: int = Field(ge=0, le=100_000_000)
    item_count: int = Field(ge=1, le=1000)
    account_age_days: int = Field(ge=0, le=36_500)
    orders_last_24h: int = Field(ge=0, le=10_000)
    address_mismatch: bool
    card_country_mismatch: bool
    order_hour: int = Field(ge=0, le=23, description="Hour of day the order was placed, UTC")


class ScoreResponse(BaseModel):
    order_id: str
    score: float
    decision: Literal["approve", "review", "decline"]
    reasons: list[str]
    model_version: str


def create_app(settings: Settings, model: FraudModel | None = None) -> FastAPI:
    @asynccontextmanager
    async def lifespan(app: FastAPI):
        # Load the model before accepting traffic. A missing or invalid model
        # stops startup: the pod never becomes ready with a broken model.
        app.state.model = model or FraudModel.load(settings.model_path)
        log.info("model loaded", extra={"fields": {
            "model_version": app.state.model.version,
            "model_sha256": app.state.model.sha256,
            "review_threshold": settings.review_threshold,
            "decline_threshold": settings.decline_threshold,
        }})
        yield
        log.info("shutdown complete")

    app = FastAPI(title="fraud-service", lifespan=lifespan, docs_url=None, redoc_url=None)

    @app.post("/score", response_model=ScoreResponse)
    def score(req: ScoreRequest) -> ScoreResponse:
        fraud_model: FraudModel = app.state.model
        result = fraud_model.score(to_features(
            req.amount_cents, req.item_count, req.account_age_days, req.orders_last_24h,
            req.address_mismatch, req.card_country_mismatch, req.order_hour,
        ))
        if result.probability >= settings.decline_threshold:
            decision = "decline"
        elif result.probability >= settings.review_threshold:
            decision = "review"
        else:
            decision = "approve"
        # Ids and the outcome only: no customer data in logs.
        log.info("order scored", extra={"fields": {
            "order_id": req.order_id, "score": round(result.probability, 4),
            "decision": decision, "model_version": fraud_model.version,
        }})
        return ScoreResponse(order_id=req.order_id, score=round(result.probability, 4), decision=decision,
                             reasons=result.reasons, model_version=fraud_model.version)

    @app.get("/healthz")
    def healthz() -> dict:
        return {"status": "ok"}

    @app.get("/readyz")
    def readyz() -> dict:
        # Reaching this handler means the lifespan finished: the model is loaded.
        return {"status": "ready", "model_version": app.state.model.version}

    return app