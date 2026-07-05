# keld-agent Load Testing + Memory-Pressure Eviction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Empirically prove the keld-agent GLiNER2 sidecar is a good citizen — no memory leak, no runaway CPU, proportional CPU throttling, and model eviction under critical RAM — via a sidecar-direct load-test harness, backed by a `/metrics` endpoint and a memory-pressure eviction state machine.

**Architecture:** Two small sidecar additions (`/metrics` observability; an eviction state machine that unloads/reloads the model based on absolute RAM headroom) plus a standalone load-test harness under `sidecar/loadtest/`. RAM is treated as static (single-flight + static weights + bounded transient) → memory is handled by evict/reload, never throttling; CPU keeps its existing proportional governor unchanged.

**Tech Stack:** Python 3.12 (sidecar venv at `~/.keld/sidecar-venv`), FastAPI, `psutil` 7.2.2, `httpx` 0.28.1, `ctypes` (glibc `malloc_trim`), `multiprocessing`. No pytest — standalone `test_*.py` scripts.

## Global Constraints

- **No pytest.** Every test file is a standalone script ending with the runner
  `if __name__ == "__main__": fns = [v for k,v in sorted(globals().items()) if k.startswith("test_")]; [ (_f(), print(f"PASS {_f.__name__}")) for _f in fns ]; print(f"\n{len(fns)} passed")`. Match the existing style in `sidecar/app/test_governor.py`.
- **Run tests with the sidecar venv:** `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_X.py` (unit) and `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest smoke` (tiers).
- **Governor is unchanged** — CPU-only pacing. Do not add a memory EWMA to it.
- **Memory levers:** CPU → proportional throttle (existing governor); RAM → eviction only.
- **Reload gate is absolute bytes:** reload only when `available_mb ≥ model_cost_mb + reload_margin_mb`, held continuously for `restore_hold_s`.
- **Dormancy is indefinite** on chronically-constrained hosts (best-effort; telemetry path is separate and unaffected).
- **New env vars (defaults):** `KELD_SIDECAR_EVICT_AVAIL_PCT=5`, `KELD_SIDECAR_RELOAD_MARGIN_MB=1024`, `KELD_SIDECAR_RESTORE_HOLD_S=60`, `KELD_SIDECAR_MEM_POLL_S=2`, `KELD_SIDECAR_IDLE_UNLOAD_S=120`, `KELD_SIDECAR_EVICT_DISABLED=0`.
- **Idle eviction (added post-plan):** LOADED + no request for `KELD_SIDECAR_IDLE_UNLOAD_S` → unload; reloads on-demand when a request resumes activity (records `last_activity`, watcher reloads given headroom, no dwell). Memory eviction keeps the headroom-dwell reload. Implemented in `MemoryWatch` (`EVICT_IDLE` action + `poll(..., last_activity, evicted_at, evict_reason)`) and wired in `main.py` (`_unload_model(reason)`, activity recorded in `_require_loaded`).
- **Injectable seams:** `MemoryWatch` takes `clock` and `sampler` params (mirrors `Governor`) so eviction is tested deterministically with no real RAM pressure.
- **Load-test asserts are relative to a same-run baseline** with generous margins; steady-state windows discard warmup.
- **RAM stressor has a hard available-RAM floor** and aborts rather than risk OOM. The real-eviction test configures the evict threshold just below measured baseline — never drives the true host to 5%.
- Follow the local-merge workflow (branch `keld-agent-loadtest-eviction`, no PRs).

---

### Task 1: MemoryWatch eviction state machine

**Files:**
- Create: `sidecar/app/memwatch.py`
- Test: `sidecar/app/test_memwatch.py`

**Interfaces:**
- Consumes: nothing (pure policy + injected `clock`/`sampler`).
- Produces:
  - States: `LOADED`, `EVICTED`, `RELOADING`, `DORMANT` (module string constants).
  - Actions: `NONE`, `EVICT`, `RELOAD` (module string constants).
  - `class MemoryWatch(evict_pct=None, reload_margin_mb=None, restore_hold_s=None, disabled=None, *, clock=time.monotonic, sampler=_avail_sampler)`
  - `MemoryWatch.has_headroom(avail_mb: float, model_cost_mb: float|None) -> bool`
  - `MemoryWatch.poll(state: str, model_cost_mb: float|None) -> str` (returns an action; updates `last_avail_pct`, `last_avail_mb`, internal hold tracking)
  - `_avail_sampler() -> tuple[float, float]` returns `(avail_pct, avail_mb)`.

- [ ] **Step 1: Write the failing test**

```python
# sidecar/app/test_memwatch.py
"""Standalone tests for the sidecar memory-pressure eviction state machine. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_memwatch.py
"""
from app.memwatch import (
    MemoryWatch, LOADED, EVICTED, RELOADING, DORMANT, NONE, EVICT, RELOAD,
)


def _watch(samples, *, evict_pct=5.0, margin=1024.0, hold=60.0):
    """A MemoryWatch driven by a scripted (avail_pct, avail_mb) sequence and a
    fake clock that advances 1s per poll."""
    t = {"now": 0.0}
    seq = list(samples)

    def clock():
        return t["now"]

    def sampler():
        v = seq.pop(0)
        t["now"] += 1.0
        return v

    return MemoryWatch(evict_pct=evict_pct, reload_margin_mb=margin,
                       restore_hold_s=hold, disabled=False,
                       clock=clock, sampler=sampler)


def test_evict_when_avail_pct_at_or_below_mark():
    w = _watch([(4.0, 500.0)], evict_pct=5.0)
    assert w.poll(LOADED, model_cost_mb=2000.0) == EVICT


def test_no_evict_above_mark():
    w = _watch([(6.0, 500.0)], evict_pct=5.0)
    assert w.poll(LOADED, model_cost_mb=2000.0) == NONE


def test_has_headroom_uses_model_cost_plus_margin():
    w = _watch([(50.0, 3100.0)], margin=1024.0)
    assert w.has_headroom(3100.0, 2000.0) is True   # 3100 >= 2000+1024? no -> False
    # 2000+1024 = 3024; 3100 >= 3024 -> True
    assert w.has_headroom(3000.0, 2000.0) is False  # 3000 < 3024


def test_reload_only_after_hold_duration():
    # headroom present every poll; hold=3s. Needs 3 continuous seconds.
    w = _watch([(50.0, 4000.0)] * 5, hold=3.0, margin=1024.0)
    # model_cost 2000 -> need 3024 mb. 4000 ok.
    a1 = w.poll(EVICTED, 2000.0)  # t=0->1, headroom_since=0, elapsed 0
    a2 = w.poll(EVICTED, 2000.0)  # t=1->2, elapsed 1
    a3 = w.poll(EVICTED, 2000.0)  # t=2->3, elapsed 2
    a4 = w.poll(EVICTED, 2000.0)  # t=3->4, elapsed 3 -> RELOAD
    assert [a1, a2, a3] == [NONE, NONE, NONE]
    assert a4 == RELOAD


def test_hold_resets_when_headroom_lost():
    # headroom, headroom, LOST, headroom... hold=2s -> the loss resets the timer.
    w = _watch([(50.0, 4000.0), (50.0, 4000.0), (50.0, 2000.0),
                (50.0, 4000.0), (50.0, 4000.0), (50.0, 4000.0)],
               hold=2.0, margin=1024.0)
    acts = [w.poll(EVICTED, 2000.0) for _ in range(6)]
    # first two build toward hold but sample 3 (2000mb < 3024) resets; reload only
    # after two more continuous headroom polls following the reset.
    assert RELOAD in acts
    assert acts.index(RELOAD) >= 4  # not reached before the reset + fresh hold


def test_disabled_never_acts():
    t = {"now": 0.0}
    w = MemoryWatch(disabled=True, clock=lambda: t["now"],
                    sampler=lambda: (1.0, 10.0))
    assert w.poll(LOADED, 2000.0) == NONE
    assert w.poll(EVICTED, 2000.0) == NONE


def test_reloading_state_is_noop():
    w = _watch([(50.0, 9000.0)])
    assert w.poll(RELOADING, 2000.0) == NONE


def test_poll_records_last_sample():
    w = _watch([(12.5, 777.0)])
    w.poll(LOADED, 2000.0)
    assert w.last_avail_pct == 12.5
    assert w.last_avail_mb == 777.0


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_memwatch.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'app.memwatch'`.

