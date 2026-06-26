from __future__ import annotations

from pathlib import Path

from ..config.merge import dump_json, load_json
from ..telemetry import gemini_telemetry
from .base import Plan, SetupParams, ToolStatus


class GeminiAdapter:
    name = "gemini"
    display_name = "Gemini CLI"

    def config_path(self) -> Path:
        return Path.home() / ".gemini" / "settings.json"

    def detect(self) -> bool:
        return self.config_path().parent.exists()

    def apply(self, current_text: str | None, params: SetupParams) -> Plan:
        obj = load_json(current_text)
        obj["telemetry"] = gemini_telemetry(params)
        after = dump_json(obj)
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed={"keys": ["telemetry"], "created": current_text is None},
            summary=["set telemetry block"],
            changed=after != (current_text or ""),
        )

    def remove(self, current_text: str | None, managed: dict) -> Plan:
        obj = load_json(current_text)
        for key in managed.get("keys", ["telemetry"]):
            obj.pop(key, None)
        after = dump_json(obj) if obj else ""
        return Plan(
            name=self.name, config_path=self.config_path(), after_text=after,
            managed=managed, summary=["remove telemetry block"],
            changed=after != (current_text or ""),
        )

    def status(self, current_text: str | None, managed: dict | None) -> ToolStatus:
        obj = load_json(current_text)
        configured = "otlpEndpoint" in obj.get("telemetry", {})
        return ToolStatus(
            name=self.name, installed=self.detect(), configured=configured,
            detail="configured" if configured else "not configured",
        )
