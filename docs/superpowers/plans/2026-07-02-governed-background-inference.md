# Governed Background Inference Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move GLiNER2 inference out of synchronous FastAPI handlers into an internal, host-load-governed background runner that executes one invocation at a time, and delete the now-redundant Go-side governor.

**Architecture:** Option 1 from the spec — the Go daemon stays the enrichment publisher (masking boundary untouched). The sidecar gains two small modules: a `Governor` (EWMA of host CPU → a minimum interval between invocation *starts*) and an `InferenceRunner` (one `asyncio.Queue` + one consumer task + a single-thread executor). Endpoints become `async def`, submit work to the runner, and await the result; the wire contract is unchanged. Overload manifests as governor pacing → bounded-queue backpressure → HTTP 503 → the facet abstains (job publishes `partial`).

**Tech Stack:** Python 3.12 sidecar (FastAPI 0.115, uvicorn, gliner2, psutil 7.2.2 — all already in `~/.keld/sidecar-venv`); Go 1.x daemon.

## Global Constraints

- Masking stays Go-side; the sidecar only ever returns/handles RAW spans — never publishes to Atlas. (spec §2)
- Single-flight: at most ONE model inference runs at any instant. (spec §1, §3.1)
- No new heavyweight deps — in-process asyncio only, no Celery/Redis. (spec §1)
- Keep the existing `_clip` input cap (`KELD_SIDECAR_MAX_CHARS`, default 20000). (spec §6)
- Keep Go `enrich/pipeline.go` Wave1 **serial** (do not re-parallelize). (spec §6)
- Sidecar tests run under `~/.keld/sidecar-venv/bin/python` with `PYTHONPATH=.` from `sidecar/`, standalone (no pytest — mirror existing `sidecar/app/test_main.py`, which ends with a `__main__` runner). (spec §7)
- Env config with safe defaults: `KELD_SIDECAR_QUEUE_MAX`=64, `KELD_GOV_HIGH`=85, `KELD_GOV_LOW`=60, `KELD_GOV_MAX_INTERVAL_MS`=2000, `KELD_GOV_DISABLED`=0. (spec §4)

**Branch setup (do once before Task 1):**
```bash
cd /home/dg/keld/keld-cli
git checkout -b feat/governed-sidecar-inference
```
The working tree already carries the earlier hotfix (serial `pipeline.go`, `_clip`, `test_main.py`); it comes along on the branch. Commit it as the first commit if desired:
```bash
git add internal/agent/enrich/pipeline.go internal/agent/enrich/extractors.go sidecar/app/main.py sidecar/app/test_main.py docs/superpowers/specs/2026-07-02-governed-background-inference-design.md
git commit -m "fix(sidecar): serialize Wave1 + input cap hotfix; add design spec"
```

---

### Task 1: Sidecar Governor

Host-load governor that paces the model-invocation rate. Pure/injectable so it tests without real timers or CPU.

**Files:**
- Create: `sidecar/app/governor.py`
- Test: `sidecar/app/test_governor.py`

**Interfaces:**
- Consumes: nothing (leaf module).
- Produces:
  - `class Governor(high=85.0, low=60.0, max_interval=2.0, disabled=False, clock=time.monotonic, sleep=asyncio.sleep, sampler=<cpu%>)`
  - `Governor.observe(sample: float) -> None` — EWMA update (alpha 0.3).
  - `Governor.sample() -> None` — `observe(self._sampler())`.
  - `Governor.interval_for(load: float) -> float` — 0.0 at/below `low`, `max_interval` at/above `high`, linear between.
  - `async Governor.await_slot() -> None` — sleeps so consecutive starts are ≥ `interval_for(ewma)` apart; no-op if `disabled`.
  - `Governor.ewma` property (float) for assertions/health.

- [ ] **Step 1: Write the failing test**