- [ ] **Step 3: Write minimal implementation**

```python
# sidecar/app/memwatch.py
"""Memory-pressure model-eviction state machine for the keld-agent sidecar.

RAM for this workload is essentially static (the model weights load once; inference
is single-flight with a char-capped, bounded transient), so slowing the request
RATE frees no memory. The only lever that reduces the sidecar's resident footprint
under critical host RAM pressure is to UNLOAD the whole model and reload it once
there is genuine headroom. This class is the pure policy for that decision; the
side effects (actually unloading/reloading, malloc_trim) live in main.py.

Levers:
  • Evict when available RAM % <= evict_pct (a danger signal; default 5%).
  • Reload only when available MB >= model_cost_mb + reload_margin_mb (absolute
    headroom — self-adapting to model size and host), held continuously for
    restore_hold_s (hysteresis + dwell so it never flaps).
On a host where headroom never appears, the model stays evicted/dormant forever —
best-effort by design; the telemetry path is separate and unaffected.
"""
import os
import time

LOADED = "loaded"
EVICTED = "evicted"
RELOADING = "reloading"
DORMANT = "dormant"

NONE = "none"
EVICT = "evict"
RELOAD = "reload"


def _avail_sampler():
    """(available %, available MB) of system RAM. psutil is present in the venv."""
    import psutil
    vm = psutil.virtual_memory()
    return (vm.available / vm.total * 100.0, vm.available / (1024.0 * 1024.0))


class MemoryWatch:
    def __init__(self, evict_pct=None, reload_margin_mb=None, restore_hold_s=None,
                 disabled=None, *, clock=time.monotonic, sampler=_avail_sampler):
        self._evict_pct = (float(os.environ.get("KELD_SIDECAR_EVICT_AVAIL_PCT", "5"))
                           if evict_pct is None else evict_pct)
        self._margin_mb = (float(os.environ.get("KELD_SIDECAR_RELOAD_MARGIN_MB", "1024"))
                           if reload_margin_mb is None else reload_margin_mb)
        self._hold_s = (float(os.environ.get("KELD_SIDECAR_RESTORE_HOLD_S", "60"))
                        if restore_hold_s is None else restore_hold_s)
        self._disabled = (os.environ.get("KELD_SIDECAR_EVICT_DISABLED", "0") == "1"
                          if disabled is None else disabled)
        self._clock = clock
        self._sampler = sampler
        self._headroom_since = None  # clock() when headroom last became continuous
        self.last_avail_pct = None
        self.last_avail_mb = None

    def has_headroom(self, avail_mb, model_cost_mb):
        """True when there is room for the model plus the safety margin. When the
        model cost is not yet known (never loaded), require the margin alone as a
        floor."""
        need = (model_cost_mb or 0.0) + self._margin_mb
        return avail_mb >= need

    def poll(self, state, model_cost_mb):
        """Sample RAM once and return an action (NONE/EVICT/RELOAD) for `state`."""
        if self._disabled:
            return NONE
        pct, mb = self._sampler()
        self.last_avail_pct, self.last_avail_mb = pct, mb
        now = self._clock()

        if self.has_headroom(mb, model_cost_mb):
            if self._headroom_since is None:
                self._headroom_since = now
        else:
            self._headroom_since = None

        if state == LOADED:
            return EVICT if pct <= self._evict_pct else NONE
        if state in (EVICTED, DORMANT):
            if (self._headroom_since is not None
                    and (now - self._headroom_since) >= self._hold_s):
                return RELOAD
            return NONE
        return NONE  # RELOADING: transition in progress
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_memwatch.py`
Expected: PASS — `8 passed`.

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/app/memwatch.py sidecar/app/test_memwatch.py
git commit -m "feat(sidecar): memory-pressure eviction state machine (MemoryWatch)"
```

---

### Task 2: Runner observability (queue depth / capacity / inflight)

**Files:**
- Modify: `sidecar/app/runner.py`
- Test: `sidecar/app/test_runner.py` (append)

**Interfaces:**
- Consumes: existing `InferenceRunner`.
- Produces (properties on `InferenceRunner`): `queue_depth -> int`, `queue_max -> int`, `inflight -> int` (0 or 1 under single-flight).

- [ ] **Step 1: Write the failing test** (append before the `__main__` block)

```python
# sidecar/app/test_runner.py  (append these functions)
def test_queue_capacity_reported():
    from app.governor import Governor
    from app.runner import InferenceRunner
    r = InferenceRunner(Governor(disabled=True), queue_max=7)
    assert r.queue_max == 7
    assert r.queue_depth == 0
    assert r.inflight == 0


def test_inflight_is_one_during_execution():
    import asyncio
    from app.governor import Governor
    from app.runner import InferenceRunner
    seen = {}
    started = asyncio.Event()
    release = asyncio.Event()

    async def run():
        r = InferenceRunner(Governor(disabled=True), queue_max=4)
        r.start()

        def blocker():
            started._loop.call_soon_threadsafe(started.set)
            # busy-wait until released via a threadsafe flag
            import time as _t
            while not release.is_set():
                _t.sleep(0.005)
            return "ok"

        fut = asyncio.ensure_future(r.submit(blocker))
        await started.wait()
        seen["inflight"] = r.inflight
        release.set()
        assert await fut == "ok"
        await r.stop()

    asyncio.run(run())
    assert seen["inflight"] == 1
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_runner.py`
Expected: FAIL — `AttributeError: 'InferenceRunner' object has no attribute 'queue_max'`.

- [ ] **Step 3: Write minimal implementation**

In `sidecar/app/runner.py`, in `InferenceRunner.__init__`, after `self._stopped = False` add:

```python
        self._inflight = 0
```

Add these properties after the existing `ready` property:

```python
    @property
    def queue_depth(self) -> int:
        return self._queue.qsize()

    @property
    def queue_max(self) -> int:
        return self._queue.maxsize

    @property
    def inflight(self) -> int:
        return self._inflight
```

In `_run`, wrap the execution so inflight tracks the single active job — replace:

```python
            try:
                await self._governor.await_slot()
                result = await loop.run_in_executor(self._executor, lambda: fn(*args, **kwargs))
```

with:

```python
            try:
                await self._governor.await_slot()
                self._inflight = 1
                try:
                    result = await loop.run_in_executor(self._executor, lambda: fn(*args, **kwargs))
                finally:
                    self._inflight = 0
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_runner.py`
Expected: PASS (all prior tests + the two new ones).

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/app/runner.py sidecar/app/test_runner.py
git commit -m "feat(sidecar): expose runner queue_depth/queue_max/inflight for metrics"
```

---

### Task 3: Counts + `/metrics` builder

**Files:**
- Create: `sidecar/app/metrics.py`
- Test: `sidecar/app/test_metrics.py`

**Interfaces:**
- Consumes: `Governor` (`.ewma`, `.interval_for`, `._disabled`), `InferenceRunner` (`.queue_depth`, `.queue_max`, `.inflight`), `MemoryWatch` (`.last_avail_pct`, `.last_avail_mb`).
- Produces:
  - `@dataclass class Counts` with int fields `submitted, completed, shed_503, failed, evicted, reloaded` (all default 0).
  - `build_metrics(*, model_state, state_since, governor, runner, watch, counts, model_cost_mb, reload_margin_mb, uptime_s, clock=time.monotonic) -> dict` producing the JSON in spec §4.1.

- [ ] **Step 1: Write the failing test**

