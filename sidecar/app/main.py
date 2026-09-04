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

from app.buildversion import BUILD_VERSION
from app.analysis.analyze import PromptNotFound, StoreBehind, WindowExpired, analyze_window
from app.analysis.blockdigest import DEFAULT_MAX_BLOCKS, digest_blocks
from app.analysis.features import DEFAULT_MAX_FEATURE_ROWS
from app.analysis.features import feature_rows as feature_rows_for
from app.analysis.features import features as features_for, manifest as feature_manifest
from app.analysis.tick import DEFAULT_MAX_WINDOWS, tick as tick_windows_for
from app.analysis.dynamics import DEFAULT_SIZER
from app.analysis.ingest import ingest_file, is_current, session_of
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
            # The TEXT ENCODER child rides the same loop, and it needs to: it costs ~1.7-2.3 GB
            # resident (measured, bf16, real weights) against a budget already documented as
            # oversubscribed, and its duty cycle is ~100 messages a day. Nothing else would ever
            # release it — `/features` only ever spawns. Guarded so a machine with the toggle off
            # never builds a source at all.
            #
            # `poll()`, not `maybe_unload()`: it also samples the child's RSS into a high-water
            # mark for /metrics, and that sample has to happen on a timer rather than on a metrics
            # poll — the peak of a ~1.7-2.3 GB child is only visible if something is looking DURING an
            # encode, which is the lesson worker_manager's own guard was rewritten for.
            try:
                src = _TEXT_SOURCE
                if src is not None:
                    await loop.run_in_executor(None, src.poll)
            except Exception:
                pass
            # ⚠️ THE ATTRIBUTION WATCHDOG RIDES THIS LOOP, AND IT IS THE ONLY THING THAT CAN
            # FREE A HUNG ENCODER. The drain thread sits inside `Encoder.encode` holding the
            # encoder lock for the length of a block, so nothing that waits on that lock can
            # ever rescue it — the kill has to come from a thread that is not participating,
            # which is this one. It fires on SILENCE (no batch returned within the heartbeat
            # window), never on elapsed time: a slow block keeps beating and is left alone,
            # and killing one because it is slow is precisely the failure this whole change
            # exists to remove. Built lazily like the verifier's manager, so a machine that
            # never attributes never constructs a queue.
            try:
                if _ATTRIB_QUEUE is not None:
                    await loop.run_in_executor(None, _attrib_watchdog, _ATTRIB_QUEUE)
            except Exception:
                pass
            # ⚠️ THE ATTRIBUTION VERIFIER'S CHILD RIDES THIS LOOP TOO, AND FOR A WHOLE
            # BRANCH IT DID NOT. `_verifier_manager()` reuses WorkerManager's spawn half
            # and nothing else: `poll()` is the SOLE driver of kills_hard, kills_pressure,
            # kills_idle and recycles, so an unpolled manager has no RSS ceiling, no
            # recycling, no idle unload and no pressure eviction. After the first
            # borderline pair a llama.cpp child held its quantized weights and its 4096-token
            # KV cache for the entire life of the sidecar, unbounded and unmeasured, on a
            # budget AGENTS.md already documents as oversubscribed with two children on it.
            #
            # Looked up per tick rather than captured like `wm` above, because this one is
            # built LAZILY — the manager does not exist until a borderline pair actually
            # needs a verdict, which is the whole reason a machine that never attributes
            # never spawns the child. A closure over the value at lifespan time would
            # capture None forever.
            try:
                vwm = _state.get("verifier_wm")
                if vwm is not None:
                    await loop.run_in_executor(None, vwm.poll)
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
    verifier_wm = _state.get("verifier_wm")
    if verifier_wm is not None:
        # Only ever set if /attribute actually needed a verdict during this process's life —
        # see _verifier_manager(). Shutting it down here (rather than leaving it to process
        # exit) matches wm.shutdown() above: the child gets a chance to unwind before the
        # daemon's SIGTERM/SIGKILL reaps the process group.
        await asyncio.get_running_loop().run_in_executor(None, verifier_wm.shutdown)
    if _TEXT_SOURCE is not None:
        await asyncio.get_running_loop().run_in_executor(None, _TEXT_SOURCE.shutdown)
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


class ResolvedFacts(BaseModel):
    """Facts the DAEMON resolved because the sidecar structurally cannot.

    /analyze is confined to KELD_ANALYZE_ROOTS and so cannot open a repo's .git/config;
    the daemon can. This keeps the request COORDINATES-PLUS-RESOLVED-FACTS and still
    never text: every field here is an identifier the daemon read from git metadata.

    Modelled explicitly rather than as a free dict so the accepted keys are a CLOSED SET.
    A dict would make this a general side channel into the analysis, and the whole reason
    the daemon is the one allowed to read a repo's config is that its output is narrow and
    nameable; an open dict would give that away at the first caller who wanted one more
    thing. A key not listed here is dropped by Pydantic, not forwarded.

    Every field is legitimately EMPTY, and empty is not an error. `repo` is "" for a
    directory that was never `git init`ed -- a scratch dir, a mounted share, a documents
    tree -- and real work happens in those; `git_branch` is "" on a detached HEAD; `project`
    is "" without a .keld.toml. What consumes these must treat "" as ABSENT (the dimension
    is omitted) and never as a value.
    """
    repo: str = ""         # normalised host/owner/repo, "" when not a checkout
    git_branch: str = ""   # from the checkout root, worktree-aware
    project: str = ""      # .keld.toml name, NOT a git fact


