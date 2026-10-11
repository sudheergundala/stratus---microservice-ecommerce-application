"""Product queries. Every query is parameterized; no SQL is built from input."""

import base64
import json
from dataclasses import dataclass

from psycopg.rows import dict_row
from psycopg_pool import ConnectionPool

COLUMNS = "sku, name, description, category, price_cents, currency, image_key, active, updated_at"


class InvalidCursor(ValueError):
    pass


def encode_cursor(price_cents: int, sku: str) -> str:
    raw = json.dumps([price_cents, sku], separators=(",", ":")).encode()
    return base64.urlsafe_b64encode(raw).decode().rstrip("=")


def decode_cursor(cursor: str) -> tuple[int, str]:
    try:
        raw = base64.urlsafe_b64decode(cursor + "=" * (-len(cursor) % 4))
        price_cents, sku = json.loads(raw)
    except (ValueError, TypeError) as exc:
        raise InvalidCursor("invalid cursor") from exc
    if not isinstance(price_cents, int) or not isinstance(sku, str):
        raise InvalidCursor("invalid cursor")
    return price_cents, sku


@dataclass(frozen=True)
class ProductInput:
    name: str
    description: str
    category: str
    price_cents: int
    currency: str
    image_key: str | None
    active: bool


class ProductStore:
    def __init__(self, pool: ConnectionPool):
        self.pool = pool

    def get(self, sku: str) -> dict | None:
        with self.pool.connection() as conn, conn.cursor(row_factory=dict_row) as cur:
            cur.execute(f"SELECT {COLUMNS} FROM products WHERE sku = %s AND active", (sku,))
            return cur.fetchone()

    def browse(self, category: str | None, max_price_cents: int | None,
               limit: int, after: tuple[int, str] | None) -> tuple[list[dict], str | None]:
        """Active products sorted by price, then sku. Keyset pagination: the
        cursor holds the last (price, sku) seen, so page 100 costs the same as
        page 1 (unlike OFFSET, which reads and discards every earlier row)."""
        where, params = ["active"], []
        if category:
            where.append("category = %s")
            params.append(category)
        if max_price_cents is not None:
            where.append("price_cents <= %s")
            params.append(max_price_cents)
        if after:
            where.append("(price_cents, sku) > (%s, %s)")
            params.extend(after)
        params.append(limit + 1)
        sql = (f"SELECT {COLUMNS} FROM products WHERE {' AND '.join(where)} "
               "ORDER BY price_cents, sku LIMIT %s")
        with self.pool.connection() as conn, conn.cursor(row_factory=dict_row) as cur:
            cur.execute(sql, params)
            rows = cur.fetchall()
        if len(rows) > limit:
            rows = rows[:limit]
            last = rows[-1]
            return rows, encode_cursor(last["price_cents"], last["sku"])
        return rows, None

    def search(self, text: str, category: str | None, max_price_cents: int | None, limit: int) -> list[dict]:
        """Full-text search, best matches first. websearch_to_tsquery accepts
        what people type ("waterproof jacket -kids", quoted phrases) and never
        raises a syntax error on user input."""
        where, params = ["active", "search @@ q"], [text]
        if category:
            where.append("category = %s")
            params.append(category)
        if max_price_cents is not None:
            where.append("price_cents <= %s")
            params.append(max_price_cents)
        params.append(limit)
        sql = (f"SELECT {COLUMNS} FROM products, websearch_to_tsquery('english', %s) AS q "
               f"WHERE {' AND '.join(where)} ORDER BY ts_rank_cd(search, q) DESC, sku LIMIT %s")
        with self.pool.connection() as conn, conn.cursor(row_factory=dict_row) as cur:
            cur.execute(sql, params)
            return cur.fetchall()

    def upsert(self, sku: str, p: ProductInput) -> dict:
        with self.pool.connection() as conn, conn.cursor(row_factory=dict_row) as cur:
            cur.execute(
                f"""INSERT INTO products (sku, name, description, category, price_cents, currency, image_key, active)
                    VALUES (%s, %s, %s, %s, %s, %s, %s, %s)
                    ON CONFLICT (sku) DO UPDATE SET
                        name = EXCLUDED.name, description = EXCLUDED.description,
                        category = EXCLUDED.category, price_cents = EXCLUDED.price_cents,
                        currency = EXCLUDED.currency, image_key = EXCLUDED.image_key,
                        active = EXCLUDED.active, updated_at = now()
                    RETURNING {COLUMNS}""",
                (sku, p.name, p.description, p.category, p.price_cents, p.currency, p.image_key, p.active))
            return cur.fetchone()