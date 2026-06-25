from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Protocol, runtime_checkable


@dataclass
class SetupParams:
    endpoint: str
    ingest_token: str
    actor: str


@dataclass
class Plan:
    name: str
    config_path: Path
    after_text: str
    managed: dict
    summary: list[str]
    changed: bool


@dataclass
class ToolStatus:
    name: str
    installed: bool
    configured: bool
    detail: str


@runtime_checkable
class ToolAdapter(Protocol):
    name: str
    display_name: str

    def detect(self) -> bool: ...
    def config_path(self) -> Path: ...
    def apply(self, current_text: str | None, params: SetupParams) -> Plan: ...
    def remove(self, current_text: str | None, managed: dict) -> Plan: ...
    def status(self, current_text: str | None, managed: dict | None) -> ToolStatus: ...
