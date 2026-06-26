from __future__ import annotations

from pathlib import Path

from .. import telemetry as t
from ..config.merge import (
    add_claude_hook, dump_json, has_hook_with_command, load_json,
    merge_env, remove_hooks_by_command, remove_section_keys,
)
from ..paths import hook_path
from .base import Plan, SetupParams, ToolStatus


class ClaudeAdapter:
    name = "claude_code"
    display_name = "Claude Code"

    def config_path(self) -> Path:
        return Path.home() / ".claude" / "settings.json"

    def detect(self) -> bool:
        return self.config_path().parent.exists()

    def apply(self, current_text: str | None, params: SetupParams) -> Plan:
        obj = load_json(current_text)
        env_keys = merge_env(obj, t.claude_env(params))
        command = t.hook_command(str(hook_path()))
        for event, matcher in t.CLAUDE_HOOK_EVENTS:
            add_claude_hook(obj, event, matcher, command)
        after = dump_json(obj)
        managed = {
            "env_keys": env_keys,
            "hook_substr": t.HOOK_COMMAND_SUBSTR,
            "created": current_text is None,
        }
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed=managed,
            summary=[f"set {len(env_keys)} OTEL env vars", "add SessionStart + CwdChanged hooks"],
            changed=after != (current_text or ""),
        )

    def remove(self, current_text: str | None, managed: dict) -> Plan:
        obj = load_json(current_text)
        remove_section_keys(obj, "env", managed.get("env_keys", []))
        remove_hooks_by_command(obj, managed.get("hook_substr", t.HOOK_COMMAND_SUBSTR))
        after = dump_json(obj) if obj else ""
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed=managed, summary=["remove Keld env vars and hooks"],
            changed=after != (current_text or ""),
        )

    def status(self, current_text: str | None, managed: dict | None) -> ToolStatus:
        obj = load_json(current_text)
        configured = (
            "OTEL_EXPORTER_OTLP_ENDPOINT" in obj.get("env", {})
            and has_hook_with_command(obj, t.HOOK_COMMAND_SUBSTR)
        )
        return ToolStatus(
            name=self.name, installed=self.detect(), configured=configured,
            detail="configured" if configured else "not configured",
        )
