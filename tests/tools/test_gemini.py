import json

from keld.tools.base import SetupParams
from keld.tools.gemini import GeminiAdapter

P = SetupParams(endpoint="https://ingest.keld.co", ingest_token="tok", actor="dg@keld.co")


def test_apply_sets_telemetry():
    plan = GeminiAdapter().apply(None, P)
    obj = json.loads(plan.after_text)
    assert obj["telemetry"]["otlpEndpoint"] == "https://ingest.keld.co/v1/logs?token=tok"
    assert plan.managed == {"keys": ["telemetry"], "created": True}


def test_round_trip():
    current = json.dumps({"theme": "dark"}, indent=2) + "\n"
    applied = GeminiAdapter().apply(current, P)
    removed = GeminiAdapter().remove(applied.after_text, applied.managed)
    assert json.loads(removed.after_text) == {"theme": "dark"}


def test_status():
    plan = GeminiAdapter().apply(None, P)
    assert GeminiAdapter().status(plan.after_text, plan.managed).configured is True
    assert GeminiAdapter().status(None, None).configured is False
