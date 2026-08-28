"""THE V2 PATH: one BLOCK of work, characterised. Not a window, and not `/analyze`.

    a transcript -> blocks of work -> one characterisation per block

Design: `docs/superpowers/specs/2026-08-25-v2-block-path-design.md`. The cutter that decides where
a block ends is `app/analysis/blocks.py` and is not touched here; this module answers the other
half -- what one block WAS -- and decides which blocks may be emitted at all.

## V2 IS A PATH, NOT A PARAMETER

The block cutter was once bolted onto v1 as an additive `block` key on `/analyze`, an endpoint
that still characterises a 60-MINUTE WINDOW ANCHORED TO A PROMPT. Every published workstream
stayed v1 and the block rode along as metadata, which made a stepping stone look like a
destination. That was reverted, and the rule that replaced it is the reason this file exists at
all: **v2 lives in its own module, behind its own entry point (`POST /blocks`), and can be deleted
or promoted wholesale without unpicking v1.**

So `analyze.py` is NOT imported here, not called here, and not modified. Nothing in this file
reaches sideways into it.

⚠️ **THE COST OF THAT RULE, STATED RATHER THAN HIDDEN: `_rollup_at`, `_effort_at` and `_prior_at`
below are second implementations of arithmetic `analyze.py` also performs.** The design says a
helper both paths need MOVES DOWN into a module both import -- but `analyze.py` is frozen until
v1 retires, and moving anything out of it is an edit to it. The resolution is that the duplication
is against the LOWEST layer available (`store.window_rows`, `reconcile`, `window.rollup`,
`magnitude.authored`, `latency.tempo`, `prior.compare`) and never against `analyze.py`'s own
private helpers: the measured definitions live in those modules and both paths call them, so what
is duplicated is a five-line composition, not a definition. When v1 retires this file becomes the
only copy; until then a change to one of those primitives still reaches both paths, which is the
property that actually matters.

## WHEN IS A BLOCK CLOSED -- the only genuinely new concept

    closed(b) == b.end <= <the store has ingested through b's last bin>
                 and ( activity exists after b.end          # something later proves b.end is real
                       or now - b.end >= IDLE_SECONDS )     # or enough silence has settled it

⚠️ **The disjunction is load-bearing.** An earlier draft required silence for EVERY block, which
emits nothing at all during an active session -- a full working day would produce its first block
only once the person stopped. A `budget` or `idle` cut is FINAL the moment it is reached, because
later activity starts a NEW block and cannot alter this one. So any block with activity after it
is settled immediately, and the ONLY provisional block is the trailing one, whose end is "where
the data currently stops" rather than a real boundary.

⚠️ **AND THE WATERMARK CLAUSE IS EVALUATED AT BIN RESOLUTION, WHICH IS A CORRECTION TO THE SPEC'S
LITERAL WORDING.** Taken as written -- `b.end <= watermark`, instant against instant -- the
trailing block can never close. A segment ends at its last active bin's END (`bin + BIN_SECONDS`,
`blocks.active_segments`) while the watermark is the last TURN's timestamp, which lies INSIDE that
bin; so `b.end > watermark` for the trailing block essentially always, and the idle branch the
spec added specifically to settle it would never be reached. Every session's final block would be
lost forever, and nothing would say so. The clause is therefore compared against the END OF THE
BIN THE WATERMARK FALLS IN: blocks are cut on 5-minute bins, so the ingestion frontier has to be
expressed at the same resolution the boundaries are.

That is safe, and not merely convenient, because of an arithmetic the idle threshold supplies.
`IDLE_SECONDS = IDLE_BINS * BIN_SECONDS` is measured from `b.end` -- the bin's end, not the last
turn -- so once it has elapsed, any turn that arrives next sits at least `IDLE_BINS` EMPTY BINS
after the trailing active bin. `active_segments` therefore splits there, the arrival opens a new
segment and a new block, and this block's start and end are both final. Measuring the idle from
the watermark instead would NOT give that: a turn arriving exactly `IDLE_SECONDS` after the last
turn can leave only two empty bins between them, which is not an idle split, and the trailing
block would silently grow after it had been emitted.

For every non-trailing block the watermark clause is implied rather than binding -- a later block
holds an active bin, so the store already holds events at `ts >= b.end` and the watermark is past
it. What the clause really guards is the two cases where the store cannot speak: never ingested
(watermark `None` -> nothing is closed) and a store whose bins somehow run past its own
checkpoint.

⚠️ **A STORE THAT IS BEHIND THE FILE DISABLES THE IDLE BRANCH.** Silence in the SERIES is not
silence on the MACHINE when there are unparsed bytes on disk, and a trailing block emitted on that
reading is a mid-session block mislabelled as settled. `digest_blocks` takes `current`
(`ingest.is_current`, the same whole-file precondition `/analyze` uses) and, when it is false,
closes blocks only on the "activity after" branch -- which is sound whatever the tail is doing.

**This is NOT the tick's frontier and must not be built on it.** `coverage.frontier` /
`tail_closed` reason about which FUTURE PROMPTS might sweep BACKWARDS over a moment, because a
prompt's window reaches back an hour. A block reaches nowhere: it is a span with a determined end,
and nothing later can overlap it. The two problems are different and the weaker rule is the
correct one here.

## WHAT ONE BLOCK'S DIGEST IS

Composition, not new analysis. Every part is already span-parameterised and none of them knows
what a prompt is:

  * `workstreams.payload` over `window.rollup` of the block's own bounds -- the seven ALLOCATION
    dimensions and the nine INVENTORY ones, in exactly the shape `/analyze` publishes them.
  * `effort` -- `magnitude.authored` + `latency.tempo` + `latency.percentiles` over the same
    bounds.
  * `dynamics` -- `dynamics_for` with the BLOCK as the budget, so the sizer is confined to a span
    that has already been checked against the retention floor. Same seam, same shipped
    `DEFAULT_SIZER`, same closed `status`/`reading` vocabularies.
  * `prior` -- `prior.compare` against `[session start (or the retention floor), block start)`.
    CONTRAST, NEVER FALLBACK: `prior.py`'s rule is unchanged by the unit shrinking.

⚠️ **The dynamics sizer was measured on 60-MINUTE budgets and a block is at most 20.** `EwmaSizer`
sees 20 observations inside a block instead of 60, so it detects less often and falls back to
`FixedSizer` more; the fallback is capped at half the span, giving a 10/10 split on a full-cap
block. Nothing about that is wrong -- both sides are still measured the same way, the shares are
still length-invariant, and `sizer`/`sizer_detail`/`slice_minutes`/`baseline_minutes` in the
payload say exactly what was cut -- but it is a smaller sample than the one the +74.6-point
precision result was measured on, and a re-measurement at block scale has not been done.

## PRIVACY

Coordinates only, and a SUBSET of what `/analyze` publishes: bin timestamps and `(level, ref)`
counts. No prompt id, no prompt text, no spans, no offsets are read -- the block <-> episode
mapping that once carried ids is deleted (see `blocks.py`, "`covers` WAS DELETED"). The
one text-derived level, `term`, publishes as `inventory.named_terms` exactly as it does on
`/analyze` -- this path adds no new class of data and, like `prior.py`, cannot: `PRIOR_DIMENSIONS`
is derived from ALLOCATION and `dynamics` carries no level value at all.
"""
import math
import time

