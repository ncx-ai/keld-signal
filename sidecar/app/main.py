"""keld-agent GLiNER2 sidecar — FastAPI app exposing the enrich.Model contract.

Vendored + adapted from inference-enrichment. The daemon spawns this as a local
child process (see ../serve.py) and talks to it over 127.0.0.1. It returns RAW
spans (surface text + offsets); masking is enforced daemon-side by the
enrichment pipeline, never here.
"""
import asyncio
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from app.adapter import normalize_classify, normalize_entities, normalize_extract
from app.governor import Governor
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
_state: dict = {}


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
