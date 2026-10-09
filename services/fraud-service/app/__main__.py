"""Entry point: python -m app

Runs uvicorn in-process so this Python process is PID 1 and receives SIGTERM
directly. uvicorn then stops accepting connections and lets in-flight
requests finish (up to SHUTDOWN_TIMEOUT_SECONDS).
"""

import logging.config
import sys

import uvicorn

from app.config import ConfigError, Settings
from app.logs import logging_config
from app.main import create_app
from app.model import FraudModel, ModelError


def main() -> int:
    try:
        settings = Settings.from_env()
    except ConfigError as exc:
        logging.config.dictConfig(logging_config("INFO"))
        logging.getLogger("fraud").critical(str(exc))
        return 2

    log_config = logging_config(settings.log_level)
    logging.config.dictConfig(log_config)
    try:
        model = FraudModel.load(settings.model_path)
    except ModelError as exc:
        logging.getLogger("fraud").critical(str(exc))
        return 2

    uvicorn.run(
        create_app(settings, model),
        host="0.0.0.0",
        port=settings.port,
        log_config=log_config,
        access_log=False,
        timeout_graceful_shutdown=settings.shutdown_timeout,
        server_header=False,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())