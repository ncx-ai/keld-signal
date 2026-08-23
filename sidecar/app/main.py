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

from app.analysis.analyze import PromptNotFound, analyze_window
from app.analysis.match import DEFAULT_BUDGET_S as _MATCH_DEFAULT_BUDGET_S
from app.analysis.match import compile_vocabulary, match_text
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


# Configured-vocabulary matching (POST /vocabulary, POST /match). See
# docs/superpowers/specs/2026-08-22-configured-vocabulary-matching-design.md and
# app/analysis/match.py (the matcher itself — pure, no I/O, already 19-test
# reviewed). These two endpoints deliberately bypass _dispatch/the single-flight
# runner entirely: matching is a regex scan over text the caller already holds,
# not an inference, so it must answer even while the runner is fully occupied
# and even when no worker has ever been spawned (asserted in test_main.py).
#
# BUDGET DECISION. match_text() takes one `budget_s` applied independently to
# EVERY raw-regex label (match.py's own docstring flags this: N pathological
# labels can cost up to N x budget_s). That is a real per-request cost surface
# once an org's vocabulary has more than a couple of `regex` labels, since this
# runs synchronously against org-authored, untrusted patterns — unlike the
# `match` (literal) path, which is re.escape'd and has no backtracking surface
# at all.
#
# Chosen: cap the TOTAL raw-regex time for one /match call, not the per-label
# ceiling. Implemented without touching match.py (finished, reviewed, pure) by
# using the fact that match_text already exposes a single `budget_s` applied
# uniformly to every entry: divide the total cap by the number of raw-regex
# labels actually installed (counted once, at /vocabulary time, not per
# /match call), so worst case raw_count * budget_s == the total cap no matter
# how many raw labels an org configures. A floor stops the per-label share
# from collapsing toward 0 on a very large vocabulary, so an ordinary pattern
# still gets a workable window — the trade-off being that a vocabulary with
# enough raw-regex labels for the floor to dominate no longer holds the total
# cap exactly. That is the same shape of trade-off match.py's own static
# nested-quantifier filter already makes (a cheap, approximate defence over an
# exact one that costs more to compute). The recommended `match` (literal)
# path is unaffected by any of this: it has no timeout and needs none.
_MATCH_TOTAL_BUDGET_S = float(os.environ.get("KELD_SIDECAR_MATCH_BUDGET_S", "0.5"))
_MATCH_BUDGET_FLOOR_S = float(os.environ.get("KELD_SIDECAR_MATCH_BUDGET_FLOOR_S", "0.01"))


def _match_budget_s() -> float:
    """Per-label raw-regex budget for the CURRENTLY installed vocabulary, sized so
    that (raw-regex label count) x (this budget) never exceeds
    _MATCH_TOTAL_BUDGET_S — see the block comment above for why."""
    raw_count = _state.get("vocab_raw_count", 0)
    if raw_count <= 0:
        return _MATCH_DEFAULT_BUDGET_S
    return max(_MATCH_BUDGET_FLOOR_S, _MATCH_TOTAL_BUDGET_S / raw_count)


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
    # (sigmoid) scoring — the basis for binary "tag or not" classification. In either form the
    # config dict's "labels" may itself be a {label: description} MAP: gliner2 reads a dict-valued
    # "labels" as per-label descriptions and injects each hint into the model prompt (the daemon
    # sends this for custom passes with authored value descriptions — single-label via a dict-labels
    # task with no multi_label, multi-label via the same with multi_label=true). classify_text()
    # accepts all these forms directly (gliner2 Schema.classification). Kept in parity with the Atlas
    # enrich-sidecar copy.
    tasks: dict[str, list[str] | dict]
    max_len: int | None = None


class ExtractIn(BaseModel):
    text: str
    labels: dict[str, str]
    tasks: dict[str, list[str]]
    max_len: int | None = None


# Wire shape matches compile_vocabulary's `raw` parameter exactly (see
# app/analysis/match.py): {key: [{"id", "match"?, "regex"?}, ...]}. Labels are
# kept as plain dicts (not a nested BaseModel) so the wire contract stays in
# lockstep with the one thing that actually reads them, compile_vocabulary,
# instead of two schemas that can drift.
class VocabularyIn(BaseModel):
    vocabulary: dict[str, list[dict]] = {}


class MatchIn(BaseModel):
    text: str


class AnalyzeIn(BaseModel):
    path: str
    prompt_id: str
    span_minutes: int = 60