```python
# sidecar/app/test_metrics.py
"""Standalone tests for the /metrics payload builder. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_metrics.py
"""
from app.governor import Governor
from app.metrics import Counts, build_metrics


class _FakeRunner:
    queue_depth = 2
    queue_max = 64
    inflight = 1


class _FakeWatch:
    last_avail_pct = 63.1
    last_avail_mb = 20140.0


def test_counts_defaults_zero():
    c = Counts()
    assert (c.submitted, c.completed, c.shed_503, c.failed, c.evicted, c.reloaded) == (0, 0, 0, 0, 0, 0)


def test_build_metrics_shape_and_values():
    g = Governor(high=85.0, low=60.0, max_interval=2.0, disabled=False)
    g.observe(60.0)  # ewma 60 -> interval 0
    counts = Counts(submitted=10, completed=9, shed_503=1)
    m = build_metrics(
        model_state="loaded", state_since=0.0, governor=g, runner=_FakeRunner(),
        watch=_FakeWatch(), counts=counts, model_cost_mb=2680.0,
        reload_margin_mb=1024.0, uptime_s=100.0, clock=lambda: 5.0,
    )
    assert m["model_state"] == "loaded"
    assert m["seconds_in_state"] == 5.0
    assert m["governor"]["cpu_ewma"] == 60.0
    assert m["governor"]["current_interval_ms"] == 0.0
    assert m["governor"]["disabled"] is False
    assert m["memory"]["avail_pct"] == 63.1
    assert m["memory"]["model_cost_mb"] == 2680.0
    assert m["memory"]["reload_headroom_mb"] == 3704.0
    assert m["runner"] == {"queue_depth": 2, "queue_max": 64, "inflight": 1}
    assert m["counts"]["submitted"] == 10 and m["counts"]["shed_503"] == 1
    assert m["uptime_s"] == 100.0


def test_build_metrics_handles_unknown_model_cost():
    g = Governor(disabled=True)
    m = build_metrics(
        model_state="dormant", state_since=0.0, governor=g, runner=_FakeRunner(),
        watch=_FakeWatch(), counts=Counts(), model_cost_mb=None,
        reload_margin_mb=1024.0, uptime_s=1.0, clock=lambda: 1.0,
    )
    assert m["memory"]["model_cost_mb"] is None
    assert m["memory"]["reload_headroom_mb"] is None


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_metrics.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'app.metrics'`.

- [ ] **Step 3: Write minimal implementation**

```python
# sidecar/app/metrics.py
"""The /metrics payload: a read-only snapshot of governor pacing, runner queue
state, memory-watch state, and lifetime counters. Pure builder so it is unit-
testable with fakes (no model, no lifespan)."""
import time
from dataclasses import dataclass


@dataclass
class Counts:
    submitted: int = 0
    completed: int = 0
    shed_503: int = 0
    failed: int = 0
    evicted: int = 0
    reloaded: int = 0


def build_metrics(*, model_state, state_since, governor, runner, watch, counts,
                  model_cost_mb, reload_margin_mb, uptime_s, clock=time.monotonic):
    interval_ms = round(governor.interval_for(governor.ewma) * 1000.0, 1) if governor else 0.0
    headroom = (round(model_cost_mb + reload_margin_mb, 1)
                if model_cost_mb else None)
    return {
        "model_state": model_state,
        "seconds_in_state": round(clock() - state_since, 1),
        "governor": {
            "cpu_ewma": round(governor.ewma, 2) if governor else None,
            "current_interval_ms": interval_ms,
            "disabled": getattr(governor, "_disabled", None) if governor else None,
        },
        "memory": {
            "avail_pct": round(watch.last_avail_pct, 2) if watch and watch.last_avail_pct is not None else None,
            "avail_mb": round(watch.last_avail_mb, 1) if watch and watch.last_avail_mb is not None else None,
            "model_cost_mb": round(model_cost_mb, 1) if model_cost_mb else None,
            "reload_headroom_mb": headroom,
        },
        "runner": {
            "queue_depth": runner.queue_depth if runner else 0,
            "queue_max": runner.queue_max if runner else 0,
            "inflight": runner.inflight if runner else 0,
        },
        "counts": dict(vars(counts)),
        "uptime_s": round(uptime_s, 1),
    }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_metrics.py`
Expected: PASS — `3 passed`.

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/app/metrics.py sidecar/app/test_metrics.py
git commit -m "feat(sidecar): /metrics payload builder + lifetime counters"
```

---

### Task 4: Endpoint 503 guard + counts wiring + `/metrics` route

**Files:**
- Modify: `sidecar/app/main.py`
- Test: `sidecar/app/test_main.py` (append)

**Interfaces:**
- Consumes: `Counts`, `build_metrics` (Task 3); `MemoryWatch` states (Task 1).
- Produces:
  - `_require_loaded() -> model` helper that raises `HTTPException(503, "unavailable — memory pressure")` unless `_state.get("model_state") == LOADED` and a model is present.
  - `GET /metrics` route.
  - `_state` keys: `"model_state"`, `"state_since"`, `"counts"`, `"model_cost_mb"`, `"started_at"`, `"watch"`.

Note: full eviction wiring (the background loop, unload/reload) is Task 5. This task adds the guard, counts, and route so they are unit-testable without torch.

- [ ] **Step 1: Write the failing test** (append before the `__main__` block)

```python
# sidecar/app/test_main.py  (append)
def test_require_loaded_raises_503_when_not_loaded():
    import asyncio
    from fastapi import HTTPException
    m = _reload_main(None)
    m._state.clear()
    m._state["model_state"] = "evicted"
    try:
        m._require_loaded()
        assert False, "expected HTTPException"
    except HTTPException as e:
        assert e.status_code == 503


def test_require_loaded_returns_model_when_loaded():
    m = _reload_main(None)
    m._state.clear()
    m._state["model_state"] = "loaded"
    m._state["model"] = _FakeModel()
    assert m._require_loaded() is m._state["model"]


def test_classify_sheds_503_and_counts_when_queue_full():
    import asyncio
    from app.metrics import Counts
    m = _reload_main(None)
    m._state.clear()
    m._state["model_state"] = "loaded"
    m._state["model"] = _FakeModel()
    m._state["counts"] = Counts()

    class _FullRunner:
        async def submit(self, *a, **k):
            from app.runner import QueueFull
            raise QueueFull()
    m._state["runner"] = _FullRunner()

    from fastapi import HTTPException
    body = m.ClassifyIn(text="hi", tasks={"task_type": ["codegen", "other"]})
    try:
        asyncio.run(m.classify(body))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503
    assert m._state["counts"].shed_503 == 1
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`
Expected: FAIL — `AttributeError: module 'app.main' has no attribute '_require_loaded'`.

- [ ] **Step 3: Write minimal implementation**

In `sidecar/app/main.py`:

Add imports near the top (after existing imports):

```python
from app.memwatch import MemoryWatch, LOADED, EVICTED, RELOADING, DORMANT
from app.metrics import Counts, build_metrics
```

Add the reload-margin constant near the other `_` constants:

```python
_RELOAD_MARGIN_MB = float(os.environ.get("KELD_SIDECAR_RELOAD_MARGIN_MB", "1024"))
```

Add the guard helper after `_state: dict = {}`:

```python
def _require_loaded():
    """Return the live model, or raise 503 when the model is unloaded (memory
    pressure / dormant / mid-reload). Every inference endpoint gates on this."""
    if _state.get("model_state") == LOADED and "model" in _state:
        return _state["model"]
    raise HTTPException(status_code=503, detail="unavailable — memory pressure")
