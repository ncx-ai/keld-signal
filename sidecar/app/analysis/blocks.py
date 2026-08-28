"""THE BLOCK CUTTER: what ends a block of work.

A `Block` is a contiguous span of one session's ACTIVE time. It is the unit the analysis attributes
over -- a window is an arbitrary hour, a block is a piece of work -- and the only question this
module answers is where one ends.

Two terminators, and the set is closed:

  * **idle** -- `IDLE_BINS` (3) consecutive empty 5-minute bins, i.e. 15 minutes of silence. Not a
    claim about the work at all: a claim that there wasn't any.
  * **budget** -- `MAX_BLOCK_MINUTES` (20) elapsed. Only "we had to cut somewhere", and on this
    corpus it is the PLURALITY case. It is reported separately from `idle` because a reader who
    cannot tell them apart cannot tell "we cut here arbitrarily" from "the work stopped".

⚠️ **THERE WAS A THIRD, AND ABLATING IT MADE EVERY NUMBER BETTER.** The detector
(`dynamics.EwmaSizer` on the `branch` series) was the third terminator in the arm that won the
pre-registered round-2 comparison. A post-hoc ablation over the same corpus
(`BLOCK-BOUND-2-ABLATION.md`) removed it and measured, at the shipped cap:

    attributable      95.29%  ->  96.21%
    held any evidence  99.3%  ->  100.0%      <- the detector was the ONLY source of empty blocks
    merge rate         1.36%  ->   0.67%
    longest block        20m  ->     20m      (unchanged; the cap is the cap)

A detected cut ends a block EARLY, so the block holds less evidence and is likelier to fall under
`MIN_EVIDENCE`. The detector was buying its 4.3% of boundaries by thinning the blocks around them.
What is given up is the claim that those cuts sat in more MEANINGFUL places -- which is exactly
what Phase 0a could not establish: 7.1% recall against a fixed-interval control's 17.3%, with every
alternative detection level failing its bar (`action` scored -3.5, better on shuffled truth than on
real truth). So the removed component improved no measured metric, fired on 32 of 496 sessions, and
failed its own validation study.

Two consequences worth knowing. The bound is now **fully domain-agnostic** -- detection was its only
branch-dependent part, so a session with no repository behaves identically to one with a repository,
nothing missing and nothing degraded. And `blocks.py` no longer imports `dynamics` at all;
`EwmaSizer` remains in use there for SLICE sizing, which is a separate, measured, shipped use that
this ablation does not touch.

Measured end-reason mix at the SHIPPED 20-minute cap (496 sessions,
arm `time_idle` n=20, detector ablated -- the mix is tabulated in `BLOCK-BOUND-2-ABLATION.md`):
**budget 48.47%, session_end 33.02%, idle 18.51%** over 1,502 blocks. Quoted at
n=20 rather than at any other row of the sweep because that is the cap this module ships; a figure
from a cap the code does not use is misleading even when the number itself is right.

## WHY THESE VALUES, AND WHY THEY ARE NOT KNOBS

Both are MEASURED, in a pre-registered four-arm study over 496 sessions
(`~/keld/refseries-context/blocks/BLOCK-BOUND-2-{PREREGISTRATION,RESULTS}.md`; harness
`scripts/block_sizing_eval.py`). Four bounds were tried against the same idle terminator: a plain
time cap (A'), an evidence-gated cap that defers until the block can attribute something (B'), a
turn count (C'), and no bound at all (D').

**A' -- the plain cap -- won, at 20 minutes.** 95.3% of blocks attributable, p50 20m, p90 20m, and
a maximum block of 0.33h. It won on the CONSTRAINTS rather than on the metric: with dead air
excluded every arm attributes within a point of every other (95.2-96.2%), so what separated them
is that A' is the only arm whose block length is bounded by construction -- being a plain cap, its
maximum block EQUALS its cap. B' is bit-identical to A' at every cap >= 30 minutes (the deferral
gate never fires once idle is handled) and buys +0.6 points at 10-20; D' produces 7.5-hour blocks
and makes 53.0% of its blocks a whole session.

20 specifically: A''s usable range is 15-45 minutes, and 20 is where attribution first clears the
bar (95.3%) with the maximum block still at 20 minutes. At 120 the whole-session share reaches
50.1% and the cap has stopped bounding anything on this corpus.

`IDLE_BINS = 3` was fixed in the pre-registration -- before the run, as a stated judgement about
what reads as "work stopped" rather than as a pause between turns -- and then swept (2/3/6 bins =
10/15/30 min) rather than left asserted. At the shipped 20-minute cap: 94.9% attributable at 10
min, **95.3% at 15**, 93.4% at 30. The series bins at 300s, so the threshold is a whole number of
bins and cannot be expressed more finely.

## THERE IS NO MERGE RULE, AND THIS IS THE LOAD-BEARING ABSENCE

A block that cannot attribute a dimension publishes that dimension UNATTRIBUTED and survives as its
own block. It is never absorbed into a neighbour.

⚠️ **"Cannot attribute" means PER LEVEL, not pooled, and the two measures diverge.** The
attributable question is `window.attribution(rollup, level, floor).reason == "attributed"` asked of
each ALLOCATION level ON ITS OWN -- that is what the 95.3% above is. It is NOT "the block holds
`MIN_EVIDENCE` observations", which pools across the eight allocation levels: a block holding one
unit at each of eight levels pools to 8, past the floor, while every individual level reads `thin`
and nothing in it is attributable at all. The results table reports both columns and they are 6
points apart at the shipped cap (95.3% attributable against 99.3% holding any evidence). Neither
measure is exported here, because nothing in Phase 1 consumes one; whoever adds the first consumer
must pick the per-level one deliberately rather than reach for a pooled sum because it is shorter
to write.

The obvious repair -- merge a thin block forward into the next one -- was built and measured
(`BLOCK-SIZING-RESULTS.md`): it changes a published value in **88.6%** of the merges it performs,
against a pre-registered 5% bar, and merges chain (thin into thin, 94.6% of 7,391 events). Merging
does not recover a thin block's answer; it overwrites it with the neighbour's. So the thin block
keeps its honest blank, which is the same call `window.MIN_EVIDENCE` and `prior.py`'s
CONTRAST-NEVER-FALLBACK make one level down. Do not add a merge rule and do not add a knob for one
-- a knob is a merge rule with the decision deferred.

## BLOCKS DO NOT TILE THE SPAN. THEY TILE ITS ACTIVE PART.

Idle splits `[from_ts, to_ts)` into ACTIVE SEGMENTS first; the cap and the detector then run WITHIN
each segment. The dead air between segments belongs to NO block. So the invariant is

    every active bin lies in exactly one block

and NOT "the blocks cover the span" -- the second is what round 1 of the study measured by
accident, tiling silence with empty 20-minute blocks, and it cost arm A 65 points of attribution
(29.8% -> 95.3% once corrected). A future reader tempted to make the blocks abut across a gap is
reintroducing exactly that defect.

## WHY `_form` STILL TAKES A CUT LIST IT IS NEVER GIVEN

`_form`'s `cuts` parameter and its `detected` branch survive the ablation, always called with `[]`.
Keeping them is what makes the shipped arithmetic IDENTICAL to the measured arm evaluated with an
empty cut list -- which is precisely what the ablation measured -- rather than a second, cut-free
code path that would have to be shown equal to it. The branch is unreachable from `cut`, so
`detected` is deliberately NOT in `REASONS`: that tuple is what `cut` can actually emit, and a
reason listed but never emitted is the defect this module's own review caught in the other
direction.

Its honest limitation, inherited: cut points come from `branch` alone, and the detector fires in
only 32 of 496 sessions. On most sessions a block is ended by the cap or by silence, and nothing
here claims otherwise.

## `covers` WAS DELETED, AND THIS IS WHY IT MUST NOT COME BACK

There used to be a `covers()` here: for each block, the prompt EPISODES overlapping it (an episode
being a human prompt plus every agent turn that follows it), clipped to the block, with a
`complete` flag. It is gone, and not because it was broken -- though it was: the daemon's
`internal/agent/watch/filter.go` yields `promptId` while `ingest.py` indexes the per-message
`uuid`, so `store.prompt_time` resolved NONE of the ids it was sent and every real run published
an empty list.

It is gone because a block is TIME, end to end. Cutting is a cap plus a silence threshold; the
digest is a rollup over a span; the principal comes from the device's own auth. Nothing in the
cutter, the digest, the dimensions, the dynamics, the prior or the effort touches a prompt id. And
Atlas has to join on time REGARDLESS: cost attribution is `event.ts in [block.start, block.end)`
within the session, never through a prompt mapping, because a turn spanning several blocks would
double-count its spend through one. That mandatory join also answers the display question, since
Atlas holds `ToolEvent.session_id` and `event_ts` -- given a span it can find the events inside,
which IS which turns overlap, and "this turn continues past the block" is derivable from the same
rows. So `covers` was a second, weaker copy of a join Atlas owes anyway.

A block is now exactly **(principal, session, span, boundary reasons, facets)**. No ids.

## PRIVACY

Everything is computed from stored coordinate rows -- bin timestamps and `(level, ref)` counts. No
prompt text, no spans, no offsets are read, and a `Block` carries four numbers and two words from a
closed vocabulary. Nothing here reads, returns or times a prompt id -- see the section above on
why `covers` was deleted rather than repaired.
"""
import collections

