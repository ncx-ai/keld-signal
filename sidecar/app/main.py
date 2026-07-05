"""keld-agent GLiNER2 sidecar — FastAPI app exposing the enrich.Model contract.

Vendored + adapted from inference-enrichment. The daemon spawns this as a local
child process (see ../serve.py) and talks to it over 127.0.0.1. It returns RAW
spans (surface text + offsets); masking is enforced daemon-side by the
enrichment pipeline, never here.
"""
import asyncio
import os
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from app.adapter import normalize_classify, normalize_entities, normalize_extract
from app.governor import Governor
from app.memwatch import MemoryWatch, LOADED, EVICTED, RELOADING, DORMANT
from app.metrics import Counts, build_metrics
from app.runner import InferenceRunner, QueueFull

# Cap input length as a guard: GLiNER2 is a transformer, so memory grows
# roughly with the square of the sequence length. A pathologically long prompt
# or transcript can allocate a huge tensor in a single call, which single-flight
# execution (see runner.py) alone would not prevent. The cap is generous (only
# clips outliers) and overridable via env. <= 0 disables clipping.
_MAX_CHARS = int(os.environ.get("KELD_SIDECAR_MAX_CHARS", "20000"))


def _clip(text: str) -> str:
    """Truncate text to _MAX_CHARS to bound single-inference memory. Pure so it
    is unit-testable without loading the model."""
    if _MAX_CHARS > 0 and len(text) > _MAX_CHARS:
        return text[:_MAX_CHARS]
    return text


_QUEUE_MAX = int(os.environ.get("KELD_SIDECAR_QUEUE_MAX", "64"))

# Load from a locally-provisioned model directory when the daemon supplies one
# (KELD_GLINER2_DIR); otherwise fall back to the pinned HF model id.
MODEL_NAME = os.environ.get("KELD_GLINER2_DIR") or os.environ.get(
    "SIDECAR_MODEL", "fastino/gliner2-large-v1"
)
# GPU-oriented accelerators — OFF by default. fp16 (`quantize`) can be SLOWER on CPU,
# so only enable these when running on a GPU. Env-gated with a clean fallback so an
# unsupported kwarg can never brick startup.
_QUANTIZE = os.environ.get("SIDECAR_QUANTIZE", "0") == "1"
_COMPILE = os.environ.get("SIDECAR_COMPILE", "0") == "1"

# Absolute headroom margin (MB) required over the model's footprint before reload.
_RELOAD_MARGIN_MB = float(os.environ.get("KELD_SIDECAR_RELOAD_MARGIN_MB", "1024"))

_state: dict = {}


def _require_loaded():
    """Return the live model, or raise 503 when the model is unloaded (memory
    pressure / idle / dormant / mid-reload). Every inference endpoint gates on
    this. Recording activity here (even on a 503) lets the watcher bring an
    idle-evicted model back on demand as soon as work resumes."""
    _state["last_activity"] = time.monotonic()
    if _state.get("model_state") == LOADED and "model" in _state:
        return _state["model"]
    raise HTTPException(status_code=503, detail="unavailable — memory pressure")


def _load_model():
    from gliner2 import GLiNER2

    kwargs: dict = {}
    if _QUANTIZE:
        kwargs["quantize"] = True
    if _COMPILE:
        kwargs["compile"] = True
    try:
        return GLiNER2.from_pretrained(MODEL_NAME, **kwargs)
    except TypeError:
        # installed gliner2 doesn't accept these kwargs — load without them
        return GLiNER2.from_pretrained(MODEL_NAME)


def _warmup(model) -> None:
    """Run one inference at startup so the FIRST real request doesn't pay torch's
    lazy graph/kernel initialization. Best-effort — never fail startup over it."""
    try:
        model.classify_text("warm up the model", {"_warmup": ["a", "b"]})
        model.extract_entities("warm up the model", {"_warmup": "a warmup label"})
    except Exception:
        pass


async def _sample_loop(governor: Governor, interval: float = 5.0) -> None:
    while True:
        governor.sample()
        await asyncio.sleep(interval)


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


def _set_state(state: str) -> None:
    _state["model_state"] = state
    _state["state_since"] = time.monotonic()


async def _unload_model(reason: str = "memory"):
    """Move to EVICTED, drain the single in-flight inference, drop the model, and
    return its RSS to the OS. `reason` ("memory" | "idle") decides how it reloads:
    memory waits for headroom to hold; idle reloads on demand when work resumes.
    Endpoints already 503 once model_state != LOADED."""
    _state["evict_reason"] = reason
    _set_state(EVICTED)
    runner = _state.get("runner")
    for _ in range(500):  # ~5s cap waiting for the single in-flight job to finish
        if not runner or runner.inflight == 0:
            break
        await asyncio.sleep(0.01)
    _state.pop("model", None)
    import gc
    gc.collect()
    _malloc_trim()
    _count("evicted")


async def _reload_model():
    _set_state(RELOADING)
    loop = asyncio.get_running_loop()
    model = await loop.run_in_executor(None, _load_model)
    _warmup(model)
    _state["model"] = model
    _state["last_activity"] = time.monotonic()  # fresh, so it isn't re-evicted as idle
    _state["evict_reason"] = None
    _set_state(LOADED)
    _count("reloaded")


