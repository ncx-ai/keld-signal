"""keld-agent GLiNER2 sidecar — FastAPI app exposing the enrich.Model contract.

Vendored + adapted from inference-enrichment. The daemon spawns this as a local
child process (see ../serve.py) and talks to it over 127.0.0.1. Inference runs
in a separate worker child (see worker.py / worker_manager.py) so the FastAPI
process holds no model and its RSS stays flat; recycling the worker reclaims its
heap via process exit. It returns RAW spans (surface text + offsets); masking is
enforced daemon-side by the enrichment pipeline, never here.
"""
import asyncio
import os
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from app.cpuscale import CpuScaler
from app.governor import Governor
from app.metrics import Counts, build_metrics
from app.runner import InferenceRunner, QueueFull
from app.worker_manager import (
    WorkerManager, WorkerTimeout, WorkerUnavailable, WorkerError, HELD,
)

# Character pre-clip. This bounds TOKENIZER cost on a pathological paste; it is
# NOT the memory bound — that is max_len, in tokens, which is what activation
# memory actually scales with (gliner2 truncates to max_len only AFTER
# tokenizing, so a huge string is still tokenized in full).
#
# Keep this generous enough never to pre-empt the token cap. It used to default
# to 8000, which is ~1100 word tokens — below the token ceiling, so the char clip
# silently became the real constraint and the adaptive token cap had no effect
# above ~1100. Sized here well above the largest token cap the daemon will send.
_MAX_CHARS = int(os.environ.get("KELD_SIDECAR_MAX_CHARS", "24000"))


def _clip(text: str) -> str:
    """Truncate text to _MAX_CHARS to bound tokenizer work on pathological
    input. Pure so it is unit-testable without loading the model."""
    if _MAX_CHARS > 0 and len(text) > _MAX_CHARS:
        return text[:_MAX_CHARS]
    return text


_QUEUE_MAX = int(os.environ.get("KELD_SIDECAR_QUEUE_MAX", "64"))

# Load from a locally-provisioned model directory when the daemon supplies one
# (KELD_GLINER2_DIR); otherwise fall back to the pinned HF model id.
MODEL_NAME = os.environ.get("KELD_GLINER2_DIR") or os.environ.get(
    "SIDECAR_MODEL", "fastino/gliner2-large-v1"
)

_state: dict = {}


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


async def _sample_loop(governor: Governor, interval: float = 5.0) -> None:
    while True:
        governor.sample()
        await asyncio.sleep(interval)


def _count(field: str) -> None:
    """Increment a lifetime counter in _state (no-op before counts are wired)."""
    c = _state.get("counts")
    if c:
        setattr(c, field, getattr(c, field) + 1)


def _threads_for_load():
    """Thread target for the next inference, computed parent-side from host load
    and passed into the worker request (the worker applies torch.set_num_threads).
    Idle ⇒ all cores, saturated ⇒ a floor, so a single enrichment never
    monopolizes a busy host."""
    scaler = _state["scaler"]
    governor = _state["governor"]
    n = scaler.threads_for(governor.ewma)
    _state["cpu_threads"] = n
    return n


def _parent_rss_mb():
    try:
        import psutil
        return psutil.Process().memory_info().rss / (1024.0 * 1024.0)
    except Exception:
        return None


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

    # 1s: poll() both samples RSS (for the peak high-water) and enforces the
    # hard limit, so the interval bounds how long an over-budget worker can run
    # before it is killed. Both reads (psutil RSS + virtual_memory) are cheap
    # enough to do every second; measured spikes last only 1-3s, so a slower
    # interval would miss them outright.
    poll_task = asyncio.create_task(_poll_loop(float(os.environ.get("KELD_SIDECAR_MEM_POLL_S", "1"))))
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


app = FastAPI(lifespan=lifespan)