from app.analysis import COMPONENT_DEPTH, SCHEMA
from app.analysis import blocks as blocks_mod
from app.analysis import latency, magnitude, prior as prior_mod, window, workstreams
from app.analysis.dynamics import DEFAULT_SIZER, dynamics_for
from app.analysis.ingest import RECONCILE_SLOT, pending_in, session_of
from app.analysis.levels import quantize
from app.analysis.reconcile import reconcile
from app.analysis.store import BIN_SECONDS
from app.analysis.transcript import _order_key

# The silence that settles the TRAILING block. DERIVED from the cutter's own terminator rather
# than restated, so the two cannot drift: `blocks.IDLE_BINS` (3) * `BIN_SECONDS` (300) = 900s.
# Restating it as a literal would let someone retune the cutter and leave the emitter closing
# blocks that the next arrival is still able to extend -- see the module docstring for why the
# threshold has to be exactly this one and has to be measured from `b.end`.
IDLE_SECONDS = float(blocks_mod.IDLE_BINS * BIN_SECONDS)

# One call's batch bound. A block is at most `blocks.MAX_BLOCK_MINUTES` (20) long, so 24 blocks is
# EIGHT HOURS OF ACTIVE WORK: a full working day's backlog clears in a single call, and a machine
# that was off for a week drains at a visible, steady rate instead of putting the week on the wire
# at once. The number is a bound on the RESPONSE, never a loss -- the caller resumes from
# `since_ts` and the next call continues where this one stopped, so a bounded response skips
# nothing.
#
# It is deliberately larger than `tick.DEFAULT_MAX_WINDOWS` (12) because the units differ: a tick
# window is an hour and a block is a third of one, so 12 blocks would be four hours and an
# ordinary day would need two calls to catch up. Cost is not what bounds it -- one digest measures
# in single-digit milliseconds -- the wire is.
DEFAULT_MAX_BLOCKS = 24


