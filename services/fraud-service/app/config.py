"""Configuration from environment variables only, validated at startup."""

import os
from dataclasses import dataclass


class ConfigError(ValueError):
    pass


@dataclass(frozen=True)
class Settings:
    port: int = 8086
    model_path: str = "model/model.json"
    review_threshold: float = 0.30
    decline_threshold: float = 0.80
    shutdown_timeout: int = 20
    log_level: str = "INFO"

    @classmethod
    def from_env(cls) -> "Settings":
        try:
            settings = cls(
                port=int(os.getenv("PORT", "8086")),
                model_path=os.getenv("MODEL_PATH", "model/model.json"),
                review_threshold=float(os.getenv("REVIEW_THRESHOLD", "0.30")),
                decline_threshold=float(os.getenv("DECLINE_THRESHOLD", "0.80")),
                shutdown_timeout=int(os.getenv("SHUTDOWN_TIMEOUT_SECONDS", "20")),
                log_level=os.getenv("LOG_LEVEL", "INFO").upper(),
            )
        except ValueError as exc:
            raise ConfigError(f"invalid configuration: {exc}") from exc
        if not 0 < settings.review_threshold < settings.decline_threshold < 1:
            raise ConfigError("thresholds must satisfy 0 < REVIEW_THRESHOLD < DECLINE_THRESHOLD < 1")
        return settings