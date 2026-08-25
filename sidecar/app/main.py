"""keld-agent GLiNER2 sidecar — FastAPI app exposing the enrich.Model contract.

Vendored + adapted from inference-enrichment. The daemon spawns this as a local
child process (see ../serve.py) and talks to it over 127.0.0.1. Inference runs
in a separate worker child (see worker.py / worker_manager.py) so the FastAPI
process holds no model and its RSS stays flat; recycling the worker reclaims its
heap via process exit. It returns RAW spans (surface text + offsets); masking is
enforced daemon-side by the enrichment pipeline, never here.
"""
import asyncio
import logging
import os
import sys
import threading
import time
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException
from pydantic import BaseModel

from app.analysis.analyze import PromptNotFound, StoreBehind, WindowExpired, analyze_window
from app.analysis.tick import DEFAULT_MAX_WINDOWS, tick as tick_windows_for
from app.analysis.dynamics import DEFAULT_SIZER
from app.analysis.ingest import ingest_file, session_of
from app.analysis.match import DEFAULT_BUDGET_S as _MATCH_DEFAULT_BUDGET_S
from app.analysis.match import compile_vocabulary, match_text
from app.analysis.store import open_store
from app.analysis.transcript import _order_key
from app.cpuscale import CpuScaler
from app.governor import Governor
from app.metrics import Counts, build_metrics
# Imported as a bare name so the module import stays cheap: app.pii pulls in presidio only inside
# its engine builder, never at import.
from app.pii import scan as pii_scan
from app.runner import InferenceRunner, QueueFull
from app.worker_manager import (
    WorkerManager, WorkerTimeout, WorkerUnavailable, WorkerError, HELD,
)

log = logging.getLogger(__name__)

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


class PiiIn(BaseModel):
    text: str
    # Which country tiers of checksum recognizers to run on top of the universal
    # ones (see app.pii.REGION_RECOGNIZERS). PER-REQUEST rather than a sidecar
    # startup flag, because the daemon resolves it from org settings it polls on
    # a live interval — an org changing its regions must take effect on the next
    # prompt, not the next sidecar restart. Absent (None) means "no opinion" and
    # app.pii falls back to KELD_PII_REGIONS / `us`; an explicit [] means the
    # universal tier only, which is a different answer.
    regions: list[str] | None = None


class AnalyzeIn(BaseModel):
    path: str
    prompt_id: str
    span_minutes: int = 60


class TickIn(BaseModel):
    """One tick over one transcript. Coordinates and instants only — never text, never a span,
    never an offset (see test_tick_response_carries_no_prompt_text).

    `prompt_ids` are the HUMAN prompts enrichment has already characterised, and WHICH ids they
    are comes from the daemon rather than from the store on purpose: the store's `prompt` index
    holds every user- AND assistant-shaped turn (`ingest.py` indexes everything `turns_in`
    yields, so an assistant uuid still resolves), which for john's transcript is ~260 rows
    against 14 human prompts. Planning against all of them computes a covered set that swallows
    the session and the tick emits nothing, ever
    (test_the_store_prompt_index_would_hide_every_gap). Only the daemon knows which ids
    enrichment actually fired on — it applies `internal/agent/watch/filter.go`'s human-prompt
    filter — so the daemon names them and the store times them. An id the store cannot resolve
    is DROPPED rather than defaulted: a prompt whose instant is unknown cannot define a covered
    interval, and inventing one would either hide a gap or manufacture one.

    `cursor_ts` is where the last tick for this transcript stopped; null starts the cursor at the
    frontier, so a transcript seen for the first time is characterised forward and never
    back-filled — the same default `KELD_WATCH_BACKFILL` sets for capture.

    `now` is the daemon's clock, injected rather than read here, because it is what the frontier
    is computed from and a test that cannot move it cannot exercise the settle rule at all.
    """
    path: str
    prompt_ids: list[str] = []
    cursor_ts: float | None = None
    now: float | None = None
    span_minutes: float = 60.0
    max_windows: int = DEFAULT_MAX_WINDOWS


