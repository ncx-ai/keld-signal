from __future__ import annotations

from pathlib import Path

from .. import telemetry as t
from ..config.merge import (
    has_keld_block, strip_keld_block, upsert_keld_block, validate_toml,
)
from ..paths import hook_path
from .base import Plan, SetupParams, ToolStatus


class CodexAdapter:
    name = "codex"
    display_name = "Codex"

    def config_path(self) -> Path:
        return Path.home() / ".codex" / "config.toml"

    def detect(self) -> bool:
        return self.config_path().parent.exists()

    def apply(self, current_text: str | None, params: SetupParams) -> Plan:
        body = t.codex_block_body(params, t.hook_command(str(hook_path())))
        after = upsert_keld_block(current_text, body)
        validate_toml(after)  # raises KeldError on duplicate-table conflict
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed={"block": True, "created": current_text is None},
            summary=["add [otel] + SessionStart/PreToolUse hooks block"],
            changed=after != (current_text or ""),
        )

    def remove(self, current_text: str | None, managed: dict) -> Plan:
        after = strip_keld_block(current_text)
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed=managed, summary=["remove Keld block"],
            changed=after != (current_text or ""),
        )

    def status(self, current_text: str | None, managed: dict | None) -> ToolStatus:
        configured = has_keld_block(current_text)
        return ToolStatus(
            name=self.name, installed=self.detect(), configured=configured,
            detail="configured" if configured else "not configured",
        )
