from __future__ import annotations

import json
from dataclasses import dataclass, field

from ..paths import keld_home, manifest_path


@dataclass
class HookRecord:
    path: str
    version: str
    sha256: str


@dataclass
class ToolManifest:
    name: str
    config_path: str
    managed: dict = field(default_factory=dict)


@dataclass
class Manifest:
    endpoint: str | None = None
    actor: str | None = None
    tools: dict[str, ToolManifest] = field(default_factory=dict)
    hook: HookRecord | None = None

    @classmethod
    def load(cls) -> "Manifest":
        path = manifest_path()
        if not path.exists():
            return cls()
        return cls.from_dict(json.loads(path.read_text()))

    def save(self) -> None:
        keld_home().mkdir(parents=True, exist_ok=True)
        manifest_path().write_text(json.dumps(self.to_dict(), indent=2) + "\n")

    def to_dict(self) -> dict:
        return {
            "endpoint": self.endpoint,
            "actor": self.actor,
            "tools": {k: vars(v) for k, v in self.tools.items()},
            "hook": vars(self.hook) if self.hook else None,
        }

    @classmethod
    def from_dict(cls, d: dict) -> "Manifest":
        tools = {k: ToolManifest(**v) for k, v in (d.get("tools") or {}).items()}
        hook = HookRecord(**d["hook"]) if d.get("hook") else None
        return cls(endpoint=d.get("endpoint"), actor=d.get("actor"), tools=tools, hook=hook)
