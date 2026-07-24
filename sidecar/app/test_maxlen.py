"""Standalone tests for max_len plumbing — the bound on per-inference memory. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_maxlen.py

gliner2's max_len defaults to None, meaning NO truncation, so transient
activation memory grows with sequence length unchecked. The daemon computes an
adaptive cap (see internal/agent/enrich/lenstat) and sends it as max_len; these
tests pin that it actually reaches every model call, because a silently dropped
max_len restores the unbounded-spike behaviour with no visible symptom.
"""
from app.worker import handle


class _RecordingModel:
    """Records the max_len each model call received."""

    def __init__(self):
        self.seen = {}

    def classify_text(self, text, tasks, include_confidence=False, max_len=None):
        self.seen["classify"] = max_len
        return {"task_type": "debug"}

    def extract_entities(self, text, labels, max_len=None):
        self.seen["entities"] = max_len
        return {"entities": {"email": ["a@b.com"]}}

    def create_schema(self):
        return _StubSchema()

    def extract(self, text, schema, include_confidence=False, max_len=None):
        self.seen["extract"] = max_len
        return {"entities": {"email": ["a@b.com"]}, "sensitivity": "pii"}


class _StubSchema:
    def entities(self, labels): return self
    def classification(self, task, options): return self


def test_classify_forwards_max_len():
    m = _RecordingModel()
    handle({"op": "classify", "text": "hi", "tasks": {"task_type": ["debug"]},
            "max_len": 512}, m)
    assert m.seen["classify"] == 512, m.seen


def test_entities_forwards_max_len():
    m = _RecordingModel()
    handle({"op": "entities", "text": "mail a@b.com", "labels": {"email": "Email"},
            "max_len": 384}, m)
    assert m.seen["entities"] == 384, m.seen


def test_extract_forwards_max_len():
    m = _RecordingModel()
    handle({"op": "extract", "text": "x", "labels": {"email": "Email"},
            "tasks": {"sensitivity": ["none", "pii"]}, "max_len": 256}, m)
    assert m.seen["extract"] == 256, m.seen


def test_absent_max_len_forwards_none():
    # No cap requested must mean "no truncation", not "truncate to 0".
    m = _RecordingModel()
    handle({"op": "classify", "text": "hi", "tasks": {"task_type": ["debug"]}}, m)
    assert m.seen["classify"] is None, m.seen


def test_zero_max_len_forwards_none():
    # 0 is the Go zero value; treat it as "unset" rather than an empty window.
    m = _RecordingModel()
    handle({"op": "classify", "text": "hi", "tasks": {"task_type": ["debug"]},
            "max_len": 0}, m)
    assert m.seen["classify"] is None, m.seen


def test_request_models_accept_max_len():
    # The FastAPI request models must accept max_len, or the daemon's cap is
    # rejected/ignored at the HTTP boundary before it ever reaches the worker.
    from app.main import ClassifyIn, ExtractIn, EntitiesIn
    assert ClassifyIn(text="x", tasks={"t": ["a"]}, max_len=512).max_len == 512
    assert EntitiesIn(text="x", labels={"e": "E"}, max_len=512).max_len == 512
    assert ExtractIn(text="x", labels={"e": "E"}, tasks={"t": ["a"]},
                     max_len=512).max_len == 512
    # Omitted stays None (no cap).
    assert ClassifyIn(text="x", tasks={"t": ["a"]}).max_len is None


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