Create `sidecar/app/test_governor.py`:
```python
"""Standalone tests for the sidecar Governor. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_governor.py
"""
import asyncio

from app.governor import Governor


def test_interval_zero_at_or_below_low():
    g = Governor(high=85.0, low=60.0, max_interval=2.0)
    assert g.interval_for(60.0) == 0.0
    assert g.interval_for(10.0) == 0.0


def test_interval_max_at_or_above_high():
    g = Governor(high=85.0, low=60.0, max_interval=2.0)
    assert g.interval_for(85.0) == 2.0
    assert g.interval_for(99.0) == 2.0


def test_interval_linear_midpoint():
    g = Governor(high=85.0, low=60.0, max_interval=2.0)
    # midpoint load 72.5 -> frac 0.5 -> 1.0s
    assert abs(g.interval_for(72.5) - 1.0) < 1e-9


def test_observe_ewma_first_sample_seeds():
    g = Governor()
    g.observe(50.0)
    assert g.ewma == 50.0
    g.observe(100.0)  # 0.3*100 + 0.7*50 = 65
    assert abs(g.ewma - 65.0) < 1e-9


def test_await_slot_paces_by_interval():
    # Fake clock + fake sleep: assert the second start waits ~max_interval.
    t = {"now": 0.0}
    waited = []

    async def fake_sleep(secs):
        waited.append(secs)
        t["now"] += secs

    g = Governor(high=85.0, low=60.0, max_interval=2.0,
                 clock=lambda: t["now"], sleep=fake_sleep)
    g.observe(99.0)  # force max interval

    async def run():
        await g.await_slot()      # first: last_start=0, no prior wait
        await g.await_slot()      # second: must wait ~2.0s
    asyncio.run(run())
    assert waited and abs(waited[-1] - 2.0) < 1e-9


def test_await_slot_noop_when_disabled():
    waited = []

    async def fake_sleep(secs):
        waited.append(secs)

    g = Governor(disabled=True, sleep=fake_sleep)
    g.observe(99.0)
    asyncio.run(g.await_slot())
    asyncio.run(g.await_slot())
    assert waited == []


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_governor.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'app.governor'`.

- [ ] **Step 3: Write minimal implementation**

Create `sidecar/app/governor.py`:
```python
"""Host-load governor for the sidecar's model-invocation rate.

Ports the EWMA / high-low-mark semantics of the (removed) Go governor to where
inference actually happens. InferenceRunner calls await_slot() before each model
invocation; under high host CPU the governor imposes a growing minimum interval
between invocation starts, so a shared machine keeps headroom for other work.
Overload shedding is NOT done here — it emerges from bounded-queue backpressure
(see runner.py): pacing slows the consumer, the queue fills, submit() rejects.
"""
import asyncio
import os
import time

_ALPHA = 0.3


def _default_sampler() -> float:
    try:
        import psutil
        return psutil.cpu_percent(interval=None)
    except Exception:  # pragma: no cover - psutil present in the sidecar venv
        return min(100.0, (os.getloadavg()[0] / (os.cpu_count() or 1)) * 100.0)


class Governor:
    def __init__(self, high=None, low=None, max_interval=None, disabled=None,
                 clock=time.monotonic, sleep=asyncio.sleep, sampler=_default_sampler):
        self._high = float(os.environ.get("KELD_GOV_HIGH", "85")) if high is None else high
        self._low = float(os.environ.get("KELD_GOV_LOW", "60")) if low is None else low
        if max_interval is None:
            max_interval = float(os.environ.get("KELD_GOV_MAX_INTERVAL_MS", "2000")) / 1000.0
        self._max_interval = max_interval
        if disabled is None:
            disabled = os.environ.get("KELD_GOV_DISABLED", "0") == "1"
        self._disabled = disabled
        self._clock, self._sleep, self._sampler = clock, sleep, sampler
        self._ewma = 0.0
        self._seen = False
        self._last_start = 0.0

    @property
    def ewma(self) -> float:
        return self._ewma

    def observe(self, sample: float) -> None:
        if not self._seen:
            self._ewma, self._seen = sample, True
        else:
            self._ewma = _ALPHA * sample + (1 - _ALPHA) * self._ewma

    def sample(self) -> None:
        self.observe(self._sampler())

    def interval_for(self, load: float) -> float:
        if load <= self._low:
            return 0.0
        if load >= self._high:
            return self._max_interval
        frac = (load - self._low) / (self._high - self._low)
        return frac * self._max_interval

    async def await_slot(self) -> None:
        if self._disabled:
            return
        wait = self._last_start + self.interval_for(self._ewma) - self._clock()
        if wait > 0:
            await self._sleep(wait)
        self._last_start = self._clock()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_governor.py`