class IngestIn(BaseModel):
    """A transcript advanced. One field, deliberately: the daemon's watcher knows WHICH file grew
    and nothing else worth sending — the byte offset to resume from is the store's own, not the
    watcher's, and the appended bytes stay on the daemon's side of the call."""
    path: str


# /analyze path confinement (KELD_ANALYZE_ROOTS).
#
# The sidecar has NO auth: serve.py binds 127.0.0.1 and that is the whole of it. That was
# adequate while every endpoint only processed text the caller had already supplied — the caller
# learned nothing it did not already know. /analyze breaks that: it opens an arbitrary
# filesystem path AS THE DAEMON'S USER and returns content derived from it (workspace, branch,
# and the named terms, which have been observed to contain real person names). On a multi-user
# host that is a confused deputy — any other local user can POST a path under another user's
# ~/.claude/projects and read back their people, repos and branches.
#
# So the path is confined to an explicit allowlist of transcript roots. The daemon sets it at
# spawn from the roots it actually watches (internal/agent/daemon/sidecarenv.go ->
# watch.AnalyzeRoots); the defaults below only serve a hand-run sidecar.
#
# os.pathsep, not a comma: it is ";" on Windows and ":" elsewhere, which is the one separator
# that cannot collide with a Windows drive letter. (KELD_WATCH_ROOTS on the Go side uses commas
# because its entries are "source:dir" pairs, which have a different problem.)
_ANALYZE_ROOTS_ENV = "KELD_ANALYZE_ROOTS"


def _default_analyze_roots():
    """The standard per-user transcript directories. Deliberately the stable ANCESTORS (e.g.
    ~/.gemini/tmp, not ~/.gemini/tmp/*/chats): the leaf directories are created as sessions
    start, so an allowlist of them, resolved once, would reject transcripts written after the
    sidecar came up."""
    home = os.path.expanduser("~")
    codex_home = os.environ.get("CODEX_HOME") or os.path.join(home, ".codex")
    roots = [os.path.join(home, ".claude", "projects"),
             os.path.join(codex_home, "sessions"),
             os.path.join(home, ".gemini", "tmp")]
    if sys.platform == "darwin":
        roots.append(os.path.join(home, "Library", "Application Support", "Claude",
                                  "local-agent-mode-sessions"))
    return roots


def _analyze_roots():
    """The resolved allowlist. Read per request rather than latched at import: it is a handful of
    realpath() calls against one request that is already a whole-transcript parse, and latching
    it would make the module's behaviour depend on import order in a way nothing here needs.

    An explicitly EMPTY value yields an empty list, which denies everything. That direction is
    deliberate: the alternative reading — empty means unrestricted — turns one misconfiguration
    back into the unauthenticated-read vulnerability, silently."""
    raw = os.environ.get(_ANALYZE_ROOTS_ENV)
    entries = raw.split(os.pathsep) if raw is not None else _default_analyze_roots()
    return [os.path.realpath(e) for e in entries if e.strip()]


def _within_roots(path, roots):
    """Whether `path` resolves inside one of `roots`.

    realpath on BOTH sides, so neither `..` nor a symlink can escape: a prefix test on the raw
    string would admit `<root>/../elsewhere/x.jsonl`, and a test on an unresolved path would
    admit a link that merely LIVES in a root while pointing anywhere on the machine. The
    `+ os.sep` guard keeps `/home/a/.claude/projects-of-someone-else` from matching the root
    `/home/a/.claude/projects`.
    """
    real = os.path.realpath(path)
    return any(real == r or real.startswith(r + os.sep) for r in roots)


