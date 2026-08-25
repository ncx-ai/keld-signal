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

## The effort block: two signals that are not references

`workstreams`/`inventory` answer "what was this window about", and every value in them is a
reference level. The `effort` block answers a different question — how much was AUTHORED and how
fast the turns came — and neither answer is a reference, which is why it is a separate block
rather than two more dimensions. Both come from material the reference levels structurally cannot
express, both were measured against the size test that refuted four sibling candidates, and both
are computed by modules that hold nothing but numbers: `app/analysis/magnitude.py` (`authored`)
and `app/analysis/latency.py` (`tempo`).

It is computed on BOTH paths — the store and the parse oracle — from one definition each, so the
field-for-field equivalence the suite asserts covers it too. That is why the tempo clock is "the
distinct timestamps of the window's reference events" (`Store.turn_times`) rather than a turn
table that only one of the two paths could have: see that method for the measurement showing the
two sets coincide on real transcripts.

Four refuted signals are deliberately NOT here — token weight (0.89% of dominant values flip),
tool-output volume (+0.552 against log volume, a restated tool-call count), error thrashing (4.8%
prevalence, below the 0.20 floor) and error rate (a window statistic, not a facet). The token
weight is nonetheless still COMPUTED and stored (`magnitude.TOKENS`) because the weighted rollup
uses it; what it is not is published.

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
import collections
import os
from datetime import datetime, timedelta

from app.analysis import COMPONENT_DEPTH, SCHEMA
from app.analysis import latency, magnitude, prior as prior_mod, window, workstreams
from app.analysis.dynamics import dynamics_for
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


def analyze_window(path, prompt_id, span_minutes=60, nlp=None, store=None, refresh=True,
                   sizer=None, end_ts=None, prior=False, resolved=None):
    """The `span_minutes` of work ending at `prompt_id` -> the workstream + inventory payload,
    served from the reference series.

    `end_ts` anchors the window at an INSTANT instead of at a prompt, and is what the tick uses
    (`app/analysis/tick.py`): work that no prompt's look-back reaches has no prompt id to end at.
    It is a second way to say WHERE the window ends and deliberately not a second way to compute
    what is in it — everything after the anchor is the one code path, and
    `test_analyze_window_anchored_by_time_agrees_with_the_same_window_by_prompt` pins the two
    against each other on the same instant. Both refusals apply unchanged: an anchored window is
    checked against the retention floor and the watermark exactly as a prompt's is, which is the
    whole reason the tick goes through this function rather than reaching into the store.

    `span_minutes` may be fractional. A gap closed by the next prompt's look-back is not a whole
    number of minutes, and rounding it up would reach back into the region that prompt already
    characterises — the one arithmetic that could make a tick double-count.

    `refresh` (the default) ingests whatever the transcript has grown by before answering. That
    is an INGEST, not a parse-and-discard fallback: it pays the parse cost once and leaves the
    store correct, where a fallback parse would pay it on every prompt and leave the store as
    empty as it found it — and would mean two implementations answering in production, only one
    of which is the one being measured. The daemon's watcher signal makes it usually a no-op
    (one `os.stat`); it is the self-heal for a signal that was never delivered, and the reason
    a never-ingested transcript answers at all.

    `sizer` (a `dynamics.Sizer`) adds the DYNAMICS block: the same window's recent slice read
    against its own longer baseline. OPT-IN, and that is a contract statement rather than a knob.
    `analyze_window_by_parse` below is the equivalence oracle — the suite asserts the two agree
    field for field — and it structurally cannot compute dynamics, because affording a second
    window is exactly what the store bought and the parse path did not have. Making the block
    opt-in is what keeps that equality expressible; defaulting it on would have meant either
    weakening the oracle comparison to "equal except for these keys" or asserting nothing about
    the digest at all.

    The sizer is confined to `[end - span_minutes, end)` — the window this function has already
    validated against the watermark and the retention serving floor — so no sizer, adaptive or
    otherwise, can open a retention surface `/analyze` has not already checked. The floor is
    handed to it for the one case it must handle itself: a baseline reaching below it.

    `resolved` is the FACTS THE CALLER RESOLVED because this process structurally cannot: a
    plain dict (never a model -- see the package docstring on pandas/pydantic) carrying the
    daemon's `repo`/`git_branch`/`project`. This endpoint is confined to KELD_ANALYZE_ROOTS
    precisely so it cannot open arbitrary paths as its user, and a repo's .git/config is outside
    that allowlist by construction; the daemon has no such confinement. So the resolution stays
    on that side and its OUTPUT travels here -- ONE resolution, feeding the analysis, rather
    than the daemon resolving for a prompt preamble while the analysis publishes a worse answer
    from a worse source. None (the default) changes nothing, which is what keeps every existing
    caller -- the study, the tests, an older daemon -- answering exactly as before.

    It is deliberately NOT persisted into the reference series. These are facts about the repo
    as it stands NOW, not observations at an instant, and writing them as events would mean a
    branch rename retroactively relabelling last week's windows.

    Raises `PromptNotFound` if the prompt is not in the transcript, and `StoreBehind` if the
    series cannot answer the window exactly. See the module docstring on why those are different.
    """
    st = store if store is not None else open_store()
    rl, start, end, effort = _rollup_from_store(st, path, prompt_id, span_minutes, nlp, refresh,
                                                end_ts=end_ts, resolved=resolved)
    out = _payload(rl, path, start, end, effort)
    if sizer is not None:
        out["dynamics"] = dynamics_for(st, session_of(path), end, span_minutes,
                                       sizer=sizer, floor=st.serving_floor())
    if prior:
        out["prior"] = _prior_block(st, path, session_of(path), rl, start, st.serving_floor())
    return out


