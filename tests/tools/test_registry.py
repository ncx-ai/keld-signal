import pytest

from keld.errors import KeldError
from keld.tools.registry import ALL_ADAPTERS, get_adapter, select_adapters


def test_all_adapters_present():
    names = {a.name for a in ALL_ADAPTERS}
    assert names == {"claude_code", "codex", "gemini"}


def test_get_adapter_unknown_raises():
    with pytest.raises(KeldError):
        get_adapter("nope")


def test_select_explicit_ignores_detection():
    selected = select_adapters(["codex"])
    assert [a.name for a in selected] == ["codex"]


def test_select_none_uses_detection(monkeypatch):
    for a in ALL_ADAPTERS:
        monkeypatch.setattr(type(a), "detect", lambda self: self.name == "gemini")
    selected = select_adapters(None)
    assert [a.name for a in selected] == ["gemini"]
