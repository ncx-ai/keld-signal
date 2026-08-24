"""One window of a transcript, analysed into the workstream payload — as a QUERY, not a parse.

Takes COORDINATES, never text — the same rule as `spool.Pointer`. The window ENDS at the prompt
and looks back, because the question a cost report asks is "what was this hour of work about",
and the work that produced a prompt precedes it. That is also why a caller needs nothing but a
transcript path and a prompt id: no timestamp bookkeeping of its own.

## Why this reads a store

Every prompt used to re-parse the whole transcript. Measured, a 60-minute window holds a mean of
3.8 user prompts (max 20 over 370 windows), so one hour of work was characterised ~4x and up to
20x, each time from scratch: 0.66-1.0s per call on a 66 MB transcript, of which 0.31s was
`scan_workspace` alone. The same work, ingested once into the reference series (`store.py`,
`ingest.py`), answers a window in about a millisecond and leaves the series behind for the
dynamics that nothing could compute before. Design:
docs/superpowers/specs/2026-08-23-incremental-reference-series-store-design.md.

**There is ONE production path: the store.** `analyze_window_by_parse` below is retained as the
EQUIVALENCE ORACLE — the test suite asserts the two agree field for field on a real multi-turn
transcript — and must not be wired up as a fallback. A silent switch between two implementations
of the same answer is precisely what makes a divergence between them undetectable, which is the
failure this repo's "no health-gated substitute" rule exists to prevent. If the store cannot
answer, `/analyze` fails visibly and the caller retries.

## Two refusals, and why they are not the same as "prompt not found"

- **The store is behind the file** (`StoreBehind`). Transient by construction — ingest is
  resumable and the caller retries — so it must not look like a 404, which means "this prompt is
  not in this transcript" and is permanent. `main.py` maps it to 503, the one status the Go
  client's `post()` waits and retries through.
- **The prompt id is not in the transcript** (`PromptNotFound`). Permanent. Distinguishable from
  the above only because the store knows whether it has read everything the file holds; a
  prompt absent from a store that is NOT caught up is the first case, not this one.
- **The window's evidence was pruned** (`WindowExpired`). Permanent, and a THIRD case rather
  than either of the above: the prompt is in the transcript and the store is caught up, but the
  events the window is made of are gone. `main.py` maps it to 410, which the Go client treats as
  a genuine error rather than retrying — correct, since retrying can never help. See
  `store.py`'s retention section for why a refusal is the only honest answer here.

## The window edge is quantized, and has to be

Row timestamps are stored at the series' own resolution — `levels.quantize`, 0.1s, which
predates this task and is pinned by the frozen-corpus identity gate. A boundary finer than that
is not representable in the stored rows, so the window is evaluated at that resolution:
`[quantize(start), quantize(end))`. The reported `window_start`/`window_end` stay the exact
instants, because that is what they mean.

Against the old exact-timestamp turn selection this moves at most one turn across an edge, and
only when a turn falls in the same 0.1s bucket as a boundary. Measured over 103 real prompts
across 30 transcripts (313 MB): 9 prompts see one such turn move, changing an `evidence` count
by 1 and the share derived from it; 3 see a VALUE change, all of them in `inventory.harness_tools`,
where the published set is a fixed top-12 cut by POSITION, so a ±1 count reorders the pair
straddling the cut — the unrepresented-tie effect `workstreams.payload` already documents. No
allocation dimension changed value in the sample.
"""
import os
from datetime import datetime, timedelta

from app.analysis import COMPONENT_DEPTH, SCHEMA
from app.analysis import window, workstreams
from app.analysis.ingest import (RECONCILE_SLOT, ingest_file, is_current, pending_in,
                                 session_of)
from app.analysis.levels import events_for_turns, quantize
from app.analysis.reconcile import reconcile
from app.analysis.store import open_store
from app.analysis.transcript import _order_key, iter_turns, turns_between


class PromptNotFound(Exception):
    """The prompt id is not in this transcript. Distinct from an empty window: one is a
    resolution failure, the other is a fact about the work. PERMANENT — a caller must not
    retry it."""


