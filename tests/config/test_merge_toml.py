import pytest

from keld.errors import KeldError
from keld.config.merge import (
    strip_keld_block, upsert_keld_block, has_keld_block, validate_toml,
    KELD_TOML_START, KELD_TOML_END,
)

BODY = '[otel]\nenvironment = "prod"\n'


def test_upsert_into_empty():
    out = upsert_keld_block("", BODY)
    assert KELD_TOML_START in out and KELD_TOML_END in out
    assert has_keld_block(out)
    validate_toml(out)


def test_upsert_preserves_user_content():
    user = '[user]\nkey = "val"\n'
    out = upsert_keld_block(user, BODY)
    assert '[user]' in out
    validate_toml(out)


def test_upsert_is_idempotent():
    once = upsert_keld_block("", BODY)
    twice = upsert_keld_block(once, BODY)
    assert once == twice


def test_strip_removes_block_only():
    user = '[user]\nkey = "val"\n'
    out = upsert_keld_block(user, BODY)
    stripped = strip_keld_block(out)
    assert not has_keld_block(stripped)
    assert '[user]' in stripped
    validate_toml(stripped)


def test_validate_toml_raises_on_duplicate_table():
    with pytest.raises(KeldError):
        validate_toml('[otel]\na=1\n[otel]\nb=2\n')