def _prior_block(store, path, session, window_rl, start, floor):
    """The session as it stood BEFORE this window, contrasted with the window's own answer.

    `[floor or the beginning of time, quantize(window start))` -- HALF-OPEN on the right, and
    that half-openness is the whole of the causal claim: an event at the boundary instant is
    inside the window being characterised, and admitting it would put the window into its own
    frame of reference (see `prior.py` on why that reading is degenerate rather than merely
    weak). The prior's upper bound is the identical `lo` the digest's own rollup used, so the
    two intervals abut exactly and no evidence is counted on both sides.

    RETENTION CLAMPS, IT DOES NOT REFUSE. A prior starts before the window it contrasts, so it
    reaches under the serving floor whenever one exists -- refusing there would 410 every window
    on a pruned store for a block that is decoration beside the digest. `clamped` says the
    prior's lower bound is retention's floor rather than the session's own start, exactly as
    `FixedSizer` reports the same fact about a baseline: a silently shorter input is the defect
    `omittedNotice` exists to prevent, one level up.

    `clamped` is the mere EXISTENCE of a floor, and it cannot be sharpened into "the floor
    actually cut something off": the rows that would say whether this session began before it
    are precisely the rows retention deleted. So it over-warns for a session younger than the
    horizon rather than under-warning for one older than it, which is the direction a reader can
    do something about -- and unlike `FixedSizer`, whose baseline has a definite intended start
    to compare against, the prior's intended start is the session's, which nothing here knows.

    RECOMPUTED, never accumulated -- see `prior.py`. Nothing is cached between calls, which is
    what makes the answer checkable against the stored events at any moment.
    """
    hi = quantize(start.timestamp())
    lo = 0.0 if floor is None else float(floor)
    prior_rl = _rollup_at(store, path, session, lo, hi) if hi > lo else {}
    return {"clamped": floor is not None,
            "dimensions": prior_mod.compare(window_rl, prior_rl)}


def analyze_window_by_parse(path, prompt_id, span_minutes=60, nlp=None, resolved=None):
    """The same answer, computed by parsing the transcript. THE EQUIVALENCE ORACLE — see the
    module docstring. Not called by any endpoint, and must not become a fallback.

    It takes `resolved` for the same reason it takes `nlp`: the oracle's claim is that the two
    paths agree FIELD FOR FIELD on the same inputs, and a parameter only one of them accepted
    would weaken that to "agree except where they were asked different questions". The facts are
    the caller's either way -- neither path resolves them, and neither could."""
    rl, start, end, effort = _rollup_by_parse(path, prompt_id, span_minutes, nlp, resolved)
    return _payload(rl, path, start, end, effort)


def _payload(rl, path, start, end, effort):
    out = workstreams.payload(rl)
    out.update(
        schema=SCHEMA,
        session=session_of(path),
        window_start=start.isoformat(),
        window_end=end.isoformat(),
        evidence=int(sum(n for items in rl.values() for _, n in items)),
        effort=effort,
    )
    return out


def _effort(auth, tmp):
    """`(Authored, Tempo)` -> the published `effort` block.

    Numbers and closed-vocabulary labels only; see the module docstring. Every key is named for
    what it is, and each number ships with the count or status that makes it readable — the
    measured lesson being that a stated conclusion beat a stated number +36.7 to -3.3, and that a
    bare number invites a wrong reading (a model once answered `2659` to "which ticket?" because
    that was the window's event count).

    `authored_bytes` is `None` when nothing was costed and `fast_share` is `None` when there was
    no gap to measure. Neither is rendered as 0: `magnitude.authored` and `latency.tempo` each
    document why, and the difference between them is the point — a sum of no terms IS zero once
    we know we looked, while 0/0 never is.

    `tempo` states the conclusion; `authored` deliberately states none, because no measurement
    supplies a cut point on a byte sum (see `magnitude.authored`). Asymmetric on purpose.
    """
    return {
        "authored_bytes": auth.nbytes,
        "authoring_turns": auth.turns,
        "authored_status": auth.status,
        "fast_share": None if tmp.fast_share is None else round(tmp.fast_share, 3),
        "gaps": tmp.n_gaps,
        "tempo": tmp.reading,
        "tempo_status": tmp.status,
    }