```

Rewrite the three inference endpoints to gate + count. Replace the bodies of
`entities`, `classify`, `extract` so each: calls `_require_loaded()`, increments
`submitted`, and on `QueueFull` increments `shed_503`, on success increments
`completed`, on other exception increments `failed`. Example for `classify`
(apply the same pattern to `entities` and `extract`):

```python
@app.post("/classify")
async def classify(body: ClassifyIn):
    model = _require_loaded()
    text = _clip(body.text)
    counts = _state.get("counts")
    if counts:
        counts.submitted += 1
    try:
        raw = await _state["runner"].submit(model.classify_text, text, body.tasks, include_confidence=True)
    except QueueFull:
        if counts:
            counts.shed_503 += 1
        raise HTTPException(status_code=503, detail="overloaded")
    except Exception:
        if counts:
            counts.failed += 1
        raise
    if counts:
        counts.completed += 1
    return {"results": normalize_classify(raw)}
```

Add the `/metrics` route (after `/health`):

```python
@app.get("/metrics")
def metrics():
    import time
    started = _state.get("started_at", time.monotonic())
    return build_metrics(
        model_state=_state.get("model_state", "dormant"),
        state_since=_state.get("state_since", started),
        governor=_state.get("governor"),
        runner=_state.get("runner"),
        watch=_state.get("watch"),
        counts=_state.get("counts", Counts()),
        model_cost_mb=_state.get("model_cost_mb"),
        reload_margin_mb=_RELOAD_MARGIN_MB,
        uptime_s=time.monotonic() - started,
    )
```

In `lifespan`, initialize the new `_state` keys (before `yield`), after the
existing `runner`/`sampler_task` setup:

```python
    import time as _time
    _state["counts"] = Counts()
    _state["started_at"] = _time.monotonic()
    _state["state_since"] = _time.monotonic()
    _state["model_state"] = LOADED
    _state.setdefault("model_cost_mb", None)
```

Also update `/health` to reflect eviction state — replace its body:

```python
@app.get("/health")
def health():
    runner = _state.get("runner")
    loaded = _state.get("model_state") == LOADED
    ok = loaded and "model" in _state and runner is not None and runner.ready
    return {"ok": ok, "model": MODEL_NAME, "state": _state.get("model_state", "dormant")}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`
Expected: PASS (existing clip/endpoint tests + the three new ones).

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/app/main.py sidecar/app/test_main.py
git commit -m "feat(sidecar): 503 guard + request counts + /metrics route"
```

---

### Task 5: Eviction wiring in lifespan (model_cost, watch loop, unload/reload, malloc_trim)

**Files:**
- Modify: `sidecar/app/main.py`

**Interfaces:**
- Consumes: `MemoryWatch.poll` (Task 1), `_state` keys (Task 4).
- Produces: a background `_mem_watch_loop`; `_unload_model()`, `_reload_model()`, `_malloc_trim()`, `_measure_model_cost_mb()`. No new unit test — the pure policy is covered by Task 1, the guard by Task 4; the real unload/reload effect is validated by the load-test real-eviction scenario (Task 9, K4). This task is integration glue that requires torch.

- [ ] **Step 1: Implement `model_cost` measurement + malloc_trim helpers**

Add near the model helpers in `main.py`:

```python
def _malloc_trim():
    """Return freed heap arenas to the OS (glibc/Linux). Python freeing objects
    alone often does not shrink RSS; without this the eviction would not relieve
    host pressure. Best-effort no-op on non-glibc platforms."""
    try:
        import ctypes
        ctypes.CDLL("libc.so.6").malloc_trim(0)
    except Exception:
        pass


def _rss_mb() -> float:
    import psutil
    return psutil.Process().memory_info().rss / (1024.0 * 1024.0)
```

- [ ] **Step 2: Implement unload / reload**

Add to `main.py`:

```python
import asyncio as _asyncio
import time as _time


async def _unload_model():
    """Move to EVICTED, drain the single in-flight inference, drop the model, and
    return its RSS to the OS. Endpoints already 503 once model_state != LOADED."""
    _set_state(EVICTED)
    runner = _state.get("runner")
    # single-flight: at most one inference in flight; wait briefly for it to end.
    for _ in range(500):  # ~5s cap
        if not runner or runner.inflight == 0:
            break
        await _asyncio.sleep(0.01)
    _state.pop("model", None)
    import gc
    gc.collect()
    _malloc_trim()
    c = _state.get("counts")
    if c:
        c.evicted += 1


async def _reload_model():
    _set_state(RELOADING)
    loop = _asyncio.get_running_loop()
    model = await loop.run_in_executor(None, _load_model)
    _warmup(model)
    _state["model"] = model
    _set_state(LOADED)
    c = _state.get("counts")
    if c:
        c.reloaded += 1


def _set_state(state: str):
    _state["model_state"] = state
    _state["state_since"] = _time.monotonic()
```

- [ ] **Step 3: Implement the watcher loop and wire it into lifespan**

Add:

```python
async def _mem_watch_loop(watch, interval: float):
    from app.memwatch import EVICT, RELOAD
    while True:
        try:
            action = watch.poll(_state.get("model_state"), _state.get("model_cost_mb"))
            if action == EVICT and _state.get("model_state") == LOADED:
                await _unload_model()
            elif action == RELOAD and _state.get("model_state") in (EVICTED, DORMANT):
                await _reload_model()
        except Exception:
            pass
        await _asyncio.sleep(interval)
```

Rewrite `lifespan` so it (a) measures `model_cost_mb` on first load, (b) starts
DORMANT when there is no headroom at startup, (c) runs the watcher:

```python
@asynccontextmanager
async def lifespan(app: FastAPI):
    governor = Governor()
    runner = InferenceRunner(governor, _QUEUE_MAX)
    runner.start()
    watch = MemoryWatch()
    _state["governor"] = governor
    _state["runner"] = runner
    _state["watch"] = watch
    _state["counts"] = Counts()
    _state["started_at"] = _time.monotonic()
    _state["model_cost_mb"] = None
    _set_state(DORMANT)

    # Load only if there is headroom (unknown model_cost at first boot ⇒ margin floor,
    # plus the evict-pct danger check). Otherwise start dormant and let the watcher
    # reload when RAM recovers.
    pct, mb = watch._sampler()
    watch.last_avail_pct, watch.last_avail_mb = pct, mb
    if pct > watch._evict_pct and watch.has_headroom(mb, None):
        before = _rss_mb()
        model = _load_model()
        _state["model_cost_mb"] = max(0.0, _rss_mb() - before)
        _warmup(model)
        _state["model"] = model
        _set_state(LOADED)

    sampler_task = _asyncio.create_task(_sample_loop(governor))
    poll_interval = float(os.environ.get("KELD_SIDECAR_MEM_POLL_S", "2"))
    watch_task = _asyncio.create_task(_mem_watch_loop(watch, poll_interval))
    yield
    for t in (sampler_task, watch_task):
        t.cancel()
        try:
            await t
        except _asyncio.CancelledError:
            pass
    await runner.stop()
    _state.clear()
```

- [ ] **Step 4: Verify the sidecar still boots and serves (real model)**

Run:
```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python serve.py --port 8799 &
sleep 90   # model load
curl -s localhost:8799/health; echo
curl -s localhost:8799/metrics; echo
curl -s -X POST localhost:8799/classify -H 'content-type: application/json' \
  -d '{"text":"write a python function","tasks":{"task_type":["codegen","other"]}}'; echo
kill %1
```
Expected: `/health` → `{"ok": true, ..., "state": "loaded"}`; `/metrics` shows `model_state: loaded`, non-null `model_cost_mb`, `counts.completed >= 1` after the classify; classify returns results.

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/app/main.py
git commit -m "feat(sidecar): wire memory-pressure eviction (unload/reload/malloc_trim/model_cost)"
```

---

### Task 6: Load-test harness — payload corpus + analysis math

**Files:**
- Create: `sidecar/loadtest/__init__.py` (empty)
- Create: `sidecar/loadtest/corpus.py`
- Create: `sidecar/loadtest/analysis.py`
- Test: `sidecar/loadtest/test_corpus.py`, `sidecar/loadtest/test_analysis.py`

**Interfaces:**
- Produces (corpus): `make_request(rng, target_len) -> (path: str, body: dict)` choosing among classify/entities/extract with realistic schemas; `LEN_BUCKETS: list[int]`; `TASKS: dict`; `ENTITY_LABELS: dict`.
- Produces (analysis): `slope(xs, ys) -> float`; `steady(series, warmup_frac=0.2) -> list`; `rss_slope_mb_per_min(samples) -> float` where samples are `(t_seconds, rss_mb)`; `relative_drop(baseline, stressed) -> float`; `nonincreasing(values, tol_frac) -> bool`.

- [ ] **Step 1: Write the failing tests**

```python
# sidecar/loadtest/test_analysis.py
"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_analysis.py"""
from loadtest.analysis import slope, steady, rss_slope_mb_per_min, relative_drop, nonincreasing