class AnalyzeIn(BaseModel):
    path: str
    prompt_id: str
    span_minutes: int = 60
    # Optional, and None must change nothing about the answer (pinned by
    # test_resolved_facts_default_to_none_and_change_nothing): every deployed daemon older
    # than this field keeps working, and so does every local caller and the study.
    resolved: ResolvedFacts | None = None


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

    `resolved` is the same channel /analyze has, for the same reason: a tick characterises a
    WINDOW, and a tick-emitted window is not a lesser window (see TickResult in the Go client).
    It is per TRANSCRIPT rather than per window because that is the granularity the facts have
    -- a transcript is scoped to one project directory, so its checkout is one checkout.
    """
    path: str
    prompt_ids: list[str] = []
    cursor_ts: float | None = None
    now: float | None = None
    span_minutes: float = 60.0
    max_windows: int = DEFAULT_MAX_WINDOWS
    resolved: ResolvedFacts | None = None


class BlocksIn(BaseModel):
    """THE V2 ENTRY POINT: which CLOSED blocks of work does this transcript have, and what were
    they. Coordinates and instants only — never text, never a span, never an offset.

    v2 is a PATH, not a parameter (see app/analysis/blockdigest.py and the design spec). This is
    its own route with its own request model and its own module behind it; /analyze is untouched
    and still answers the v1 question — a 60-minute window anchored to a prompt. The one previous
    attempt threaded blocks through /analyze as an additive key and was reverted.

    ⚠️ **THERE IS NO `prompts` FIELD, and an older daemon that still sends one is TOLERATED.**
    This model takes pydantic's default `extra="ignore"`, so the key is dropped rather than 422'd
    — which is what lets this half and the daemon half ship in either order. `prompts` fed the
    deleted `covers` mapping (see app/analysis/blocks.py) and fed nothing else: a block is TIME
    end to end, and Atlas must join `event.ts in [block.start, block.end)` within the session for
    cost attribution anyway, which answers the display question too. Do not re-add it.

    `since_ts` is where the last call for this transcript stopped, compared against a block's
    START: the caller passes the last emitted block's END, and because blocks abut inside an
    active segment that admits the next block and excludes the one already emitted. Null means
    from the beginning of the session, so FORWARD-ONLY is the caller's choice to make by seeding
    its own cursor (matching KELD_WATCH_BACKFILL's default), not something this endpoint decides.

    `now` is the daemon's clock, injected rather than read here, for TickIn's reason: it is what
    the trailing block's idle settle is measured against, and a test that cannot move it cannot
    exercise the settle rule at all.

    `resolved` is the same channel /analyze and /tick have — facts the daemon read off a repo's
    .git/config, which this process is confined out of reading. It reaches ingest currency here
    (is_current), not the digest: the `repo` level is written as EVENTS, so by the time a block is
    rolled up the facts are already rows.
    """
    path: str
    since_ts: float | None = None
    now: float | None = None
    max_blocks: int = DEFAULT_MAX_BLOCKS
    resolved: ResolvedFacts | None = None


class FeaturesIn(BaseModel):
    """THE FEATURE ROWS one transcript has produced since a CURSOR. Coordinates and instants only
    — never text, never a span, never an offset, and never a prompt id.

    ⚠️ **THE SIDECAR ENUMERATES THE ANCHORS. The caller supplies a cursor, not a grid.** This
    process owns the reference-series store, so it is the only side that can see where the
    non-empty 5-minute bins and the CLOSED blocks are; a daemon asking for a grid it cannot see
    would have to guess one, and a guessed grid is silently wrong rather than visibly wrong. It is
    also exactly the shape `POST /blocks` already has (`{path, since_ts, now, resolved}` ->
    `{rows, watermark}`), and consistency with the sibling route outweighs either half's local
    preference. The anchor-instant form is kept as `POST /features/probe` for studies.

    `since_ts` is where the last call for this transcript stopped, compared against A ROW'S OWN
    INSTANT with `>`: rows are chronological across all three anchor kinds, so that admits the
    next row and excludes the one already sent. Null means from the beginning of the session, so
    FORWARD-ONLY is the caller's choice to make by seeding its own cursor (matching
    `KELD_WATCH_BACKFILL`'s default), not something this endpoint decides.

    `now` is the daemon's clock, injected rather than read here, for BlocksIn's reason: it is what
    the trailing bin's and the trailing block's idle settle are measured against, and a test that
    cannot move it cannot exercise the settle rule at all.

    `resolved` is the same channel /analyze, /ingest, /tick and /blocks have — facts the daemon
    read off a repo's .git/config, which this process is confined out of reading. It reaches
    ingest currency here (`is_current`), which is what makes the idle settle honest: silence in
    the SERIES is not silence on the MACHINE while there are unparsed bytes on disk.

    ⚠️ **There is no `capture` field and there must not be one.** Whether the capture rows exist
    is a property of the STORE, fingerprinted per transcript into `parse_state`, and the row
    reports it back as `capture_recorded`. A caller-supplied flag would let a daemon assert
    capture over a transcript whose rows were written without it — the incoherent-corpus failure
    the fingerprint exists to prevent. The same argument covers text: there is no `text` field
    either, and `text_recorded` is reported rather than asserted.
    """
    path: str
    since_ts: float | None = None
    now: float | None = None
    max_rows: int = DEFAULT_MAX_FEATURE_ROWS
    resolved: ResolvedFacts | None = None


class FeaturesProbeIn(BaseModel):
    """`S(t)` at one or more ANCHOR INSTANTS THE CALLER CHOSE. THE STUDY ENTRY, not the daemon's.

    Kept as a second, clearly separated route rather than folded into /features, because it is
    genuinely a different question and the two answers have different shapes. A study sweeps
    seeded anchors over a frozen corpus at instants no emitter would ever pick, needs them in the
    order it asked, and wants the RAW floats in `manifest()` order rather than the quantised wire
    form. Nothing on this route is emittable and nothing here has a cursor.

    `ats` are epoch seconds, the unit the whole block/feature path keys on (see BlocksIn on why an
    ISO string would be the wrong type for a cursor).

    `manifest` asks for the ordered slot names beside the rows. Off by default: it is
    `features.DIMS` strings, an order of magnitude more bytes than the vectors it describes, and
    a caller needs it once per build. `spec_sha` rides every response so a cached manifest can be
    invalidated without fetching it.
    """
    path: str
    ats: list[float] = []
    max_rows: int = DEFAULT_MAX_FEATURE_ROWS
    manifest: bool = False


class IngestIn(BaseModel):
    """A transcript advanced. The path is what the daemon's watcher knows and the byte offset to
    resume from is the store's own, not the watcher's — the appended bytes stay on the daemon's
    side of the call.

    `resolved` is the SECOND field, and it is here rather than only on /analyze because the
    `repo` level is written as EVENTS during ingest (`levels.events_for_turns`), not overlaid on
    a digest. Ingest is therefore the only place those rows can be created, and a transcript
    whose tail was ingested without them holds turns that nothing can later supply a `repo` row
    for — the same trap `ingest.terms_mode` exists for. /analyze keeps the field too, since its
    own `refresh=True` can be the first thing to ingest a transcript.
    """
    path: str
    resolved: ResolvedFacts | None = None


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


def _embed_stats():
    """The text encoder child's block for /metrics. Never raises, never has a side effect.

    ⚠️ Reads `_TEXT_SOURCE` DIRECTLY and never calls `_text_source()`: that one BUILDS the source,
    and a metrics poll must not create a subsystem it is only supposed to describe. With the
    toggle on but no `/features` call yet the honest answer is "not running", which is what
    `featuretext.embed_stats(None)` states.

    The import is deferred for the same reason `_text_source`'s is — the default path must not pay
    for the analysis text modules — but nothing here is heavy: no torch, no spawn, no transcript.
    """
    try:
        from app.analysis import featuretext
        return featuretext.embed_stats(_TEXT_SOURCE)
    except Exception as exc:                     # noqa: BLE001 - /metrics must always answer
        return {"error": type(exc).__name__}


def _resolved_dict(resolved):
    """`ResolvedFacts | None` -> a plain dict or None, for the analysis package.

    None in, None out -- deliberately NOT an empty dict. `app/analysis/` treats a falsy
    `resolved` as "the caller sent nothing", and the two spellings must not be able to mean
    different things there.
    """
    return None if resolved is None else resolved.model_dump()


def _analyze_blocking(path, prompt_id, span_minutes, resolved=None):
    """The whole of /analyze's work, on an executor thread.

    `resolved` arrives as a PLAIN DICT, not the Pydantic model: `app/analysis/` is imported by
    `scripts/refseries.py` as well as by this app and may not depend on pydantic (see that
    package's docstring). Converting at this boundary is what keeps the analysis importable by
    both front ends. None means the caller sent nothing, which must change nothing.

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
    # `block` turns on the BLOCK block (app/analysis/blocks.py): which measured block of work
    # this prompt fell in -- span and the two boundary reasons, nothing else. Opt-in in
    # `analyze_window` for the third time and for the same reason `sizer` and `prior` are: the
    # parse-path oracle holds no store and cannot cut a session into blocks. ADDITIVE -- the
    # window is unchanged and still the 60 minutes ending at the prompt; this only makes the
    # boundary the pre-registered study measured VISIBLE beside it.
    out = analyze_window(path, prompt_id, span_minutes, nlp, store=st, sizer=DEFAULT_SIZER,
                         prior=True, resolved=resolved, block=True)

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


def _ingest_blocking(path, resolved=None):
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
    return ingest_file(st, path, nlp, resolved)


def _tick_blocking(path, prompt_ids, cursor_ts, now, span_minutes, max_windows, resolved=None):
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
                            max_windows=max_windows, prior=True, resolved=resolved)


def _blocks_blocking(path, since_ts, now, max_blocks, resolved=None):
    """The whole of /blocks' work, on an executor thread.

    A QUERY, never a parse: `digest_blocks` reads the series and this function does not ingest,
    for the reason /tick does not either. The emitter behind this route runs on a TIMER, and
    paying a whole-file parse on a timer is exactly what the watcher's /ingest signal exists to
    avoid — a first whole-file ingest measured 5.1 s on a 90 MB transcript. What the store has
    not read yet simply is not closed yet, and the next call gets it.

    `is_current` is consulted rather than ignored, and it is what makes the trailing block's idle
    settle honest: silence in the SERIES is not silence on the MACHINE while there are unparsed
    bytes on disk, and a trailing block closed on that reading is a mid-session block mislabelled
    as settled. When the store is behind, only the "activity after" branch can close a block —
    which is sound whatever the tail is doing. Mid-session blocks are therefore never delayed by
    it.

    `_analysis_nlp()` is NOT resolved here, unlike /analyze and /ingest: nothing on this path
    parses a transcript, so no spaCy pipeline is needed and a multi-second load must not be
    triggered by a read. It is passed to `is_current` as None, which is the honest question for a
    caller that will not ingest — the terms-mode fingerprint only matters to something about to
    write rows.
    """
    st = _store()
    if st is None:
        raise StoreBehind("the reference-series store could not be opened")
    try:
        current = is_current(st, path, None, resolved)
    except OSError:
        # The transcript is gone or unreadable. Not fatal: the SERIES still holds everything that
        # was ingested from it, and those blocks are as closed as they will ever be. Treated as
        # "not current" so only the activity-after branch closes anything.
        current = False
    out = digest_blocks(st, path, since_ts=since_ts, now=now,
                        max_blocks=max_blocks, current=current)
    # Switched off means not reported, exactly as on /analyze: the regex half of
    # terms.candidates() needs no model and still ran at INGEST time, so returning its output
    # would contradict the status beside it and make the switch look like a performance knob.
    status = _terms_status()
    if status == _TERMS_DISABLED:
        for b in out["blocks"]:
            inv = b.get("inventory")
            if isinstance(inv, dict) and "named_terms" in inv:
                inv["named_terms"] = []
    for b in out["blocks"]:
        b["named_terms_status"] = status
    return out


@app.post("/blocks")
async def blocks(body: BlocksIn):
    """The CLOSED blocks of work in one transcript, each characterised. THE V2 PATH.

        a transcript -> blocks of work -> one characterisation per block

    No prompt anchor, no look-back window, no gap-filling: blocks TILE ACTIVE TIME, so coverage
    is 100% of activity by construction and there are no holes for a tick to patch. See
    app/analysis/blocks.py for where a block ends (a measured 20-minute cap and a 15-minute idle
    terminator, from a pre-registered four-arm study over 496 sessions) and
    app/analysis/blockdigest.py for when it may be EMITTED and what its digest holds.

    Returns `{"blocks": [...], "watermark": ...}`. A block carries its span
    (`start`/`end`/`block_minutes`, epoch seconds — the unit `since_ts` is in), the two boundary
    reasons from the closed `blocks.REASONS` vocabulary, and the same analysis payload /analyze
    publishes for a window: `workstreams`, `inventory`, `inventory_omitted`, `evidence`, `effort`,
    `dynamics`, `prior`. NO prompt ids: a block is (principal, session, span, reasons, facets),
    and the `covers` mapping that once carried them is deleted — see BlocksIn.

    `watermark` is returned even when no block is closed, because it is the one fact that
    separates "nothing has settled yet" from "this transcript has never been ingested" (null).

    Answers with NO refusal status of its own beyond the two below, and that is the design. A
    block whose evidence retention pruned is DROPPED rather than 410'd: this route answers about a
    RANGE, and one unanswerable block must not discard the answerable ones behind it — the same
    call /tick makes. A store that is behind is likewise not a refusal here, only fewer closed
    blocks.

    Like /analyze, /ingest and /tick it bypasses _dispatch and the single-flight runner: this is a
    series query, not inference, and it must answer while the runner is occupied or no worker has
    ever been spawned. It runs in the default executor so a long session's cut cannot stall the
    event loop out from under /health and /metrics.
    """
    # Confinement BEFORE anything else, the SAME allowlist /analyze, /ingest and /tick use — the
    # sidecar has no auth, and this reads a transcript's series as the daemon's user and answers
    # with its workspaces, branches and named terms. 403, not 404: a rejected path and an
    # unresolvable one are different facts.
    if not _within_roots(body.path, _analyze_roots()):
        # Counted as analyze_rejected rather than under a counter of its own: it is literally the
        # same allowlist and the operator reading is identical (the daemon and the sidecar
        # disagree about where transcripts live, or someone is probing). A blocks_rejected field
        # would mean editing app/metrics.py, and v2's rule is that main.py is the only existing
        # file this path touches.
        _count("analyze_rejected")
        raise HTTPException(status_code=403, detail="path is outside the configured transcript roots")
    now = body.now if body.now is not None else time.time()
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(
            None, _blocks_blocking, body.path, body.since_ts, now,
            body.max_blocks, _resolved_dict(body.resolved))
    except StoreBehind:
        # The store could not be OPENED at all. A store that is merely behind the file is not
        # this — it just closes fewer blocks (see _blocks_blocking).
        raise HTTPException(status_code=503,
                            detail="the reference-series store is unavailable") from None


# The text half's process-wide handle: the transcript reader, the per-message vector cache, and the
# encoder child. Lazily built and only when `KELD_TEXTEMBED` is on, so the default path imports no
# torch, spawns no child and opens no transcript. `None` is passed straight through to
# `feature_rows`, which then produces no `message` anchors and reports `text_recorded: False` —
# ABSENT, never zeros.
_TEXT_SOURCE = None
_TEXT_LOCK = threading.Lock()


def _text_source():
    """The `featuretext.TextSource`, or `None` when text embedding is switched off.

    The import is INSIDE the function for the reason `textembed` keeps its own heavy imports child-
    side: `featuretext` pulls the transcript reader and, transitively, the encoder handle, and the
    default path must not pay for either. Re-checked per call rather than latched, because the
    toggle is read from the environment and a latch would make an operator restart the sidecar to
    turn the feature OFF as well as on.
    """
    from app.analysis import featuretext, textembed

    if not textembed.enabled():
        return None
    global _TEXT_SOURCE
    with _TEXT_LOCK:
        if _TEXT_SOURCE is None:
            _TEXT_SOURCE = featuretext.TextSource()
        return _TEXT_SOURCE


def _features_blocking(path, since_ts, now, max_rows, resolved=None):
    """The whole of /features' work, on an executor thread.

    A QUERY of the series, never a parse of it, for `_blocks_blocking`'s reason: the emitter behind
    this route runs on a timer, and paying a whole-file series parse on a timer is exactly what the
    watcher's /ingest signal exists to avoid. What the store has not read yet simply has no row
    yet, and the next call gets it.

    ⚠️ **The TEXT half does open the transcript, and only when the toggle is on.** That is not a
    contradiction of the line above: `features.py` stays a pure store query (pinned by
    `test_features_never_opens_a_transcript`) and the reader lives in `featuretext.py` behind an
    injected seam. The cost is a `turns_in` pass — 0.79 s on a 90 MB transcript — plus a forward
    pass per message the cache has not already seen, which after the first call on a session is a
    handful. With `KELD_TEXTEMBED` off neither is paid at all.

    `is_current` is consulted for the reason /blocks consults it: it gates the IDLE settle that
    closes the trailing bin and the trailing block. Silence in the series is not silence on the
    machine while there are unparsed bytes on disk, and a trailing anchor emitted on that reading
    describes a span that is still growing.

    `_analysis_nlp()` is NOT resolved here — nothing on this path writes rows, so no spaCy pipeline
    is needed and a multi-second load must not be triggered by a read. The `term` level reaches the
    vector as five shape statistics computed off rows written at INGEST time; whether the terms
    pipeline ran is already fingerprinted into `parse_state`.
    """
    st = _store()
    if st is None:
        raise StoreBehind("the reference-series store could not be opened")
    try:
        current = is_current(st, path, None, resolved)
    except OSError:
        # The transcript is gone or unreadable. Not fatal: the SERIES still holds everything that
        # was ingested from it. Treated as "not current" so only the activity-after branch closes
        # anything — the same call /blocks makes.
        current = False
    return feature_rows_for(st, path, since_ts=since_ts, now=now, max_rows=max_rows,
                            current=current, text=_text_source())


def _features_probe_blocking(path, ats, max_rows, want_manifest):
    """The whole of /features/probe's work, on an executor thread. The study entry — anchors in,
    raw float rows out, no cursor and no text half."""
    st = _store()
    if st is None:
        raise StoreBehind("the reference-series store could not be opened")
    out = features_for(st, path, ats, max_rows=max_rows)
    if want_manifest:
        out["manifest"] = list(feature_manifest())
    return out


@app.post("/features")
async def features(body: FeaturesIn):
    """THE FEATURE ROWS one transcript has produced since a cursor. THE SIGNAL-EMBEDDINGS PATH.

        a reference series (+ the messages)  ->  quantised feature rows  ->  the daemon  ->  Atlas

    `docs/superpowers/specs/2026-08-26-signal-embeddings-design.md`. NOTHING IS PUBLISHED BY THIS
    ROUTE: it computes and returns, and the daemon's emitter (`internal/agent/features`) owns the
    cursor, the batching and the wire. See app/analysis/features.py for the disjoint shell ladder,
    the frozen vocabulary manifest, the normalisation transforms and why `workstreams.payload` is
    the wrong input; app/analysis/featuretext.py for the text half.

    Returns `{"schema", "feature_spec", "spec_sha", "dims", "session", "rows": [...],
    "watermark"}`. THE SIDECAR CHOOSES THE ANCHORS — see FeaturesIn — emitting three kinds, oldest
    first, every one of them strictly after `since_ts`:

      * `message` — one per user/assistant TURN, carrying that turn's own text vectors under
        `text` and its `role`. Present ONLY where the text half ran: a message has no lookback, so
        there is no structured vector to compute, and with `KELD_TEXTEMBED` off this kind is
        ABSENT rather than emitted empty. `anchor_id` is REQUIRED here and is the turn's uuid —
        instants are quantized to 0.1 s and two turns can collide on one tick, so the instant
        alone is not a key and a row published under one could upsert its neighbour.
      * `bin` — one per non-empty, CLOSED 5-minute bin, at the bin's end.
      * `block` — one per CLOSED block (app/analysis/blocks.py's measured 20-minute cap and
        15-minute idle terminator), at the block's end, plus the two boundary reasons.

    A `bin`/`block` row carries `structured`: the full `dims`-wide vector, int8-quantised as
    `{"dims", "scale", "q"}` with `q` base64 of two's-complement bytes. `dims` is redundant with
    `len(q)` ON PURPOSE — they are compared at the Go decode boundary, so a truncated payload is
    refused rather than read as a shorter vector.

    ⚠️ ABSENT IS NOT ZERO. `capture_recorded` and `text_recorded` say whether the capture slots and
    the per-shell text slots may be read at all; the `text` block is absent, never zeroed, wherever
    no encoder produced a vector. `watermark` is returned even with no rows, because it is the one
    fact separating "nothing new yet" from "never ingested" (null).

    ⚠️ `feature_spec` and `spec_sha` are not decoration. A vector is the one artefact where pooling
    two incompatible versions is invisible — the widths can even match while index 700 means two
    different things — so both ride every response and every row, and a corpus builder must
    partition on them. Same argument as `ingest.terms_mode`.

    Like /analyze, /ingest, /tick and /blocks it bypasses _dispatch and the single-flight runner:
    this is a series query, not inference, and it must answer while the runner is occupied or no
    worker has ever been spawned. It runs in the default executor so a long batch cannot stall
    the event loop out from under /health and /metrics.
    """
    # Confinement BEFORE anything else, the SAME allowlist /analyze, /ingest, /tick and /blocks
    # use. The sidecar has no auth, and this reads a transcript's series — and, with the text half
    # on, the transcript itself — as the daemon's user. 403, not 404: a rejected path and an
    # unresolvable one are different facts. Counted as analyze_rejected for /blocks' reason: it is
    # literally the same allowlist and a features_rejected field would mean editing
    # app/metrics.py, which this path does not touch.
    if not _within_roots(body.path, _analyze_roots()):
        _count("analyze_rejected")
        raise HTTPException(status_code=403,
                            detail="path is outside the configured transcript roots")
    now = body.now if body.now is not None else time.time()
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(
            None, _features_blocking, body.path, body.since_ts, now,
            max(0, int(body.max_rows)), _resolved_dict(body.resolved))
    except StoreBehind:
        raise HTTPException(status_code=503,
                            detail="the reference-series store is unavailable") from None


@app.post("/features/probe")
async def features_probe(body: FeaturesProbeIn):
    """`S(t)` at anchor instants THE CALLER chose. THE STUDY ENTRY — see FeaturesProbeIn.

    Returns `{"feature_spec", "spec_sha", "dims", "session", "rows": [...]}`, one row per anchor
    the store could characterise, IN THE ORDER ASKED. A row is `{"at", "session", "session_start",
    "capture_recorded", "text_recorded", "values"}` where `values` is `dims` RAW floats in
    `manifest()` order — numbers only, no identity string anywhere in it, and unquantised because a
    study wants the numbers rather than the transport. Anchors the store cannot characterise
    (before the session's first active bin, or a transcript never ingested) are SKIPPED rather than
    returned as a row of zeros, which would enter a training set as a real observation of nothing
    happening.

    No cursor, no watermark, no text half: this route emits nothing and advances nothing.
    """
    if not _within_roots(body.path, _analyze_roots()):
        _count("analyze_rejected")
        raise HTTPException(status_code=403,
                            detail="path is outside the configured transcript roots")
    loop = asyncio.get_running_loop()
    try:
        return await loop.run_in_executor(
            None, _features_probe_blocking, body.path, list(body.ats),
            max(0, int(body.max_rows)), bool(body.manifest))
    except StoreBehind:
        raise HTTPException(status_code=503,
                            detail="the reference-series store is unavailable") from None


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
            body.span_minutes, body.max_windows, _resolved_dict(body.resolved))
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
        result = await loop.run_in_executor(None, _ingest_blocking, body.path,
                                            _resolved_dict(body.resolved))
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
    #
    # `version` is this BUILD's version, not the model's and not a schema — it is
    # what lets the daemon see that the two separately-shipped halves disagree.
    # It rides /health rather than /metrics because /health is the one route the
    # daemon already calls on every sidecar and answers with the worker down.
    # "dev" means "cannot tell" (a source checkout or a local freeze); see
    # app/buildversion.py.
    wm = _state.get("wm")
    return {"ok": bool(wm) and wm.state != HELD, "model": MODEL_NAME,
            "state": wm.state if wm else "down", "version": BUILD_VERSION}


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
        store_stats=_store_stats(), embed_stats=_embed_stats(),
        verifier_stats=_verifier_stats(), attribution_stats=_attribution_stats(),
    )