# Lazy, load-once spaCy for the `term` level (named entities in message TEXT — the one level
# analyze_window can compute that isn't a deterministic tool-input scan). Same sentinel pattern as
# scripts/refseries.py's term_nlp() (kept in parity rather than inventing a second one): a 3-state
# ["unset"] -> None|model list, because `None` is itself a valid loaded-and-degraded outcome and
# can't double as the "not tried yet" marker. KELD_TERMS=0 opts out even when spaCy is installed
# (it materially adds cost); anything else missing/failing degrades the same way — the `term`
# level is simply absent from the payload, never a request failure.
_ANALYSIS_NLP = ["unset"]


def _analysis_nlp():
    if _ANALYSIS_NLP[0] == "unset":
        _ANALYSIS_NLP[0] = None
        if os.environ.get("KELD_TERMS", "1") != "0":
            try:
                import spacy
                m = spacy.load("en_core_web_sm",
                               exclude=["tagger", "parser", "lemmatizer", "attribute_ruler"])
                m.max_length = 20_000_000
                _ANALYSIS_NLP[0] = m
            except Exception:  # not installed, or no model: degrade, never fail the request
                pass
    return _ANALYSIS_NLP[0]


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


@app.post("/vocabulary")
def install_vocabulary(body: VocabularyIn):
    """Compile and install an org's configured vocabulary into THIS (parent)
    process. The parent survives inference-worker recycles and deliberately
    holds no model, so a recycle can never silently empty the vocabulary — a
    daemon restart is the only thing that does, and the daemon re-pushes on
    every settings-poll change (see the design spec).

    Returns the rejects rather than logging them: the caller (the daemon's
    settings poll) needs to report a bad label once per distinct reject set,
    not once per poll — logging here turned one bad org config into ~288
    client events per machine per day before.
    """
    compiled, rejects = compile_vocabulary(body.vocabulary)
    raw_count = sum(1 for entries in compiled.values() for e in entries if e["raw"] is not None)
    _state["vocabulary"] = compiled
    _state["vocab_raw_count"] = raw_count
    _count("vocab_installs")
    return {"rejects": rejects}


@app.post("/match")
async def match(body: MatchIn):
    """Match text against the currently-installed vocabulary. Deliberately does
    NOT go through _dispatch/the single-flight runner (see the block comment
    above _match_budget_s): this is a regex scan over text the caller already
    holds, not an inference, and it must answer even while the runner is fully
    occupied or no worker has ever been spawned. Run in the default executor
    (NOT the runner's dedicated single-worker executor) purely so a
    pathological-but-bounded raw pattern can't stall the event loop out from
    under /health and /metrics while it burns its (capped) budget.

    No span, no offset, no source text is ever in the return value — match_text
    itself only ever returns id/count/confidence/alternates (see its docstring
    and test_analysis_match.py's wire-shape test); this endpoint adds nothing
    that could reintroduce one.
    """
    compiled = _state.get("vocabulary") or {}
    _count("match_served")
    if not compiled or not body.text:
        return {}
    loop = asyncio.get_running_loop()
    return await loop.run_in_executor(None, match_text, body.text, compiled, _match_budget_s())


@app.post("/analyze")
async def analyze(body: AnalyzeIn):
    """Turn `span_minutes` of one transcript ending at `prompt_id` into the workstream +
    inventory payload (see app.analysis.analyze.analyze_window). Coordinates in — a path and a
    prompt id — never text; the response itself carries no span/offset/text either (see
    test_analyze_response_carries_no_prompt_text).

    Deliberately does NOT go through _dispatch/the single-flight runner, for the same reason
    /match does not (see the block comment above _match_budget_s): this is a transcript read
    plus regex and (optionally) spaCy work, not inference, and it must answer while the runner
    is occupied or no worker has ever been spawned. Run in the default executor so a large
    transcript — or a slow spaCy pass over one — cannot stall the event loop out from under
    /health and /metrics, exactly the reason /match is already run there.

    DEVIATION FROM THE DESIGN SPEC, RECORDED DELIBERATELY:
    docs/superpowers/specs/2026-08-22-sidecar-analysis-tier-design.md calls for analysis to run
    in a third, long-lived worker process, because the FastAPI parent's flat RSS depends on
    holding no model — and spaCy (_analysis_nlp above) is a model by that definition. This runs
    it in the parent instead. That is a decision, not an oversight: the user deprioritised memory
    sizing for this on 2026-08-22. Revisit before this endpoint sees production-scale traffic.
    """
    _count("analyze_served")
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(
            None, analyze_window, body.path, body.prompt_id, body.span_minutes, _analysis_nlp())
    except PromptNotFound:
        raise HTTPException(status_code=404, detail="prompt not found in transcript")