def test_slope_of_flat_is_zero():
    assert abs(slope([0, 1, 2, 3], [5, 5, 5, 5])) < 1e-9


def test_slope_positive():
    assert abs(slope([0, 1, 2], [0, 2, 4]) - 2.0) < 1e-9


def test_steady_drops_warmup_fraction():
    assert steady(list(range(10)), warmup_frac=0.2) == list(range(2, 10))


def test_rss_slope_per_minute():
    # +60 mb over 60 s == +60 mb/min
    assert abs(rss_slope_mb_per_min([(0.0, 100.0), (60.0, 160.0)]) - 60.0) < 1e-6


def test_relative_drop():
    assert abs(relative_drop(100.0, 25.0) - 0.75) < 1e-9


def test_nonincreasing_within_tolerance():
    assert nonincreasing([10.0, 9.9, 8.0, 8.05], tol_frac=0.05) is True
    assert nonincreasing([10.0, 20.0], tol_frac=0.05) is False


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

```python
# sidecar/loadtest/test_corpus.py
"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_corpus.py"""
import random
from loadtest.corpus import make_request, LEN_BUCKETS


def test_make_request_returns_known_path_and_body():
    rng = random.Random(1)
    path, body = make_request(rng, target_len=500)
    assert path in ("/classify", "/entities", "/extract")
    assert "text" in body and isinstance(body["text"], str)
    assert len(body["text"]) <= 500 + 200  # roughly bounded to target


def test_make_request_is_deterministic_under_seed():
    a = make_request(random.Random(42), 1000)
    b = make_request(random.Random(42), 1000)
    assert a == b


def test_len_buckets_span_short_to_max():
    assert min(LEN_BUCKETS) <= 300
    assert max(LEN_BUCKETS) >= 20000


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_analysis.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'loadtest.analysis'`.

- [ ] **Step 3: Write implementations**

```python
# sidecar/loadtest/__init__.py
```

```python
# sidecar/loadtest/analysis.py
"""Pure analysis helpers for the load tiers. All assertions are relative to a
same-run baseline, so these are simple, dependency-free statistics."""


def slope(xs, ys):
    """Least-squares slope of ys over xs."""
    n = len(xs)
    if n < 2:
        return 0.0
    mx = sum(xs) / n
    my = sum(ys) / n
    num = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    den = sum((x - mx) ** 2 for x in xs)
    return num / den if den else 0.0


def steady(series, warmup_frac=0.2):
    """Drop the leading warmup fraction so ramp/JIT transients don't skew stats."""
    k = int(len(series) * warmup_frac)
    return series[k:]


def rss_slope_mb_per_min(samples):
    """samples: list of (t_seconds, rss_mb). Returns MB/min."""
    if len(samples) < 2:
        return 0.0
    xs = [t for t, _ in samples]
    ys = [r for _, r in samples]
    return slope(xs, ys) * 60.0


def relative_drop(baseline, stressed):
    """Fraction by which `stressed` fell below `baseline` (0..1)."""
    if baseline <= 0:
        return 0.0
    return (baseline - stressed) / baseline


def nonincreasing(values, tol_frac):
    """True if each value is <= previous within a tolerance (noise margin)."""
    for prev, cur in zip(values, values[1:]):
        if cur > prev * (1.0 + tol_frac):
            return False
    return True
```

```python
# sidecar/loadtest/corpus.py
"""Realistic sidecar request payloads. Schemas mirror what the Go enrich client
sends (internal/agent/enrich/labels.go): classification tasks + entity labels.
Deterministic under a seeded Random; no PII in the corpus."""

TASKS = {
    "task_type": ["codegen", "summarization", "extraction", "translation",
                  "rag_qa", "classification", "reasoning", "agentic_tool_use", "other"],
    "domain": ["software", "legal", "medical", "finance", "science",
               "business", "education", "creative", "general"],
    "sensitivity": ["none", "pii", "secrets", "phi", "pci", "proprietary"],
    "activity_type": ["generate", "transform", "analyze", "retrieve", "converse", "review"],
}

ENTITY_LABELS = {
    "language": "Programming languages such as Python, Rust, TypeScript",
    "framework": "Software frameworks such as Django, React, FastAPI",
    "library": "Software libraries or packages such as numpy, pandas, requests",
    "org": "Organizations, companies, or institutions",
    "product": "Named products, tools, or services",
    "email": "Email addresses",
    "person": "Personal names of individuals",
}

_BASE = [
    "Write a Python function that parses a CSV file and returns rows as dicts.",
    "Refactor this Django view to use the ORM efficiently and add pagination.",
    "Summarize the quarterly revenue report and highlight risks for finance.",
    "Debug why the FastAPI websocket disconnects under load and propose a fix.",
    "Translate this API error message into French for the product UI.",
    "Given these logs, analyze the root cause of the memory spike in the service.",
]

LEN_BUCKETS = [200, 1000, 5000, 15000, 20000]


def _text(rng, target_len):
    parts = []
    total = 0
    while total < target_len:
        s = rng.choice(_BASE)
        parts.append(s)
        total += len(s) + 1
    return (" ".join(parts))[:target_len]


def make_request(rng, target_len):
    """Return (path, body) for a random endpoint at ~target_len characters."""
    text = _text(rng, target_len)
    kind = rng.choice(("classify", "entities", "extract"))
    if kind == "classify":
        return "/classify", {"text": text, "tasks": TASKS}
    if kind == "entities":
        return "/entities", {"text": text, "labels": ENTITY_LABELS}
    return "/extract", {"text": text, "labels": ENTITY_LABELS,
                        "tasks": {"task_type": TASKS["task_type"]}}
```

- [ ] **Step 4: Run tests to verify they pass**

Run both:
```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_analysis.py
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_corpus.py
```
Expected: `6 passed` and `3 passed`.

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/loadtest/__init__.py sidecar/loadtest/corpus.py sidecar/loadtest/analysis.py sidecar/loadtest/test_corpus.py sidecar/loadtest/test_analysis.py
git commit -m "feat(loadtest): payload corpus + analysis math (pure, unit-tested)"
```

---

### Task 7: Load-test harness — sidecar process, sampler, driver, stressor

**Files:**
- Create: `sidecar/loadtest/harness.py`, `sidecar/loadtest/sampler.py`, `sidecar/loadtest/driver.py`, `sidecar/loadtest/stressor.py`

**Interfaces:**
- Produces:
  - `harness.SidecarProcess(env: dict|None=None)` with `.start(timeout=240) -> None` (spawns `serve.py`, waits `/health` ok), `.stop()`, attrs `.base_url: str`, `.pid: int`.
  - `harness.free_port() -> int`.
  - `sampler.Sampler(pid, metrics_url, interval=0.5)` with `.start()`, `.stop() -> list[Sample]`; `Sample` dataclass `(t, rss_mb, cpu_pct, metrics)`.
  - `driver.run_load(base_url, duration_s, concurrency, rng, target_len=None) -> list[Result]`; `Result` dataclass `(t, status, latency_s, path)`; `driver.flood(base_url, n, target_len) -> list[Result]` (fire n concurrent, no pacing — for backpressure).
  - `stressor.CpuStressor(workers)` / `stressor.MemStressor(target_mb, floor_mb)` each with `.start()`, `.stop()`.

- [ ] **Step 1: Implement `harness.py`**

```python
# sidecar/loadtest/harness.py
"""Launch the real sidecar as a subprocess for load tests and wait until healthy."""
import os
import socket
import subprocess
import sys
import time
from pathlib import Path