def _count(field: str) -> None:
    """Increment a lifetime counter in _state (no-op before counts are wired)."""
    c = _state.get("counts")
    if c:
        setattr(c, field, getattr(c, field) + 1)


async def _mem_watch_loop(watch, interval: float):
    from app.memwatch import EVICT, EVICT_IDLE, RELOAD
    while True:
        try:
            state = _state.get("model_state")
            action = watch.poll(
                state, _state.get("model_cost_mb"),
                last_activity=_state.get("last_activity"),
                evicted_at=_state.get("state_since"),
                evict_reason=_state.get("evict_reason"),
            )
            if action == EVICT and state == LOADED:
                await _unload_model(reason="memory")
            elif action == EVICT_IDLE and state == LOADED:
                await _unload_model(reason="idle")
            elif action == RELOAD and state in (EVICTED, DORMANT):
                await _reload_model()
        except Exception:
            pass
        await asyncio.sleep(interval)


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
    _state["started_at"] = time.monotonic()
    _state["model_cost_mb"] = None
    _state["last_activity"] = time.monotonic()
    _state["evict_reason"] = None
    _set_state(DORMANT)

    # Load only when there is headroom. model_cost is unknown at first boot, so the
    # gate is the margin floor plus the evict-pct danger check; after the first load
    # model_cost is known and reloads use the absolute headroom gate. If there is no
    # room, start dormant and let the watcher reload once RAM recovers.
    pct, mb = watch._sampler()
    watch.last_avail_pct, watch.last_avail_mb = pct, mb
    if pct > watch._evict_pct and watch.has_headroom(mb, None):
        before = _rss_mb()
        model = _load_model()
        _state["model_cost_mb"] = max(0.0, _rss_mb() - before)
        _warmup(model)
        _state["model"] = model
        _state["last_activity"] = time.monotonic()
        _set_state(LOADED)

    sampler_task = asyncio.create_task(_sample_loop(governor))
    poll_interval = float(os.environ.get("KELD_SIDECAR_MEM_POLL_S", "2"))
    watch_task = asyncio.create_task(_mem_watch_loop(watch, poll_interval))
    yield
    for t in (sampler_task, watch_task):
        t.cancel()
        try:
            await t
        except asyncio.CancelledError:
            pass
    await runner.stop()
    _state.clear()


app = FastAPI(lifespan=lifespan)


class EntitiesIn(BaseModel):
    text: str
    labels: dict[str, str]


class ClassifyIn(BaseModel):
    text: str
    tasks: dict[str, list[str]]


class ExtractIn(BaseModel):
    text: str
    labels: dict[str, str]
    tasks: dict[str, list[str]]


@app.get("/health")
def health():
    runner = _state.get("runner")
    loaded = _state.get("model_state") == LOADED
    ok = loaded and "model" in _state and runner is not None and runner.ready
    return {"ok": ok, "model": MODEL_NAME, "state": _state.get("model_state", DORMANT)}


@app.get("/metrics")
def metrics():
    import time
    started = _state.get("started_at", time.monotonic())
    return build_metrics(
        model_state=_state.get("model_state", DORMANT),
        state_since=_state.get("state_since", started),
        governor=_state.get("governor"),
        runner=_state.get("runner"),
        watch=_state.get("watch"),
        counts=_state.get("counts", Counts()),
        model_cost_mb=_state.get("model_cost_mb"),
        reload_margin_mb=_RELOAD_MARGIN_MB,
        uptime_s=time.monotonic() - started,
        evict_reason=_state.get("evict_reason"),
    )


@app.post("/entities")
async def entities(body: EntitiesIn):
    model = _require_loaded()
    text = _clip(body.text)
    _count("submitted")
    try:
        raw = await _state["runner"].submit(model.extract_entities, text, body.labels)
    except QueueFull:
        _count("shed_503")
        raise HTTPException(status_code=503, detail="overloaded")
    except Exception:
        _count("failed")
        raise
    _count("completed")
    return {"entities": normalize_entities(raw, text)}


@app.post("/classify")
async def classify(body: ClassifyIn):
    model = _require_loaded()
    text = _clip(body.text)
    _count("submitted")
    try:
        raw = await _state["runner"].submit(model.classify_text, text, body.tasks, include_confidence=True)
    except QueueFull:
        _count("shed_503")
        raise HTTPException(status_code=503, detail="overloaded")
    except Exception:
        _count("failed")
        raise
    _count("completed")
    return {"results": normalize_classify(raw)}


@app.post("/extract")
async def extract(body: ExtractIn):
    model = _require_loaded()
    text = _clip(body.text)
    _count("submitted")

    def _run():
        schema = model.create_schema().entities(body.labels)
        for task, options in body.tasks.items():
            schema = schema.classification(task, options)
        return model.extract(text, schema, include_confidence=True)

    try:
        raw = await _state["runner"].submit(_run)
    except QueueFull:
        _count("shed_503")
        raise HTTPException(status_code=503, detail="overloaded")
    except Exception:
        _count("failed")
        raise
    _count("completed")
    return normalize_extract(raw, text, list(body.tasks.keys()))
