import tomllib

from keld.tools.base import SetupParams
from keld import telemetry as t


P = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok123", actor="dg@keld.co")


def test_claude_env():
    env = t.claude_env(P)
    assert env["CLAUDE_CODE_ENABLE_TELEMETRY"] == "1"
    assert env["OTEL_EXPORTER_OTLP_ENDPOINT"] == "https://ingest.keld.co"
    assert "x-keld-ingest-token=tok123" in env["OTEL_EXPORTER_OTLP_HEADERS"]
    assert "x-keld-actor=dg@keld.co" in env["OTEL_EXPORTER_OTLP_HEADERS"]


def test_hook_command_contains_substr():
    cmd = t.hook_command("/h/.keld/keld-context.py")
    assert t.HOOK_COMMAND_SUBSTR in cmd
    assert cmd.endswith("; true")


def test_gemini_telemetry():
    g = t.gemini_telemetry(P)
    assert g["enabled"] is True
    assert g["otlpEndpoint"] == "https://ingest.keld.co/v1/logs?token=tok123"


def test_codex_block_body_is_valid_toml():
    body = t.codex_block_body(P, t.hook_command("/h/.keld/keld-context.py"))
    parsed = tomllib.loads(body)
    assert parsed["otel"]["exporter"]["otlp-http"]["endpoint"] == \
        "https://ingest.keld.co/v1/logs?token=tok123"
    assert len(parsed["hooks"]["SessionStart"]) == 1
    assert len(parsed["hooks"]["PreToolUse"]) == 1
