import tomllib

from keld.tools.base import SetupParams
from keld.tools.codex import CodexAdapter

P = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_apply_to_empty():
    plan = CodexAdapter().apply(None, P)
    parsed = tomllib.loads(plan.after_text)
    assert parsed["otel"]["environment"] == "prod"
    assert "keld-context.py" in plan.after_text
    assert plan.managed == {"block": True, "created": True}


def test_round_trip_preserves_user_toml():
    user = '[user]\nfoo = "bar"\n'
    applied = CodexAdapter().apply(user, P)
    assert '[user]' in applied.after_text
    removed = CodexAdapter().remove(applied.after_text, applied.managed)
    assert tomllib.loads(removed.after_text) == {"user": {"foo": "bar"}}


def test_apply_idempotent():
    first = CodexAdapter().apply(None, P)
    second = CodexAdapter().apply(first.after_text, P)
    assert first.after_text == second.after_text


def test_apply_conflict_returns_conflict_plan_not_raises():
    plan = CodexAdapter().apply('[otel]\nenvironment = "dev"\n', P)
    assert plan.conflict is not None
    assert "otel" in plan.conflict.lower()
    assert plan.changed is False


def test_status():
    plan = CodexAdapter().apply(None, P)
    assert CodexAdapter().status(plan.after_text, plan.managed).configured is True
    assert CodexAdapter().status("[user]\nx=1\n", None).configured is False