import httpx

_VENV_PY = os.path.expanduser("~/.keld/sidecar-venv/bin/python")
_SIDECAR_DIR = Path(__file__).resolve().parent.parent  # .../sidecar
_SERVE = _SIDECAR_DIR / "serve.py"


def free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


class SidecarProcess:
    def __init__(self, env=None):
        self.port = free_port()
        self.base_url = f"http://127.0.0.1:{self.port}"
        self._env = {**os.environ, **(env or {})}
        self._proc = None

    @property
    def pid(self) -> int:
        return self._proc.pid

    def start(self, timeout=240):
        self._proc = subprocess.Popen(
            [_VENV_PY, str(_SERVE), "--port", str(self.port)],
            env=self._env, cwd=str(_SIDECAR_DIR),
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if self._proc.poll() is not None:
                raise RuntimeError(f"sidecar exited early ({self._proc.returncode})")
            try:
                r = httpx.get(self.base_url + "/health", timeout=2.0)
                if r.status_code == 200 and r.json().get("ok"):
                    return
            except Exception:
                pass
            time.sleep(0.5)
        self.stop()
        raise TimeoutError("sidecar did not become healthy in time")

    def stop(self):
        if self._proc and self._proc.poll() is None:
            self._proc.terminate()
            try:
                self._proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                self._proc.kill()
```

- [ ] **Step 2: Implement `sampler.py`**

```python
# sidecar/loadtest/sampler.py
"""Poll the sidecar process RSS/CPU and its /metrics into a time series."""
import threading
import time
from dataclasses import dataclass, field

import httpx
import psutil


@dataclass
class Sample:
    t: float
    rss_mb: float
    cpu_pct: float
    metrics: dict = field(default_factory=dict)


class Sampler:
    def __init__(self, pid, metrics_url, interval=0.5):
        self._proc = psutil.Process(pid)
        self._url = metrics_url
        self._interval = interval
        self._rows = []
        self._stop = threading.Event()
        self._thread = None
        self._t0 = None

    def _loop(self):
        self._proc.cpu_percent(None)  # prime
        while not self._stop.is_set():
            try:
                rss = self._proc.memory_info().rss / (1024.0 * 1024.0)
                cpu = self._proc.cpu_percent(None)
            except psutil.NoSuchProcess:
                break
            metrics = {}
            try:
                metrics = httpx.get(self._url, timeout=2.0).json()
            except Exception:
                pass
            self._rows.append(Sample(time.monotonic() - self._t0, rss, cpu, metrics))
            self._stop.wait(self._interval)

    def start(self):
        self._t0 = time.monotonic()
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()

    def stop(self):
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=5)
        return self._rows
```

- [ ] **Step 3: Implement `driver.py`**

```python
# sidecar/loadtest/driver.py
"""Fire sidecar requests concurrently for a duration (or an n-request flood)."""
import concurrent.futures
import random
import time
from dataclasses import dataclass

import httpx

from loadtest.corpus import make_request, LEN_BUCKETS


@dataclass
class Result:
    t: float
    status: int
    latency_s: float
    path: str


def _one(client, base_url, rng, target_len, t0):
    tl = target_len if target_len is not None else rng.choice(LEN_BUCKETS)
    path, body = make_request(rng, tl)
    start = time.monotonic()
    try:
        r = client.post(base_url + path, json=body, timeout=60.0)
        status = r.status_code
    except Exception:
        status = 0
    return Result(time.monotonic() - t0, status, time.monotonic() - start, path)


def run_load(base_url, duration_s, concurrency, rng, target_len=None):
    results = []
    t0 = time.monotonic()
    deadline = t0 + duration_s
    with httpx.Client() as client, \
            concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as ex:
        while time.monotonic() < deadline:
            futs = [ex.submit(_one, client, base_url, random.Random(rng.random()),
                              target_len, t0) for _ in range(concurrency)]
            for f in futs:
                results.append(f.result())
    return results


def flood(base_url, n, target_len, rng=None):
    rng = rng or random.Random(0)
    t0 = time.monotonic()
    with httpx.Client() as client, \
            concurrent.futures.ThreadPoolExecutor(max_workers=n) as ex:
        futs = [ex.submit(_one, client, base_url, random.Random(rng.random()),
                          target_len, t0) for _ in range(n)]
        return [f.result() for f in futs]
```

- [ ] **Step 4: Implement `stressor.py`**

```python
# sidecar/loadtest/stressor.py
"""External host CPU / RAM pressure for governor + eviction tests. Separate
processes so the pressure is genuinely external to the sidecar. The RAM stressor
has a hard available-RAM floor and aborts rather than risk OOMing the host."""
import multiprocessing as mp
import time

import psutil


def _cpu_spin(stop):
    x = 0
    while not stop.is_set():
        x = (x + 1) % 1_000_000


def _mem_hold(target_mb, floor_mb, stop):
    blocks = []
    chunk = 64  # MB per allocation step
    allocated = 0
    while allocated < target_mb and not stop.is_set():
        if psutil.virtual_memory().available / (1024.0 * 1024.0) - chunk < floor_mb:
            break  # safety floor: never cross it
        b = bytearray(chunk * 1024 * 1024)
        b[::4096] = b"\x01" * len(b[::4096])  # touch pages -> resident
        blocks.append(b)
        allocated += chunk
    while not stop.is_set():
        time.sleep(0.1)


class CpuStressor:
    def __init__(self, workers):
        self._workers = workers
        self._stop = mp.Event()
        self._procs = []

    def start(self):
        for _ in range(self._workers):
            p = mp.Process(target=_cpu_spin, args=(self._stop,), daemon=True)
            p.start()
            self._procs.append(p)

    def stop(self):
        self._stop.set()
        for p in self._procs:
            p.join(timeout=5)


class MemStressor:
    def __init__(self, target_mb, floor_mb):
        self._target_mb = target_mb
        self._floor_mb = floor_mb
        self._stop = mp.Event()
        self._proc = None

    def start(self):
        self._proc = mp.Process(target=_mem_hold,
                                args=(self._target_mb, self._floor_mb, self._stop),
                                daemon=True)
        self._proc.start()

    def stop(self):
        self._stop.set()
        if self._proc:
            self._proc.join(timeout=10)
```

- [ ] **Step 5: Smoke-verify the harness wiring (no assertions yet)**

Run:
```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -c "
from loadtest.harness import SidecarProcess
from loadtest.sampler import Sampler
from loadtest.driver import run_load
import random
s = SidecarProcess(); s.start()
sm = Sampler(s.pid, s.base_url + '/metrics', interval=0.5); sm.start()
res = run_load(s.base_url, duration_s=8, concurrency=4, rng=random.Random(0))
rows = sm.stop(); s.stop()
print('requests', len(res), 'ok', sum(1 for r in res if r.status==200), 'samples', len(rows))
assert any(r.status==200 for r in res)
print('HARNESS OK')
"
```
Expected: prints request/ok/sample counts and `HARNESS OK`.

- [ ] **Step 6: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/loadtest/harness.py sidecar/loadtest/sampler.py sidecar/loadtest/driver.py sidecar/loadtest/stressor.py
git commit -m "feat(loadtest): sidecar launcher, sampler, driver, external stressors"
```

---

### Task 8: Smoke tier (S1–S5) + CLI

**Files:**
- Create: `sidecar/loadtest/smoke.py`, `sidecar/loadtest/__main__.py`, `sidecar/loadtest/README.md`

**Interfaces:**
- Consumes: harness, sampler, driver, stressor, analysis (Tasks 6–7).
- Produces: `smoke.run(quick=True) -> int` (0 = all pass, else count of failures; prints per-check PASS/FAIL with the measured numbers). CLI: `python -m loadtest smoke`.

