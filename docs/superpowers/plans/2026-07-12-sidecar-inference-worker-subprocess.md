# Sidecar inference-worker subprocess Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move GLiNER2 inference out of the long-lived FastAPI process into a managed child process that can be killed/recycled, so the service's RSS stays flat, a fragmented worker heap is reclaimed by process exit (every OS), and hung inferences are killable.

**Architecture:** The FastAPI parent keeps the HTTP contract, bounded queue, rate governor, and metrics but holds no model and imports no torch. A parent-side `WorkerManager` owns one inference child process (spawned via `multiprocessing` spawn), dispatches one job at a time over request/response queues with a per-job deadline, samples the worker's RSS, and recycles it on an RSS ceiling / idle / memory pressure / timeout / crash. The child (`worker.py`) loads the model and runs the ops.

**Tech Stack:** Python 3.12 sidecar venv (`~/.keld/sidecar-venv`), FastAPI/uvicorn, `multiprocessing` (spawn), psutil, GLiNER2/torch (worker only). Standalone test scripts (no pytest).

## Global Constraints

- Sidecar tests are **standalone scripts** (no pytest); each ends with a `__main__` runner executing every `test_*` function. Run with `PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_X.py` from `sidecar/`.
- **Privacy invariant:** the sidecar returns raw/normalized spans; it never publishes. Masking is Go-side. Unchanged here.
- **No vocabulary change** → do NOT bump `enrich.SchemaVersion`; no eval re-run.
- **Cross-platform:** no `LD_PRELOAD`/`MALLOC_*`/jemalloc reliance. Use `multiprocessing.get_context("spawn")` (works Linux/macOS/Windows; torch-safe).
- **Single-flight:** at most one inference in flight at any moment (preserved).
- Worker (`app/worker.py`) must have **no heavy module-level imports** — import torch/gliner2 lazily inside functions so `spawn` re-import is cheap and the parent never imports torch.
- Commit trailer on every commit: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Work from `/home/dg/keld/keld-cli`. The daemon spawns the sidecar from source live (dev wrapper), so `systemctl --user restart keld-agent` deploys Python changes.

---

## File Structure

- `sidecar/app/worker.py` (new) — child process: `handle(req, model)` pure op-dispatch (+ adapter normalization) and `serve(req_q, resp_q, model_factory)` loop.
- `sidecar/app/worker_manager.py` (new) — parent: `WorkerManager` (spawn/kill/recycle/`call`/`poll`), injectable `spawn_fn`/`rss_fn`/`ram_fn`/`clock`.
- `sidecar/app/test_worker.py`, `sidecar/app/test_worker_manager.py` (new).
- `sidecar/app/main.py` (modify) — endpoints dispatch to the worker; lifespan wires manager + poll loop; `/health` + `/metrics` from manager; remove in-process model/trim/evict.
- `sidecar/app/metrics.py` + `test_metrics.py` (modify) — worker state/rss/parent-rss/recycle+kill counters; remove `trims`.
- `sidecar/app/memwatch.py` + `test_memwatch.py` (delete) — model-eviction state machine + idle-trim subsumed by `WorkerManager`; its RAM sampler is reimplemented in `worker_manager.py`.
- `README.md`, `sidecar/README.md`, `sidecar/loadtest/README.md` (modify, final task).

---

## Task 1: Worker child process (`worker.py`)

**Files:**
- Create: `sidecar/app/worker.py`
- Create: `sidecar/app/test_worker.py`

**Interfaces:**
- Produces:
  - `def handle(req: dict, model) -> dict` — dispatch `req["op"]` ∈ {`classify`,`entities`,`extract`} to the model, return the final endpoint-shaped dict (normalized).
  - `def serve(req_q, resp_q, model_factory) -> None` — load model, warm, put `{"ready": True}`, then loop reading requests until a `None` sentinel.

- [ ] **Step 1: Write the failing test**

Create `sidecar/app/test_worker.py`:

```python
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'app.worker'`.

- [ ] **Step 3: Implement `worker.py`**

Create `sidecar/app/worker.py`:

```python
"""Inference worker — a child process that owns the GLiNER2 model and runs one
op at a time. The parent (WorkerManager) sends request dicts on req_q and reads
response dicts on resp_q. Isolating inference here means recycling this process
reclaims its heap via process exit — the only cross-platform memory reset.

No heavy module-level imports: torch/gliner2 are imported lazily so the spawn
re-import stays cheap and the parent never pulls in torch."""
from app.adapter import normalize_classify, normalize_entities, normalize_extract


def handle(req: dict, model) -> dict:
    """Run one request against the model and return the final endpoint-shaped,
    normalized dict. Pure w.r.t. the model object, so it is unit-testable with a
    stub."""
    op = req["op"]
    text = req["text"]
    if op == "classify":
        raw = model.classify_text(text, req["tasks"], include_confidence=True)
        return {"results": normalize_classify(raw)}
    if op == "entities":
        raw = model.extract_entities(text, req["labels"])
        return {"entities": normalize_entities(raw, text)}
    if op == "extract":
        schema = model.create_schema().entities(req["labels"])
        for task, options in req["tasks"].items():
            schema = schema.classification(task, options)
        raw = model.extract(text, schema, include_confidence=True)
        return normalize_extract(raw, text, list(req["tasks"].keys()))
    raise ValueError(f"unknown op: {op}")


def _apply_threads(n):
    if n:
        import torch
        torch.set_num_threads(int(n))


def serve(req_q, resp_q, model_factory) -> None:
    """Child entrypoint: load + warm the model, signal ready, then serve one
    request at a time until a None sentinel. Each response is
    {"ok": True, "result": {...}} or {"ok": False, "error": "..."}."""
    model = model_factory()
    try:
        model.classify_text("warm up the model", {"_warmup": ["a", "b"]})
    except Exception:
        pass
    resp_q.put({"ready": True})
    while True:
        req = req_q.get()
        if req is None:
            return
        try:
            _apply_threads(req.get("threads"))
            resp_q.put({"ok": True, "result": handle(req, model)})
        except Exception as e:  # never let one bad request kill the worker
            resp_q.put({"ok": False, "error": repr(e)})
```

- [ ] **Step 4: Run to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker.py`
Expected: PASS — `5 passed`.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/worker.py sidecar/app/test_worker.py
git commit -m "feat(sidecar): inference worker child-process (handle + serve loop)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: WorkerManager — spawn, dispatch, timeout-kill, crash (`worker_manager.py`)

**Files:**
- Create: `sidecar/app/worker_manager.py`
- Create: `sidecar/app/test_worker_manager.py`

**Interfaces:**
- Consumes: `app.worker.serve` (production spawn target).
- Produces:
  - States `DOWN`, `SPAWNING`, `READY`, `HELD`.
  - `class WorkerManager(*, spawn_fn, rss_fn, ram_fn, clock, job_deadline_s, spawn_timeout_s, idle_timeout_s, evict_pct, margin_mb)`
  - `.call(req: dict) -> dict` — blocking single-op dispatch; spawns on demand; raises `WorkerTimeout` (killed) / `WorkerUnavailable` (held) / `WorkerError` (worker-side exception).
  - `.ready() -> bool`, `.state`, `.model_cost_mb`, `.worker_rss_mb()`, `.counts` (dict), `.shutdown()`.
  - `spawn_fn() -> (proc, req_q, resp_q)` seam: `proc` has `.pid`, `.is_alive()`, `.kill()`, `.join(timeout)`. Production `spawn_fn` uses `multiprocessing.get_context("spawn")`.

- [ ] **Step 1: Write the failing test**

Create `sidecar/app/test_worker_manager.py`:

```python
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
    m._test["resp"].items.clear()          # drop the ready sentinel path detail
    m._test["ready"] = True

    # emulate: on spawn, ready is queued; then the result for our call
    def spawn_then_result():
        pass
    m2 = make()
    # After ensure_up consumes {"ready":True}, queue a result for the call:
    orig_put = m2._test  # noqa
    # push result AFTER spawn/ready is consumed by call -> use a responses script
    m2 = make()
    m2._call_hook = lambda req: m2._test["resp"].put({"ok": True, "result": {"echo": req["op"]}})
    out = m2.call({"op": "classify", "text": "hi", "tasks": {}})
    assert out == {"echo": "classify"} and m2.state == READY


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
```

> Implementer note: the `_call_hook` seam is how a test injects "what the worker does when a request is put". Wire `call()` to invoke `self._call_hook(req)` (if set) immediately after putting the request, before reading the response — this stands in for the child processing the request. Keep `_call_hook = None` in production (real worker responds asynchronously).

- [ ] **Step 2: Run to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker_manager.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'app.worker_manager'`.