Expected: `6 passed`.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/governor.py sidecar/app/test_governor.py
git commit -m "feat(sidecar): add host-load Governor for invocation pacing"
```

---

### Task 2: Sidecar InferenceRunner

The in-process background-jobs mechanism: one queue, one consumer, single-thread executor. Single-flight execution replaces the hotfix `_infer_lock`.

**Files:**
- Create: `sidecar/app/runner.py`
- Test: `sidecar/app/test_runner.py`

**Interfaces:**
- Consumes: `Governor` (Task 1) — calls `await governor.await_slot()` before each invocation.
- Produces:
  - `class QueueFull(Exception)`
  - `class InferenceRunner(governor, queue_max: int)`
  - `InferenceRunner.start() -> None` — create the consumer task (call inside a running loop).
  - `async InferenceRunner.submit(fn, *args) -> Any` — enqueue `fn(*args)`; await its result; raise `QueueFull` immediately if the queue is at capacity.
  - `async InferenceRunner.stop() -> None` — cancel consumer, shut down executor.
  - `InferenceRunner.ready` property (bool) — True once `start()` has created a live consumer.

- [ ] **Step 1: Write the failing test**

Create `sidecar/app/test_runner.py`:
```python
"""Standalone tests for InferenceRunner. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_runner.py
"""
import asyncio
import threading

from app.runner import InferenceRunner, QueueFull


class _NoWaitGov:
    async def await_slot(self):
        return


def test_submit_runs_and_returns_result():
    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=8)
        r.start()
        try:
            out = await r.submit(lambda x: x * 2, 21)
            assert out == 42
        finally:
            await r.stop()
    asyncio.run(run())


def test_single_flight_never_overlaps():
    active = {"n": 0}
    overlaps = []
    lock = threading.Lock()

    def work(_):
        with lock:
            active["n"] += 1
            if active["n"] > 1:
                overlaps.append(True)
        # busy a moment so an overlap would be observable
        s = 0
        for i in range(200000):
            s += i
        with lock:
            active["n"] -= 1
        return s

    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=32)
        r.start()
        try:
            await asyncio.gather(*[r.submit(work, i) for i in range(10)])
        finally:
            await r.stop()
    asyncio.run(run())
    assert not overlaps, "runner permitted concurrent inference"


def test_queue_full_rejects():
    started = asyncio.Event()
    release = threading.Event()

    def block(_):
        release.wait(2.0)
        return 1

    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=1)
        r.start()
        try:
            # First submit occupies the consumer; give it a beat to dequeue.
            first = asyncio.create_task(r.submit(block, 0))
            await asyncio.sleep(0.05)
            # Queue capacity is 1: fill it, then the next submit must reject.
            second = asyncio.create_task(r.submit(block, 1))
            await asyncio.sleep(0.05)
            rejected = False
            try:
                await r.submit(lambda _: 2, 2)
            except QueueFull:
                rejected = True
            assert rejected, "expected QueueFull at capacity"
            release.set()
            await asyncio.gather(first, second)
        finally:
            release.set()
            await r.stop()
    asyncio.run(run())


