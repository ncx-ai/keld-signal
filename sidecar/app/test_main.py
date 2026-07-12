"""Tests for the sidecar's load-protection guards (input clipping) and the
async endpoints that route inference through the governed InferenceRunner into
the worker (WorkerManager.call). These import app.main (light — fastapi/pydantic
only; the GLiNER2 model lives in a worker child, never at import) so they run
without torch. Runnable under pytest OR standalone: `python app/test_main.py`.
"""
import importlib
import os


def _reload_main(max_chars: str | None):
    """Reload app.main with KELD_SIDECAR_MAX_CHARS set, so module-level _MAX_CHARS
    picks up the value."""
    if max_chars is None:
        os.environ.pop("KELD_SIDECAR_MAX_CHARS", None)
    else:
        os.environ["KELD_SIDECAR_MAX_CHARS"] = max_chars
    import app.main as m

    return importlib.reload(m)


def test_clip_truncates_above_cap():
    m = _reload_main("100")
    assert len(m._clip("x" * 500)) == 100


def test_clip_leaves_short_text():
    m = _reload_main("100")
    assert m._clip("hello") == "hello"


def test_clip_disabled_when_nonpositive():
    m = _reload_main("0")
    assert m._clip("x" * 5000) == "x" * 5000


def test_default_cap_is_generous():
    m = _reload_main(None)
    assert m._MAX_CHARS == 20000  # clips only pathological inputs


import asyncio as _asyncio

from app.cpuscale import CpuScaler
from app.governor import Governor
from app.runner import InferenceRunner, QueueFull
from app.worker import handle
from fastapi import HTTPException


class _FakeModel:
    def classify_text(self, text, tasks, include_confidence=False):
        # Mirror gliner2: with include_confidence it returns {"label","confidence"} dicts
        # carrying the real score; without it, a bare label string (→ adapter fabricates 1.0).
        if include_confidence:
            return {t: {"label": opts[0], "confidence": 0.73} for t, opts in tasks.items()}
        return {t: opts[0] for t, opts in tasks.items()}  # top label = first option

    def extract_entities(self, text, labels):
        return {"entities": {}}

    def create_schema(self):
        return self

    def entities(self, labels):
        return self

    def classification(self, task, options):
        return self

    def extract(self, text, schema, include_confidence=False):
        return {"entities": {}}


class _FakeWM:
    """Stands in for WorkerManager: call() runs worker.handle against a fake
    model in-thread (the runner still executes it single-flight), so endpoints
    see the same already-normalized payload the real worker returns."""
    def __init__(self, model=None, state="ready"):
        self._model = model or _FakeModel()
        self.state = state
        self.model_cost_mb = None
        self.counts = {"recycles": 0, "kills_timeout": 0, "kills_pressure": 0,
                       "kills_idle": 0, "crashes": 0}

    def call(self, req):
        return handle(req, self._model)

    def ready(self):
        return self.state == "ready"

    def worker_rss_mb(self):
        return 0.0


def _wire(main, queue_max=8, wm=None, runner=None):
    gov = Governor(disabled=True)
    scaler = CpuScaler()
    runner = runner or InferenceRunner(gov, queue_max=queue_max)
    main._state.clear()
    main._state["governor"] = gov
    main._state["scaler"] = scaler
    main._state["runner"] = runner
    main._state["wm"] = wm or _FakeWM()
    return runner


def test_classify_endpoint_routes_through_worker():
    m = _reload_main(None)
    runner = _wire(m)

    async def run():
        runner.start()
        try:
            out = await m.classify(m.ClassifyIn(text="hello", tasks={"task_type": ["a", "b"]}))
            assert out["results"]["task_type"][0]["label"] == "a"
            # real GLiNER2 score must survive — not the fabricated 1.0 from a bare-string label
            assert out["results"]["task_type"][0]["confidence"] == 0.73
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


def test_health_state_down_when_no_worker():
    m = _reload_main(None)
    m._state.clear()
    h = m.health()
    assert h["ok"] is False and h["state"] == "down"


def test_dispatch_503_when_worker_held():
    # Memory pressure holds the worker; endpoints shed with 503 rather than block.
    m = _reload_main(None)
    _wire(m, wm=_FakeWM(state="held"))
    body = m.ClassifyIn(text="hi", tasks={"task_type": ["codegen", "other"]})
    try:
        _asyncio.run(m.classify(body))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503


def test_classify_sheds_503_and_counts_when_queue_full():
    from app.metrics import Counts

    class _FullRunner:
        async def submit(self, *a, **k):
            raise QueueFull()

    m = _reload_main(None)
    _wire(m, runner=_FullRunner())
    m._state["counts"] = Counts()

    body = m.ClassifyIn(text="hi", tasks={"task_type": ["codegen", "other"]})
    try:
        _asyncio.run(m.classify(body))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503
    assert m._state["counts"].shed_503 == 1


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