def _effort_from_rows(rows):
    """The effort block from `events_for_turns` output — the ORACLE's half.

    Grouped by timestamp, not by row: the store sums a turn's several edit events into one
    `turn_magnitude` row (`_aggregate_mag`) and `turn_magnitudes` then groups by `ts`, so a
    per-row count here would disagree with the store on `authoring_turns` while agreeing on every
    byte. Zeros are dropped for the same reason — a zero magnitude is never stored.
    """
    per_ts = collections.defaultdict(float)
    recorded = False
    for r in rows:
        if r[5] != "mag":
            continue
        recorded = recorded or bool(r[8])
        if r[6] == magnitude.EDIT_BYTES:
            per_ts[r[0]] += float(r[8])
    return _effort(magnitude.authored(per_ts.values(), recorded=recorded),
                   latency.tempo([r[0] for r in rows if r[5] == "ref"]))


def _effort_from_store(store, session, lo, hi):
    """The same block, out of the series. Three indexed queries, no transcript opened."""
    return _effort(
        magnitude.authored(
            (v for _ts, v in store.turn_magnitudes(session, lo, hi, kind=magnitude.EDIT_BYTES)),
            recorded=store.has_magnitudes(session, lo, hi)),
        latency.tempo(store.turn_times(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))))


def _bounds(end_iso, span_minutes):
    end = datetime.fromisoformat(end_iso.replace("Z", "+00:00"))
    return end - timedelta(minutes=span_minutes), end


def _rollup_from_store(store, path, prompt_id, span_minutes=60, nlp=None, refresh=True,
                       end_ts=None, resolved=None):
    """`(rollup, start, end, effort)` for the window, out of the series. No transcript is
    opened.

    `resolved` reaches only the INGEST below, never the rollup: the `repo` level is written as
    events by `levels.events_for_turns`, so by the time a window is rolled up the facts are
    already rows and there is nothing left to overlay. That is the whole difference between a
    dimension the analysis ANALYSES and a label stamped onto the payload."""
    current = is_current(store, path, nlp, resolved)
    if refresh and not current:
        # FileNotFoundError if it is gone. This is also the path that installs the `repo` level
        # on a store first ingested without the daemon's facts: `is_current` reads False for a
        # stale repository fingerprint, so the refresh reparses and the level appears for the
        # transcript's whole history rather than only its tail.
        ingest_file(store, path, nlp, resolved)
        current = is_current(store, path, nlp, resolved)

    session = session_of(path)
    # An anchored window skips prompt resolution and NOTHING ELSE: the retention floor, the
    # currency check and the watermark guard below all still run, on the same window bounds.
    end_iso = end_ts if end_ts is not None else store.prompt_time(session, prompt_id)
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
    return (_rollup_at(store, path, session, lo, hi), start, end,
            _effort_from_store(store, session, lo, hi))


def _rollup_at(store, path, session, lo, hi):
    """`[lo, hi)` of one session -> a rollup, THE one way this file computes one.

    Extracted so the digest window and the SESSION PRIOR beside it cannot be computed
    differently. `prior.contrast`'s `departure` subtracts the session's share from the window's,
    which is only a number if both were measured the same way -- and `language` is the dimension
    that makes that real, since `lang` rows exist ONLY through reconcile (see its module
    docstring) and reconcile's answer depends on WHICH declarations are in scope. A prior served
    from the stored, FILE-scoped reconciliation while the window re-scoped its own would compare
    two different quantities and look entirely plausible doing it.

    That is also the deliberate divergence from `dynamics`, which passes NO `exclude_slots` and
    takes the precomputed bins: its two sides are compared only against each other, so consistency
    with the digest costs it nothing and the bins buy it a long baseline. Here the comparison
    CROSSES that boundary, so the digest's method wins and the bins are forgone.
    """
    rows = store.window_rows(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    recon_rows, _stats = reconcile(pending_in(store, path, lo, hi), COMPONENT_DEPTH)
    return window.rollup(rows + recon_rows)


def _prompt_time(path, prompt_id):
    """The target prompt's own timestamp, by a pass over the transcript. The oracle's half of
    what the `prompt` index now answers from the store."""
    for o in iter_turns(path):
        if o.get("uuid") == prompt_id:
            return o["timestamp"]
    raise PromptNotFound(prompt_id)


def _rollup_by_parse(path, prompt_id, span_minutes=60, nlp=None, resolved=None):
    """`(rollup, start, end, effort)` for the window, by parsing. See `analyze_window_by_parse`.

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
    rows, pending, _n_lines = events_for_turns(turns, path, root, (), nlp, resolved=resolved)
    # `pending` is reconciled prose paths, not optional decoration: `file`/`dir`/`ext`/`lang`/
    # `component` rows are ONLY ever produced by reconcile() (see its module docstring), so
    # skipping this step would leave the "language" workstream permanently unattributed.
    recon_rows, _stats = reconcile(pending, COMPONENT_DEPTH)
    # `rows`, not `rows + recon_rows`: a reconcile row copies its turn's timestamp, and the store
    # path excludes that slot (see `Store.turn_times`), so including it here would break the very
    # equality this oracle exists to check.
    return window.rollup(rows + recon_rows), start, end, _effort_from_rows(rows)