class WindowExpired(Exception):
    """The window starts before the reference series' retention floor: its evidence was pruned.
    PERMANENT — a caller must not retry, because a pruned row is never coming back.

    Raised rather than answering from whatever survived, and that is not a nicety. `/analyze`
    serves every window with `exclude_slots=(RECONCILE_SLOT,)`, and `Store.window_rows` answers
    an excluded-slot query entirely from `event` — a `bin` row has no slot dimension to filter on
    — so for the digest path a pruned event has no rollup standing behind it. Measured on the
    test fixture: prune the events, keep every bin, and the window comes back with `evidence`
    179 -> 36, `project`/`branch`/`model` silently `null`, and a confident 0.833 share computed
    from a fifth of the data, with `is_current()` still True so nothing objects. That is a
    plausible wrong number, which is worse than no number.

    Distinct from `StoreBehind` for the reason its own docstring gives in reverse: that one is
    transient and the Go client waits and retries through the 503 it maps to, which is right for
    a window one append from answerable and catastrophic for one that can never be answered.
    """


class StoreBehind(Exception):
    """The reference series does not yet hold everything this transcript's bytes say, so the
    window cannot be answered exactly. TRANSIENT — the caller retries and ingest catches up.

    Raised rather than serving what the store happens to hold. A window missing its last
    minutes, or computed from a prefix whose whole-file evidence was incomplete, is a
    confidently wrong attribution, which is the failure mode this project keeps hitting.
    """


def analyze_window(path, prompt_id, span_minutes=60, nlp=None, store=None, refresh=True):
    """The `span_minutes` of work ending at `prompt_id` -> the workstream + inventory payload,
    served from the reference series.

    `refresh` (the default) ingests whatever the transcript has grown by before answering. That
    is an INGEST, not a parse-and-discard fallback: it pays the parse cost once and leaves the
    store correct, where a fallback parse would pay it on every prompt and leave the store as
    empty as it found it — and would mean two implementations answering in production, only one
    of which is the one being measured. The daemon's watcher signal makes it usually a no-op
    (one `os.stat`); it is the self-heal for a signal that was never delivered, and the reason
    a never-ingested transcript answers at all.

    Raises `PromptNotFound` if the prompt is not in the transcript, and `StoreBehind` if the
    series cannot answer the window exactly. See the module docstring on why those are different.
    """
    st = store if store is not None else open_store()
    rl, start, end = _rollup_from_store(st, path, prompt_id, span_minutes, nlp, refresh)
    return _payload(rl, path, start, end)


def analyze_window_by_parse(path, prompt_id, span_minutes=60, nlp=None):
    """The same answer, computed by parsing the transcript. THE EQUIVALENCE ORACLE — see the
    module docstring. Not called by any endpoint, and must not become a fallback."""
    rl, start, end = _rollup_by_parse(path, prompt_id, span_minutes, nlp)
    return _payload(rl, path, start, end)


def _payload(rl, path, start, end):
    out = workstreams.payload(rl)
    out.update(
        schema=SCHEMA,
        session=session_of(path),
        window_start=start.isoformat(),
        window_end=end.isoformat(),
        evidence=int(sum(n for items in rl.values() for _, n in items)),
    )
    return out


def _bounds(end_iso, span_minutes):
    end = datetime.fromisoformat(end_iso.replace("Z", "+00:00"))
    return end - timedelta(minutes=span_minutes), end