# Named-terms extraction (the `term` level). ON by default; KELD_TERMS=0 switches it off.
#
# `term` is the only analysis level that reads message TEXT rather than tool-call inputs, and the
# only one needing spaCy — which is loaded into this, the long-lived FastAPI parent, and stays
# there: the parent is never recycled. Measured at +619 MB in 2.27 s.
#
# That is affordable. What was NOT affordable is that nothing accounted for it: worker_manager.py
# derived the inference worker's hard limit as (total budget - a hard-coded 150 MB parent
# reserve), so once spaCy was resident the parent cost ~4.5x what the guard assumed and the
# worker's limit was ~470 MB too generous — under-protection, arrived at silently, in the one
# direction that matters. The fix is in worker_manager.parent_reserve_mb(): the reserve is now
# the constant OR the measured high-water parent RSS, whichever is larger. See its docstring.
#
# This service is the client-side analysis and enrichment service, not a GLiNER2 wrapper. It
# must start and serve without GLiNER2 ever being loaded — /analyze answers with the worker
# still DOWN (test_main.py pins this against the real lifespan), and spaCy and GLiNER2 are
# expected to coexist: measured together they are ~60 + ~619 + ~2740 MB, comfortably inside the
# 4096 MB budget once the accounting is honest.
_TERMS_OK = "ok"
_TERMS_DISABLED = "skipped:disabled"
_TERMS_NO_SPACY = "degraded:spacy_unavailable"


def _terms_status():
    """Whether the `term` level was computed, and if not, why — reported to the caller as
    `named_terms_status`, because an empty `named_terms` is otherwise not self-describing: a
    window that genuinely held no terms and a level that never ran look identical.

    `skipped:` means nothing was measured, so nothing is reported. `degraded:` means the regex
    shapes in terms.candidates() ran without spaCy — a genuine partial measurement, reported
    with the reason beside it rather than thrown away."""
    if os.environ.get("KELD_TERMS", "1") == "0":
        return _TERMS_DISABLED
    return _TERMS_OK


# spaCy's per-document guard, restored. It was set to 20_000_000, which disables it outright: one
# 880k-character window measured 14.7 s and a 3.4 GB transient peak. spaCy's own rule of thumb is
# ~1 GB of transient memory per 100k characters for the full pipeline; the NER-only pipeline
# loaded below is cheaper than that, so 100k characters per MESSAGE is a conservative bound —
# still far above any real chat turn, and 200x below what it replaces.
#
# The bound is per message and all-or-nothing: an over-length message is skipped by the NER pass
# (terms.candidates() applies it) rather than cut, and the regex shapes still read it in full, so
# no message leaves the level and nothing is read half-way. AGENTS.md's "never cut text
# mid-sentence" rule does not govern this in any case — it is a memory bound, not a legibility
# one, the same exemption lenstat's max_len already has.
_TERMS_MAX_LEN = int(os.environ.get("KELD_TERMS_MAX_LEN", "100000"))

# Lazy, load-once. Same sentinel pattern as scripts/refseries.py's term_nlp() (kept in parity
# rather than inventing a second one): a 3-state ["unset"] -> None|model list, because `None` is
# itself a valid loaded-and-degraded outcome and can't double as the "not tried yet" marker.
_ANALYSIS_NLP = ["unset"]


def _analysis_nlp():
    """The spaCy pipeline, or None (not installed, no model, or switched off).

    NEVER call this from the event loop — the load is seconds long and hundreds of MB. /analyze
    resolves it inside its executor (see _analyze_blocking).
    """
    if _ANALYSIS_NLP[0] == "unset":
        _ANALYSIS_NLP[0] = None
        if _terms_status() == _TERMS_OK:
            try:
                import spacy
                m = spacy.load("en_core_web_sm",
                               exclude=["tagger", "parser", "lemmatizer", "attribute_ruler"])
                m.max_length = _TERMS_MAX_LEN
                _ANALYSIS_NLP[0] = m
            except Exception:  # not installed, or no model: degrade, never fail the request
                pass
    return _ANALYSIS_NLP[0]


# The reference-series store, opened once and kept for the process's life.
#
# Lazy, and by the same 3-state sentinel pattern as _analysis_nlp above rather than at import:
# opening it creates ~/.keld/state/refseries.db, which must happen when a request needs it and
# under the KELD_HOME in force then, not as a side effect of importing this module.
#
# There is deliberately NO parse fallback if it cannot be opened. The store is not a
# lower-fidelity substitute — it answers exactly what a parse answers, which the test suite
# asserts field for field — but a silent switch between two implementations of one answer is how
# a divergence between them goes unnoticed, the same reasoning behind this repo's rule against a
# health-gated substitute for the model's own facets. A store that will not open is reported as
# 503 and the caller retries.
_STORE = ["unset"]
_STORE_LOCK = threading.Lock()