- [ ] **Step 3: Implement `worker_manager.py` (spawn/call/timeout/crash)**

Create `sidecar/app/worker_manager.py`:

```python
"""Parent-side manager for the single GLiNER2 inference worker child process.

Owns the worker lifecycle so the FastAPI service holds no model and its RSS
stays flat: dispatch is single-flight with a per-job deadline; a hung job or a
grown/idle/pressured worker is killed (freeing its heap via process exit) and
respawned on demand. Dependencies are injected (spawn_fn/rss_fn/ram_fn/clock)
so the policy is unit-testable without a real process or model."""
import os
import queue
import threading

DOWN, SPAWNING, READY, HELD = "down", "spawning", "ready", "held"


class WorkerTimeout(Exception):
    """A job exceeded its deadline; the worker was killed."""


class WorkerUnavailable(Exception):
    """The worker cannot serve (spawning failed, or held under memory pressure)."""


class WorkerError(Exception):
    """The worker returned an error for the request."""


def _default_spawn():
    import multiprocessing as mp
    from app.worker import serve
    from app.main import _model_factory  # lazy: avoids import cycle at module load
    ctx = mp.get_context("spawn")
    req_q, resp_q = ctx.Queue(), ctx.Queue()
    proc = ctx.Process(target=serve, args=(req_q, resp_q, _model_factory), daemon=True)
    proc.start()
    return proc, req_q, resp_q


def _default_rss(pid):
    import psutil
    try:
        return psutil.Process(pid).memory_info().rss / (1024.0 * 1024.0)
    except Exception:
        return 0.0


def _default_ram():
    import psutil
    vm = psutil.virtual_memory()
    return (vm.available / vm.total * 100.0, vm.available / (1024.0 * 1024.0))


class WorkerManager:
    def __init__(self, *, spawn_fn=_default_spawn, rss_fn=_default_rss,
                 ram_fn=_default_ram, clock=None,
                 job_deadline_s=None, spawn_timeout_s=None, idle_timeout_s=None,
                 evict_pct=None, margin_mb=None):
        import time
        self._spawn_fn = spawn_fn
        self._rss_fn = rss_fn
        self._ram_fn = ram_fn
        self._clock = clock or time.monotonic
        self._deadline = float(os.environ.get("KELD_SIDECAR_JOB_DEADLINE_S", "60")) if job_deadline_s is None else job_deadline_s
        self._spawn_timeout = float(os.environ.get("KELD_SIDECAR_SPAWN_TIMEOUT_S", "120")) if spawn_timeout_s is None else spawn_timeout_s
        self._idle_timeout = float(os.environ.get("KELD_SIDECAR_IDLE_UNLOAD_S", "600")) if idle_timeout_s is None else idle_timeout_s
        self._evict_pct = float(os.environ.get("KELD_SIDECAR_EVICT_AVAIL_PCT", "5")) if evict_pct is None else evict_pct
        self._margin = float(os.environ.get("KELD_SIDECAR_RSS_MARGIN_MB", "1024")) if margin_mb is None else margin_mb
        self._lock = threading.RLock()
        self._proc = self._req = self._resp = None
        self.state = DOWN
        self.model_cost_mb = None
        self._last_activity = self._clock()
        self.counts = {"recycles": 0, "kills_timeout": 0, "kills_pressure": 0,
                       "kills_idle": 0, "crashes": 0}
        self._call_hook = None  # test seam

    # ---- lifecycle -------------------------------------------------------
    def _spawn(self):
        self._proc, self._req, self._resp = self._spawn_fn()
        self.state = SPAWNING
        # Wait for the child's {"ready": True}; measure its post-load RSS.
        try:
            msg = self._resp.get(timeout=self._spawn_timeout)
        except queue.Empty:
            self._kill("crashes")
            raise WorkerUnavailable("worker failed to become ready")
        if not (isinstance(msg, dict) and msg.get("ready")):
            self._kill("crashes")
            raise WorkerUnavailable("unexpected worker handshake")
        self.state = READY
        self._last_activity = self._clock()
        if self.model_cost_mb is None:
            self.model_cost_mb = self._rss_fn(self._proc.pid)

    def _kill(self, count_key):
        if self._proc is not None:
            try:
                self._proc.kill(); self._proc.join(timeout=5)
            except Exception:
                pass
        self._proc = self._req = self._resp = None
        self.state = DOWN
        if count_key:
            self.counts[count_key] = self.counts.get(count_key, 0) + 1

    def shutdown(self):
        with self._lock:
            if self._req is not None:
                try:
                    self._req.put(None)
                except Exception:
                    pass
            self._kill(None)

    # ---- dispatch --------------------------------------------------------
    def ready(self):
        return self.state == READY

    def worker_rss_mb(self):
        return self._rss_fn(self._proc.pid) if self._proc else 0.0

    def _ensure_up(self):
        if self.state == HELD:
            raise WorkerUnavailable("held under memory pressure")
        if self.state != READY:
            self._spawn()

    def call(self, req: dict) -> dict:
        with self._lock:
            self._ensure_up()
            self._req.put(req)
            if self._call_hook is not None:   # test seam: emulate the child
                self._call_hook(req)
            try:
                msg = self._resp.get(timeout=self._deadline)
            except queue.Empty:
                crashed = self._proc is not None and not self._proc.is_alive()
                self._kill("crashes" if crashed else "kills_timeout")
                raise WorkerTimeout("inference exceeded deadline or worker died")
            self._last_activity = self._clock()
            if not isinstance(msg, dict) or not msg.get("ok"):
                err = msg.get("error") if isinstance(msg, dict) else "bad response"
                raise WorkerError(err)
            return msg["result"]
```