def _attribution_stats():
    """The attribution queue's block for /metrics, or None if this process has never built one.

    Read WITHOUT constructing the queue: /metrics must never be the thing that creates state,
    and a machine with attribution off should report the absence rather than manufacture an
    empty queue to report zeros from."""
    q = _ATTRIB_QUEUE
    if q is None:
        return None
    try:
        return q.stats()
    except Exception:      # noqa: BLE001 — a metrics read must never fail the route
        return None


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


class ProjectsIn(BaseModel):
    projects: list[dict]


@app.post("/projects")
async def projects(body: ProjectsIn):
    """Org project definitions for block attribution. A cache write, not
    inference — bypasses _dispatch for the same reason /vocabulary does.
    Embedding happens lazily on the first /attribute that needs vectors."""
    from app.analysis import attribution
    h = attribution.set_projects(body.projects)
    return {"count": len(body.projects), "hash": h}


class AttributeIn(BaseModel):
    """WHICH DECLARED PROJECTS does this closed block belong to. Coordinates in, ids out.

    `start`/`end` are the block's span in epoch seconds — the unit `/blocks` answers in and the
    daemon's attribution job stores — and the span is read HALF-OPEN, `[start, end)`, so two
    abutting blocks can never both claim a turn on the boundary.

    `session_id` is the daemon's own key for the job (`(session, block.start)`) and is accepted
    so a request is self-describing in a log the daemon writes; the sidecar resolves the session
    from `path`, which is the only thing it can actually open. It is deliberately not checked
    against `ingest.session_of(path)`: a disagreement there would be a daemon bug, and refusing
    the block would turn it into a permanently unattributable one.

    `dims` are the block's own deterministic dimensions as `/blocks` already published them —
    repo, branch and friends. They feed the exact-match metadata boost, and they are the
    caller's facts rather than something re-derived here.
    """
    path: str
    session_id: str | None = None
    start: float
    end: float
    dims: dict = {}