def _store():
    """The store, or None if it could not be opened. Never raises."""
    with _STORE_LOCK:
        if _STORE[0] == "unset":
            try:
                _STORE[0] = open_store()
            except Exception as exc:
                # Path only in the log, never the exception's message: a sqlite error can quote
                # the statement it failed on.
                log.error("reference-series store unavailable: %s", type(exc).__name__)
                _STORE[0] = None
        return _STORE[0]


def _store_stats():
    """The reference-series store's block for /metrics, or None if it could not be opened.

    Never raises, and never opens the store as a side effect of a metrics poll: `_store()` is
    already lazy and cached, and `store_stats` is itself TTL-cached because at 400 days of
    retention a bare `MIN(ts)` is a 35 ms full scan (see its docstring) and this endpoint is
    polled.
    """
    st = _store()
    if st is None:
        return None
    try:
        return st.store_stats()
    except Exception as exc:                     # noqa: BLE001 - /metrics must always answer
        return {"error": type(exc).__name__}


def _analyze_blocking(path, prompt_id, span_minutes):
    """The whole of /analyze's work, on an executor thread.

    _analysis_nlp() is resolved HERE and not at the call site. It used to be evaluated as an
    ARGUMENT to run_in_executor, which means it ran on the EVENT LOOP: the multi-second spaCy
    load blocked /health and /metrics, precisely the failure the executor hop exists to prevent
    (see analyze()'s docstring).

    The nlp is also what the INGEST behind this call uses, which is why it must be resolved
    before analyze_window rather than inside it: the `term` level is never re-derived, so the
    pipeline that ingests is part of the store's checkpoint (see ingest.terms_mode).
    """
    status = _terms_status()
    nlp = _analysis_nlp() if status == _TERMS_OK else None
    if status == _TERMS_OK and nlp is None:
        status = _TERMS_NO_SPACY    # enabled, but the model would not load

    st = _store()
    if st is None:
        raise StoreBehind("the reference-series store could not be opened")
    # `sizer` is what turns the DYNAMICS block on (app/analysis/dynamics.py): the same window's
    # recent slice read against its own longer baseline, so the response says how the work is
    # CHANGING and not only what it contains. Opt-in in `analyze_window` because the parse-path
    # equivalence oracle cannot compute it -- see that function's docstring -- so production is
    # the one caller that passes a sizer. `DEFAULT_SIZER` is now the EWMA change-point sizer:
    # Task 3's pre-registered comparison measured it at +74.6 precision / +27.0 recall points over
    # the fixed 15-minute slice with no new dependency, and the fixed slice survives as its
    # no-detection fallback (see `dynamics.EWMA_FAST` for the table and the control).
    # `prior` turns on the SESSION PRIOR block (app/analysis/prior.py): the session as it stood
    # BEFORE this window, reported beside the window's own answer and never supplying one it
    # lacked. Opt-in in `analyze_window` for the same reason `sizer` is -- the parse-path
    # equivalence oracle structurally cannot compute a second, much wider rollup -- so
    # production is again the one caller that asks for it.
    out = analyze_window(path, prompt_id, span_minutes, nlp, store=st, sizer=DEFAULT_SIZER,
                         prior=True)

    if status == _TERMS_DISABLED:
        # Switched off means not reported. The regex half of terms.candidates() needs no model
        # and still runs inside analyze_window, so returning its output would contradict the
        # status beside it and make the switch look like a performance knob. (A DEGRADED run is
        # the opposite case: it did measure something.)
        inv = out.get("inventory")
        if isinstance(inv, dict) and "named_terms" in inv:
            inv["named_terms"] = []
    out["named_terms_status"] = status
    return out


