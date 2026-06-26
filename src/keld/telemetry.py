from __future__ import annotations

from .tools.base import SetupParams

HOOK_COMMAND_SUBSTR = "keld-context.py"

# (event, matcher) pairs for Claude Code hooks. None matcher = no matcher key.
CLAUDE_HOOK_EVENTS: list[tuple[str, str | None]] = [
    ("SessionStart", "startup"),
    ("SessionStart", "resume"),
    ("CwdChanged", None),
]

CODEX_HOOK_EVENTS: list[str] = ["SessionStart", "PreToolUse"]


def hook_command(hook_path: str) -> str:
    return f"python3 {hook_path}; true"


def claude_env(p: SetupParams) -> dict[str, str]:
    return {
        "CLAUDE_CODE_ENABLE_TELEMETRY": "1",
        "OTEL_LOGS_EXPORTER": "otlp",
        "OTEL_METRICS_EXPORTER": "otlp",
        "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json",
        "OTEL_EXPORTER_OTLP_ENDPOINT": p.endpoint,
        "OTEL_EXPORTER_OTLP_HEADERS": (
            f"x-keld-ingest-token={p.ingest_token},x-keld-actor={p.actor}"
        ),
    }


def gemini_telemetry(p: SetupParams) -> dict:
    return {
        "enabled": True,
        "target": "local",
        "otlpProtocol": "http",
        "otlpEndpoint": f"{p.endpoint}/v1/logs?token={p.ingest_token}",
        "logPrompts": False,
    }


def codex_block_body(p: SetupParams, hook_cmd: str) -> str:
    endpoint = f"{p.endpoint}/v1/logs?token={p.ingest_token}"
    hook_blocks = "\n".join(
        f"[[hooks.{event}]]\n"
        f"hooks = [ {{ type = \"command\", command = '{hook_cmd}' }} ]\n"
        for event in CODEX_HOOK_EVENTS
    )
    return (
        "[otel]\n"
        'environment = "prod"\n'
        "log_user_prompt = false\n"
        f'exporter = {{ otlp-http = {{ endpoint = "{endpoint}", '
        f'protocol = "json", headers = {{ "x-keld-actor" = "{p.actor}" }} }} }}\n'
        "\n"
        f"{hook_blocks}"
    )
