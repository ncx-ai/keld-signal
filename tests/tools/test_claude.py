import json

from keld.tools.base import SetupParams
from keld.tools.claude import ClaudeAdapter

P = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_apply_to_empty_config():
    plan = ClaudeAdapter().apply(None, P)
    obj = json.loads(plan.after_text)
    assert obj["env"]["OTEL_EXPORTER_OTLP_ENDPOINT"] == "https://ingest.keld.co"
    assert "keld-context.py" in json.dumps(obj["hooks"])
    assert plan.changed is True
    assert plan.managed["created"] is True
    assert "OTEL_EXPORTER_OTLP_ENDPOINT" in plan.managed["env_keys"]


def test_apply_preserves_user_settings():
    current = json.dumps({"env": {"MY": "1"}, "model": "opus"})
    plan = ClaudeAdapter().apply(current, P)
    obj = json.loads(plan.after_text)
    assert obj["env"]["MY"] == "1"
    assert obj["model"] == "opus"
    assert plan.managed["created"] is False


def test_apply_then_remove_round_trips():
    current = json.dumps({"env": {"MY": "1"}, "model": "opus"}, indent=2) + "\n"
    applied = ClaudeAdapter().apply(current, P)
    removed = ClaudeAdapter().remove(applied.after_text, applied.managed)
    assert json.loads(removed.after_text) == {"env": {"MY": "1"}, "model": "opus"}


def test_apply_is_idempotent():
    first = ClaudeAdapter().apply(None, P)
    second = ClaudeAdapter().apply(first.after_text, P)
    assert first.after_text == second.after_text
    assert second.changed is False


def test_status_reports_configured():
    plan = ClaudeAdapter().apply(None, P)
    st = ClaudeAdapter().status(plan.after_text, plan.managed)
    assert st.configured is True
    st_empty = ClaudeAdapter().status(None, None)
    assert st_empty.configured is False
