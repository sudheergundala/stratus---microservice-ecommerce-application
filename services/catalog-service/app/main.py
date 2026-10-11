"""HTTP API: product browse, search, read and upsert, plus health probes."""

import logging
import time
from contextlib import asynccontextmanager
from datetime import datetime
from typing import Annotated

import psycopg
from fastapi import FastAPI, HTTPException, Path, Query, Request, Response
from fastapi.responses import JSONResponse
from psycopg_pool import ConnectionPool, PoolTimeout
from pydantic import BaseModel, ConfigDict, Field

from app.config import Settings
from app.store import InvalidCursor, ProductInput, ProductStore, decode_cursor

log = logging.getLogger("catalog")

SKU = Annotated[str, Path(pattern=r"^[A-Z0-9-]{3,32}$")]
SLOW_REQUEST_SECONDS = 1.0


class ProductIn(BaseModel):
    # Unknown fields are rejected: callers can't send data we'd silently ignore.
    model_config = ConfigDict(extra="forbid")

    name: str = Field(min_length=1, max_length=200)
    description: str = Field(default="", max_length=5000)
    category: str = Field(pattern=r"^[a-z0-9-]{2,40}$")
    price_cents: int = Field(ge=0, le=100_000_000)
    currency: str = Field(default="USD", pattern=r"^[A-Z]{3}$")
    # The S3 object key of the main image, e.g. "products/SKU-1/main.webp".
    image_key: str | None = Field(default=None, pattern=r"^products/[A-Za-z0-9/._-]{1,200}$")
    active: bool = True


class ProductOut(BaseModel):
    sku: str
    name: str
    description: str
    category: str
    price_cents: int
    currency: str
    image_url: str | None
    updated_at: datetime


class ProductPage(BaseModel):
    items: list[ProductOut]
    next_cursor: str | None


def create_app(settings: Settings, pool: ConnectionPool | None = None) -> FastAPI:
    pool = pool or ConnectionPool(
        settings.database_dsn,
        min_size=settings.pool_min_size,
        max_size=settings.pool_max_size,
        # How long a request waits for a free connection before failing (503).
        timeout=settings.pool_timeout_seconds,
        # Checks a pooled connection before handing it out, so connections
        # broken by a database restart or failover are replaced, not used.
        check=ConnectionPool.check_connection,
        # Recycle connections hourly, so they follow DNS after an RDS failover.
        max_lifetime=3600,
        kwargs={
            "connect_timeout": 3,
            "application_name": "catalog-service",
            # The database cancels any query running longer than this.
            "options": f"-c statement_timeout={settings.statement_timeout_ms}",
        },
        open=False,
        name="catalog",
    )
    store = ProductStore(pool)

    @asynccontextmanager
    async def lifespan(app: FastAPI):
        # wait=False: start even if the database is down. Requests fail fast
        # with 503 until it is back; the pod doesn't crash-loop.
        pool.open(wait=False)
        log.info("catalog started", extra={"fields": {
            "pool_max_size": settings.pool_max_size,
            "statement_timeout_ms": settings.statement_timeout_ms,
            "image_base_url": settings.image_base_url,
        }})
        yield
        pool.close(timeout=5)
        log.info("shutdown complete")

    app = FastAPI(title="catalog-service", lifespan=lifespan, docs_url=None, redoc_url=None)

    def to_out(row: dict) -> ProductOut:
        key = row["image_key"]
        return ProductOut(
            sku=row["sku"], name=row["name"], description=row["description"], category=row["category"],
            price_cents=row["price_cents"], currency=row["currency"], updated_at=row["updated_at"],
            image_url=f"{settings.image_base_url}/{key}" if key else None,
        )

    def cacheable(response: Response) -> None:
        # Lets the CDN and browsers reuse catalog reads for a short time.
        response.headers["Cache-Control"] = f"public, max-age={settings.cache_max_age_seconds}"

    @app.middleware("http")
    async def log_failures_and_slow_requests(request: Request, call_next):
        started = time.perf_counter()
        response = await call_next(request)
        elapsed = time.perf_counter() - started
        if response.status_code >= 500 or elapsed >= SLOW_REQUEST_SECONDS:
            log.warning("request", extra={"fields": {
                "method": request.method, "path": request.url.path,
                "status": response.status_code, "duration_ms": round(elapsed * 1000),
            }})
        return response

    @app.exception_handler(PoolTimeout)
    @app.exception_handler(psycopg.OperationalError)
    async def database_unavailable(request: Request, exc: Exception) -> JSONResponse:
        # Covers: database down or unreachable, pool exhausted, query cancelled
        # by statement_timeout. All are temporary from the caller's view.
        log.warning("database unavailable", extra={"fields": {
            "error": type(exc).__name__, "detail": str(exc).splitlines()[0] if str(exc) else ""}})
        return JSONResponse({"detail": "catalog store unavailable; retry"}, status_code=503,
                            headers={"Retry-After": "1", "Cache-Control": "no-store"})

    @app.exception_handler(psycopg.IntegrityError)
    async def rejected_by_database(request: Request, exc: Exception) -> JSONResponse:
        return JSONResponse({"detail": "product rejected by database constraints"}, status_code=422)

    @app.get("/products", response_model=ProductPage)
    def list_products(
        response: Response,
        q: Annotated[str | None, Query(min_length=1, max_length=200)] = None,
        category: Annotated[str | None, Query(pattern=r"^[a-z0-9-]{2,40}$")] = None,
        max_price_cents: Annotated[int | None, Query(ge=0)] = None,
        limit: Annotated[int, Query(ge=1, le=50)] = 20,
        cursor: Annotated[str | None, Query(max_length=200)] = None,
    ) -> ProductPage:
        if q:
            # Search returns the best matches only; it is not paginated.
            if cursor:
                raise HTTPException(400, "cursor is not supported with q")
            rows, next_cursor = store.search(q, category, max_price_cents, limit), None
        else:
            try:
                after = decode_cursor(cursor) if cursor else None
            except InvalidCursor as exc:
                raise HTTPException(400, str(exc)) from exc
            rows, next_cursor = store.browse(category, max_price_cents, limit, after)
        cacheable(response)
        return ProductPage(items=[to_out(r) for r in rows], next_cursor=next_cursor)

    @app.get("/products/{sku}", response_model=ProductOut)
    def get_product(sku: SKU, response: Response) -> ProductOut:
        row = store.get(sku)
        if row is None:
            raise HTTPException(404, "product not found")
        cacheable(response)
        return to_out(row)

    @app.put("/products/{sku}", response_model=ProductOut)
    def put_product(sku: SKU, body: ProductIn, response: Response) -> ProductOut:
        # Idempotent: sending the same body twice leaves the same product.
        row = store.upsert(sku, ProductInput(**body.model_dump()))
        log.info("product saved", extra={"fields": {"sku": sku, "active": body.active}})
        response.headers["Cache-Control"] = "no-store"
        return to_out(row)

    @app.get("/healthz")
    def healthz() -> dict:
        return {"status": "ok"}

    @app.get("/readyz")
    def readyz() -> dict:
        # Deliberately does not check the database. If it did, a database
        # outage would mark every pod unready at once and remove them all
        # from the Service; requests already fail fast with a clear 503.
        return {"status": "ready"}

    return app