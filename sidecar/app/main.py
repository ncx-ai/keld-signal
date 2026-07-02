"""keld-agent GLiNER2 sidecar — FastAPI app exposing the enrich.Model contract.

Vendored + adapted from inference-enrichment. The daemon spawns this as a local
child process (see ../serve.py) and talks to it over 127.0.0.1. It returns RAW
spans (surface text + offsets); masking is enforced daemon-side by the
enrichment pipeline, never here.
"""
import os
import threading
from contextlib import asynccontextmanager

from fastapi import FastAPI
from pydantic import BaseModel

from app.adapter import normalize_classify, normalize_entities, normalize_extract

# Serialize ALL model inference. FastAPI runs these sync endpoints in a
# threadpool, so several requests (e.g. the daemon's per-job extractor fan-out)
# can otherwise hit the shared GLiNER2 model at once — each concurrent inference
# allocates its own activation tensors, which is what OOM-killed the sidecar.
# Holding this lock means the model only ever runs ONE inference at a time, so
# resident memory is bounded to a single call's footprint. Extra requests block
# (cheaply) in their threadpool thread until the lock frees.
_infer_lock = threading.Lock()

# Cap input length as a second guard: GLiNER2 is a transformer, so memory grows
# roughly with the square of the sequence length. A pathologically long prompt
# or transcript can allocate a huge tensor in a single call, which the inference
# lock alone would not prevent. The cap is generous (only clips outliers) and
# overridable via env. <= 0 disables clipping.
_MAX_CHARS = int(os.environ.get("KELD_SIDECAR_MAX_CHARS", "20000"))


def _clip(text: str) -> str:
    """Truncate text to _MAX_CHARS to bound single-inference memory. Pure so it
    is unit-testable without loading the model."""
    if _MAX_CHARS > 0 and len(text) > _MAX_CHARS:
        return text[:_MAX_CHARS]
    return text

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


@asynccontextmanager
async def lifespan(app: FastAPI):
    model = _load_model()
    _warmup(model)
    _state["model"] = model  # set last → /health reports ok only once warm & ready
    yield
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
    return {"ok": "model" in _state, "model": MODEL_NAME}


@app.post("/entities")
def entities(body: EntitiesIn):
    text = _clip(body.text)
    with _infer_lock:
        raw = _state["model"].extract_entities(text, body.labels)
    return {"entities": normalize_entities(raw, text)}


@app.post("/classify")
def classify(body: ClassifyIn):
    text = _clip(body.text)
    with _infer_lock:
        raw = _state["model"].classify_text(text, body.tasks)
    return {"results": normalize_classify(raw)}


@app.post("/extract")
def extract(body: ExtractIn):
    text = _clip(body.text)
    with _infer_lock:
        schema = _state["model"].create_schema().entities(body.labels)
        for task, options in body.tasks.items():
            schema = schema.classification(task, options)
        raw = _state["model"].extract(text, schema)
    return normalize_extract(raw, text, list(body.tasks.keys()))