> Implementer note on the test's `test_call_spawns_and_returns_result`: simplify it to the working shape — construct `m = make()`, set `m._call_hook = lambda req: m._test["resp"].put({"ok": True, "result": {"echo": req["op"]}})`, then assert `m.call({...}) == {"echo": "classify"}`. Delete the exploratory scaffolding lines. Keep the assertion on `state == READY` and one result echo.

- [ ] **Step 4: Run to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker_manager.py`
Expected: PASS — all `test_*` (spawn+return, timeout-kill, worker-error, crash).

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/worker_manager.py sidecar/app/test_worker_manager.py
git commit -m "feat(sidecar): WorkerManager spawn/dispatch/timeout-kill/crash

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: WorkerManager `poll` policy — idle / pressure / RSS-ceiling

**Files:**
- Modify: `sidecar/app/worker_manager.py`
- Modify: `sidecar/app/test_worker_manager.py`

**Interfaces:**
- Consumes: Task-2 `WorkerManager` internals (`state`, `_proc`, `_last_activity`, `_kill`, `_lock`, `model_cost_mb`, `_rss_fn`, `_ram_fn`, `counts`).
- Produces: `.poll() -> None` — called periodically by the parent; applies pressure/idle/ceiling policy. `.ceiling_mb() -> float | None`.

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_worker_manager.py`:

```python
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
```

- [ ] **Step 2: Run to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker_manager.py`
Expected: FAIL — `AttributeError: 'WorkerManager' object has no attribute 'poll'`.

- [ ] **Step 3: Implement `poll`**

Add to `WorkerManager` in `sidecar/app/worker_manager.py`:

```python
    def ceiling_mb(self):
        if self.model_cost_mb is None:
            return None
        return self.model_cost_mb + self._margin

    def poll(self):
        """Periodic lifecycle check (called off the event loop). Pressure wins,
        then idle, then RSS ceiling. Kills set DOWN (lazy respawn on next call);
        pressure sets HELD until headroom returns."""
        avail_pct, avail_mb = self._ram_fn()
        with self._lock:
            if self.state == HELD:
                need = (self.model_cost_mb or 0.0) + self._margin
                if avail_mb >= need:
                    self.state = DOWN     # headroom back; respawn on demand
                return
            if self.state != READY:
                return
            if avail_pct <= self._evict_pct:
                self._kill("kills_pressure"); self.state = HELD
                return
            if (self._clock() - self._last_activity) >= self._idle_timeout:
                self._kill("kills_idle")
                return
            ceiling = self.ceiling_mb()
            if ceiling is not None and self._rss_fn(self._proc.pid) > ceiling:
                self._kill("recycles")    # DOWN; next call respawns a fresh heap
                return