def test_exception_propagates():
    async def run():
        r = InferenceRunner(_NoWaitGov(), queue_max=4)
        r.start()
        try:
            raised = False
            try:
                await r.submit(lambda: (_ for _ in ()).throw(ValueError("boom")))
            except ValueError:
                raised = True
            assert raised
        finally:
            await r.stop()
    asyncio.run(run())


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_runner.py`
Expected: FAIL — `ModuleNotFoundError: No module named 'app.runner'`.

- [ ] **Step 3: Write minimal implementation**

Create `sidecar/app/runner.py`:
```python
"""In-process background inference runner: a single consumer executes model
invocations one at a time (single-flight), paced by the Governor. This is the
lightweight 'background jobs' mechanism (asyncio.Queue + one worker task + a
single-thread executor) that replaces running inference inline in request
handlers. Single-flight replaces the hotfix _infer_lock and bounds resident
memory to one inference. A bounded queue provides backpressure: when full,
submit() rejects with QueueFull so the endpoint can return 503.
"""
import asyncio
from concurrent.futures import ThreadPoolExecutor


class QueueFull(Exception):
    """Raised by submit() when the runner's queue is at capacity (backpressure)."""


class InferenceRunner:
    def __init__(self, governor, queue_max: int):
        self._governor = governor
        self._queue = asyncio.Queue(maxsize=queue_max)
        self._executor = ThreadPoolExecutor(max_workers=1)
        self._consumer = None

    @property
    def ready(self) -> bool:
        return self._consumer is not None and not self._consumer.done()

    def start(self) -> None:
        if self._consumer is None:
            self._consumer = asyncio.create_task(self._run())

    async def submit(self, fn, *args):
        loop = asyncio.get_running_loop()
        future = loop.create_future()
        try:
            self._queue.put_nowait((fn, args, future))
        except asyncio.QueueFull:
            raise QueueFull()
        return await future

    async def _run(self) -> None:
        loop = asyncio.get_running_loop()
        while True:
            fn, args, future = await self._queue.get()
            try:
                await self._governor.await_slot()
                result = await loop.run_in_executor(self._executor, lambda: fn(*args))
                if not future.cancelled():
                    future.set_result(result)
            except Exception as e:  # noqa: BLE001 - propagate to the awaiting caller
                if not future.cancelled():
                    future.set_exception(e)
            finally:
                self._queue.task_done()

    async def stop(self) -> None:
        if self._consumer is not None:
            self._consumer.cancel()
            try:
                await self._consumer
            except asyncio.CancelledError:
                pass
            self._consumer = None
        self._executor.shutdown(wait=False)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_runner.py`
Expected: `4 passed`.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/runner.py sidecar/app/test_runner.py
git commit -m "feat(sidecar): add single-flight InferenceRunner (background jobs)"
```

---

### Task 3: Wire runner + governor into FastAPI; async endpoints; drop `_infer_lock`

**Files:**
- Modify: `sidecar/app/main.py`
- Test: `sidecar/app/test_main.py` (extend)

**Interfaces:**
- Consumes: `Governor` (Task 1), `InferenceRunner` + `QueueFull` (Task 2).
- Produces: async endpoints `/entities`, `/classify`, `/extract` that `await _state["runner"].submit(...)`; `_state` holds `"model"`, `"governor"`, `"runner"`; `/health` ok only when model AND runner are ready.

Current `main.py` (post-hotfix) has `_infer_lock` and `_clip`. This task removes `_infer_lock`, keeps `_clip`, and routes every model call through the runner.

- [ ] **Step 1: Write the failing test** (append to `sidecar/app/test_main.py`, before the `__main__` block)

