"""Standalone tests for the inference worker. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker.py
"""
from app.worker import handle, serve


class _StubModel:
    def classify_text(self, text, tasks, include_confidence=False):
        return {"task_type": "debug"}
    def extract_entities(self, text, labels):
        return {"entities": {"email": ["a@b.com"]}}
    def create_schema(self):
        return _StubSchema()
    def extract(self, text, schema, include_confidence=False):
        return {"entities": {"email": ["a@b.com"]}, "sensitivity": "pii"}


class _StubSchema:
    def entities(self, labels): return self
    def classification(self, task, options): return self


def test_handle_classify():
    out = handle({"op": "classify", "text": "hi", "tasks": {"task_type": ["debug"]}}, _StubModel())
    assert out["results"]["task_type"][0]["label"] == "debug"


def test_handle_entities():
    out = handle({"op": "entities", "text": "mail a@b.com", "labels": {"email": "Email"}}, _StubModel())
    assert out["entities"][0]["label"] == "email" and out["entities"][0]["text"] == "a@b.com"


def test_handle_extract():
    out = handle({"op": "extract", "text": "x", "labels": {"email": "Email"},
                  "tasks": {"sensitivity": ["none", "pii"]}}, _StubModel())
    assert "entities" in out and out["results"]["sensitivity"][0]["label"] == "pii"


def test_handle_unknown_op_raises():
    try:
        handle({"op": "nope", "text": "x"}, _StubModel())
        assert False, "expected ValueError"
    except ValueError:
        pass


def test_serve_ready_then_handles_then_stops():
    reqs = [{"op": "classify", "text": "hi", "tasks": {"t": ["a"]}}, None]
    sent = []

    class Q:
        def __init__(self, items=None): self.items = list(items or [])
        def get(self): return self.items.pop(0)
        def put(self, x): sent.append(x)

    serve(Q(reqs), Q(), lambda: _StubModel())
    assert sent[0] == {"ready": True}
    assert sent[1]["ok"] is True and "results" in sent[1]["result"]


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