def span_of(store, session):
    """`(lo, hi)` -- the BIN-ALIGNED span `blocks.cut` is asked to cut, or `None` for a session
    with no active bin.

    The first active bin's start to the last active bin's end, which is the study's own
    `session_bounds`: the blocks that ship have to be the blocks that were measured, and the
    measured arm formed them over the WHOLE session rather than per window.

    ⚠️ **`cut` REQUIRES a bin-aligned `from_ts` and does not check it** -- see its docstring.
    `active_segments` filters on bin STARTS, so a bin straddling a non-aligned `lo` is dropped
    from every segment while `rollup_window` still counts the events inside it: evidence lands in
    no block at all, nothing errors, and no block looks wrong. A `bin_ts` is aligned by
    construction, so the floor/ceil below are no-ops today; they are here because the precondition
    is invisible at the call site otherwise.
    """
    bins = blocks_mod.active_bins(store, session)
    if not bins:
        return None
    lo = math.floor(float(bins[0]) / BIN_SECONDS) * BIN_SECONDS
    hi = math.ceil(float(bins[-1] + BIN_SECONDS) / BIN_SECONDS) * BIN_SECONDS
    return float(lo), float(hi)


def _rollup_at(store, path, session, lo, hi):
    """`[lo, hi)` of one session -> a rollup. THE one way this module computes one.

    The reconcile rows are EXCLUDED from the stored query and RECOMPUTED at this block's scope.
    `reconcile` resolves a prose path against every path a tool DECLARED, so its answer depends on
    which declarations are in scope; the store holds the whole file's reconciliation, and slicing
    that by timestamp would attribute a path using a declaration the block never saw.

    Extracted so the block's own rollup and the SESSION PRIOR beside it cannot be computed
    differently -- `prior.contrast`'s `departure` subtracts the session's share from the block's,
    which is only a number if both were measured the same way. `language` is what makes that real:
    `lang` rows exist ONLY through reconcile, so a prior served from the stored, file-scoped
    reconciliation while the block re-scoped its own would compare two different quantities and
    look entirely plausible doing it.
    """
    rows = store.window_rows(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    recon_rows, _stats = reconcile(pending_in(store, path, lo, hi), COMPONENT_DEPTH)
    return window.rollup(rows + recon_rows)


def _effort_at(store, session, lo, hi):
    """The `effort` block for `[lo, hi)`: how much was AUTHORED and how fast the turns came.

    Not a reference level -- which is why it is its own block rather than two more dimensions --
    and computed out of the series by four indexed queries, with no transcript opened.

    `magnitude.REQUEST_TOKENS`, never `magnitude.TOKENS`: the latter carries a request's cost on
    every LINE of that request (median 2, up to 12), so summing it produces a plausible, roughly
    2x-over-counted total that looks like spend. That is the failure the two constants exist to
    prevent. `authored_bytes` is `None` when nothing was costed and `fast_share` is `None` when
    there was no gap to measure; neither is rendered as 0, because a sum of no terms IS zero once
    we know we looked, while 0/0 never is.
    """
    turn_times = store.turn_times(session, lo, hi, exclude_slots=(RECONCILE_SLOT,))
    spend_vals = [v for _ts, v in store.turn_magnitudes(session, lo, hi,
                                                        kind=magnitude.REQUEST_TOKENS)]
    auth = magnitude.authored(
        (v for _ts, v in store.turn_magnitudes(session, lo, hi, kind=magnitude.EDIT_BYTES)),
        recorded=store.has_magnitudes(session, lo, hi))
    tmp = latency.tempo(turn_times)
    pcts = latency.percentiles(turn_times)
    return {
        "authored_bytes": auth.nbytes,
        "authoring_turns": auth.turns,
        "authored_status": auth.status,
        "fast_share": None if tmp.fast_share is None else round(tmp.fast_share, 3),
        "gaps": tmp.n_gaps,
        "tempo": tmp.reading,
        "tempo_status": tmp.status,
        "request_tokens": int(round(sum(spend_vals))) if spend_vals else None,
        "gap_p50_s": pcts.p50,
        "gap_p90_s": pcts.p90,
    }


def _prior_at(store, path, session, block_rl, lo, floor):
    """The session as it stood BEFORE this block, contrasted with the block's own answer.

    `[floor or the beginning of time, block start)` -- HALF-OPEN on the right, and that
    half-openness is the whole of the causal claim: an event at the boundary instant is inside the
    block being characterised, and admitting it would put the block into its own frame of
    reference. The prior's upper bound is the identical `lo` the block's own rollup used, so the
    two intervals abut exactly and no evidence is counted on both sides.

    RETENTION CLAMPS, IT DOES NOT REFUSE. A prior starts before the block it contrasts, so it
    reaches under the serving floor whenever one exists; refusing there would drop the block for a
    thing that is decoration beside it. `clamped` is the mere EXISTENCE of a floor and cannot be
    sharpened into "the floor actually cut something off" -- the rows that would say whether this
    session began before it are precisely the rows retention deleted. It therefore over-warns for
    a young session rather than under-warning for an old one, which is the direction a reader can
    do something about.
    """
    hi = float(lo)
    low = 0.0 if floor is None else float(floor)
    prior_rl = _rollup_at(store, path, session, low, hi) if hi > low else {}
    return {"clamped": floor is not None,
            "dimensions": prior_mod.compare(block_rl, prior_rl)}


def digest(store, session, block, path, floor=None, sizer=None, prior=True):
    """Characterise ONE block: `blocks.Block` -> the published payload.

    `start`/`end` are EPOCH SECONDS, the unit `blocks.py` keys everything on and the unit
    `since_ts` is in. `/analyze` converts its window edges to ISO
    because a window is anchored to a prompt's own ISO instant; a block is anchored to a bin, and
    a consumer that has to parse an ISO string back into a float to advance its cursor is being
    handed the wrong type.

    `start_reason`/`end_reason` come from `blocks.REASONS` and are never interchangeable: `budget`
    is only "we had to cut somewhere" -- the plurality case at 48.5% -- while `idle` is a claim
    there was no work at all. A reader who cannot tell them apart cannot tell an arithmetic
    boundary from a real pause.

    `sizer` and `prior` are parameters for the same reason they are on `/analyze`: so a study can
    reproduce this exact arithmetic without them. Production passes neither and gets both.

    `floor` is the retention serving floor (`store.serving_floor()`), threaded to the two parts
    that can reach below it -- the sizer's baseline and the prior's lower bound. It is NOT a
    refusal here: `digest_blocks` refuses an expired block before it ever gets this far.
    """
    lo, hi = quantize(float(block.start)), quantize(float(block.end))
    rl = _rollup_at(store, path, session, lo, hi)
    minutes = max(0.0, (hi - lo) / 60.0)
    out = workstreams.payload(rl)
    out.update(
        schema=SCHEMA,
        session=session,
        start=float(block.start),
        end=float(block.end),
        start_reason=block.start_reason,
        end_reason=block.end_reason,
        block_minutes=round(minutes, 3),
        evidence=int(sum(n for items in rl.values() for _, n in items)),
        effort=_effort_at(store, session, lo, hi),
    )
    if minutes > 0:
        # The BLOCK is the budget. `dynamics_for` is the same call `/analyze` makes, so the seam
        # is exercised in production rather than only in tests, and the sizer is confined to a
        # span already checked against the retention floor -- it cannot open a surface this
        # function has not validated. See the module docstring on what the smaller budget costs.
        out["dynamics"] = dynamics_for(store, session, hi, minutes,
                                       sizer=sizer if sizer is not None else DEFAULT_SIZER,
                                       floor=floor)
    if prior:
        out["prior"] = _prior_at(store, path, session, rl, lo, floor)
    return out


def bin_ceiling(ts):
    """The END of the 5-minute bin `ts` falls in. The resolution the watermark clause is
    evaluated at -- see the module docstring on why the literal instant comparison wedges the
    trailing block forever."""
    return math.floor(float(ts) / BIN_SECONDS) * BIN_SECONDS + BIN_SECONDS


def is_closed(cut, i, watermark, now, current=True, idle_seconds=IDLE_SECONDS):
    """Whether `cut[i]` may be emitted. `cut` is `blocks.cut`'s whole list, in time order.

    "Activity exists after `b.end`" is asked as "is there a LATER BLOCK", and the two are the same
    question: `cut` is formed over the active bins of the whole session, so every block after this
    one holds at least one, and the last block in the list is the only one with no activity behind
    it. Reading the bin table again to ask the same thing would be a second copy of the cutter's
    own knowledge, free to drift from it.
    """
    if watermark is None:
        return False
    b = cut[i]
    if float(b.end) > bin_ceiling(watermark):
        return False
    if i < len(cut) - 1:
        return True
    # The TRAILING block. Only silence can settle it, and only if the series is what the machine
    # actually did -- unparsed bytes on disk are not silence (see the module docstring).
    return bool(current) and (float(now) - float(b.end)) >= float(idle_seconds)


def digest_blocks(store, path, since_ts=None, now=None,
                  max_blocks=DEFAULT_MAX_BLOCKS, current=True, floor=None, sizer=None,
                  prior=True):
    """`POST /blocks`' whole answer: `{"blocks": [...], "watermark": ...}`.

    Every CLOSED block of this transcript starting at or after `since_ts`, oldest first, bounded
    to `max_blocks`, each one characterised.

    `since_ts` IS COMPARED AGAINST A BLOCK'S START, and `>=` rather than `>`, so the caller
    resumes by passing THE LAST EMITTED BLOCK'S END. Blocks abut inside an active segment
    (`next.start == prev.end`), so `>=` admits the next block and excludes the one just emitted;
    across an idle gap `next.start > prev.end` and it admits it just the same. `None` means from
    the beginning of the session -- BACKFILL, and the caller owns that choice: the daemon's
    emitter starts forward-only, matching `KELD_WATCH_BACKFILL`, by seeding its cursor rather than
    by this function second-guessing it.

    `watermark` is returned unconditionally, INCLUDING when no block is closed, because it is the
    one fact that distinguishes "nothing is settled yet" from "this transcript has never been
    ingested" (`None`). A caller cannot infer either from an empty list.

    RETENTION: a block whose evidence was pruned is DROPPED rather than answered from what
    survived. `Store.window_rows` serves an excluded-slot query entirely from `event` (a `bin` row
    has no slot to filter on), and this module always excludes the reconcile slot, so a pruned
    block would come back with a confident share computed off whatever fraction of its rows
    happened to outlive the horizon. That is a plausible wrong number, which is worse than no
    number. Unlike `/analyze` it is not raised as a refusal: `/analyze` answers about ONE window a
    caller asked for by id, while this answers about a range, and one unanswerable block must not
    discard the answerable ones behind it -- the same call `tick.py` makes for the same reason.

    Cost is dominated by the per-block digest (two rollups for dynamics, one for the block, one
    for the prior, four indexed effort queries). `blocks.cut` itself is paid ONCE for the whole
    call, measured p50 0.3 ms / p90 2.3 ms over the 496-session frozen corpus.
    """
    now = time.time() if now is None else float(now)
    session = session_of(path)
    wm_iso = store.watermark(path)
    watermark = None if wm_iso is None else _order_key(wm_iso).timestamp()
    out = {"blocks": [], "watermark": watermark}
    if watermark is None:
        return out

    span = span_of(store, session)
    if span is None:
        return out
    cut = blocks_mod.cut(store, session, *span)
    if not cut:
        return out

    floor = store.serving_floor() if floor is None else floor
    for i, b in enumerate(cut):
        if len(out["blocks"]) >= max_blocks:
            break
        if since_ts is not None and float(b.start) < float(since_ts):
            continue
        if not is_closed(cut, i, watermark, now, current=current):
            continue
        if floor is not None and quantize(float(b.start)) < float(floor):
            continue
        out["blocks"].append(
            digest(store, session, b, path, floor=floor, sizer=sizer, prior=prior))
    return out