```python
import asyncio as _asyncio

from app.governor import Governor
from app.runner import InferenceRunner, QueueFull
from fastapi import HTTPException


class _FakeModel:
    def classify_text(self, text, tasks):
        return {t: opts[0] for t, opts in tasks.items()}  # top label = first option

    def extract_entities(self, text, labels):
        return {"entities": {}}

    def create_schema(self):
        return self

    def entities(self, labels):
        return self

    def classification(self, task, options):
        return self

    def extract(self, text, schema):
        return {"entities": {}}


def _wire(main, queue_max=8):
    gov = Governor(disabled=True)
    runner = InferenceRunner(gov, queue_max=queue_max)
    main._state.clear()
    main._state["model"] = _FakeModel()
    main._state["governor"] = gov
    main._state["runner"] = runner
    return runner


def test_classify_endpoint_routes_through_runner():
    m = _reload_main(None)
    runner = _wire(m)

    async def run():
        runner.start()
        try:
            out = await m.classify(m.ClassifyIn(text="hello", tasks={"task_type": ["a", "b"]}))
            assert out["results"]["task_type"][0]["label"] == "a"
        finally:
            await runner.stop()
    _asyncio.run(run())


def test_extract_endpoint_queue_full_returns_503():
    m = _reload_main(None)
    runner = _wire(m, queue_max=1)

    async def run():
        runner.start()
        try:
            import threading
            release = threading.Event()

            def block(_):
                release.wait(2.0)
                return {"entities": {}}

            # Occupy consumer + fill the single queue slot.
            t1 = _asyncio.create_task(runner.submit(block, 0))
            await _asyncio.sleep(0.05)
            t2 = _asyncio.create_task(runner.submit(block, 1))
            await _asyncio.sleep(0.05)
            status = None
            try:
                await m.extract(m.ExtractIn(text="hi", labels={}, tasks={}))
            except HTTPException as e:
                status = e.status_code
            assert status == 503
            release.set()
            await _asyncio.gather(t1, t2)
        finally:
            release.set()
            await runner.stop()
    _asyncio.run(run())
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`
Expected: FAIL — `classify` is not async / `_state` has no `"runner"` / `TypeError` awaiting a dict (endpoints not yet async through the runner).

- [ ] **Step 3: Write minimal implementation** — edit `sidecar/app/main.py`

Replace the imports block:
```python
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from app.adapter import normalize_classify, normalize_entities, normalize_extract
from app.governor import Governor
from app.runner import InferenceRunner, QueueFull
```

Delete the `_infer_lock = threading.Lock()` block and its comment, and the now-unused `import threading`. Keep `_MAX_CHARS` and `_clip`. Add after `_clip`:
```python
_QUEUE_MAX = int(os.environ.get("KELD_SIDECAR_QUEUE_MAX", "64"))
```

Replace the lifespan with one that also builds the governor + runner and a sampling loop:
```python
import asyncio


async def _sample_loop(governor: Governor, interval: float = 5.0) -> None:
    while True:
        governor.sample()
        await asyncio.sleep(interval)


@asynccontextmanager
async def lifespan(app: FastAPI):
    model = _load_model()
    _warmup(model)
    governor = Governor()
    runner = InferenceRunner(governor, _QUEUE_MAX)
    runner.start()
    sampler_task = asyncio.create_task(_sample_loop(governor))
    _state["model"] = model
    _state["governor"] = governor
    _state["runner"] = runner  # set last → /health ok only once fully wired
    yield
    sampler_task.cancel()
    try:
        await sampler_task
    except asyncio.CancelledError:
        pass
    await runner.stop()
    _state.clear()
```