class _EncoderNotReady(Exception):
    """The encoder child could not answer this call, carrying textembed's own status."""

    def __init__(self, status):
        super().__init__(status)
        self.status = status


class _VerifierUnavailable(Exception):
    """The verifier was needed and could not be loaded."""


class _EncoderAdapter:
    """`textembed.Encoder` in the shape `attribution.score_block` takes.

    ⚠️ **The two shapes differ and the difference is silent.** `score_block` wants
    `.encode(texts) -> [vec, ...]`; the production encoder returns `(vectors, status)` and
    NEVER raises — absent weights, a failed spawn and a hung child are all "no vectors, and
    here is why" (see `textembed.Encoder.encode`). Handed the raw encoder, `score_block` would
    zip a two-element tuple against the projects and silently score the block against the
    string `"ok"`. So the adaptation is here, and so is the decision about what each status
    MEANS to a block:

      * `ok`                            -> the vectors.
      * `skipped:disabled`              -> `skipped:disabled`: the text encoder is switched off
                                           on this machine, so no sweep will ever answer.
      * `degraded:weights_unavailable`  -> `degraded:weights_unavailable` (AC-4 as amended):
                                           no attribution at all, re-attributed once the
                                           daemon has provisioned the weights.
      * anything else                   -> `pending`. `degraded:encoder_unavailable` is retried
                                           on a cooldown by the encoder itself and
                                           `pending:encoding` is a backlog, so both are
                                           transient STATES — and `pending` is the one answer
                                           that costs the daemon's job no attempt. Never a
                                           confident answer produced without the encoder.

    Raising rather than returning `[]` is deliberate: an empty vector list would flow into the
    cosine as "similar to nothing" and publish a confident negative from a check that did not
    run."""

    def __init__(self, child, on_batch=None):
        self._child = child
        # The liveness callback, threaded through to `Encoder.encode`. `None` for every caller
        # that is not the attribution worker (the warm-up, the feature path), because nothing
        # else is being watched — a heartbeat with no watchdog behind it is just overhead.
        self._on_batch = on_batch

    def encode(self, texts):
        # The status is compared against textembed's own constant rather than a literal here:
        # that vocabulary is closed and a second spelling of "ok" in this file is exactly how
        # two halves drift. The import is deferred like every other analysis import in main.
        from app.analysis import textembed

        vectors, status = self._child.encode(texts, on_batch=self._on_batch)
        if status != textembed.STATUS_OK or len(vectors) != len(texts):
            raise _EncoderNotReady(status)
        return vectors


