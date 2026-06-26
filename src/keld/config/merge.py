from __future__ import annotations

import json
import tomllib

from ..errors import KeldError


def load_json(text: str | None) -> dict:
    if not text or not text.strip():
        return {}
    return json.loads(text)


def dump_json(obj: dict) -> str:
    return json.dumps(obj, indent=2) + "\n"


def merge_env(obj: dict, env: dict[str, str]) -> list[str]:
    section = obj.setdefault("env", {})
    section.update(env)
    return list(env.keys())


def remove_section_keys(obj: dict, section: str, keys: list[str]) -> None:
    sec = obj.get(section)
    if not isinstance(sec, dict):
        return
    for key in keys:
        sec.pop(key, None)
    if not sec:
        obj.pop(section, None)


def add_claude_hook(obj: dict, event: str, matcher: str | None, command: str) -> None:
    entry: dict = {"hooks": [{"type": "command", "command": command}]}
    if matcher is not None:
        entry = {"matcher": matcher, **entry}
    arr = obj.setdefault("hooks", {}).setdefault(event, [])
    if entry not in arr:
        arr.append(entry)


def has_hook_with_command(obj: dict, substr: str) -> bool:
    hooks = obj.get("hooks")
    if not isinstance(hooks, dict):
        return False
    return substr in json.dumps(hooks)


def remove_hooks_by_command(obj: dict, substr: str) -> None:
    hooks = obj.get("hooks")
    if not isinstance(hooks, dict):
        return
    for event in list(hooks):
        arr = hooks[event]
        arr[:] = [e for e in arr if substr not in json.dumps(e)]
        if not arr:
            del hooks[event]
    if not hooks:
        obj.pop("hooks", None)


KELD_TOML_START = "# >>> keld (managed by keld CLI — do not edit between markers)"
KELD_TOML_END = "# <<< keld"


def has_keld_block(text: str | None) -> bool:
    return bool(text) and KELD_TOML_START in text


def strip_keld_block(text: str | None) -> str:
    if not text or KELD_TOML_START not in text:
        return text or ""
    lines = text.splitlines()
    out: list[str] = []
    inside = False
    for line in lines:
        if line.strip() == KELD_TOML_START:
            inside = True
            continue
        if inside and line.strip() == KELD_TOML_END:
            inside = False
            continue
        if not inside:
            out.append(line)
    result = "\n".join(out).rstrip("\n")
    return result + "\n" if result else ""


def upsert_keld_block(text: str | None, body: str) -> str:
    base = strip_keld_block(text)
    body = body if body.endswith("\n") else body + "\n"
    block = f"{KELD_TOML_START}\n{body}{KELD_TOML_END}\n"
    if not base.strip():
        return block
    return base + "\n" + block


def validate_toml(text: str) -> None:
    try:
        tomllib.loads(text)
    except tomllib.TOMLDecodeError as exc:
        raise KeldError(f"resulting TOML is invalid: {exc}") from exc