Update `/health` and make the three endpoints async, routing through the runner:
```python
@app.get("/health")
def health():
    runner = _state.get("runner")
    ok = "model" in _state and runner is not None and runner.ready
    return {"ok": ok, "model": MODEL_NAME}


@app.post("/entities")
async def entities(body: EntitiesIn):
    text = _clip(body.text)
    model = _state["model"]
    try:
        raw = await _state["runner"].submit(model.extract_entities, text, body.labels)
    except QueueFull:
        raise HTTPException(status_code=503, detail="overloaded")
    return {"entities": normalize_entities(raw, text)}


@app.post("/classify")
async def classify(body: ClassifyIn):
    text = _clip(body.text)
    model = _state["model"]
    try:
        raw = await _state["runner"].submit(model.classify_text, text, body.tasks)
    except QueueFull:
        raise HTTPException(status_code=503, detail="overloaded")
    return {"results": normalize_classify(raw)}


@app.post("/extract")
async def extract(body: ExtractIn):
    text = _clip(body.text)
    model = _state["model"]

    def _run():
        schema = model.create_schema().entities(body.labels)
        for task, options in body.tasks.items():
            schema = schema.classification(task, options)
        return model.extract(text, schema)

    try:
        raw = await _state["runner"].submit(_run)
    except QueueFull:
        raise HTTPException(status_code=503, detail="overloaded")
    return normalize_extract(raw, text, list(body.tasks.keys()))
```

Move the `import asyncio` to the top with the other imports (shown inline above for locality; place it in the import block).

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`
Expected: all `_clip` tests plus `test_classify_endpoint_routes_through_runner` and `test_extract_endpoint_queue_full_returns_503` pass.

- [ ] **Step 5: Smoke-test the real app boots** (loads model; heavier)

Run:
```bash
cd /home/dg/keld/keld-cli/sidecar && \
KELD_GLINER2_DIR="$HOME/.keld/models/gliner2-large-v1" \
~/.keld/sidecar-venv/bin/python -c "
import asyncio, app.main as m
async def go():
    async with m.lifespan(m.app):
        print('health:', m.health())
asyncio.run(go())
"
```
Expected: `health: {'ok': True, 'model': ...}` (or, if the model dir is absent, an expected load error — in which case rely on Step 4 and note it). Do NOT leave a server running.

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/main.py sidecar/app/test_main.py
git commit -m "feat(sidecar): async endpoints via governed InferenceRunner; drop _infer_lock"
```

---

### Task 4: Remove the Go-side governor

Independent of Tasks 1–3 (Go only). Deletes `internal/agent/govern` and unwires it from the daemon; shedding now lives solely in the sidecar.

**Files:**
- Delete: `internal/agent/govern/govern.go`, `internal/agent/govern/govern_test.go`, `internal/agent/govern/sampler.go`, `internal/agent/govern/sampler_test.go`
- Modify: `internal/agent/daemon/daemon.go`
- Modify: `internal/agent/daemon/daemon_test.go`

**Interfaces:**
- Produces: `Worker(q, m, pub, actor, includeEntityText, ready)` (no `admit`); `mlBackend(ctx, set) (enrich.Model, func() bool)` and `mlBackendWithOpts(ctx, opts) (enrich.Model, func() bool)` (no third return).

- [ ] **Step 1: Update the tests first (they encode the new signatures)** — edit `internal/agent/daemon/daemon_test.go`

  - Delete the entire `TestWorkerAdmitFalseSheds` function (around line 160–194) and `TestWorkerAdmitTruePublishes` (around line 196–219), plus any now-unused `alwaysShed`/`alwaysAdmit` locals.
  - In every remaining `Worker(...)` call, drop the trailing admit argument. E.g.:
    - `go Worker(q, enrich.NewDeterministic(), fs, "dg@keld.co", func() bool { return false }, func() bool { return true }, nil)` → remove the final `, nil`.
    - Same for the calls at ~line 93, 131, 293.
  - Change the two `mlBackendWithOpts` destructurings:
    - `router, gate, admit := mlBackendWithOpts(ctx, mlBackendOpts{...})` → `router, gate := mlBackendWithOpts(ctx, mlBackendOpts{...})`
    - and the following `Worker(..., gate, admit)` → `Worker(..., gate)` (lines ~391 and ~448).

- [ ] **Step 2: Run tests to verify they fail to compile**

Run: `go test ./internal/agent/daemon/... 2>&1 | head`
Expected: build errors — `Worker` still declares `admit`, `mlBackendWithOpts` still returns 3 values.