def _ingest_blocking(path):
    """The whole of /ingest's work, on an executor thread.

    `_analysis_nlp()` is resolved HERE, for the same reason `_analyze_blocking` resolves it here
    (the multi-second load must not run on the event loop) and for one more that is specific to
    ingest: the pipeline that ingests is part of the store's checkpoint (`ingest.terms_mode`),
    because `term` is the one level never re-derived. Ingesting under a different pipeline than
    /analyze resolves would force the very reparse the fingerprint exists to force.
    """
    nlp = _analysis_nlp() if _terms_status() == _TERMS_OK else None
    st = _store()
    if st is None:
        raise StoreBehind("the reference-series store could not be opened")
    return ingest_file(st, path, nlp)


def _tick_blocking(path, prompt_ids, cursor_ts, now, span_minutes, max_windows):
    """The whole of /tick's work, on an executor thread.

    `_analysis_nlp()` is resolved here for the same reason `_analyze_blocking` resolves it here
    (a multi-second load must never run on the event loop), and `DEFAULT_SIZER` is passed for the
    same reason /analyze passes it: a tick-emitted window is not a lesser window, and its
    dynamics block comes from the same EWMA change-point sizer production already measured.
    """
    nlp = _analysis_nlp() if _terms_status() == _TERMS_OK else None
    st = _store()
    if st is None:
        raise StoreBehind("the reference-series store could not be opened")
    # Coordinates in, instants out: the daemon names the human prompts and the store times them
    # (see TickIn.prompt_ids). An unresolvable id is dropped, never defaulted.
    session = session_of(path)
    prompt_ts = []
    for pid in prompt_ids:
        iso = st.prompt_time(session, pid)
        if iso is not None:
            prompt_ts.append(_order_key(iso).timestamp())
    return tick_windows_for(st, path, cursor_ts=cursor_ts, prompt_ts=prompt_ts, now=now,
                            span_minutes=span_minutes, nlp=nlp, sizer=DEFAULT_SIZER,
                            max_windows=max_windows, prior=True)


@app.post("/tick")
async def tick(body: TickIn):
    """Characterise the slices of one transcript that NO prompt's look-back will ever reach.

    Enrichment fires per prompt and every window looks back an hour, so the work a prompt CAUSES
    falls outside that prompt's own window; when the next prompt is more than an hour later,
    nothing characterises it. Measured over the frozen corpus (scripts/tick_coverage.py), that is
    43.6% of john's reference events and 44.5% of the Claude Code corpus's turns. See
    app/analysis/coverage.py for the planner and the guarantee that a tick-emitted window can
    never overlap a prompt's, and app/analysis/tick.py for what happens to a window the store
    cannot answer.

    Answers with NO refusal status of its own, and that is the design rather than an omission.
    Per-window, a 503-shaped failure stops the cursor and is reported as `behind`, and a
    410-shaped one drops the window and is reported in `expired` — because a tick asks for
    several windows at once and one unanswerable window must not discard the answerable ones
    beside it. Both are detected through `analyze_window`'s own `StoreBehind`/`WindowExpired`,
    the same guards /analyze maps to those statuses; the tick does not re-derive either.

    Like /analyze and /ingest it bypasses _dispatch/the single-flight runner: this is a series
    query, not inference, and it must answer while the runner is occupied or no worker has ever
    been spawned.
    """
    # Confinement BEFORE anything else, the SAME allowlist /analyze and /ingest use — the
    # sidecar has no auth, and a tick reads a transcript's series as the daemon's user.
    if not _within_roots(body.path, _analyze_roots()):
        _count("tick_rejected")
        raise HTTPException(status_code=403, detail="path is outside the configured transcript roots")
    _count("tick_served")
    now = body.now if body.now is not None else time.time()
    loop = asyncio.get_running_loop()
    try:
        out = await loop.run_in_executor(
            None, _tick_blocking, body.path, body.prompt_ids, body.cursor_ts, now,
            body.span_minutes, body.max_windows)
    except FileNotFoundError:
        # The transcript is GONE. Permanent, like /ingest's 404 for the same case.
        raise HTTPException(status_code=404, detail="transcript not found") from None
    except StoreBehind:
        # The store could not be OPENED at all — no cursor to report and nothing to characterise.
        # A window that is merely behind is reported in the body, not here (see the docstring).
        _count("tick_behind")
        raise HTTPException(status_code=503, detail="the reference-series store is unavailable") from None
    for _ in range(len(out["windows"])):
        _count("tick_windows")
    for _ in range(out["empty"]):
        _count("tick_empty")
    for _ in range(out["expired"]):
        _count("tick_expired")
    if out["behind"]:
        _count("tick_behind")
    return out


