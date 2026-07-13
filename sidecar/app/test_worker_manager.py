"""Standalone tests for the WorkerManager. Fake spawn_fn/rss_fn/ram_fn/clock so
no real process or model is needed. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker_manager.py
"""
from app.worker_manager import (
    WorkerManager, WorkerTimeout, WorkerUnavailable, WorkerError,
    DOWN, SPAWNING, READY, HELD,
)


class FakeQueue:
    def __init__(self, advance=None): self.items = []; self._advance = advance
    def put(self, x): self.items.append(x)
    def get(self, timeout=None):
        if not self.items:
            if self._advance and timeout:  # simulate blocking for `timeout`
                self._advance(timeout)
            import queue
            raise queue.Empty()
        return self.items.pop(0)


class FakeProc:
    def __init__(self, pid=4242): self.pid = pid; self._alive = True
    def is_alive(self): return self._alive
    def kill(self): self._alive = False
    def join(self, timeout=None): pass


def make(**over):
    """A manager whose worker becomes ready immediately; tests drive per-call
    worker responses via m._call_hook."""
    state = {"proc": None, "req": None, "resp": None, "ready": True}

    now = {"t": 100.0}

    def spawn_fn():
        adv = lambda dt: now.__setitem__("t", now["t"] + dt)
        proc, req, resp = FakeProc(), FakeQueue(), FakeQueue(advance=adv)
        if state["ready"]:
            resp.put({"ready": True})
        state.update(proc=proc, req=req, resp=resp)
        return proc, req, resp

    def default_rss(pid): return over.pop("rss", 2700.0)
    kw = dict(spawn_fn=spawn_fn, rss_fn=default_rss,
              ram_fn=lambda: (50.0, 9000.0), clock=lambda: now["t"],
              job_deadline_s=5.0, live_poll_s=1.0, spawn_timeout_s=5.0, idle_timeout_s=600.0,
              evict_pct=5.0, margin_mb=1024.0)
    kw.update(over)
    m = WorkerManager(**kw)
    m._test = state
    m._now = now
    return m


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


def test_worker_death_detected_before_deadline():
    # A worker that dies mid-job must be noticed within ~one liveness poll, not
    # after blocking the whole (long) job deadline, since it holds the
    # single-flight slot until then.
    m = make(job_deadline_s=20.0, live_poll_s=1.0)
    start = m._now["t"]
    m._call_hook = lambda req: setattr(m._test["proc"], "_alive", False)  # dies, no reply
    try:
        m.call({"op": "classify", "text": "x", "tasks": {}})
        assert False, "expected WorkerTimeout"
    except WorkerTimeout:
        pass
    assert m.counts["crashes"] == 1
    assert (m._now["t"] - start) <= 2.0, f"detection took {m._now['t'] - start}s, want ~1 poll"


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


def _ready_manager(**over):
    m = make(**over)
    m._call_hook = lambda req: m._test["resp"].put({"ok": True, "result": {}})
    m.call({"op": "classify", "text": "x", "tasks": {}})  # -> READY, model_cost set
    return m


def test_poll_idle_kills_worker():
    m = _ready_manager(idle_timeout_s=10.0)
    m._now["t"] += 11.0                       # 11s since last activity
    m.poll()
    assert m.state == DOWN and m.counts["kills_idle"] == 1


def test_poll_idle_disabled_when_zero():
    # idle_timeout_s <= 0 disables idle eviction; a long-idle worker stays READY
    # (regression: without the >0 guard, `now - last_activity >= 0` is always
    # true, so the worker gets idle-killed immediately after every request).
    m = _ready_manager(idle_timeout_s=0.0)
    m._now["t"] += 10000.0
    m.poll()
    assert m.state == READY and m.counts["kills_idle"] == 0


def test_poll_pressure_kills_and_holds():
    m = _ready_manager()
    m._ram_fn = lambda: (3.0, 300.0)          # <= evict_pct
    m.poll()
    assert m.state == HELD and m.counts["kills_pressure"] == 1


def test_poll_held_releases_on_headroom():
    m = _ready_manager()
    m._ram_fn = lambda: (3.0, 300.0); m.poll()
    assert m.state == HELD
    m._ram_fn = lambda: (50.0, 9000.0); m.poll()
    assert m.state == DOWN                    # respawns on next call


def test_poll_rss_ceiling_recycles_when_idle():
    m = _ready_manager(margin_mb=1000.0)      # ceiling = model_cost(2700)+1000 = 3700
    m._rss_fn = lambda pid: 4000.0            # over ceiling
    m.poll()
    assert m.state == DOWN and m.counts["recycles"] == 1


def test_poll_no_recycle_below_ceiling():
    m = _ready_manager(margin_mb=1000.0)
    m._rss_fn = lambda pid: 3000.0            # under 3700
    m.poll()
    assert m.state == READY and m.counts["recycles"] == 0


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