- [ ] **Step 1: Implement `smoke.py`**

```python
# sidecar/loadtest/smoke.py
"""Smoke tier (~2-3 min): gross leak, flat-vs-rate, one CPU-throttle step,
backpressure, and idle no-spin. Assertions are relative to a same-run baseline
with generous margins. Returns the number of failed checks."""
import os
import random

from loadtest.analysis import rss_slope_mb_per_min, steady, relative_drop
from loadtest.driver import run_load, flood
from loadtest.harness import SidecarProcess
from loadtest.sampler import Sampler

PEAK_RSS_CAP_MB = float(os.environ.get("KELD_LOADTEST_PEAK_RSS_MB", "6144"))
LEAK_MB_PER_MIN = float(os.environ.get("KELD_LOADTEST_LEAK_MB_PER_MIN", "5"))


def _report(name, ok, detail):
    print(f"{'PASS' if ok else 'FAIL'} {name}: {detail}")
    return 0 if ok else 1


def run(quick=True):
    fails = 0
    sc = SidecarProcess()
    sc.start()
    try:
        rng = random.Random(7)
        # --- S1/S2: baseline at low rate, then high rate ---
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        run_load(sc.base_url, duration_s=20, concurrency=1, rng=rng)     # low
        rows_low = sm.stop()

        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        res_hi = run_load(sc.base_url, duration_s=30, concurrency=8, rng=rng)  # high
        rows_hi = sm.stop()

        rss = [(r.t, r.rss_mb) for r in steady(rows_hi)]
        slope = rss_slope_mb_per_min(rss)
        peak = max((r.rss_mb for r in rows_hi), default=0.0)
        fails += _report("S1 no-leak", slope < LEAK_MB_PER_MIN, f"slope={slope:.2f} MB/min (<{LEAK_MB_PER_MIN})")
        fails += _report("S1 peak-rss", peak < PEAK_RSS_CAP_MB, f"peak={peak:.0f} MB (<{PEAK_RSS_CAP_MB})")

        rss_low = sum(r.rss_mb for r in steady(rows_low)) / max(1, len(steady(rows_low)))
        rss_hi = sum(r.rss_mb for r in steady(rows_hi)) / max(1, len(steady(rows_hi)))
        drift = abs(rss_hi - rss_low)
        fails += _report("S2 flat-vs-rate", drift < 300, f"low={rss_low:.0f} high={rss_hi:.0f} drift={drift:.0f} MB (<300)")

        # baseline throughput (unstressed)
        base = [r for r in res_hi if r.status == 200]
        base_rate = len(base) / 30.0

        # --- S3: CPU throttle under external stress ---
        from loadtest.stressor import CpuStressor
        cpu = CpuStressor(workers=max(2, (os.cpu_count() or 4)))
        cpu.start()
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        res_st = run_load(sc.base_url, duration_s=30, concurrency=8, rng=rng)
        rows_st = sm.stop()
        cpu.stop()
        st_rate = len([r for r in res_st if r.status == 200]) / 30.0
        ewma_max = max((s.metrics.get("governor", {}).get("cpu_ewma") or 0 for s in rows_st), default=0)
        drop = relative_drop(base_rate, st_rate)
        fails += _report("S3 cpu-throttle", drop > 0.15 and ewma_max >= 60,
                         f"base={base_rate:.1f}/s stressed={st_rate:.1f}/s drop={drop:.0%} ewma_max={ewma_max:.0f}")

        # --- S4: backpressure ---
        res_flood = flood(sc.base_url, n=200, target_len=8000)
        got_503 = sum(1 for r in res_flood if r.status == 503)
        got_200 = sum(1 for r in res_flood if r.status == 200)
        maxq = 0
        m = None
        try:
            import httpx
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            maxq = m["runner"]["queue_depth"]
        except Exception:
            pass
        fails += _report("S4 backpressure", got_503 >= 1 and got_200 >= 1,
                         f"200={got_200} 503={got_503} queue_depth_now={maxq}")

        # --- S5: idle no-spin ---
        import time
        time.sleep(2)
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        time.sleep(5)
        idle = sm.stop()
        idle_cpu = max((s.cpu_pct for s in idle), default=100.0)
        fails += _report("S5 idle-no-spin", idle_cpu < 20, f"idle_cpu_max={idle_cpu:.0f}% (<20)")
    finally:
        sc.stop()
    print(f"\nsmoke: {'ALL PASS' if fails == 0 else str(fails) + ' FAILED'}")
    return fails
```

- [ ] **Step 2: Implement `__main__.py`**

```python
# sidecar/loadtest/__main__.py
"""CLI: python -m loadtest smoke | soak [--minutes N] [--live]"""
import argparse
import sys


def main():
    ap = argparse.ArgumentParser(prog="loadtest")
    sub = ap.add_subparsers(dest="cmd", required=True)
    sub.add_parser("smoke")
    sk = sub.add_parser("soak")
    sk.add_argument("--minutes", type=float, default=30.0)
    sk.add_argument("--live", action="store_true")
    args = ap.parse_args()

    if args.cmd == "smoke":
        from loadtest.smoke import run
        sys.exit(run())
    if args.cmd == "soak":
        from loadtest.soak import run
        sys.exit(run(minutes=args.minutes, live=args.live))


if __name__ == "__main__":
    main()
```

- [ ] **Step 3: Write `README.md`**

```markdown
# keld-agent sidecar load tests

Sidecar-direct load tests proving resource safety (no leak / no runaway CPU) and
governor soundness (CPU throttle + RAM eviction). See the design spec:
`docs/superpowers/specs/2026-07-05-keld-agent-loadtest-and-memory-eviction-design.md`.

## Run

```bash
cd sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest smoke        # ~2-3 min
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest soak --minutes 45 --live
```

Unit tests (fast, no model):
```bash
cd sidecar
for f in app/test_memwatch.py app/test_metrics.py loadtest/test_corpus.py loadtest/test_analysis.py; do
  PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f"; done
```

## Tunable env

`KELD_LOADTEST_PEAK_RSS_MB` (6144), `KELD_LOADTEST_LEAK_MB_PER_MIN` (5),
plus the sidecar knobs `KELD_SIDECAR_EVICT_AVAIL_PCT`, `KELD_SIDECAR_RELOAD_MARGIN_MB`,
`KELD_SIDECAR_RESTORE_HOLD_S`, `KELD_SIDECAR_MEM_POLL_S`.
```

- [ ] **Step 4: Run the smoke tier**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest smoke`
Expected: each check prints PASS with its numbers; final line `smoke: ALL PASS` (exit 0). If a threshold is marginal on this host, tune via the env knobs above and note it — do not loosen a real regression away.

- [ ] **Step 5: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/loadtest/smoke.py sidecar/loadtest/__main__.py sidecar/loadtest/README.md
git commit -m "feat(loadtest): smoke tier (S1-S5) + CLI"
```

---

### Task 9: Soak tier (K1 leak, K2 CPU sweep, K4 real eviction)

**Files:**
- Create: `sidecar/loadtest/soak.py`

**Interfaces:**
- Consumes: harness, sampler, driver, stressor, analysis.
- Produces: `soak.run(minutes=30.0, live=False) -> int` (failed-check count).

- [ ] **Step 1: Implement `soak.py`**