@app.post("/ingest")
async def ingest(body: IngestIn):
    """The daemon's watcher signalling that a transcript advanced: parse the appended tail into
    the reference series from the stored byte-offset checkpoint.

    Coordinates only — a path, and the response carries counts, never content.

    WHY THIS ENDPOINT EXISTS. Ingest is also reachable from /analyze, which ingests on demand
    when it finds the store behind (see analyze.analyze_window's `refresh`) — that stays as the
    correctness backstop and this does NOT replace it. What it changes is WHEN the work happens:
    a first whole-file ingest measured 5.1 s on a 90 MB transcript, and inside an /analyze
    request that lands on an enrichment job's per-pass deadline. Signalled from the watcher it
    lands on nothing. The daemon signals; the sidecar ingests (see the design spec at
    docs/superpowers/specs/2026-08-23-incremental-reference-series-store-design.md); the sidecar
    deliberately does not poll for growth, because the watcher already knows.

    A DROPPED SIGNAL IS RECOVERABLE, AND THAT IS THE DESIGN. Ingest resumes from the stored
    offset, so a signal lost to a restarting sidecar costs nothing but latency: the next signal
    for that file catches up on everything since, and /analyze's on-demand ingest catches up even
    if no further signal ever arrives. That is what lets the daemon side be fire-and-forget with
    no retry (internal/agent/daemon/ingestsignal.go).

    Like /analyze it bypasses _dispatch/the single-flight runner — a transcript parse is not an
    inference — and runs in the default executor so a large file cannot stall the event loop out
    from under /health and /metrics. It needs no model at all.
    """
    # Confinement BEFORE the open, and the SAME allowlist /analyze uses (see the block above
    # _default_analyze_roots). The sidecar has no auth, so an unconfined /ingest would let any
    # local user make the daemon's user parse an arbitrary path — /analyze's confused deputy with
    # a persistence side effect, since what it derives is then written to the store.
    if not _within_roots(body.path, _analyze_roots()):
        _count("ingest_rejected")
        raise HTTPException(status_code=403, detail="path is outside the configured transcript roots")
    loop = asyncio.get_running_loop()
    try:
        result = await loop.run_in_executor(None, _ingest_blocking, body.path)
    except FileNotFoundError:
        # 404, not 503: the transcript is GONE, which is a permanent fact about the file, and a
        # transient status would make the daemon re-signal a path that will never come back.
        # (ingest_file raises this deliberately — see its docstring.)
        _count("ingest_missing")
        raise HTTPException(status_code=404, detail="transcript not found") from None
    except StoreBehind:
        _count("ingest_failed")
        raise HTTPException(status_code=503, detail="the reference-series store is unavailable") from None
    except Exception as exc:
        # Class name only, and `from None` — the same rule /pii follows and for the same reason:
        # a parse or sqlite error message can quote the transcript content it failed on, and an
        # error path is exactly where prompt text escapes.
        _count("ingest_failed")
        log.warning("transcript ingest failed: %s", type(exc).__name__)
        raise HTTPException(status_code=503, detail="ingest unavailable") from None
    _count("ingest_served")
    # Counts and one boolean. `new_lines` is speech turns parsed, so 0 with a moved offset is
    # correct and normal (a tail of tool_result lines). No watermark timestamp, no offset, no
    # session: the caller needs none of them to decide anything, and each would be a field a
    # later publish path could pick up.
    return {"new_lines": result.new_lines, "reparsed": result.reparsed}


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
        hard_limit_mb=wm.hard_limit_mb(), parent_reserve_mb=wm.parent_reserve_mb(),
        budget_shortfall_mb=wm.budget_shortfall_mb() if wm.ceiling_mb() is not None else None,
        store_stats=_store_stats(),
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