- [ ] **Step 3: Edit `internal/agent/daemon/daemon.go`**

  - Remove the import line `"github.com/ncx-ai/keld-cli/internal/agent/govern"`.
  - `Worker`: change the signature to
    ```go
    func Worker(q *queue.Queue, m enrich.Model, pub Sender, actor string, includeEntityText func() bool, ready func() bool) {
    ```
    and delete the admit block:
    ```go
    		if admit != nil && !admit() {
    			// Governor overload shedding: ...
    			continue
    		}
    ```
    Also trim the `admit is an optional load-shedding gate...` paragraph from the doc comment.
  - `mlBackend`: change signature to `(enrich.Model, func() bool)`; update `deterministic` closure to `return enrich.NewDeterministic(), func() bool { return true }` (two values) and its two `return deterministic()` sites are unaffected. Return `mlBackendWithOpts(...)` (now two values).
  - `mlBackendWithOpts`: change signature to `(enrich.Model, func() bool)`. Delete the governor block:
    ```go
    	g := govern.New(govern.CPUSampler{}, 1)
    	go func() { ...ticker... g.Sample() ... }()
    ```
    Delete the `admit := func() bool {...}` closure. Change the final `return router, gate, admit` → `return router, gate`.
  - `Run`: change `model, gate, admit := mlBackend(ctx, set)` → `model, gate := mlBackend(ctx, set)` and `go Worker(q, model, pub, actor, live.IncludeEntityText, gate, admit)` → `go Worker(q, model, pub, actor, live.IncludeEntityText, gate)`.

- [ ] **Step 4: Delete the govern package**

```bash
git rm internal/agent/govern/govern.go internal/agent/govern/govern_test.go internal/agent/govern/sampler.go internal/agent/govern/sampler_test.go
```

- [ ] **Step 5: Build, vet, test**

Run:
```bash
go build ./... && go vet ./internal/agent/daemon/... && go test ./internal/agent/daemon/... ./internal/agent/enrich/...
```
Expected: build OK; `ok` for daemon and enrich. (The pre-existing env-dependent `TestSidecarBinPathEnv*` failures documented in the debugging pass may still fail on this machine — unrelated to this task; confirm the set of failures is unchanged.)

- [ ] **Step 6: Commit**

```bash
git add -A internal/agent/daemon internal/agent/govern
git commit -m "refactor(agent): remove Go governor; shedding now lives in the sidecar"
```

---

## Self-Review

**Spec coverage:**
- §1 single-flight, not-inline → Task 2 (runner) + Task 3 (async endpoints). ✓
- §2 masking stays Go-side → enforced by NOT moving publish/mask (no task touches them). ✓
- §3.1 InferenceRunner → Task 2. ✓
- §3.2 Governor (EWMA, interval, await_slot, sampling) → Task 1 + sampling loop in Task 3. ✓
- §3.3 async endpoints, 503 on queue-full, `_clip`, health → Task 3. ✓
- §4 env config → Task 1 (gov envs) + Task 3 (`KELD_SIDECAR_QUEUE_MAX`). ✓
- §5 overload → pacing→queue-full→503→partial → Task 1/2/3 behavior; verified by `test_extract_endpoint_queue_full_returns_503`. ✓
- §6 remove Go governor; keep `_clip`; keep Go serial pipeline → Task 4 (removal); `_clip` retained in Task 3; pipeline untouched. ✓
- §7 tests standalone under sidecar venv → all Python tasks. ✓

**Placeholder scan:** none — every code/ test step shows full content.

**Type consistency:** `Governor.await_slot`/`interval_for`/`observe`/`sample`/`ewma` used identically in Tasks 1→2→3. `InferenceRunner(governor, queue_max)`, `.start()`, `.submit(fn,*args)`, `.stop()`, `.ready`, and `QueueFull` used identically in Tasks 2→3. Go `Worker`/`mlBackend`/`mlBackendWithOpts` new signatures consistent across Task 4 steps. ✓
