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
    # Collect warnings instead of writing them to stderr: several fixtures are (truthfully)
    # infeasible configurations, and a passing suite should not print like a failing one.
    warns = []
    kw = dict(spawn_fn=spawn_fn, rss_fn=default_rss, warn_fn=warns.append,
              ram_fn=lambda: (50.0, 9000.0), clock=lambda: now["t"],
              job_deadline_s=5.0, live_poll_s=1.0, spawn_timeout_s=5.0, idle_timeout_s=600.0,
              evict_pct=5.0, margin_mb=1024.0)
    kw.update(over)
    m = WorkerManager(**kw)
    m._warns = warns
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
    m._hard_limit_mb = 9000.0                 # isolate: test the drift guard, not the hard limit
    m._rss_fn = lambda pid: 4000.0            # over ceiling
    m.poll()
    assert m.state == DOWN and m.counts["recycles"] == 1


def test_poll_no_recycle_below_ceiling():
    m = _ready_manager(margin_mb=1000.0)
    m._rss_fn = lambda pid: 3000.0            # under 3700
    m.poll()
    assert m.state == READY and m.counts["recycles"] == 0


# --- the parent's share of the budget is MEASURED, not assumed ------------------------------
#
# hard_limit_mb() = total budget - what the parent costs. The parent's cost was a constant
# (KELD_SIDECAR_PARENT_RESERVE_MB, 150), which was true only while the parent held nothing but
# FastAPI. This service is the client-side analysis and enrichment service, not a GLiNER2
# wrapper: spaCy for the `term` level lives in the parent and costs ~619 MB, so the constant
# went stale by ~470 MB and the guard under-protected by exactly that much — silently, because
# nothing measured the number it was asserting.

def test_the_parent_reserve_starts_at_the_configured_constant():
    """The measured figure only ever RAISES the reserve. Before anything is sampled the
    behaviour must be identical to the constant-only design."""
    m = make(parent_rss_fn=lambda: 0.0)
    m._budget_mb, m._parent_reserve_mb = 4096.0, 150.0
    assert m.parent_reserve_mb() == 150.0
    assert m.hard_limit_mb() == 3946.0


def test_a_larger_measured_parent_tightens_the_worker_limit():
    """~619 MB of spaCy plus a ~60 MB base parent: the worker's share of a 4096 MB budget is
    ~3417 MB, not the 3946 MB the constant claimed."""
    parent = {"mb": 60.0}
    m = make(parent_rss_fn=lambda: parent["mb"])
    m._budget_mb, m._parent_reserve_mb = 4096.0, 150.0
    m.observe_parent_rss()
    assert m.hard_limit_mb() == 3946.0, "a parent below the constant must not LOOSEN the limit"

    parent["mb"] = 679.0                       # spaCy has loaded
    m.observe_parent_rss()
    assert m.parent_reserve_mb() == 679.0
    assert m.hard_limit_mb() == 4096.0 - 679.0


def test_the_reserve_is_a_high_water_mark_and_never_falls():
    """A limit that moved with a live sample would oscillate: the parent dips, the limit rises,
    and a worker that was over-limit is under it again with nothing having changed about the
    risk. The parent is never recycled, so its cost is monotone in practice — taking the
    high-water makes the guard monotone too, and a guard that only tightens cannot oscillate.
    This is the same failure the RSS guard already had once, by sampling the trough."""
    parent = {"mb": 700.0}
    m = make(parent_rss_fn=lambda: parent["mb"])
    m._budget_mb, m._parent_reserve_mb = 4096.0, 150.0
    m.observe_parent_rss()
    parent["mb"] = 200.0                       # a dip: glibc returned some arenas
    m.observe_parent_rss()
    assert m.parent_reserve_mb() == 700.0


def test_a_measured_parent_never_pushes_the_hard_limit_below_the_ceiling():
    """AGENTS.md's standing invariant: the hard limit must never sit below
    ceiling + KELD_SIDECAR_RSS_HARD_MARGIN_MB, or an ordinary transient spike becomes a mid-job
    kill. Tightening the reserve must not be able to breach it."""
    m = make(parent_rss_fn=lambda: 1500.0)     # an absurd parent, to force the collision
    m._budget_mb, m._parent_reserve_mb, m._hard_margin = 4096.0, 150.0, 512.0
    m.model_cost_mb = 2400.0                   # ceiling = 3424
    m.observe_parent_rss()
    assert m.hard_limit_mb() == m.ceiling_mb() + 512.0