def _pii_text(text: str):
    """The text /pii will actually scan, and whether anything was dropped to get it.

    Two bounds, both already justified elsewhere in this file: the char clip (tokenizer/parser cost
    on a pathological paste) and spaCy's own per-document guard (_TERMS_MAX_LEN — the shared
    pipeline is loaded with exactly that max_length, and handing it more raises).

    Head truncation, and the drop is REPORTED rather than swallowed: offsets into the head stay
    valid for the caller's copy of the text, but an unscanned tail is undetected PII, and the
    caller has to be able to call the facet partial rather than clean. (AGENTS.md's
    never-cut-mid-sentence rule does not govern this — like max_len, the constraint here is
    memory, not legibility.)
    """
    clipped = _clip(text)
    if len(clipped) > _TERMS_MAX_LEN:
        clipped = clipped[:_TERMS_MAX_LEN]
    return clipped, len(clipped) < len(text)


def _pii_blocking(text: str, regions):
    """The whole of /pii's work, on an executor thread.

    NO SPACY MODEL. This used to hand pii_scan the shared _analysis_nlp() pipeline so presidio
    would not load a second en_core_web_sm into this never-recycled parent. It no longer needs
    one at all: dropping SpacyRecognizer left only pattern recognizers, and app.pii now builds
    its own blank tokenizer (~5 MB, no weights). Measured identical spans and scores either way.
    The point is that /pii no longer REQUIRES the ~50 MB model in the parent — whose RSS is
    subtracted from the inference worker's hard limit — which is what changes under KELD_TERMS=0.
    Still on the executor: presidio's first-call import is not event-loop work.
    """
    return pii_scan(text, regions)


@app.post("/pii")
async def detect_pii(body: PiiIn):
    """Find leaked personal data in `text` and return WHERE it is, never WHAT it is.

    Takes text, like /classify, /extract and /entities — loopback, on-device, and the daemon read
    that text locally to begin with. Returns offsets + types + scores and nothing else: the caller
    already holds the text and slices its own copy, so putting the matched value in this body would
    add raw PII to an HTTP response, and to whatever logs it later. (/entities does return raw
    spans; that is an older contract with its own reasoning, not a precedent this follows.)

    Deliberately does NOT go through _dispatch/the single-flight runner, and touches no
    WorkerManager — same as /match and /analyze. This is presidio PATTERNS ONLY (regex + Luhn +
    libphonenumber; the NER came out when it measured ~1% precision on real prompts), not
    inference, and it is the endpoint that has to answer when GLiNER2 is absent entirely:
    ml_backend:"deterministic" runs the sensitivity facet with no model at all. Pinned against the
    real lifespan in test_main.py. Run in the default executor so neither the spaCy load nor a
    large document stalls the event loop out from under /health and /metrics.
    """
    text, truncated = _pii_text(body.text)
    _count("pii_served")
    if not text.strip():
        return {"spans": [], "truncated": truncated}
    loop = asyncio.get_running_loop()
    try:
        spans = await loop.run_in_executor(None, _pii_blocking, text, body.regions)
    except Exception as exc:
        # The exception is NOT propagated or formatted: a presidio/spaCy error message can quote
        # the analysed string, and an error path is exactly where prompt text escapes. Class name
        # only — enough to tell "no model installed" from "bad input" in an operator's logs.
        _count("pii_failed")
        log.warning("pii scan failed: %s", type(exc).__name__)
        # `from None` is load-bearing, not style: without it the original exception stays chained
        # as __context__ and ANY later traceback render ("During handling of the above
        # exception...") reprints its message — the analysed text with it.
        raise HTTPException(status_code=503, detail="pii scan unavailable") from None
    return {"spans": spans, "truncated": truncated}