from app.analysis.store import BIN_SECONDS

# The measured cap. See "WHY THESE VALUES" above: 20 is where attribution first clears the bar with
# the maximum block still equal to the cap. Not a tuning parameter -- retuning it re-opens the
# four-arm comparison.
MAX_BLOCK_MINUTES = 20

# Idle = this many consecutive EMPTY 5-minute bins. 3 bins = 15 minutes. Fixed in the
# pre-registration and then swept (2/3/6); 3 is the best of the three at the shipped cap.
IDLE_BINS = 3

# One block of work. `start_reason`/`end_reason` are never interchangeable -- `budget` is only "we
# had to cut somewhere" while `idle` is a claim there was no work at all, and a reader who cannot
# tell them apart cannot tell an arithmetic boundary from a real pause. On this corpus `budget` is
# the common case. `session_start`/`session_end` mark the two ends of the span, which are boundaries
# of neither kind. Timestamps are epoch SECONDS, the unit the series is keyed on.
Block = collections.namedtuple("Block", "start end start_reason end_reason")

# The closed set. Exported so a consumer can validate rather than restate it -- the same reason
# `dynamics.DynamicReadings` exists Go-side.
# `detected` is absent BY ABLATION, not by oversight -- see the module docstring. `_form` retains
# the branch for arithmetic equality with the measured arm, but `cut` never supplies a cut, so no
# block can carry it.
REASONS = ("session_start", "idle", "budget", "session_end")


