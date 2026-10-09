"""JSON logs on stdout, for our code and for uvicorn's own messages."""

import json
import logging
from datetime import UTC, datetime


class JsonFormatter(logging.Formatter):
    def format(self, record: logging.LogRecord) -> str:
        entry = {
            "time": datetime.fromtimestamp(record.created, UTC).isoformat(timespec="milliseconds").replace("+00:00", "Z"),
            "level": record.levelname,
            "logger": record.name,
            "message": record.getMessage(),
            "service": "fraud-service",
        }
        entry.update(getattr(record, "fields", None) or {})
        if record.exc_info:
            entry["exception"] = self.formatException(record.exc_info)
        return json.dumps(entry, default=str)


def logging_config(level: str) -> dict:
    """A logging.dictConfig used both by the app and by uvicorn."""
    return {
        "version": 1,
        "disable_existing_loggers": False,
        "formatters": {"json": {"()": JsonFormatter}},
        "handlers": {"stdout": {"class": "logging.StreamHandler", "formatter": "json", "stream": "ext://sys.stdout"}},
        "root": {"handlers": ["stdout"], "level": level},
        "loggers": {
            # uvicorn's loggers go through the same JSON handler.
            "uvicorn": {"handlers": [], "level": level, "propagate": True},
            "uvicorn.error": {"handlers": [], "level": level, "propagate": True},
            "uvicorn.access": {"handlers": [], "level": "WARNING", "propagate": True},
        },
    }