@app.post("/analyze")
async def analyze(body: AnalyzeIn):
    """Turn `span_minutes` of one transcript ending at `prompt_id` into the workstream +
    inventory payload (see app.analysis.analyze.analyze_window). Coordinates in — a path and a
    prompt id — never text; the response itself carries no span/offset/text either (see
    test_analyze_response_carries_no_prompt_text).

    The response also carries the `effort` block: the two transcript signals that survived
    measurement out of six candidates — the diff magnitude (`authored_bytes`/`authoring_turns`)
    and the turn tempo (`fast_share`/`gaps`/`tempo`/`tempo_status`). Numbers and closed-vocabulary
    labels only; the byte sum is a LENGTH (`app/analysis/magnitude.py`'s `edit_bytes` returns an
    int and is the only thing permitted to read an edit payload) and the tempo is derived from
    timestamps alone. See `app/analysis/analyze.py`'s "effort block" section for the verdicts and
    for the four refuted candidates that deliberately do not appear.

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
    it in the parent instead.

    Until that third process exists, spaCy is resident in this parent and the sidecar's memory
    accounting is what keeps that honest: worker_manager.parent_reserve_mb() measures the parent
    rather than assuming 150 MB, so the inference worker's hard limit reflects the headroom that
    actually remains. See the block comment above _terms_status.

    Note also that this endpoint requires no model at all: it answers with the GLiNER2 worker
    never spawned (pinned in test_main.py against the real lifespan), which is the property that
    makes this a general analysis service rather than a GLiNER2 wrapper.
    """
    # Confinement BEFORE the open (see the block above _default_analyze_roots). 403, not 404:
    # a rejected path and a legitimate-but-unresolvable one are different facts, and collapsing
    # them would make an allowlist miss indistinguishable from a bad prompt id.
    if not _within_roots(body.path, _analyze_roots()):
        _count("analyze_rejected")
        raise HTTPException(status_code=403, detail="path is outside the configured transcript roots")
    _count("analyze_served")
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(
            None, _analyze_blocking, body.path, body.prompt_id, body.span_minutes)
    except PromptNotFound:
        raise HTTPException(status_code=404, detail="prompt not found in transcript")
    except StoreBehind:
        # 503, and the status matters. The window cannot be answered EXACTLY yet — the
        # reference series has not caught up with the transcript's bytes (or could not be
        # opened) — and that is transient: ingest is resumable, so the same request succeeds
        # moments later. 404 is wrong because it means "this prompt is not in this transcript",
        # a permanent fact; a 500 is wrong because it claims a defect. And 503 is the ONE status
        # the Go client's post() waits and retries through (sidecar/client.go: 503 -> wait +
        # retry with backoff, anything else -> ok=false), so this reads to the daemon as "not
        # ready yet, ask again" rather than as errAnalysisUnavailable, which would fail the
        # workstreams facet and publish the profile as "partial" for a facet that was one
        # append away from succeeding. That is the same reasoning the enrich pipeline already
        # applies to a sidecar that is not ready: queue, never degrade.
        _count("analyze_not_ingested")
        raise HTTPException(status_code=503, detail="window not yet ingested")
    except WindowExpired:
        # 410 Gone, and NOT the 503 above. The window's evidence was pruned (see
        # app/analysis/store.py's retention section), so it is permanently unanswerable and
        # retrying can never help — whereas 503 is the one status the Go client's post() waits
        # and retries through, which would spin forever here. 410 falls into that client's
        # `default: return false, false` ("genuine error — do not spin forever"), so the
        # workstreams facet fails and the profile publishes as `partial`. That is the honest
        # outcome for a facet whose inputs no longer exist, and it is the same idiom this
        # pipeline already uses for a pass that could not complete.
        #
        # Not 404 either: 404 means "this prompt is not in this transcript", and conflating
        # "never existed" with "expired" would hide a retention horizon set shorter than the
        # windows being requested. `counts.analyze_expired` plus the store block's
        # `serving_floor_ts` are what make that diagnosable.
        _count("analyze_expired")
        raise HTTPException(status_code=410, detail="window evidence has been pruned")