# max_len is the daemon's adaptive per-inference token cap (see
# internal/agent/enrich/lenstat). It is the real bound on transient activation
# memory: the _clip char guard below only bounds characters, while attention cost
# scales with TOKENS. None means no cap, matching gliner2's default.
class EntitiesIn(BaseModel):
    text: str
    labels: dict[str, str]
    max_len: int | None = None


class ClassifyIn(BaseModel):
    text: str
    # A task value is either a bare label list (single-label softmax — pick one) OR a config dict
    # {"labels": [...], "multi_label": true, "cls_threshold": 0.5} for independent per-label
    # (sigmoid) scoring — the basis for binary "tag or not" classification. classify_text() accepts
    # both forms directly (gliner2 Schema.classification). Kept in parity with the Atlas
    # enrich-sidecar copy.
    tasks: dict[str, list[str] | dict]
    max_len: int | None = None


class ExtractIn(BaseModel):
    text: str
    labels: dict[str, str]
    tasks: dict[str, list[str]]
    max_len: int | None = None


@app.get("/health")
def health():
    # ok = "the service can serve on demand", NOT "a worker is already loaded".
    # DOWN/SPAWNING/READY all serve (a request spawns the worker lazily); only
    # HELD (memory pressure) cannot. Reporting ok=False while lazily DOWN would
    # deadlock the daemon's supervisor + readiness gate (it waits for ok before
    # sending the request that would spawn the worker).
    wm = _state.get("wm")
    return {"ok": bool(wm) and wm.state != HELD, "model": MODEL_NAME,
            "state": wm.state if wm else "down"}


@app.get("/metrics")
def metrics():
    started = _state.get("started_at", time.monotonic())
    wm = _state.get("wm")
    if wm is None:  # pre-lifespan / post-shutdown; degrade rather than 500
        return {"worker": {"state": "down"}, "uptime_s": round(time.monotonic() - started, 1)}
    return build_metrics(
        worker_state=wm.state, worker_rss_mb=wm.worker_rss_mb(),
        parent_rss_mb=_parent_rss_mb(), model_cost_mb=wm.model_cost_mb,
        governor=_state.get("governor"), runner=_state.get("runner"),
        counts=_state.get("counts", Counts()),
        recycles=wm.counts["recycles"],
        kills={"timeout": wm.counts["kills_timeout"], "pressure": wm.counts["kills_pressure"],
               "idle": wm.counts["kills_idle"], "hard": wm.counts["kills_hard"],
               "crash": wm.counts["crashes"]},
        uptime_s=time.monotonic() - started, cpu_threads=_state.get("cpu_threads"),
        peak_rss_mb=wm.peak_rss_mb, ceiling_mb=wm.ceiling_mb(),
        hard_limit_mb=wm.hard_limit_mb(),
    )


async def _dispatch(req: dict):
    """Submit a worker request through the governed runner, translating worker
    lifecycle exceptions into HTTP status + lifetime counters. Endpoints return
    the worker's already-normalized result verbatim."""
    wm = _state["wm"]
    if wm.state == HELD:
        _count("shed_503")  # count pressure sheds so they're visible in /metrics
        raise HTTPException(status_code=503, detail="unavailable — memory pressure")
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


@app.post("/entities")
async def entities(body: EntitiesIn):
    req = {"op": "entities", "text": _clip(body.text), "labels": body.labels,
           "threads": _threads_for_load(), "max_len": body.max_len}
    return await _dispatch(req)


@app.post("/classify")
async def classify(body: ClassifyIn):
    req = {"op": "classify", "text": _clip(body.text), "tasks": body.tasks,
           "threads": _threads_for_load(), "max_len": body.max_len}
    return await _dispatch(req)


@app.post("/extract")
async def extract(body: ExtractIn):
    req = {"op": "extract", "text": _clip(body.text), "labels": body.labels,
           "tasks": body.tasks, "threads": _threads_for_load(), "max_len": body.max_len}
    return await _dispatch(req)