def active_bins(store, session):
    """The non-empty 5-minute bins of `session`, ascending, as bin-start epoch seconds.

    A bin with no `bin` row has no event -- bins are derived from events -- so its rollup is empty
    and every level in it is `absent`. It can neither be attributed nor be one side of a
    transition, which is what makes an empty bin the right unit for "no work happened here".

    Read straight off `bin` rather than off `event`: `bin` is exactly the pre-aggregated form of
    this question, and `rollup_window` already serves whole intervals from it.
    """
    return [t for (t,) in store._conn().execute(
        "SELECT DISTINCT bin_ts FROM bin WHERE session=? ORDER BY bin_ts", (session,))]


def active_segments(store, session, lo, hi, idle_bins=IDLE_BINS):
    """`[(start, end), ...]` -- `[lo, hi)` split at every idle gap, with the dead air EXCLUDED.

    A run of `idle_bins` or more consecutive empty bins ENDS a segment; the next segment begins at
    the next active bin. A shorter run is a pause inside one segment, not the end of it.

    The excluded air belongs to no block, which is the whole point (see the module docstring): the
    returned segments do not cover `[lo, hi)`, they cover its active part, and `cut` inherits that.
    A segment's end is the last active bin's END (`bin + BIN_SECONDS`), clamped to `hi`, so a block
    can hold the whole of the bin its last event fell in.
    """
    bins = [b for b in active_bins(store, session) if lo <= b < hi]
    if not bins:
        return []
    segs, seg_start, prev = [], bins[0], bins[0]
    for b in bins[1:]:
        empty_between = int((b - prev) // BIN_SECONDS) - 1
        if empty_between >= idle_bins:
            segs.append((float(seg_start), float(prev + BIN_SECONDS)))
            seg_start = b
        prev = b
    segs.append((float(seg_start), float(prev + BIN_SECONDS)))
    return [(s, min(e, float(hi))) for s, e in segs]


def _form(cuts, lo, hi, max_minutes):
    """Contiguous blocks over ONE active segment `[lo, hi)`, cut at `cuts`, capped at
    `max_minutes`.

    A block ends at the next cut when that falls inside the cap (`detected`), otherwise at the cap
    (`budget`), otherwise at the segment end. DETECTION WINS over the bound wherever both are
    available -- a block ending `budget` where a cut was reachable would mean the detector is not
    reaching the cutter at all.

    Private: the segmentation is not optional. `cut` is the only correct entry point, because a
    caller handed this directly would tile dead air.
    """
    cap = max_minutes * 60.0
    remaining = [c for c in sorted(cuts) if lo < c < hi]
    out, t, reason = [], float(lo), "session_start"
    while t < hi:
        nxt = next((c for c in remaining if c > t), None)
        if nxt is not None and nxt - t <= cap:
            end, end_reason = nxt, "detected"
        elif t + cap < hi:
            end, end_reason = t + cap, "budget"
        else:
            end, end_reason = float(hi), "session_end"
        out.append(Block(t, end, reason, end_reason))
        t, reason = end, end_reason
    return out


def cut(store, session, from_ts, to_ts, max_minutes=MAX_BLOCK_MINUTES, idle_bins=IDLE_BINS):
    """`[Block, ...]` over the ACTIVE part of `[from_ts, to_ts)`, in time order.

    The whole cutter: idle splits the span into active segments, then the cap and the detector run
    within each one. Blocks abut inside a segment and are separated by the dead air between
    segments -- see the module docstring for why that is the invariant rather than tiling.

    Reasons chain: each block's `start_reason` is the previous block's `end_reason`. At a segment
    seam the earlier block ends `idle` and the later one starts `idle`; the first block of the span
    starts `session_start` and the last ends `session_end`. A span with no active bin returns `[]`
    -- there is no work to cut, and an empty list says so more honestly than one empty block.

    ⚠️ **PRECONDITION: `from_ts` and `to_ts` MUST BE BIN-ALIGNED** -- multiples of `BIN_SECONDS`
    (floor `from_ts`, ceil `to_ts`). The caller owns this; nothing here checks it.

    What goes wrong if you ignore it is SILENT. `active_segments` filters on bin STARTS
    (`lo <= b < hi`), so a bin whose start falls before a non-aligned `from_ts` is dropped from
    every segment -- while `rollup_window`, which takes exact instants, still counts the events
    inside it. Concretely: events at 100s, 250s and 400s with `from_ts = 150.0` yields segments
    `[(300.0, 600.0)]`, and the 250s event lands in NO block despite being inside the requested
    span. Nothing errors and no block looks wrong; evidence just quietly goes missing.

    Aligning rather than clamping internally is deliberate. Task 2 pins this function
    byte-identical to the measured arm A', whose own harness always passed bin-aligned session
    bounds, so adding a clamp here would mean the shipped cutter is no longer the arm that was
    measured. The alignment belongs at the call site.

    `max_minutes` and `idle_bins` are parameters so the study's sweep stays reproducible against
    this exact arithmetic (the same reason `prior.compare` takes `dimensions`). They are NOT
    switches a request may flip: nothing on the `/analyze` path forwards one.
    """
    segs = active_segments(store, session, from_ts, to_ts, idle_bins)
    if not segs:
        return []
    # No detection: ablated, see the module docstring. `_form` keeps its `cuts` parameter so
    # the arithmetic stays identical to the measured arm with the cut list empty, which is
    # exactly what the ablation measured -- rather than a second, cut-free code path.
    cuts = []
    out = []
    for i, (s, e) in enumerate(segs):
        bl = _form(cuts, s, e, max_minutes)
        if not bl:
            # UNREACHABLE today: `active_segments` never returns a segment with `e <= s` (a
            # segment ends at its last active bin's END, which is strictly past its start), and
            # `_form` emits at least one block for any non-empty interval. Kept because the
            # measured arm has it, so `cut` stays byte-identical to `with_idle(bound_time)`.
            # If it ever DID fire on the final segment, the seam bookkeeping would be wrong: the
            # preceding block would keep the `idle` end it was given as a non-final segment, and
            # nothing in the returned list would end `session_end`. Anything relying on the last
            # block's reason would break silently rather than raise.
            continue
        # Only the reasons AT A SEAM are rewritten; the cap and detection semantics inside a
        # segment are untouched, which is what keeps this equal to the measured arm A'.
        if i > 0:
            bl[0] = bl[0]._replace(start_reason="idle")
        if i < len(segs) - 1:
            bl[-1] = bl[-1]._replace(end_reason="idle")
        out.extend(bl)
    return out
