from __future__ import annotations

from ..errors import KeldError
from .base import ToolAdapter
from .claude import ClaudeAdapter
from .codex import CodexAdapter
from .gemini import GeminiAdapter

ALL_ADAPTERS: list[ToolAdapter] = [ClaudeAdapter(), CodexAdapter(), GeminiAdapter()]


def get_adapter(name: str) -> ToolAdapter:
    for adapter in ALL_ADAPTERS:
        if adapter.name == name:
            return adapter
    known = ", ".join(a.name for a in ALL_ADAPTERS)
    raise KeldError(f"unknown tool '{name}'. Known tools: {known}")


def select_adapters(names: list[str] | None) -> list[ToolAdapter]:
    if names:
        return [get_adapter(n) for n in names]
    return [a for a in ALL_ADAPTERS if a.detect()]
