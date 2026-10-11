"""Schema migrations: python -m app migrate

Runs as a one-off Job before each rollout, with the migrator's credentials
(MIGRATE_DATABASE_DSN), never the app's.

- Files are migrations/V<number>__<name>.sql, applied in number order.
- Each file runs in its own transaction: it applies completely or not at all.
- Applied files are recorded with a SHA-256 checksum. Editing a file that was
  already applied is an error: write a new migration instead.
- A PostgreSQL advisory lock makes concurrent runs wait instead of colliding.
- Re-running is safe: already applied files are skipped.
- lock_timeout: a migration that can't get its table lock quickly gives up
  (the Job fails and Kubernetes retries it) instead of waiting in the lock
  queue, where it would block every read of that table behind it.
"""

import hashlib
import logging
import re
from dataclasses import dataclass
from pathlib import Path

import psycopg

log = logging.getLogger("catalog.migrate")

MIGRATIONS_DIR = Path(__file__).resolve().parent.parent / "migrations"
FILE_PATTERN = re.compile(r"^V(\d{3})__([a-z0-9_]+)\.sql$")
# Any fixed number shared by all runners of this service's migrations.
ADVISORY_LOCK_ID = 7_085_001
# Must stay below the app's statement_timeout (2s): reads queued behind a
# waiting migration then wait at most this long and still succeed.
DEFAULT_LOCK_TIMEOUT = "1s"


class MigrationError(RuntimeError):
    pass


@dataclass(frozen=True)
class Migration:
    version: int
    name: str
    sql: str
    checksum: str


def discover(directory: Path = MIGRATIONS_DIR) -> list[Migration]:
    migrations = []
    for path in sorted(directory.glob("*.sql")):
        match = FILE_PATTERN.match(path.name)
        if not match:
            raise MigrationError(f"bad migration file name: {path.name} (expected V001__name.sql)")
        sql = path.read_text(encoding="utf-8")
        migrations.append(Migration(int(match.group(1)), match.group(2), sql,
                                    hashlib.sha256(sql.encode()).hexdigest()))
    versions = [m.version for m in migrations]
    if len(versions) != len(set(versions)):
        raise MigrationError("two migration files share a version number")
    return migrations


def migrate(dsn: str, directory: Path = MIGRATIONS_DIR, lock_timeout: str = DEFAULT_LOCK_TIMEOUT) -> int:
    """Applies pending migrations. Returns how many were applied."""
    migrations = discover(directory)
    with psycopg.connect(dsn, autocommit=True, connect_timeout=5) as conn:
        # Waits for another migration run to finish (bounded by the Job's deadline).
        conn.execute("SELECT pg_advisory_lock(%s)", (ADVISORY_LOCK_ID,))
        try:
            # From here on, every lock wait (ALTER TABLE, CREATE INDEX, ...) is bounded.
            conn.execute("SELECT set_config('lock_timeout', %s, false)", (lock_timeout,))
            # Kept out of the public schema, so the app role gets no access to it.
            conn.execute("CREATE SCHEMA IF NOT EXISTS migrations")
            conn.execute("""
                CREATE TABLE IF NOT EXISTS migrations.applied (
                    version    integer     PRIMARY KEY,
                    name       text        NOT NULL,
                    checksum   text        NOT NULL,
                    applied_at timestamptz NOT NULL DEFAULT now()
                )""")
            applied = {row[0]: row[1] for row in conn.execute("SELECT version, checksum FROM migrations.applied")}

            for version, checksum in applied.items():
                known = next((m for m in migrations if m.version == version), None)
                if known is None:
                    raise MigrationError(f"V{version:03d} is applied in the database but missing from this image")
                if known.checksum != checksum:
                    raise MigrationError(f"V{version:03d} was edited after it was applied; add a new migration instead")

            count = 0
            for m in migrations:
                if m.version in applied:
                    continue
                with conn.transaction():
                    conn.execute(m.sql)
                    conn.execute("INSERT INTO migrations.applied (version, name, checksum) VALUES (%s, %s, %s)",
                                 (m.version, m.name, m.checksum))
                count += 1
                log.info("migration applied", extra={"fields": {"version": m.version, "name": m.name}})
            log.info("migrations complete", extra={"fields": {
                "applied_now": count, "total": len(migrations)}})
            return count
        finally:
            conn.execute("SELECT pg_advisory_unlock(%s)", (ADVISORY_LOCK_ID,))