# The background warm-up: one thread at a time, so a burst of `/attribute` calls on a cold child
# cannot spawn a queue of them. Module-level so a test can join it.
_WARM_THREAD = None
_WARM_LOCK = threading.Lock()

# The attribution work queue and the single thread that drains it. See analysis/attribqueue.py
# for why the encode is off the request path at all; this is only the wiring.
_ATTRIB_QUEUE = None
_ATTRIB_WORKER = None
_ATTRIB_LOCK = threading.Lock()

# The verifier's own drift margin over its measured model_cost_mb (WorkerManager.ceiling_mb()).
# Deliberately its OWN env var and its OWN default rather than reusing
# KELD_SIDECAR_RSS_MARGIN_MB (1024): that number was measured for GLiNER2, a torch transformer
# whose transient activation memory scales with input length and batch size. The verifier is a
# llama.cpp Q4_K_M GGUF loaded with a fixed n_ctx (4096, see app/verifier.py) and no batching —
# its resident cost is dominated by the (mostly mmap'd) quantized weights plus one bounded KV
# cache, so it has much less headroom to grow between spawn and drift-recycle than a transformer
# whose worst case is an unbounded-length input. Half of GLiNER2's measured margin is a
# defensible starting point pending a real measurement on this model; env-overridable in the
# same style as the shared ceiling so an operator can tighten or loosen it without a code change.
_VERIFIER_RSS_MARGIN_MB = float(os.environ.get("KELD_VERIFIER_RSS_MARGIN_MB", "512"))

# The verifier's own WorkerManager, distinct from `_state["wm"]` (GLiNER2's). Built lazily by
# `_verifier_manager()`; guarded so two racing `/attribute` calls build at most one.
_VERIFIER_WM_LOCK = threading.Lock()


def _verifier_model_factory():
    """Load the verifier's GGUF model in the worker CHILD, never here. Mirrors `_model_factory`
    above: llama_cpp is imported only inside `verifier.Verifier.__init__`, which this calls only
    from inside the spawned child process (see `_verifier_spawn`) — so the FastAPI parent never
    imports it, matching the invariant `_model_factory` already keeps for torch/gliner2."""
    from app import verifier as verifier_mod
    return verifier_mod.Verifier()


def _verifier_spawn():
    """spawn_fn for the verifier's WorkerManager — the sibling of worker_manager.py's own
    `_default_spawn`, reusing the same child entry point (`worker.serve`) with a different
    model_factory. A second, independent Process/Queue pair: the verifier's child is recycled
    on its own schedule and never shares a process with the GLiNER2 worker."""
    import multiprocessing as mp
    from app.worker import serve
    ctx = mp.get_context("spawn")
    req_q, resp_q = ctx.Queue(), ctx.Queue()
    proc = ctx.Process(target=serve, args=(req_q, resp_q, _verifier_model_factory), daemon=True)
    proc.start()
    return proc, req_q, resp_q


def _verifier_reserve_rss():
    """What the verifier's WorkerManager must subtract from the shared memory budget before
    it decides its own hard limit: everything in this process tree that is NOT its own child.

    ⚠️ THREE CHILDREN NOW DRAW ON ONE BUDGET AND ONLY ONE OF THEM USED TO EXIST.
    `KELD_SIDECAR_MEM_BUDGET_MB` (4096) is a claim about the whole sidecar, and
    `WorkerManager.hard_limit_mb()` spends it as `budget - parent_reserve_mb()`. Two
    independent managers each reading `parent_reserve_mb()` as "the FastAPI parent" would
    each hand their own child the whole remainder — the budget promised twice, and the
    verifier's limit at its most generous exactly when the GLiNER2 worker and the encoder
    are both resident. That is the same shape as the constant `KELD_SIDECAR_PARENT_RESERVE_MB`
    the parent-reserve work replaced: a number asserting a configuration the service no
    longer has, wrong in the direction that under-protects.

    So this reports parent RSS PLUS the GLiNER2 worker's and the encoder child's high-water
    peaks. Three properties are deliberate:

      * PEAKS, not live samples, for `observe_parent_rss`'s own reason one manager over: a
        limit that tracked live siblings would relax the moment the GLiNER2 worker was
        recycled, with nothing about the risk having changed. High-water composes with
        `observe_parent_rss`'s own high-water latch to stay monotone.
      * The verifier's own child is excluded, since that is what the limit bounds.
      * The overrun is REPORTED, not absorbed: `budget_shortfall_mb()` is already computed
        from this reserve and appears in /metrics under `verifier`, so an oversubscribed
        machine says so rather than quietly exceeding the budget it promises.
    """
    total = _parent_rss_mb() or 0.0
    wm = _state.get("wm")
    if wm is not None:
        try:
            total += wm.peak_rss_mb or 0.0
        except Exception:
            pass
    src = _TEXT_SOURCE
    if src is not None:
        try:
            total += src.encoder.peak_rss_mb or 0.0
        except Exception:
            pass
    return total


def _verifier_stats():
    """The `verifier` block of /metrics — the same fields `worker` reports, for the same
    reason, about the other recycled child.

    ⚠️ It exists because this child shipped INVISIBLE. Its manager was never polled, so
    `recycles`/`kills` were structurally always zero and there was no RSS reading of any
    kind: a llama.cpp child holding its weights and a 4096-token KV cache for the sidecar's
    whole life looked exactly like a machine that had never spawned one. `peak_rss_mb` is
    reported beside the live sample for the measured reason `worker` and `embed` both are.

    Pure reads, like `_embed_stats`: it must never BUILD the manager (that would spawn a
    3 GB child off a metrics poll), so it reads `_state` directly and answers
    `built: false` when nothing has needed a verdict yet — which is an answer, not a gap.
    """
    wm = _state.get("verifier_wm")
    if wm is None:
        return {"built": False, "state": None}
    try:
        return {
            "built": True,
            "state": wm.state,
            "rss_mb": round(wm.worker_rss_mb(), 1),
            "peak_rss_mb": round(wm.peak_rss_mb, 1),
            "model_cost_mb": round(wm.model_cost_mb, 1) if wm.model_cost_mb else None,
            "ceiling_mb": round(wm.ceiling_mb(), 1) if wm.ceiling_mb() is not None else None,
            "hard_limit_mb": round(wm.hard_limit_mb(), 1) if wm.hard_limit_mb() is not None else None,
            # What the shared budget already owes the parent and the other two children —
            # see _verifier_reserve_rss. The number that says whether this child has any
            # headroom left at all.
            "siblings_reserve_mb": round(wm.parent_reserve_mb(), 1),
            "budget_shortfall_mb": (round(wm.budget_shortfall_mb(), 1)
                                    if wm.ceiling_mb() is not None else None),
            "recycles": wm.counts["recycles"],
            "kills": {"timeout": wm.counts["kills_timeout"],
                      "pressure": wm.counts["kills_pressure"],
                      "idle": wm.counts["kills_idle"],
                      "hard": wm.counts["kills_hard"],
                      "crash": wm.counts["crashes"]},
        }
    except Exception as exc:                     # noqa: BLE001 - /metrics must always answer
        return {"built": True, "error": type(exc).__name__}


