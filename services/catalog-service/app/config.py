"""Configuration from environment variables only, validated at startup."""

import os
from dataclasses import dataclass


class ConfigError(ValueError):
    pass


def _required(name: str) -> str:
    value = os.getenv(name, "").strip()
    if not value:
        raise ConfigError(f"{name} is required")
    return value


@dataclass(frozen=True)
class Settings:
    database_dsn: str
    image_base_url: str
    port: int = 8085
    pool_min_size: int = 1
    pool_max_size: int = 10
    pool_timeout_seconds: float = 2.0
    statement_timeout_ms: int = 2000
    cache_max_age_seconds: int = 60
    shutdown_timeout: int = 20
    log_level: str = "INFO"

    @classmethod
    def from_env(cls) -> "Settings":
        try:
            settings = cls(
                database_dsn=_required("DATABASE_DSN"),
                image_base_url=_required("IMAGE_BASE_URL").rstrip("/"),
                port=int(os.getenv("PORT", "8085")),
                pool_min_size=int(os.getenv("DB_POOL_MIN_SIZE", "1")),
                pool_max_size=int(os.getenv("DB_POOL_MAX_SIZE", "10")),
                pool_timeout_seconds=float(os.getenv("DB_POOL_TIMEOUT_SECONDS", "2")),
                statement_timeout_ms=int(os.getenv("DB_STATEMENT_TIMEOUT_MS", "2000")),
                cache_max_age_seconds=int(os.getenv("CACHE_MAX_AGE_SECONDS", "60")),
                shutdown_timeout=int(os.getenv("SHUTDOWN_TIMEOUT_SECONDS", "20")),
                log_level=os.getenv("LOG_LEVEL", "INFO").upper(),
            )
        except ValueError as exc:
            if isinstance(exc, ConfigError):
                raise
            raise ConfigError(f"invalid configuration: {exc}") from exc
        settings.validate()
        return settings

    def validate(self) -> None:
        if not self.image_base_url.startswith(("https://", "http://")):
            raise ConfigError("IMAGE_BASE_URL must start with https:// (or http:// for local runs)")
        if not 1 <= self.pool_min_size <= self.pool_max_size <= 100:
            raise ConfigError("pool sizes must satisfy 1 <= DB_POOL_MIN_SIZE <= DB_POOL_MAX_SIZE <= 100")
        if not 0 < self.pool_timeout_seconds <= 10:
            raise ConfigError("DB_POOL_TIMEOUT_SECONDS must be in (0, 10]")
        if not 100 <= self.statement_timeout_ms <= 30_000:
            raise ConfigError("DB_STATEMENT_TIMEOUT_MS must be between 100 and 30000")
        if self.log_level not in ("DEBUG", "INFO", "WARNING", "ERROR"):
            raise ConfigError("LOG_LEVEL must be DEBUG, INFO, WARNING or ERROR")
        if not 0 <= self.cache_max_age_seconds <= 86_400:
            raise ConfigError("CACHE_MAX_AGE_SECONDS must be between 0 and 86400")