"""Standalone tests for the WorkerManager. Fake spawn_fn/rss_fn/ram_fn/clock so
no real process or model is needed. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker_manager.py
"""
from app.worker_manager import (
    WorkerManager, WorkerTimeout, WorkerUnavailable, WorkerError,
    DOWN, SPAWNING, READY, HELD,
)


class FakeQueue:
    def __init__(self): self.items = []
    def put(self, x): self.items.append(x)
    def get(self, timeout=None):
        if not self.items:
            import queue
            raise queue.Empty()
        return self.items.pop(0)


class FakeProc:
    def __init__(self, pid=4242): self.pid = pid; self._alive = True
    def is_alive(self): return self._alive
    def kill(self): self._alive = False
    def join(self, timeout=None): pass


def make(**over):
    """A manager whose worker becomes ready immediately and echoes results.
    `scripted` lets a test control what the worker 'returns' per call."""
    state = {"proc": None, "req": None, "resp": None, "ready": True,
             "responses": over.pop("responses", None)}

    def spawn_fn():
        proc, req, resp = FakeProc(), FakeQueue(), FakeQueue()
        if state["ready"]:
            resp.put({"ready": True})
        state.update(proc=proc, req=req, resp=resp)
        return proc, req, resp

    def default_rss(pid): return over.pop("rss", 2700.0)
    now = {"t": 100.0}
    kw = dict(spawn_fn=spawn_fn, rss_fn=default_rss,
              ram_fn=lambda: (50.0, 9000.0), clock=lambda: now["t"],
              job_deadline_s=5.0, spawn_timeout_s=5.0, idle_timeout_s=600.0,
              evict_pct=5.0, margin_mb=1024.0)
    kw.update(over)
    m = WorkerManager(**kw)
    m._test = state
    m._now = now
    return m


def _feed_result(m, result):
    """Simulate the worker having produced a response for the next call."""
    m._test["resp"].put({"ok": True, "result": result})


def test_call_spawns_and_returns_result():
    m = make()
    m._call_hook = lambda req: m._test["resp"].put({"ok": True, "result": {"echo": req["op"]}})
    out = m.call({"op": "classify", "text": "hi", "tasks": {}})
    assert out == {"echo": "classify"} and m.state == READY


def test_call_timeout_kills_and_raises():
    m = make()
    m._call_hook = lambda req: None       # worker never responds
    try:
        m.call({"op": "classify", "text": "x", "tasks": {}})
        assert False, "expected WorkerTimeout"
    except WorkerTimeout:
        pass
    assert m.state == DOWN and m.counts["kills_timeout"] == 1


def test_worker_error_is_raised():
    m = make()
    m._call_hook = lambda req: m._test["resp"].put({"ok": False, "error": "boom"})
    try:
        m.call({"op": "classify", "text": "x", "tasks": {}})
        assert False, "expected WorkerError"
    except WorkerError:
        pass


def test_crash_detected_and_raises():
    m = make()
    def crash(req):
        m._test["proc"]._alive = False    # worker died without responding
    m._call_hook = crash
    try:
        m.call({"op": "classify", "text": "x", "tasks": {}})
        assert False, "expected WorkerTimeout/crash"
    except (WorkerTimeout, WorkerError):
        pass
    assert m.state == DOWN and m.counts["crashes"] == 1


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