def _verifier_manager():
    """The process's one verifier WorkerManager — a SECOND, independent instance beside
    `_state["wm"]` (GLiNER2's), reusing WorkerManager's spawn/recycle/RSS-ceiling machinery
    unmodified rather than inventing any.

    ⚠️ "REUSING THE MACHINERY" USED TO MEAN THE SPAWN HALF ALONE, and this docstring said
    otherwise. `WorkerManager.poll()` is the sole driver of the RSS ceiling, the recycle, the
    idle unload and the pressure eviction; nothing polled this instance, so the child held its
    weights and KV cache for the sidecar's whole life. `lifespan`'s `_poll_loop` now polls it
    (looked up lazily there, since this manager may not exist yet), `/metrics` reports it under
    `verifier`, and `_verifier_reserve_rss` is what keeps its share of the ONE memory budget
    honest now that three children draw on it.

    Built lazily here, on first genuine need: `/attribute`
    only ever reaches this function when a borderline pair actually needs a verdict, which is
    itself gated (see the route) on attribution having projects to score, the opt-out flag being
    off, and the weights being present on disk — so a machine with attribution off, the verifier
    opted out, or no weights provisioned never spawns this child at all."""
    wm = _state.get("verifier_wm")
    if wm is not None:
        return wm
    with _VERIFIER_WM_LOCK:
        wm = _state.get("verifier_wm")
        if wm is None:
            wm = WorkerManager(spawn_fn=_verifier_spawn, margin_mb=_VERIFIER_RSS_MARGIN_MB,
                               parent_rss_fn=_verifier_reserve_rss)
            _state["verifier_wm"] = wm
        return wm


def _verify_call(block_text, dims, project):
    """One verdict, via the dedicated verifier WorkerManager. The worker child is spawned
    lazily, on first call, by `_verifier_manager()`.

    A dead/unspawnable child (WorkerTimeout/WorkerUnavailable/WorkerError — the child failed to
    start, crashed mid-job, or returned an error) degrades to "the verifier could not answer"
    rather than crashing the request: translated to `_VerifierUnavailable`, which
    `_attribute_blocking` catches and re-runs the decision on the threshold alone, with the
    verifier reported `unavailable` — never a silent narrowing that looks like the full
    decision (AC-6)."""
    try:
        result = _verifier_manager().call({
            "op": "verify", "block_text": block_text, "dims": dims or {}, "project": project,
        })
    except (WorkerTimeout, WorkerUnavailable, WorkerError):
        raise _VerifierUnavailable() from None
    return result["verdict"], result["seconds"]


class _WorkerVerifier:
    """A verifier whose every verdict rides its OWN dedicated WorkerManager
    (`_verifier_manager`) — a second, recycled worker child holding the GGUF model, never the
    FastAPI parent and never the GLiNER2 worker.

    Replaces the old `_RunnerVerifier`, which piggybacked on `_state["runner"]` — the shared
    single-flight GLiNER2 dispatch went through — because that shared the wrong resource: what
    the verifier needs is its own worker with its own recycling, not a slot in someone else's
    queue. `WorkerManager.call()` already blocks its caller until the worker answers or the job
    deadline trips, so no bridging back to the event loop is needed here (unlike the old class,
    which had to `run_coroutine_threadsafe` onto the runner's thread). `verify()` runs on an
    executor thread already (called from `_attribute_blocking`), so blocking here blocks that
    thread, never the loop."""

    def verify(self, block_text, dims, project):
        return _verify_call(block_text, dims, project)


def _span_texts(path, start, end):
    """The block span's texts, BOTH streams, read through the ONE sanctioned door:
    `(user_texts, assistant_texts)`.

    `transcript.iter_turns` is the only function in this package that opens a transcript, and
    `textembed.messages_in` is the only one that lifts message text off a parsed turn — the same
    pair the text half uses (`featuretext.TextSource.vectors`). Reusing them is not tidiness:
    `turns_in` skips `tool_result` lines unparsed (which is what keeps a parse seconds long
    rather than minutes long), `messages_in` reads only `text` content blocks, and it drops
    command echoes and injected skill files, which are the harness talking to itself and not a
    person describing work.

    ⚠️ THIS RETURNED THE USER STREAM ONLY UNTIL 2026-09-03, ON AN ARGUMENT THAT WAS PLAUSIBLE
    AND UNMEASURED: "an assistant turn is this machine's own words about the block, and scoring
    a project against the model's own prose would attribute work to whatever the assistant
    happened to name." The eval recorded the cost the day it shipped — the external benchmark
    scored 0.929 with assistant text and the ported pipeline 0.823 without — and kept the rule.
    Measured on 61 real, labelled blocks (docs/notes/whats-next-attribution.md §9): user text
    alone put 28% of blocks on the right project; the whole block, mean-pooled and centred, put
    92% there. The user's words on a real block are often "continue" or "why is it so slow?";
    the reply is the paragraph that names the work. And 24 of the 25 blocks with NO user text
    at all — agent continuations of an earlier prompt — have assistant text, so this is also
    how the structurally-silent third of a machine's work becomes attributable. The feared
    failure ("whatever the assistant happened to name") did occur, on 4 of 61: a reply about a
    task queue landed a block on the wrong project. It is the minority mode, and MEAN pooling
    (mean, under attribution.SCORING) is what keeps one tangent from deciding a block.

    The two streams are returned SEPARATELY because they are not used alike downstream: both
    are scored, but only the user's words feed `concepts` (which publishes phrases) and the
    verifier prompt — see `attribution.attribute_block`.

    Half-open `[start, end)`: blocks abut inside an active segment, so a closed interval would
    let two of them claim the same turn.

    ⚠️ Text lives on this stack and in the encoder child, and nowhere else. Nothing here logs,
    persists or returns it upward — the route answers with ids."""
    from app.analysis import textembed
    from app.analysis.capture import epoch
    from app.analysis.transcript import iter_turns

    user, asst = [], []
    for m in textembed.messages_in(iter_turns(path), epoch):
        if not (start <= m.t < end):
            continue
        (user if m.stream == textembed.USER else asst).append(m.text)
    return user, asst


_OFFSETS = None
_OFFSETS_LOCK = threading.Lock()


def _offsets():
    """The process's centring state (attribution.Offsets), built once. Scalars on disk under
    KELD_HOME/state; see the class for the gate and why it is all-or-nothing."""
    global _OFFSETS
    with _OFFSETS_LOCK:
        if _OFFSETS is None:
            from app.analysis import attribution
            _OFFSETS = attribution.Offsets(attribution.default_offsets_path())
        return _OFFSETS


def _warm_encoder_async(child):
    """Bring the encoder child up OFF the request path, and embed the project docs while it is.

    The daemon's sweep is the retry loop, so a cold child answers `pending` immediately — but
    something has to actually warm it or every sweep answers `pending` forever. This is that
    something, and it is not a second queue: it holds no block, no backlog and no state, and one
    thread runs at a time however many blocks arrive. The work it does is the work the next call
    would otherwise pay first — `project_vectors` is memoised per project-list hash — so the
    spawn (~2.8 s warm, ~20 s cold) and the project embedding are both behind the caller.

    ⚠️ **THE SPAWN IS ASKED FOR, NEVER INFERRED FROM THE EMBEDDING.** `project_vectors` used to
    be the whole of this function, and it brings the child up only as a SIDE EFFECT of having an
    encode to run — which it has exactly once per project list, because it is memoised on that
    list's hash. So the second time the child went down (`maybe_unload` kills it after ~5 idle
    minutes) this thread had nothing to encode, spawned nothing, and `/attribute` answered
    `pending` on a child that nothing would ever start again. Nine blocks sat in that loop for two
    and a half hours in the smoke agent on 2026-09-03, with no error logged anywhere: the daemon
    was correctly holding a `pending` job without spending an attempt, and the sidecar was
    correctly reporting a child that was down. `warm()` is the missing sentence — up first, and
    the vectors after, whether or not there is anything to embed.
    """
    from app.analysis import attribution

    def run():
        try:
            # Independent of the memo below, and FIRST: a ready child is what the next sweep
            # needs, and it is needed even when every project doc is already embedded.
            child.warm()
            attribution.project_vectors(_EncoderAdapter(child))
        except Exception:      # noqa: BLE001 — a warm-up that failed is retried by the next sweep
            pass

    global _WARM_THREAD
    with _WARM_LOCK:
        if _WARM_THREAD is not None and _WARM_THREAD.is_alive():
            return _WARM_THREAD
        _WARM_THREAD = threading.Thread(target=run, name="keld-attribute-warm", daemon=True)
        _WARM_THREAD.start()
        return _WARM_THREAD


