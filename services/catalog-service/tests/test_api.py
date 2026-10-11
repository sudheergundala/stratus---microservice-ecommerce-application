"""API tests against a real PostgreSQL (CATALOG_TEST_DSN), not mocks: the SQL,
the indexes and the full-text search are what we need to prove."""

import os
import shutil
import time

import psycopg
import pytest
from fastapi.testclient import TestClient

from app.config import Settings
from app.main import create_app
from app.migrate import MIGRATIONS_DIR, MigrationError, migrate

CDN = "https://cdn.example.test"


@pytest.fixture(scope="session")
def dsn():
    value = os.getenv("CATALOG_TEST_DSN")
    if not value:
        pytest.fail("set CATALOG_TEST_DSN to a disposable test database, "
                    "e.g. host=localhost port=5432 dbname=catalog_test user=stratus password=...")
    # Start from an empty schema, then prove migrations apply and re-apply cleanly.
    with psycopg.connect(value, autocommit=True) as conn:
        conn.execute("DROP SCHEMA IF EXISTS migrations CASCADE")
        conn.execute("DROP TABLE IF EXISTS products CASCADE")
    assert migrate(value) >= 1
    assert migrate(value) == 0
    return value


@pytest.fixture
def client(dsn):
    with psycopg.connect(dsn, autocommit=True) as conn:
        conn.execute("TRUNCATE products")
    with TestClient(create_app(Settings(database_dsn=dsn, image_base_url=CDN))) as c:
        yield c


def product(**overrides):
    body = {"name": "Alpine Shell Jacket", "description": "Waterproof three-layer shell.",
            "category": "jackets", "price_cents": 18900, "image_key": "products/JKT-001/main.webp"}
    return {**body, **overrides}


def test_put_then_get(client):
    saved = client.put("/products/JKT-001", json=product())
    assert saved.status_code == 200
    body = client.get("/products/JKT-001").json()
    assert body["name"] == "Alpine Shell Jacket"
    assert body["image_url"] == f"{CDN}/products/JKT-001/main.webp"
    assert body["currency"] == "USD"


def test_put_is_idempotent(client, dsn):
    first = client.put("/products/JKT-001", json=product()).json()
    second = client.put("/products/JKT-001", json=product()).json()
    assert {k: v for k, v in first.items() if k != "updated_at"} == \
           {k: v for k, v in second.items() if k != "updated_at"}
    with psycopg.connect(dsn) as conn:
        assert conn.execute("SELECT count(*) FROM products").fetchone()[0] == 1


def test_unknown_and_inactive_products_are_not_found(client):
    assert client.get("/products/NOPE-1").status_code == 404
    client.put("/products/OLD-1", json=product(active=False))
    assert client.get("/products/OLD-1").status_code == 404
    assert client.get("/products").json()["items"] == []


def test_browse_pages_through_everything_in_price_order(client):
    prices = {"A-001": 500, "B-001": 100, "C-001": 300, "D-001": 300, "E-001": 900}
    for sku, price in prices.items():
        client.put(f"/products/{sku}", json=product(name=sku, price_cents=price))
    seen, cursor = [], None
    while True:
        params = {"limit": 2, **({"cursor": cursor} if cursor else {})}
        page = client.get("/products", params=params).json()
        seen += [(p["price_cents"], p["sku"]) for p in page["items"]]
        cursor = page["next_cursor"]
        if cursor is None:
            break
    assert seen == sorted((price, sku) for sku, price in prices.items())


def test_filters(client):
    client.put("/products/JKT-001", json=product(price_cents=18900))
    client.put("/products/JKT-002", json=product(price_cents=7900))
    client.put("/products/BTS-001", json=product(category="boots", price_cents=5900))
    skus = [p["sku"] for p in client.get("/products?category=jackets&max_price_cents=10000").json()["items"]]
    assert skus == ["JKT-002"]


