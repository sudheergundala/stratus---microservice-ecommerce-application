"""Entry point.

python -m app           serve the API (needs DATABASE_DSN, IMAGE_BASE_URL)
python -m app migrate   apply schema migrations, then exit (needs MIGRATE_DATABASE_DSN)

uvicorn runs in-process so this Python process is PID 1 and receives SIGTERM
directly. uvicorn then stops accepting connections and lets in-flight
requests finish (up to SHUTDOWN_TIMEOUT_SECONDS).
"""

import logging
import logging.config
import os
import sys

import psycopg
import uvicorn

from app.config import ConfigError, Settings
from app.logs import logging_config
from app.main import create_app
from app.migrate import MigrationError, migrate

log = logging.getLogger("catalog")


def run_migrations() -> int:
    dsn = os.getenv("MIGRATE_DATABASE_DSN", "").strip()
    if not dsn:
        log.critical("MIGRATE_DATABASE_DSN is required")
        return 2
    try:
        migrate(dsn)
    except MigrationError as exc:
        log.critical(str(exc))
        return 1
    except psycopg.Error as exc:
        log.critical("migration failed", extra={"fields": {
            "error": type(exc).__name__, "detail": str(exc).splitlines()[0] if str(exc) else ""}})
        return 1
    return 0


def serve() -> int:
    try:
        settings = Settings.from_env()
    except ConfigError as exc:
        log.critical(str(exc))
        return 2
    log_config = logging_config(settings.log_level)
    logging.config.dictConfig(log_config)
    uvicorn.run(
        create_app(settings),
        host="0.0.0.0",
        port=settings.port,
        log_config=log_config,
        access_log=False,
        timeout_graceful_shutdown=settings.shutdown_timeout,
        server_header=False,
    )
    return 0


def main(argv: list[str]) -> int:
    # INFO until the settings are validated; an invalid LOG_LEVEL is reported, not crashed on.
    logging.config.dictConfig(logging_config("INFO"))
    command = argv[1] if len(argv) > 1 else "serve"
    if command == "serve":
        return serve()
    if command == "migrate":
        return run_migrations()
    log.critical(f"unknown command: {command} (expected serve or migrate)")
    return 2


if __name__ == "__main__":
    sys.exit(main(sys.argv))