def _attrib_queue():
    """The process's attribution queue, built once."""
    global _ATTRIB_QUEUE
    with _ATTRIB_LOCK:
        if _ATTRIB_QUEUE is None:
            from app.analysis import attribqueue
            _ATTRIB_QUEUE = attribqueue.Queue()
        return _ATTRIB_QUEUE


def _attrib_worker_run():
    """Drain the attribution queue, one block at a time, forever.

    ⚠️ **One block at a time is a statement about the CAPACITY, not a simplification.** The
    encoder is a single child holding ~1.7 GB of weights behind one lock, so a second concurrent
    job could only ever block on the first — it would look like parallelism in the queue and be
    strict serialisation in fact, while doubling the RAM if it were ever given its own child.
    What was missing was never a second worker; it was a queue in front of the one that exists.

    Every block that reaches here has already been checked for the things that need no encoder
    (declared projects, the toggle, the weights, an empty span), so the only outcomes are a real
    answer, an encoder status, or a kill by the watchdog."""
    from app.analysis import attribqueue

    while True:
        # Resolved per iteration rather than captured once. In this process the queue is a
        # singleton built at first use, so the two are the same thing — but a captured reference
        # means a thread that outlives a rebuilt queue drains the dead one forever, silently and
        # with every counter looking healthy. Re-reading a module global costs nothing.
        q = _attrib_queue()
        job = q.take()
        if job is None:
            time.sleep(0.25)
            continue
        try:
            result = _attribute_job(job, q)
        except Exception as exc:   # noqa: BLE001 — a worker that dies stops every future block
            # The CLASS, never the message: an exception string from this path can carry a
            # filesystem path, and the same redaction rule the client-events gate applies
            # (`clientevents/redact.go`) holds for a line written on device. The attempt count
            # says how close this block is to being retired, which is what turns one line into
            # a pattern a reader can act on.
            log.warning("attribution job failed: %s (attempt %d/%d)",
                        type(exc).__name__, job.attempts + 1, attribqueue.MAX_ATTEMPTS)
            q.fail(job.key, "error")
            continue
        if result is None:
            # The watchdog killed this job mid-encode and has already re-queued it; `finish`
            # would be refused anyway, and calling `fail` here would spend a SECOND attempt on
            # one kill.
            continue
        q.finish(job.key, result)


def _attribute_job(job, q):
    """Encode and score ONE queued block. Returns its answer, or None if it was killed.

    The heartbeat is wired here and nowhere else: `beat` is handed to the encoder adapter, which
    threads it into `Encoder.encode`, which calls it as each batch returns. That is the only
    reason the watchdog can tell this thread apart from a wedged one."""
    from app.analysis import attribution
    from app import verifier as verifier_mod

    source = _text_source()
    if source is None:
        return attribution.stated(attribution.STATUS_SKIPPED_DISABLED)
    child = source.encoder

    try:
        texts, asst_texts = _span_texts(job.path, job.start, job.end)
    except OSError:
        # The transcript went away between the POST that queued this block and now. There is no
        # answer to be had and no later sweep can produce one, so it is a genuine failure and
        # spends an attempt — the bounded path, not an endless re-queue.
        q.fail(job.key, "unreadable")
        return None

    verifier_obj, verifier_absent = None, "opted_out"
    if verifier_mod.enabled():
        if verifier_mod.weights_path() is None:
            verifier_absent = "unavailable"
        else:
            verifier_obj = _WorkerVerifier()

    def beat(_i, _n, _ms):
        q.beat(job.key)

    # A job the watchdog killed mid-encode still returns an answer here — the encoder's status
    # rather than a decision. It is discarded by `Queue.finish`, which refuses any answer for a
    # job that is no longer the running one; there is deliberately no second check for it here.
    return _attribute_blocking(texts, job.dims, _EncoderAdapter(child, on_batch=beat),
                               verifier_obj, verifier_absent, asst_texts, job.key)


def _attrib_watchdog(q):
    """Kill the encoder child if the running job has gone silent. Returns whether it fired.

    Imports `attribqueue` for the window it reports; the module is already resident by the time
    a queue exists to be watched.

    Called from `lifespan`'s 1 s poll loop, beside the two `WorkerManager.poll()`s — the same
    place, and for the same reason: a guard that is constructed but never driven is not a guard.
    The kill is lock-free (`Encoder.kill_child`) because the thread it is rescuing holds the
    encoder lock by definition."""
    from app.analysis import attribqueue

    key = q.stalled()
    if key is None:
        return False
    source = _text_source()
    if source is not None:
        source.encoder.kill_child()
    outcome = q.fail(key, "heartbeat")
    # ⚠️ **KILLING A MODEL CHILD IS THE LOUDEST THING THIS SUBSYSTEM DOES AND IT WAS SILENT.**
    # The counters said it had happened; nothing in the log an operator tails said so, which is
    # the one shape this repo's standing rule forbids — a skip is stated, never inferred. It
    # names the WINDOW as well as the event, because "the encoder went quiet for 60s" and "the
    # encoder is slow" are the two readings a reader has to choose between, and only the first
    # is what fired. No key and no path: the block's identity is `session@start` and the path is
    # under someone's home, so both stay out of the line for `clientevents/redact.go`'s reason.
    log.warning("attribution: encoder silent for %.0fs, child killed; block %s",
                attribqueue.HEARTBEAT_TIMEOUT_S, outcome)
    return True


def _ensure_attrib_worker():
    """Start the drain thread if it is not already running. Idempotent, and safe to call from
    every request — the check is a liveness test on the thread, so a worker that somehow died
    is replaced rather than silently never restarted."""
    global _ATTRIB_WORKER
    with _ATTRIB_LOCK:
        if _ATTRIB_WORKER is not None and _ATTRIB_WORKER.is_alive():
            return _ATTRIB_WORKER
        _ATTRIB_WORKER = threading.Thread(target=_attrib_worker_run,
                                          name="keld-attribute-worker", daemon=True)
        _ATTRIB_WORKER.start()
        return _ATTRIB_WORKER


def _attribute_blocking(texts, dims, encoder, verifier_obj, verifier_absent, asst_texts=(),
                        block_key=None):
    """The whole of /attribute's decision, on an executor thread.

    Both failure modes it translates are ones the decision itself cannot see: whether the
    encoder child answered, and whether the verifier could load. Everything else is
    `attribution.attribute_block`."""
    from app.analysis import attribution

    offsets = _offsets()
    try:
        try:
            return attribution.attribute_block(texts, dims, encoder, verifier_obj,
                                               verifier_absent, asst_texts=asst_texts,
                                               offsets=offsets, block_key=block_key)
        except _VerifierUnavailable:
            # The threshold still answers, and the meta NAMES the verifier's absence rather
            # than letting a narrower decision look like the full one (AC-6). Re-run rather
            # than patch the meta: `verifier_absent` is an input to the decision, not a label
            # on it, and the re-run costs one re-encode of a block on a path that fires once
            # per process (the failed load is latched).
            return attribution.attribute_block(texts, dims, encoder, None, "unavailable",
                                               asst_texts=asst_texts, offsets=offsets,
                                               block_key=block_key)
    except _EncoderNotReady as exc:
        # Outside both, so the re-run's own encode is covered too.
        return _encoder_status_answer(exc.status)