def _rollup_from_store(store, path, prompt_id, span_minutes=60, nlp=None, refresh=True):
    """`(rollup, start, end)` for the window, out of the series. No transcript is opened."""
    current = is_current(store, path, nlp)
    if refresh and not current:
        ingest_file(store, path, nlp)                 # FileNotFoundError if it is gone
        current = is_current(store, path, nlp)

    session = session_of(path)
    end_iso = store.prompt_time(session, prompt_id)
    if end_iso is None:
        # Absent from a store that has read the whole file is a fact about the transcript;
        # absent from one that has not is a fact about the store. Collapsing them would either
        # spin forever on a genuine 404 or discard a window that was one append from answerable.
        if current:
            raise PromptNotFound(prompt_id)
        raise StoreBehind(path)
    start, end = _bounds(end_iso, span_minutes)
    # Retention's floor, checked BEFORE staleness. A window can be both stale and expired, and
    # reporting the transient failure would make the caller retry forever on something
    # permanent, so the permanent one wins. See `WindowExpired` on why a pruned window is
    # refused rather than answered from what survived.
    floor = store.serving_floor()
    if floor is not None and quantize(start.timestamp()) < floor:
        raise WindowExpired(path)
    if not current:
        raise StoreBehind(path)
    # The spec's rule, stated in its own terms: never serve past the watermark. `is_current`
    # above is the stronger condition and is what fires in practice (see its docstring on why
    # whole-file evidence makes currency the real precondition); this is the narrower guard it
    # cannot express — a checkpoint that advanced past turns that were never stored, where the
    # bytes all look accounted for and the last minutes of the window are simply missing.
    wm = store.watermark(path)
    if wm is None or _order_key(wm) < end:
        raise StoreBehind(path)

    lo, hi = quantize(start.timestamp()), quantize(end.timestamp())
    # The reconcile rows are EXCLUDED from the query and recomputed at this window's scope.
    # `reconcile` resolves a prose path against every path a tool DECLARED, so its answer
    # depends on which declarations are in scope; the store holds the whole file's
    # reconciliation, and slicing that by timestamp would attribute a path using a declaration
    # the window never saw. `pending_in` re-scopes it for ~1 ms. Excluding a slot also forgoes
    # the precomputed bins (see `Store.rollup_window`), which for one hour costs nothing.
    rows = store.window_rows(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    recon_rows, _stats = reconcile(pending_in(store, path, lo, hi), COMPONENT_DEPTH)
    return window.rollup(rows + recon_rows), start, end


def _prompt_time(path, prompt_id):
    """The target prompt's own timestamp, by a pass over the transcript. The oracle's half of
    what the `prompt` index now answers from the store."""
    for o in iter_turns(path):
        if o.get("uuid") == prompt_id:
            return o["timestamp"]
    raise PromptNotFound(prompt_id)


def _rollup_by_parse(path, prompt_id, span_minutes=60, nlp=None):
    """`(rollup, start, end)` for the window, by parsing. See `analyze_window_by_parse`.

    Turn selection is quantized to match the series' resolution (see the module docstring), so
    that this and `_rollup_from_store` are comparable at all: the store cannot distinguish two
    turns inside one 0.1s bucket, and a boundary drawn inside a bucket is not a boundary the
    rows can express.
    """
    start, end = _bounds(_prompt_time(path, prompt_id), span_minutes)
    lo, hi = quantize(start.timestamp()), quantize(end.timestamp())
    turns = [o for o in turns_between(path, (start - timedelta(seconds=1)).isoformat(),
                                      (end + timedelta(seconds=1)).isoformat())
             if lo <= quantize(_order_key(o["timestamp"]).timestamp()) < hi]

    # `root` is reconcile.py's machine-scope key: in production a transcript's path is
    # `<root>/<projdir>/<session>.jsonl` for one of `--roots` (refseries.py), so the collection
    # root is recovered the same way here rather than inventing a second meaning for it.
    # `repo_root=()` is resolve_workspace's own no-fixture default (levels.py's comment on the
    # `repo_root or ()` line): this layer has no configured filesystem repo-root list to confirm
    # a candidate checkout against, and doesn't need one to resolve a workspace from transcript
    # evidence alone.
    root = os.path.dirname(os.path.dirname(path))
    rows, pending, _n_lines = events_for_turns(turns, path, root, (), nlp)
    # `pending` is reconciled prose paths, not optional decoration: `file`/`dir`/`ext`/`lang`/
    # `component` rows are ONLY ever produced by reconcile() (see its module docstring), so
    # skipping this step would leave the "language" workstream permanently unattributed.
    recon_rows, _stats = reconcile(pending, COMPONENT_DEPTH)
    return window.rollup(rows + recon_rows), start, end
