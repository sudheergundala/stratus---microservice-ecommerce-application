"""Tests that need no database."""

import pytest

from app.config import ConfigError, Settings
from app.migrate import MigrationError, discover
from app.store import InvalidCursor, decode_cursor, encode_cursor

ENV = {"DATABASE_DSN": "host=db dbname=catalog", "IMAGE_BASE_URL": "https://cdn.example.test/"}


def test_settings_from_env(monkeypatch):
    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    settings = Settings.from_env()
    assert settings.port == 8085
    assert settings.image_base_url == "https://cdn.example.test"  # trailing slash removed


@pytest.mark.parametrize("missing", ["DATABASE_DSN", "IMAGE_BASE_URL"])
def test_required_settings(monkeypatch, missing):
    for k, v in ENV.items():
        if k != missing:
            monkeypatch.setenv(k, v)
    monkeypatch.delenv(missing, raising=False)
    with pytest.raises(ConfigError, match=missing):
        Settings.from_env()


@pytest.mark.parametrize("name, value", [
    ("IMAGE_BASE_URL", "cdn.example.test"),
    ("DB_POOL_MAX_SIZE", "0"),
    ("DB_STATEMENT_TIMEOUT_MS", "10"),
    ("PORT", "eighty"),
    ("LOG_LEVEL", "LOUD"),
])
def test_invalid_settings(monkeypatch, name, value):
    for k, v in ENV.items():
        monkeypatch.setenv(k, v)
    monkeypatch.setenv(name, value)
    with pytest.raises(ConfigError):
        Settings.from_env()


def test_cursor_round_trip():
    assert decode_cursor(encode_cursor(4599, "JKT-001")) == (4599, "JKT-001")


@pytest.mark.parametrize("bad", ["not-base64!", "e30", encode_cursor(1, "x")[:-2] + "zz"])
def test_bad_cursor(bad):
    with pytest.raises(InvalidCursor):
        decode_cursor(bad)


def test_migration_file_names_are_enforced(tmp_path):
    (tmp_path / "V001__ok.sql").write_text("SELECT 1;")
    (tmp_path / "create_table.sql").write_text("SELECT 1;")
    with pytest.raises(MigrationError, match="bad migration file name"):
        discover(tmp_path)


def test_shipped_migrations_are_valid():
    versions = [m.version for m in discover()]
    assert versions == sorted(versions) and versions[0] == 1