def _encoder_status_answer(status):
    """One of `textembed.STATUSES` -> this block's answer. See `_EncoderAdapter`."""
    from app.analysis import attribution, textembed

    if status == textembed.STATUS_DISABLED:
        return attribution.stated(attribution.STATUS_SKIPPED_DISABLED)
    if status == textembed.STATUS_NO_WEIGHTS:
        return attribution.stated(attribution.STATUS_DEGRADED_WEIGHTS)
    return attribution.pending()


@app.post("/attribute")
async def attribute(body: AttributeIn):
    """WHICH DECLARED PROJECTS one closed block belongs to.

        a block span -> the user's own words -> scored against the declared projects -> ids

    Returns `{"status", "projects": [{"id", "confidence", "source"}], "attribution": {...}}`.
    `status` is `attribution.STATUSES`, the closed vocabulary mirrored Go-side as
    `enrich.Projects*`. **Coordinates in, ids out**: the request carries a path and two instants,
    the response carries ids, confidences, closed enums and integer millisecond timings. No text,
    no span, no offset, in either direction — `attribution.attribute_block` is where that is
    enforced and `test_attribution_endpoint.py` pins it.

    NOTHING IS PUBLISHED BY THIS ROUTE. The daemon's attribution job (internal/agent/attrib) owns
    the retry, the durability and the re-publish; this answers one question once.

    ⚠️ **`pending` is a STATE, not a failure, and the daemon's sweep is the retry loop.** A cold
    encoder child costs ~2.8 s warm / ~20 s cold to bring up and ~1.6 s per message to run, so a
    synchronous encode could not land inside any sane client timeout. This route therefore never
    waits for a model: it answers `pending` immediately, warms the child on one background thread
    (`_warm_encoder_async`) and lets the next sweep collect the answer. There is deliberately no
    second queue in this process — the durable job store already is one.

    Execution follows `/blocks`: the span read and the scoring run in the DEFAULT EXECUTOR so a
    long block cannot stall the event loop out from under `/health` and `/metrics`, while the
    verifier — the one genuine inference here — goes through its OWN dedicated worker child
    (`_WorkerVerifier`, backed by its own `WorkerManager`; see `_verifier_manager`), never the
    FastAPI parent and never the GLiNER2 worker's queue. This sidecar still never fans out
    inference — each model has its own single-flight, via its own manager — it just no longer
    shares the GLiNER2 dispatch's runner to get it.
    """
    # Confinement BEFORE anything else, the SAME allowlist /analyze, /ingest, /tick, /blocks and
    # /features use — the sidecar has no auth, and this OPENS A TRANSCRIPT as the daemon's user
    # and answers from what it says. 403, not 404: a rejected path and an unresolvable one are
    # different facts. Counted as analyze_rejected for /blocks' reason: it is literally the same
    # allowlist, and an attribute_rejected field would mean editing app/metrics.py.
    if not _within_roots(body.path, _analyze_roots()):
        _count("analyze_rejected")
        raise HTTPException(status_code=403,
                            detail="path is outside the configured transcript roots")
    from app.analysis import attribqueue, attribution, textembed

    projects, _ = attribution.current_projects()
    if not projects:
        # Answered without opening anything: with nothing declared to match against, reading a
        # person's words would be reading them for no purpose.
        return attribution.stated(attribution.STATUS_SKIPPED_NO_PROJECTS)

    # ⚠️ THE QUEUE IS CONSULTED BEFORE THE TRANSCRIPT IS OPENED, and the order is the point.
    # The daemon re-POSTs a `pending` block on every 45 s sweep, up to 24 of them, so a route
    # that read the span first would re-scan two dozen transcripts a minute to answer a question
    # it already knows the answer to. Asking the queue first makes re-asking free, which is what
    # lets `pending` be the normal case rather than an expensive one.
    q = _attrib_queue()
    key = f"{body.session_id}@{body.start}"
    done = q.collect(key)
    if done is not None:
        return done
    state = q.state(key)
    if state in (attribqueue.QUEUED, attribqueue.RUNNING):
        return attribution.pending()
    if state == attribqueue.QUARANTINED:
        # Four genuine failures on this block. `pending` would be a lie that costs the daemon a
        # re-POST every sweep forever; the encoder is present and simply cannot finish this one,
        # which is a degradation of the model half and is stated as such.
        return attribution.stated(attribution.STATUS_DEGRADED_WEIGHTS)

    loop = asyncio.get_running_loop()
    try:
        texts, asst_texts = await loop.run_in_executor(
            None, _span_texts, body.path, body.start, body.end)
    except OSError:
        # The transcript is gone or unreadable. Unlike /blocks and /features there is no series
        # to fall back on — the span's words ARE the input — so this is a refusal rather than a
        # narrower answer. Not 503: nothing about retrying makes an absent file readable, and the
        # daemon's job store bounds the retries of a genuine error.
        #
        # ⚠️ 410, AND EMPHATICALLY NOT 404. THE DAEMON NOW READS 404 AS "THIS SIDECAR HAS NO
        # /attribute ROUTE AT ALL" — version skew during a staged rollout — and HOLDS the job
        # forever with no attempt consumed, because the work becomes doable once the other half
        # updates. That is right for a missing route and catastrophic here: a deleted, rotated or
        # moved transcript would leave its job in the store permanently, re-POSTed every sweep,
        # one leaked job per affected block, competing for the 24-job sweep budget with no bound.
        # The comment this replaces justified 404 by leaning on exactly the bound that reading
        # made unreachable.
        #
        # 410 keeps the two facts apart at the status line rather than in a body string, and it
        # is the precedent this route already has one endpoint over: /analyze answers 410 for a
        # window whose evidence was pruned — permanently gone, do not keep asking — instead of
        # overloading 404. A 404 from this path can now only mean the route itself is absent,
        # which is the one thing a router can answer without any of our code running.
        #
        # ⚠️ Pinned across the language boundary by
        # TestTheAttributeRouteNeverAnswers404ForAnythingButAMissingRoute
        # (internal/agent/enrich/sidecar), which reads THIS function's source and drives every
        # status it raises through the real Go client. Change one half and that test fails.
        raise HTTPException(status_code=410,
                            detail="the transcript could not be read") from None

    source = _text_source()
    if source is None:
        # KELD_TEXTEMBED is off, so there is no encoder child on this machine and there will not
        # be one this daemon lifetime — a terminal, stated skip rather than an endless `pending`.
        return attribution.stated(attribution.STATUS_SKIPPED_DISABLED)
    child = source.encoder
    if textembed.weights_dir() is None:
        # AC-4 as amended: no model, no attribution — not a weaker one. attribute_block states it.
        return attribution.attribute_block(texts, body.dims, None, None, asst_texts=asst_texts)
    ready = child.state == textembed.READY
    if not texts and not asst_texts:
        # A block with no words in EITHER stream. Terminal — no later sweep can change it — so
        # it is answered here rather than deferred behind a cold child. (Until 2026-09-03 this
        # tested the user stream alone, which made every agent-only block terminal-empty while
        # its assistant turns described the work; see _span_texts.)
        return attribution.attribute_block(
            [], body.dims, _EncoderAdapter(child) if ready else None, None)
    if not ready:
        _warm_encoder_async(child)
        return attribution.pending()

    # ⚠️ **THE ENCODE IS HANDED OVER, NOT AWAITED, AND THAT IS THE WHOLE CHANGE.** This used to
    # run `_attribute_blocking` in the default executor and return its answer — 65-110 s of
    # encoding inside a call the daemon bounds at 2 minutes. When that bound fired it freed
    # nothing: the executor thread encoded on to the end and discarded the answer, and the next
    # request queued behind it on the encoder's lock. Handing the block to a queue makes the
    # deadline unreachable rather than merely larger; `attribqueue`'s docstring carries the
    # measurement and the reasoning.
    #
    # The block's identity is (session, start) — what Atlas upserts on and what the daemon's
    # durable job is keyed by — so it is also the dedupe key here, and a re-POSTed job is
    # folded into the centring baseline once rather than once per sweep.
    q.submit(attribqueue.Job(key, body.path, body.start, body.end, body.dims))
    _ensure_attrib_worker()
    return attribution.pending()


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
            None, _analyze_blocking, body.path, body.prompt_id, body.span_minutes,
            _resolved_dict(body.resolved))
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