```

> Note: `poll` takes `self._lock`, so it cannot fire mid-`call` (single-flight) — a recycle/idle kill only happens when no job is in flight. Pressure kill also waits for the lock, so it lands between jobs; under genuine ≤5% RAM this is acceptable latency.

- [ ] **Step 4: Run to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_worker_manager.py`
Expected: PASS — all lifecycle tests.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/worker_manager.py sidecar/app/test_worker_manager.py
git commit -m "feat(sidecar): WorkerManager poll policy (idle/pressure/RSS-ceiling)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: Integrate into `main.py`; remove in-process model, idle-trim, memwatch

Wire the endpoints, lifespan, `/health`, and `/metrics` to the WorkerManager; delete the obsolete in-process model lifecycle, the idle `malloc_trim`, and `memwatch`.

**Files:**
- Modify: `sidecar/app/main.py`
- Modify: `sidecar/app/metrics.py`, `sidecar/app/test_metrics.py`
- Delete: `sidecar/app/memwatch.py`, `sidecar/app/test_memwatch.py`

**Interfaces:**
- Consumes: `WorkerManager`, `Governor`, `CpuScaler`, `InferenceRunner`.
- Produces: `def _model_factory()` in `main.py` (used by the production `spawn_fn`); `/metrics` payload with `worker_state`, `worker_rss_mb`, `parent_rss_mb`, `model_cost_mb`, `recycles`, `kills_*`.

- [ ] **Step 1: Write the failing test (metrics shape)**

Edit `sidecar/app/test_metrics.py` — replace the `trims`-based assertion and add worker fields. Replace `test_counts_defaults_zero` body and `test_build_metrics_shape_and_values` expectations:

```python
def test_counts_defaults_zero():
    c = Counts()
    assert (c.submitted, c.completed, c.shed_503, c.failed) == (0, 0, 0, 0)


def test_build_metrics_reports_worker_state():
    g = Governor(disabled=True)
    m = build_metrics(
        worker_state="ready", worker_rss_mb=2743.1, parent_rss_mb=95.0,
        model_cost_mb=2650.1, governor=g, runner=_FakeRunner(), counts=Counts(),
        recycles=2, kills={"timeout": 1, "pressure": 0, "idle": 3, "crash": 0},
        uptime_s=10.0, cpu_threads=2, clock=lambda: 1.0,
    )
    assert m["worker"]["state"] == "ready"
    assert m["worker"]["worker_rss_mb"] == 2743.1
    assert m["worker"]["parent_rss_mb"] == 95.0
    assert m["worker"]["model_cost_mb"] == 2650.1
    assert m["worker"]["recycles"] == 2 and m["worker"]["kills"]["idle"] == 3
```

Delete `test_build_metrics_reports_live_rss`, `test_build_metrics_rss_optional`, `test_build_metrics_handles_unknown_model_cost` (their `memory`/`reload_headroom` fields are gone), and the old `test_build_metrics_shape_and_values` memory assertions. Keep governor/runner/counts assertions in a trimmed `test_build_metrics_shape_and_values` that calls the new signature.

- [ ] **Step 2: Run to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_metrics.py`
Expected: FAIL — `build_metrics()` got unexpected/ missing keyword args (`worker_state` etc.).

- [ ] **Step 3: Rewrite `metrics.py`**

Replace `sidecar/app/metrics.py` with:

```python
"""The /metrics payload: governor pacing, runner queue, worker lifecycle, and
lifetime counters. Pure builder, testable with fakes."""
import time
from dataclasses import dataclass


@dataclass
class Counts:
    submitted: int = 0
    completed: int = 0
    shed_503: int = 0
    failed: int = 0