# --- the composition of reserve and ceiling must be monotone too -----------------------------
#
# parent_reserve_mb() is monotone (high-water). hard_limit_mb() was NOT monotone in it: it
# returned budget-minus-reserve whenever that came out even 1 MB above the ceiling, and the
# ceiling+margin floor only when it did not. Measured against the real class (model_cost 2385,
# ceiling 3409, budget 4096): parent 686 -> hard 3410; parent 688 -> hard 3921. A 2 MB rise in
# the parent RAISED the worker's limit by 511 MB and abandoned the budget without bound. The
# guard relaxed precisely when memory pressure was highest, and spaCy alone (619.6 MB) sits one
# NER transient away from the cliff edge.

def _default_config_manager(parent_mb):
    """A manager at the DELIVERED defaults: budget/reserve/hard-margin come from the env
    defaults in __init__, not from hand-picked test values, so these tests fail if the shipped
    numbers stop satisfying the invariant."""
    m = make(parent_rss_fn=lambda: parent_mb)
    m.model_cost_mb = 2385.0                   # measured GLiNER2-large; ceiling = +1024 = 3409
    m.observe_parent_rss()
    return m


def test_the_hard_limit_is_monotone_in_the_measured_parent():
    """A larger parent may only ever TIGHTEN the worker's limit. The sweep spans the old cliff
    (budget - ceiling = 687 MB) so a reappearing discontinuity is caught, not just a wrong
    endpoint."""
    prev = None
    for parent in range(600, 1501, 1):
        hard = _default_config_manager(float(parent)).hard_limit_mb()
        if prev is not None:
            assert hard <= prev + 1e-9, (
                f"hard limit ROSE from {prev} to {hard} as the parent grew to {parent} MB")
        prev = hard


def test_the_hard_limit_holds_the_invariant_at_the_delivered_defaults():
    """AGENTS.md: the hard limit never sits below ceiling + KELD_SIDECAR_RSS_HARD_MARGIN_MB, or
    an ordinary transient spike becomes a mid-job kill. Pinned at the real default config with
    the real measured parent (spaCy loaded, 619.6 MB) rather than at a parent chosen to be on
    the comfortable side of a branch."""
    m = _default_config_manager(619.6)
    assert m.hard_limit_mb() >= m.ceiling_mb() + m._hard_margin, (
        f"hard {m.hard_limit_mb()} < ceiling {m.ceiling_mb()} + margin {m._hard_margin}")


def test_an_unsatisfiable_budget_is_reported_once_per_worker_generation():
    """parent 619.6 + ceiling 3409 + margin 512 = 4540.6 MB against a 4096 MB budget: the
    configuration genuinely cannot be satisfied. Honouring the invariant means exceeding the
    budget, so say so — naming every term — instead of quietly picking a winner. Once per
    worker generation: poll() runs every second and a per-poll line is a log flood, not a
    warning."""
    said = []
    m = make(parent_rss_fn=lambda: 619.6, warn_fn=said.append)
    m.model_cost_mb = 2385.0
    for _ in range(5):
        m.poll()
    assert len(said) == 1, f"expected one warning, got {len(said)}: {said}"
    msg = said[0]
    for term in ("619.6", "3409", "512", "4540", "4096"):
        assert term in msg, f"warning does not name {term}: {msg}"

    # A new generation re-measures model_cost_mb, so the shortfall is a new fact and is said
    # again. Latching it forever would hide a configuration that changed under a restart.
    m._spawn()
    m.model_cost_mb = 2385.0
    m.poll()
    assert len(said) == 2, f"a fresh worker generation must re-report: {said}"


def test_a_satisfiable_budget_says_nothing():
    said = []
    m = make(parent_rss_fn=lambda: 100.0, warn_fn=said.append)
    m.model_cost_mb = 1000.0                   # ceiling 2024; 2536 + 150 = 2686 < 4096
    for _ in range(5):
        m.poll()
    assert said == [], said


def test_polling_samples_the_parent():
    """The reserve is only honest if something keeps measuring it; poll() is the one loop that
    already runs off the event loop every second."""
    m = make(parent_rss_fn=lambda: 800.0)
    m._budget_mb, m._parent_reserve_mb = 4096.0, 150.0
    m._hard_limit_mb = 9000.0                  # isolate: no kill, just the sampling
    m.poll()
    assert m.parent_reserve_mb() == 800.0


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