```python
# sidecar/loadtest/soak.py
"""Soak tier (opt-in, long): slow-leak slope (K1), CPU stress sweep (K2), and a
real model unload/reload (K4) with the evict threshold configured just below the
measured baseline available% so the true host is never driven to 5%. Deterministic
eviction transitions (K3) are covered by app/test_memwatch.py."""
import os
import random
import time

import httpx
import psutil

from loadtest.analysis import rss_slope_mb_per_min, steady, relative_drop, nonincreasing
from loadtest.driver import run_load
from loadtest.harness import SidecarProcess
from loadtest.sampler import Sampler


def _report(name, ok, detail):
    print(f"{'PASS' if ok else 'FAIL'} {name}: {detail}")
    return 0 if ok else 1


def _k1_k2(minutes, live):
    fails = 0
    sc = SidecarProcess()
    sc.start()
    try:
        rng = random.Random(11)
        # K1: sustained moderate load; RSS slope over the whole run.
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=1.0)
        sm.start()
        dur = minutes * 60.0
        if live:
            _live_load(sc, dur, rng)
        else:
            run_load(sc.base_url, duration_s=dur, concurrency=4, rng=rng)
        rows = sm.stop()
        rss = [(r.t, r.rss_mb) for r in steady(rows, warmup_frac=0.15)]
        slope = rss_slope_mb_per_min(rss)
        drift = (rss[-1][1] - rss[0][1]) if rss else 0.0
        fails += _report("K1 slow-leak", abs(drift) < 50 and slope < 2.0,
                         f"drift={drift:.1f} MB slope={slope:.3f} MB/min")

        # K2: CPU stress sweep 0 -> high; throughput must be non-increasing.
        from loadtest.stressor import CpuStressor
        cores = os.cpu_count() or 4
        rates = []
        for w in (0, max(1, cores // 2), cores, cores * 2):
            st = CpuStressor(workers=w) if w else None
            if st:
                st.start()
            res = run_load(sc.base_url, duration_s=20, concurrency=8, rng=rng)
            if st:
                st.stop()
            rates.append(len([r for r in res if r.status == 200]) / 20.0)
            print(f"  sweep workers={w} rate={rates[-1]:.1f}/s")
        fails += _report("K2 cpu-sweep-monotonic", nonincreasing(rates, tol_frac=0.20),
                         f"rates={['%.1f' % r for r in rates]}")
        # recovery: last step (no stress again) ~ baseline
        res = run_load(sc.base_url, duration_s=20, concurrency=8, rng=rng)
        recov = len([r for r in res if r.status == 200]) / 20.0
        fails += _report("K2 recovers", recov >= rates[0] * 0.7,
                         f"baseline={rates[0]:.1f}/s recovered={recov:.1f}/s")
    finally:
        sc.stop()
    return fails


def _k4_real_eviction():
    """Configure evict-mark just below current available% and reload gate small,
    apply a bounded memory stressor to cross it, and assert the model actually
    unloads (RSS drops ~model_cost) and reloads on recovery."""
    fails = 0
    vm = psutil.virtual_memory()
    avail_pct = vm.available / vm.total * 100.0
    evict_at = max(1.0, avail_pct - 3.0)  # trip with a small stressor; never 5% of a full box
    env = {
        "KELD_SIDECAR_EVICT_AVAIL_PCT": f"{evict_at:.1f}",
        "KELD_SIDECAR_RESTORE_HOLD_S": "5",     # shortened for the test
        "KELD_SIDECAR_RELOAD_MARGIN_MB": "256",
        "KELD_SIDECAR_MEM_POLL_S": "1",
    }
    sc = SidecarProcess(env=env)
    sc.start()
    try:
        m0 = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
        cost = m0["memory"]["model_cost_mb"] or 0.0
        rss0 = psutil.Process(sc.pid).memory_info().rss / (1024.0 * 1024.0)

        # Cross the evict mark with a bounded stressor (safety floor 1024 MB).
        from loadtest.stressor import MemStressor
        need_mb = int((avail_pct - evict_at + 1.0) / 100.0 * vm.total / (1024.0 * 1024.0))
        ms = MemStressor(target_mb=need_mb, floor_mb=1024)
        ms.start()

        state, rss1 = "loaded", rss0
        for _ in range(30):
            time.sleep(1)
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            state = m["model_state"]
            rss1 = psutil.Process(sc.pid).memory_info().rss / (1024.0 * 1024.0)
            if state in ("evicted", "reloading"):
                break
        fails += _report("K4 evicts", state in ("evicted", "reloading"), f"state={state}")
        # 503 while evicted
        r = httpx.post(sc.base_url + "/classify",
                       json={"text": "hi", "tasks": {"task_type": ["codegen", "other"]}},
                       timeout=5.0)
        fails += _report("K4 503-while-evicted", r.status_code == 503, f"status={r.status_code}")
        dropped = rss0 - rss1
        fails += _report("K4 rss-released", cost == 0.0 or dropped > cost * 0.4,
                         f"rss0={rss0:.0f} rss1={rss1:.0f} dropped={dropped:.0f} cost={cost:.0f}")

        # Release pressure -> reload after hold.
        ms.stop()
        reloaded = False
        for _ in range(40):
            time.sleep(1)
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            if m["model_state"] == "loaded":
                reloaded = True
                break
        fails += _report("K4 reloads", reloaded, f"final_state={m['model_state']}")
    finally:
        sc.stop()
    return fails


def _live_load(sc, dur, rng):
    from loadtest.sampler import Sampler  # separate live line
    t0 = time.monotonic()
    while time.monotonic() - t0 < dur:
        run_load(sc.base_url, duration_s=10, concurrency=4, rng=rng)
        try:
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            rss = psutil.Process(sc.pid).memory_info().rss / (1024.0 * 1024.0)
            print(f"  t={time.monotonic()-t0:6.0f}s rss={rss:7.0f}MB "
                  f"state={m['model_state']} ewma={m['governor']['cpu_ewma']} "
                  f"interval={m['governor']['current_interval_ms']}ms "
                  f"completed={m['counts']['completed']}")
        except Exception:
            pass


def run(minutes=30.0, live=False):
    fails = 0
    fails += _k1_k2(minutes, live)
    fails += _k4_real_eviction()
    print(f"\nsoak: {'ALL PASS' if fails == 0 else str(fails) + ' FAILED'}")
    return fails
```

- [ ] **Step 2: Run a short soak to validate wiring (not the full 30 min)**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest soak --minutes 2 --live`
Expected: K1 prints live RSS lines and a slope; K2 prints the sweep rates and monotonic verdict; K4 evicts, returns 503 while evicted, shows an RSS drop, then reloads. Some checks may be marginal at 2 min — the goal here is that every check runs and reports numbers. Note any host-specific tuning.

- [ ] **Step 3: Commit**

```bash
cd ~/keld/keld-cli
git add sidecar/loadtest/soak.py
git commit -m "feat(loadtest): soak tier (K1 leak, K2 cpu sweep, K4 real eviction)"
```

---

## Self-Review

**Spec coverage:**
- §4.1 `/metrics` → Tasks 3 (builder) + 4 (route). ✓
- §4.2 eviction state machine (evict/reload/hysteresis/hold/model_cost/malloc_trim/503/startup-dormant/disable) → Tasks 1 (policy) + 5 (wiring). ✓
- §4.3 injectable samplers → Task 1 (`clock`/`sampler` params). ✓
- §4.4 governor unchanged → no task modifies it (Global Constraints). ✓
- §5 harness (fixture/driver/sampler/stressor/corpus) → Tasks 6–7. ✓
- §6 scenarios: S1–S5 → Task 8; K1/K2/K4 → Task 9; K3 → Task 1 (`test_memwatch.py`). ✓
- §7 layout/gating (standalone, explicit invocation) → Tasks 6–9. ✓
- §8 risks: RAM floor (Task 7 `MemStressor`), configured-high evict threshold (Task 9 K4). ✓

**Placeholder scan:** No TBD/TODO; every code step has complete code; commands have expected output. ✓

**Type consistency:** `MemoryWatch.poll(state, model_cost_mb)`/`has_headroom` (Tasks 1,5); `Counts` fields + `build_metrics(...)` signature (Tasks 3,4); runner `queue_depth`/`queue_max`/`inflight` (Tasks 2,3,4); `Result`/`Sample` dataclasses + `run_load`/`flood`/`Sampler`/`SidecarProcess` (Tasks 7,8,9); state constants `LOADED/EVICTED/RELOADING/DORMANT` (Tasks 1,4,5). Consistent. ✓