def test_search_ranks_name_matches_first(client):
    client.put("/products/JKT-001", json=product(name="Insulated Parka", description="Warm winter coat."))
    client.put("/products/HAT-001", json=product(name="Wool Beanie", description="Pairs with any parka.",
                                                 category="hats"))
    client.put("/products/BTS-001", json=product(name="Trail Boots", description="Leather.", category="boots"))
    skus = [p["sku"] for p in client.get("/products", params={"q": "parka"}).json()["items"]]
    assert skus == ["JKT-001", "HAT-001"]
    # Stemming: "parkas" matches "parka".
    assert len(client.get("/products", params={"q": "parkas"}).json()["items"]) == 2


@pytest.mark.parametrize("text", ['"unterminated', "jacket -", "&&& !!", "OR"])
def test_search_never_fails_on_user_input(client, text):
    assert client.get("/products", params={"q": text}).status_code == 200


@pytest.mark.parametrize("url", [
    "/products?q=jacket&cursor=abc",
    "/products?cursor=not-a-cursor",
])
def test_bad_query_requests(client, url):
    assert client.get(url).status_code == 400


@pytest.mark.parametrize("change", [
    {"price_cents": -1},
    {"category": "Jackets!"},
    {"image_key": "../../etc/passwd"},
    {"currency": "usd"},
    {"unexpected_field": 1},
])
def test_invalid_products_are_rejected(client, change):
    assert client.put("/products/JKT-001", json=product(**change)).status_code == 422


def test_invalid_sku_is_rejected(client):
    assert client.put("/products/bad sku", json=product()).status_code == 422


def test_reads_are_cacheable_and_writes_are_not(client):
    write = client.put("/products/JKT-001", json=product())
    assert write.headers["cache-control"] == "no-store"
    assert client.get("/products/JKT-001").headers["cache-control"] == "public, max-age=60"
    assert client.get("/products").headers["cache-control"] == "public, max-age=60"


def test_database_down_returns_fast_503():
    # Nothing listens on port 1: every connection attempt fails.
    settings = Settings(database_dsn="host=127.0.0.1 port=1 dbname=catalog user=x connect_timeout=1",
                        image_base_url=CDN, pool_timeout_seconds=1)
    with TestClient(create_app(settings)) as c:
        started = time.perf_counter()
        response = c.get("/products")
        elapsed = time.perf_counter() - started
        assert response.status_code == 503
        assert response.headers["retry-after"] == "1"
        assert response.headers["cache-control"] == "no-store"
        assert elapsed < 3
        # Liveness and readiness don't depend on the database.
        assert c.get("/healthz").status_code == 200
        assert c.get("/readyz").status_code == 200


def test_edited_migration_is_refused(dsn, tmp_path):
    for f in MIGRATIONS_DIR.glob("*.sql"):
        shutil.copy(f, tmp_path)
    first = sorted(tmp_path.glob("*.sql"))[0]
    first.write_text(first.read_text() + "\n-- edited after release\n")
    with pytest.raises(MigrationError, match="edited after it was applied"):
        migrate(dsn, tmp_path)


def test_migration_missing_from_image_is_refused(dsn, tmp_path):
    with pytest.raises(MigrationError, match="missing from this image"):
        migrate(dsn, tmp_path)



def test_migration_gives_up_fast_when_the_table_is_busy(dsn, tmp_path):
    """Regression test for drill D: a migration must not queue behind a
    running query (and block every later read) for longer than lock_timeout."""
    for f in MIGRATIONS_DIR.glob("*.sql"):
        shutil.copy(f, tmp_path)
    (tmp_path / "V999__add_column.sql").write_text("ALTER TABLE products ADD COLUMN test_only text;")
    with psycopg.connect(dsn) as reader:
        # An open transaction that has read the table, like a slow report.
        reader.execute("SELECT count(*) FROM products")
        started = time.perf_counter()
        with pytest.raises(psycopg.errors.LockNotAvailable):
            migrate(dsn, tmp_path, lock_timeout="500ms")
        assert time.perf_counter() - started < 3
    with psycopg.connect(dsn) as conn:
        # Nothing half-applied: no recorded version, no new column.
        assert conn.execute("SELECT count(*) FROM migrations.applied WHERE version = 999").fetchone()[0] == 0
        assert conn.execute("SELECT count(*) FROM information_schema.columns "
                            "WHERE table_name = 'products' AND column_name = 'test_only'").fetchone()[0] == 0