def build_metrics(*, worker_state, worker_rss_mb, parent_rss_mb, model_cost_mb,
                  governor, runner, counts, recycles, kills, uptime_s,
                  cpu_threads=None, clock=time.monotonic):
    interval_ms = round(governor.interval_for(governor.ewma) * 1000.0, 1) if governor else 0.0
    return {
        "worker": {
            "state": worker_state,
            "worker_rss_mb": round(worker_rss_mb, 1) if worker_rss_mb is not None else None,
            "parent_rss_mb": round(parent_rss_mb, 1) if parent_rss_mb is not None else None,
            "model_cost_mb": round(model_cost_mb, 1) if model_cost_mb else None,
            "recycles": recycles,
            "kills": dict(kills),
        },
        "governor": {
            "cpu_ewma": round(governor.ewma, 2) if governor else None,
            "current_interval_ms": interval_ms,
            "cpu_threads": cpu_threads,
            "disabled": getattr(governor, "_disabled", None) if governor else None,
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

Keep the `_FakeRunner` in `test_metrics.py`; remove `_FakeWatch` if now unused.

- [ ] **Step 4: Rewrite `main.py` to use the WorkerManager**

In `sidecar/app/main.py`:

(a) Replace the model-loading section with a factory used by the spawn seam:

```python
def _model_factory():
    """Load the GLiNER2 model in the worker process. Runs in the child, so torch
    and gliner2 are imported here, never in the parent."""
    from gliner2 import GLiNER2
    kwargs = {}
    if os.environ.get("SIDECAR_QUANTIZE", "0") == "1":
        kwargs["quantize"] = True
    if os.environ.get("SIDECAR_COMPILE", "0") == "1":
        kwargs["compile"] = True
    try:
        return GLiNER2.from_pretrained(MODEL_NAME, **kwargs)
    except TypeError:
        return GLiNER2.from_pretrained(MODEL_NAME)
```

Delete `_load_model`, `_warmup`, `_unload_model`, `_reload_model`, `_malloc_trim`, `_maintenance_trim`, `_mem_watch_loop`, `_apply_threads`, `_set_state`, `_require_loaded`, and the `_state["model"]` usage. Remove the `from app.memwatch import ...` import.

(b) Lifespan wires the manager + governor + runner + a poll loop:

```python
@asynccontextmanager
async def lifespan(app: FastAPI):
    governor = Governor()
    scaler = CpuScaler()
    runner = InferenceRunner(governor, _QUEUE_MAX)
    wm = WorkerManager()
    _state.update(governor=governor, scaler=scaler, runner=runner, wm=wm,
                  counts=Counts(), started_at=time.monotonic())
    runner.start()

    async def _poll_loop(interval):
        loop = asyncio.get_running_loop()
        while True:
            try:
                await loop.run_in_executor(None, wm.poll)  # poll may block on kill/join
            except Exception:
                pass
            await asyncio.sleep(interval)

    poll_task = asyncio.create_task(_poll_loop(float(os.environ.get("KELD_SIDECAR_MEM_POLL_S", "2"))))
    sample_task = asyncio.create_task(_sample_loop(governor))
    yield
    for t in (poll_task, sample_task):
        t.cancel()
        try:
            await t
        except asyncio.CancelledError:
            pass
    await runner.stop()
    await asyncio.get_running_loop().run_in_executor(None, wm.shutdown)
    _state.clear()
```

(c) Each endpoint computes the thread target parent-side and dispatches via the runner to the worker. Example for `/classify` (apply the same shape to `/entities` and `/extract`):

```python
def _threads_for_load():
    scaler = _state["scaler"]; governor = _state["governor"]
    n = scaler.threads_for(governor.ewma)
    _state["cpu_threads"] = n
    return n


@app.post("/classify")
async def classify(body: ClassifyIn):
    wm = _state["wm"]
    if wm.state == "held":
        raise HTTPException(status_code=503, detail="unavailable — memory pressure")
    req = {"op": "classify", "text": _clip(body.text), "tasks": body.tasks,
           "threads": _threads_for_load()}
    _count("submitted")
    try:
        result = await _state["runner"].submit(wm.call, req)
    except QueueFull:
        _count("shed_503"); raise HTTPException(status_code=503, detail="overloaded")
    except WorkerTimeout:
        _count("failed"); raise HTTPException(status_code=503, detail="inference timed out")
    except (WorkerUnavailable,):
        _count("shed_503"); raise HTTPException(status_code=503, detail="worker unavailable")
    except WorkerError:
        _count("failed"); raise HTTPException(status_code=500, detail="inference failed")
    _count("completed")
    return result
```

`/entities` builds `{"op": "entities", "text":..., "labels": body.labels, "threads":...}` and returns `result`; `/extract` builds `{"op": "extract", "text":..., "labels": body.labels, "tasks": body.tasks, "threads":...}` and returns `result`. (The worker already normalized — endpoints return `result` verbatim.)

(d) `/health` and `/metrics`:

```python
@app.get("/health")
def health():
    wm = _state.get("wm")
    return {"ok": bool(wm and wm.ready()), "model": MODEL_NAME,
            "state": wm.state if wm else "down"}


@app.get("/metrics")
def metrics():
    wm = _state["wm"]
    started = _state.get("started_at", time.monotonic())
    return build_metrics(
        worker_state=wm.state, worker_rss_mb=wm.worker_rss_mb(),
        parent_rss_mb=_parent_rss_mb(), model_cost_mb=wm.model_cost_mb,
        governor=_state.get("governor"), runner=_state.get("runner"),
        counts=_state.get("counts", Counts()),
        recycles=wm.counts["recycles"],
        kills={"timeout": wm.counts["kills_timeout"], "pressure": wm.counts["kills_pressure"],
               "idle": wm.counts["kills_idle"], "crash": wm.counts["crashes"]},
        uptime_s=time.monotonic() - started, cpu_threads=_state.get("cpu_threads"),
    )


def _parent_rss_mb():
    try:
        import psutil
        return psutil.Process().memory_info().rss / (1024.0 * 1024.0)
    except Exception:
        return None
```

Add imports at top of `main.py`:
```python
from app.worker_manager import WorkerManager, WorkerTimeout, WorkerUnavailable, WorkerError
```
Remove the now-unused `_RELOAD_MARGIN_MB`, `LOADED/EVICTED/...` imports, and `pre_run=_apply_threads` arg to `InferenceRunner` (thread scaling is parent-computed + passed in the request now). If `InferenceRunner.__init__` requires `pre_run`, pass `pre_run=None`.

(e) Delete the obsolete files:
```bash
git rm sidecar/app/memwatch.py sidecar/app/test_memwatch.py
```

- [ ] **Step 5: Run the full sidecar suite + import check**

Run:
```bash
cd sidecar
for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" | tail -1; done
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -c 'import app.main; print("main import OK")'
```
Expected: every test file prints `N passed`; `main import OK`. Fix any breakage (e.g. `test_main.py` referenced `_require_loaded` — update it to assert `/health` returns `state:"down"` when no worker, or delete those now-obsolete cases).

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/main.py sidecar/app/metrics.py sidecar/app/test_metrics.py
git add -u sidecar/app/
git commit -m "refactor(sidecar): route inference through WorkerManager; drop in-process model + idle-trim + memwatch

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: Live end-to-end verification

Not a code task — verify against the real running service. No commit unless a fix is needed (fix via TDD in the owning task).

- [ ] **Step 1: Deploy**

Run: `systemctl --user restart keld-agent` (sidecar runs source live). Wait ~15s.

- [ ] **Step 2: Service stays flat, worker holds the model**

Run: `~/.local/bin/keld-agent metrics` (repeat a few times).
Expected: `worker.state:"ready"`, `worker.parent_rss_mb` small (tens–low-hundreds MB) and stable, `worker.worker_rss_mb` ≈ model_cost (~2.7 GB).

- [ ] **Step 3: Recycle on ceiling (forced low ceiling)**

Restart with a low margin so a couple of prompts trip the ceiling:
`systemctl --user set-environment KELD_SIDECAR_RSS_MARGIN_MB=1` is not wired through the daemon; instead run a standalone sidecar for this check:
```bash
cd sidecar
KELD_GLINER2_DIR="$HOME/.keld/models/gliner2-large-v1" KELD_SIDECAR_RSS_MARGIN_MB=1 \
  KELD_SIDECAR_MEM_POLL_S=1 PYTHONPATH=. ~/.keld/sidecar-venv/bin/python serve.py --port=39931 &
# health, send a couple /classify, then watch /metrics: worker PID rotates,
# recycles>=1, service (this python) PID and uptime_s keep climbing.
```
Expected: `/metrics` shows `recycles` incrementing and `uptime_s` monotonically increasing across a recycle (service never restarts); `curl /health` stays ok (aside from the brief reload window).

- [ ] **Step 4: Timeout kill**

Temporarily set `KELD_SIDECAR_JOB_DEADLINE_S=1` on a standalone sidecar and send a long (~18KB) `/extract`; expect a 503 and `kills.timeout>=1`, then the next request succeeds (respawned). Kill the standalone sidecar afterward.

---

## Task 6: Update READMEs (main + sidecar + loadtest)

Bring docs in line with the new architecture: inference runs in a recyclable worker subprocess; the service RSS stays flat; memory is bounded by process recycle (cross-platform), not by glibc `malloc_trim`/arena caps.

**Files:**
- Modify: `README.md`
- Modify: `sidecar/README.md`
- Modify: `sidecar/loadtest/README.md`
- Modify: `AGENTS.md` (Resource-safety paragraph)

- [ ] **Step 1: Update the docs**

In each, replace descriptions of in-process model + RAM/idle **eviction** and `malloc_trim` with the worker-subprocess model:
- `README.md` "Invisible good citizen" bullet: the sidecar isolates inference in a child process that is **recycled** (killed + respawned) on an RSS ceiling / idle / memory pressure / hung-job timeout; the long-lived service stays flat; single-flight preserved; CPU still throttled by governor + thread scaling.
- `sidecar/README.md`: architecture — parent service (control plane, no model) + inference worker child (model, ops); `/metrics` fields (`worker.state`, `worker_rss_mb`, `parent_rss_mb`, `recycles`, `kills`).
- `sidecar/loadtest/README.md`: replace the "RAM/idle eviction" + "ThreadPoolExecutor(max_workers=1)" + `malloc_trim` descriptions with worker recycle/kill semantics; update the env-knob list (`KELD_SIDECAR_RSS_MARGIN_MB`, `KELD_SIDECAR_JOB_DEADLINE_S`, `KELD_SIDECAR_SPAWN_TIMEOUT_S`, kept `KELD_SIDECAR_IDLE_UNLOAD_S`/`EVICT_AVAIL_PCT`/`MEM_POLL_S`); note the eviction load-test scenarios (K3, S1 peak-rss) now assert **recycle** behavior and a flat parent RSS.
- `AGENTS.md` Resource-safety paragraph: replace the memory-eviction / arena-cap / idle-trim sentences with the worker-recycle model (keep the arena/thread spawn-env note as a Linux-only baseline reducer, now applied to the worker).

- [ ] **Step 2: Sanity-check references**

Run: `grep -rniE 'malloc_trim|idle-trim|memwatch|in-process model|ThreadPoolExecutor\(max_workers=1\)' README.md sidecar/README.md sidecar/loadtest/README.md AGENTS.md`
Expected: no stale references remain (or each remaining hit is intentional and correct).

- [ ] **Step 3: Commit**

```bash
git add README.md sidecar/README.md sidecar/loadtest/README.md AGENTS.md
git commit -m "docs: describe the inference-worker subprocess memory model (READMEs + AGENTS)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage:** worker child (T1) ✓; WorkerManager dispatch+timeout+crash (T2) ✓; poll policy idle/pressure/ceiling (T3) ✓; main integration + remove in-process model/idle-trim/memwatch + metrics (T4) ✓; live verification incl. flat service + recycle + timeout (T5) ✓; README/AGENTS updates (T6) ✓. Governor kept (T4 lifespan) ✓; CPU thread target computed parent-side and applied in worker (T1 `_apply_threads` + T4 `_threads_for_load`) ✓; arena/thread spawn-env kept as Linux baseline (unchanged Go + noted in T6) ✓.
- **Placeholder scan:** the Task-2 test contains exploratory scaffolding in `test_call_spawns_and_returns_result`; the implementer note directs simplifying it to the `_call_hook` echo shape before relying on it — flagged, not left vague.
- **Type consistency:** `spawn_fn()` returns `(proc, req_q, resp_q)` everywhere; response envelope is `{"ok": bool, "result"|"error"}` and handshake `{"ready": True}` in both `worker.serve` (T1) and `WorkerManager` (T2/T3); `build_metrics` signature in `metrics.py` (T4 step 3) matches its call in `main.py` (T4 step 4) and the test (T4 step 1). `kills` dict keys (`timeout/pressure/idle/crash`) map from `counts` keys (`kills_timeout/kills_pressure/kills_idle/crashes`) consistently in T4 `/metrics`.
- **Scope:** single cohesive sidecar refactor; Go daemon, installers, vocabulary untouched.
